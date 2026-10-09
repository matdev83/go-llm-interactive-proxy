package runtimebundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func TestDecision_StandardHostFailoverUsageAndBilling(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name          string
		billed        bool
		invalidAnswer bool
	}{{"unbilled-overload", false, false}, {"billed-overload", true, false}, {"billed-invalid-answer", true, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			billed := scenario.billed
			var firstCalls, secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				if scenario.invalidAnswer {
					_, _ = io.WriteString(w, `{"model":"jev","answers":{},"usage":{"input_tokens":3,"output_tokens":0,"cost":0.0000005}}`)
					return
				}
				w.WriteHeader(529)
				_, _ = io.WriteString(w, `{"detail":"overloaded"}`)
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				if r.Header.Get("X-Upstream-Session") != "" {
					t.Error("client header reached decision upstream")
				}
				_, _ = io.WriteString(w, `{"model":"jev-resolved","answers":{"q":{"type":"noul","noul":0.8}},"usage":{"input_tokens":10,"output_tokens":2,"cost":0.000001}}`)
			}))
			defer second.Close()
			store := openBillingHostLoopStore(t)
			prod := runtimebundle.ProductionOptions{}
			accountID := "decision-account"
			if billed {
				catalog := billingcompose.NewSnapshotCatalog()
				pricing := billing.PricingSnapshot{Ref: billing.VersionRef{ID: "decision-pricing", Version: "v1"}, Currency: "USD", InputRatePresent: true, OutputRatePresent: true, InputPerMillionNano: 100}
				policy := billing.ChargePolicy{Ref: billing.VersionRef{ID: "decision-policy", Version: "v1"}, PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true}
				if err := catalog.PutPricing(pricing); err != nil {
					t.Fatal(err)
				}
				if err := catalog.PutPolicy(policy); err != nil {
					t.Fatal(err)
				}
				if err := catalog.SetDefaults(pricing.Ref, policy.Ref); err != nil {
					t.Fatal(err)
				}
				operator := billing.OperatorRateSnapshot{Ref: billing.VersionRef{ID: "decision-provider", Version: "v1"}, Currency: "USD", InputRatePresent: true, OutputRatePresent: true}
				if err := catalog.PutOperatorRate(operator); err != nil {
					t.Fatal(err)
				}
				for _, backend := range []string{"first", "second"} {
					if err := catalog.SetOperatorRateBinding(backend, "jev", operator.Ref); err != nil {
						t.Fatal(err)
					}
				}
				identity := billingcompose.PrincipalSessionIdentity(billingcompose.SnapshotRefFuncs{CustomerPricingRef: catalog.CustomerPricingRef, ChargePolicyRef: catalog.ChargePolicyRef, OperatorRateRef: catalog.OperatorRateRef})
				identity.AccountID = func(context.Context, lipapi.Call) string { return accountID }
				var err error
				prod, err = runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{Store: store, TerminalUsageSink: store, Catalog: catalog, Identity: &identity, Currency: "USD", ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 0, false, nil }})
				if err != nil {
					t.Fatal(err)
				}
				provisionBillingHostLoopAccount(t, store, accountID)
			}
			host, aLeg, logs := buildDecisionHost(t, first.URL, second.URL, false, prod)
			req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"first:jev|second:jev","state":{"evidence":"private-systemone-evidence-canary"},"questions":{"q":{"type":"noul"}}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Upstream-Session", "must-not-forward")
			w := httptest.NewRecorder()
			host.HTTPHandler().ServeHTTP(w, req)
			var response struct {
				Model   string `json:"model"`
				Answers map[string]struct {
					Noul float64 `json:"noul"`
				} `json:"answers"`
				Usage map[string]int `json:"usage"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Model != "jev-resolved" || response.Answers["q"].Noul != 0.8 || response.Usage["input_tokens"] != 10 || response.Usage["output_tokens"] != 2 || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
				t.Fatalf("decision failover = %d %s, calls=%d/%d logs=%s", w.Code, w.Body.String(), firstCalls.Load(), secondCalls.Load(), logs.String())
			}
			if strings.Contains(logs.String(), "private-systemone-evidence-canary") {
				t.Fatal("decision evidence reached default logs")
			}
			ex := hostActiveExecutor(t, host)
			attempts, err := ex.Store.LoadAttempts(t.Context(), *aLeg.Load())
			if err != nil || len(attempts) != 2 || attempts[0].BLegID == attempts[1].BLegID {
				t.Fatalf("B-leg lineage = %+v, err=%v headers=%v", attempts, err, w.Header())
			}
			calls, err := store.ListCallUsage(t.Context(), accountID)
			if err != nil || (!billed && len(calls) != 0) || (billed && len(calls) != 1) {
				t.Fatalf("billing calls = %+v, billed=%t err=%v", calls, billed, err)
			}
			if billed {
				legs, err := store.ListCallLegUsage(t.Context(), calls[0].CallID)
				if err != nil || len(legs) != 2 {
					t.Fatalf("billing legs = %+v err=%v", legs, err)
				}
				var winner, failedUsage bool
				for _, leg := range legs {
					if leg.BackendID == "second" {
						winner = leg.Evidence.InputTokens.Present && leg.Evidence.InputTokens.Value == 10 && leg.Evidence.OutputTokens.Present && leg.Evidence.OutputTokens.Value == 2 && leg.Evidence.Cost.Present && leg.Evidence.Cost.NanoUnits == 1000
					}
					if leg.BackendID == "first" {
						failedUsage = leg.Evidence.InputTokens.Present && leg.Evidence.InputTokens.Value == 3 && leg.Evidence.OutputTokens.Present && leg.Evidence.OutputTokens.Value == 0 && leg.Evidence.Cost.Present && leg.Evidence.Cost.NanoUnits == 500
					}
				}
				if !winner {
					t.Fatal("winning B-leg lacks provider usage/cost evidence")
				}
				if scenario.invalidAnswer && !failedUsage {
					t.Fatal("failed billed attempt lost provider-reported usage/cost evidence")
				}
			}
		})
	}
}

func TestDecision_StandardHostGuardAndChatRefuseBeforeUpstream(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	host, _, _ := buildDecisionHost(t, upstream.URL, upstream.URL, true, runtimebundle.ProductionOptions{})
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/v1/systemone", `{"model":"first:jev","state":"x","questions":{"q":{"type":"noul"}}}`, 403},
		{"/v1/chat/completions", `{"model":"first:jev","messages":[{"role":"user","content":"hi"}]}`, 400},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		host.HTTPHandler().ServeHTTP(w, r)
		if w.Code != tc.status || calls.Load() != 0 {
			t.Fatalf("refusal = %d %s, calls=%d", w.Code, w.Body.String(), calls.Load())
		}
	}
}

func buildDecisionHost(t *testing.T, first, second string, guarded bool, prod runtimebundle.ProductionOptions) (*runtimebundle.Host, *atomic.Pointer[string], *decisionTestLog) {
	t.Helper()
	features := "  features: []\n"
	if guarded {
		features = "  features:\n    - id: secrets-guard\n      enabled: true\n      config:\n        action: block\n"
	}
	text := `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 2
  default_route: "first:jev|second:jev"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
plugins:
  frontends:
    - id: systemone
      enabled: true
    - id: openai-legacy
      enabled: true
  backends:
`
	for i, endpoint := range []string{first, second} {
		id := []string{"first", "second"}[i]
		text += fmt.Sprintf("    - id: %s\n      kind: custom-systemone-compatible\n      enabled: true\n      config:\n        backend_prefix: %s\n        base_url: %s\n        models:\n          source: inline\n          items:\n            - canonical_id: jev\n              native_id: jev\n", id, id, endpoint)
	}
	text += features
	text += fmt.Sprintf("model_catalog:\n  enabled: true\n  external_updates_enabled: false\n  cache_path: %q\n", filepath.Join(t.TempDir(), "catalog.json"))
	path := filepath.Join(t.TempDir(), "decision.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &decisionTestLog{}
	aLeg := &atomic.Pointer[string]{}
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath: path, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: logs, Production: prod,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-systemone-compatible", func(original pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(id string, node yaml.Node, client *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					result, err := original(id, node, client, deps)
					if err != nil {
						return result, err
					}
					open := result.Backend.Open
					result.Backend.Open = func(ctx context.Context, call lipapi.Call, candidate routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						id := call.Session.ALegID
						aLeg.Store(&id)
						return open(ctx, call, candidate)
					}
					return result, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)
	return host, aLeg, logs
}

type decisionTestLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *decisionTestLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(data)
}

func (l *decisionTestLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
