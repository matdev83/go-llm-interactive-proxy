package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// TestF1RuntimeBundleNativeAudioV2Admission proves the production wiring: a
// configured backend instance whose trusted factory kind uses the OpenAI native
// usage mapper is flagged in the candidate executor and fails closed for every
// V2 admission before any provider open. It holds for both the default
// PutPricing card and a route-specific PutPricing override, while an unrelated
// non-OpenAI-mapper factory still admits normally.
func TestF1RuntimeBundleNativeAudioV2Admission(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}]}`)
	}))
	t.Cleanup(srv.Close)

	const nativeID = "native-arbitrary-instance"
	const controlID = "control-arbitrary-instance"

	for _, routeOverride := range []bool{false, true} {
		routeOverride := routeOverride
		name := "default-pricing"
		if routeOverride {
			name = "route-specific-pricing"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			store := openBillingHostLoopStore(t)
			catalog, pricing, _, _ := seedBillingHostLoopCatalog(t)
			if routeOverride {
				override := pricing
				override.Ref = billing.VersionRef{ID: "pricing-route-native", Version: "v1"}
				if err := catalog.PutPricing(override); err != nil {
					t.Fatalf("PutPricing route override: %v", err)
				}
				if err := catalog.SetRoutePricing(nativeID, billingHostLoopModelID, override.Ref); err != nil {
					t.Fatalf("SetRoutePricing: %v", err)
				}
			}

			accountID := fmt.Sprintf("f1native%d", billingHostLoopSeq.Add(1))
			provisionBillingHostLoopAccount(t, store, accountID)
			activateV2CutoverForTest(ctx, t, store)

			ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
			prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
				Store:             store,
				TerminalUsageSink: store,
				Catalog:           catalog,
				Currency:          "USD",
				ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
					return 128000, true, nil
				},
				Strict:              true,
				ConservativeCeiling: &ceiling,
				PostTurnBatchSize:   1,
			})
			if err != nil {
				t.Fatalf("ComposeBilling: %v", err)
			}

			path := writeNativeAudioAdmissionConfig(t, srv.URL, nativeID, controlID)
			host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
				ConfigPath:      path,
				Mandatory:       lipsdk.StandardDistributionRequirements(),
				LogWriter:       io.Discard,
				HandlerComposer: stdhttp.ComposeStandardHTTP,
				Production:      prod,
			})
			if err != nil {
				t.Fatalf("BuildHost: %v", err)
			}
			hostServeCleanup(t, host)

			exec := hostActiveExecutor(t, host)
			admitter, ok := exec.BillingExposureAdmission.(interface {
				AdmitV2(context.Context, coreruntime.BillingExposureAdmissionInput) (billing.CallExposure, error)
			})
			if !ok || admitter == nil {
				t.Fatalf("active admission %T must expose explicit AdmitV2", exec.BillingExposureAdmission)
			}

			nativeCallID, err := billing.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := admitter.AdmitV2(ctx, nativeAudioAdmissionInput(nativeID, accountID, nativeCallID.String())); !errors.Is(err, billing.ErrEstimateInvalid) {
				t.Fatalf("configured OpenAI-native instance %q must fail closed for V2, got %v", nativeID, err)
			}

			controlCallID, err := billing.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := admitter.AdmitV2(ctx, nativeAudioAdmissionInput(controlID, accountID, controlCallID.String())); err != nil {
				t.Fatalf("unrelated non-OpenAI-mapper factory must still admit: %v", err)
			}

			opens := injectNativeAudioStubBackend(t, exec, nativeID)
			stream, err := executeNativeAudioCall(ctx, exec, accountID, nativeID)
			assertBillingHostLoopAdmissionDenied(t, stream, err, opens, billing.ErrEstimateInvalid, coreruntime.ErrBillingAdmissionDenied)
			if got := opens.Load(); got != 0 {
				t.Fatalf("provider Open = %d, want 0 before spend", got)
			}
		})
	}
}

func activateV2CutoverForTest(ctx context.Context, t *testing.T, store *billingstore.DurableStore) {
	t.Helper()
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("EnsureAccountingCutover: %v", err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: marker.Version, ExpectedEpoch: marker.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f1-native-shadow",
	}); err != nil {
		t.Fatalf("TransitionAccountingCutover(shadow): %v", err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f1-native-drain"); err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f1-native-activate"); err != nil {
		t.Fatalf("ActivateCutoverV2: %v", err)
	}
}

func nativeAudioAdmissionInput(backendID, accountID, callID string) coreruntime.BillingExposureAdmissionInput {
	primary := routing.Primary{Backend: backendID, Model: billingHostLoopModelID}
	return coreruntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreruntime.BillingAdmissionInput{
			Call:        lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-f1-native"}},
			ALegID:      "a-f1-native",
			Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
			RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
			Scope:       scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)},
			SessionID:   "sess-f1-native",
			AccountID:   accountID,
		},
		CallID: callID,
	}
}

func injectNativeAudioStubBackend(t *testing.T, executor *coreruntime.Executor, backendID string) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	be := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return newBillingHostLoopUsageStream(false), nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends[backendID] = be
	capFn := func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
		return execbackend.EffectiveCaps(ctx, be, call, cand)
	}
	switch capMap := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		capMap[backendID] = capFn
	case nil:
		executor.CapsResolver = capabilities.MapResolver{backendID: capFn}
	default:
		t.Fatalf("CapsResolver type %T cannot accept injected backend caps", executor.CapsResolver)
	}
	return &opens
}

func executeNativeAudioCall(ctx context.Context, executor *coreruntime.Executor, accountID, backendID string) (lipapi.EventStream, error) {
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: backendID + ":" + billingHostLoopModelID},
		Session: lipapi.SessionRef{
			ClientSessionID: "client-f1-native-hint",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	return executor.Execute(execCtx, call)
}

func writeNativeAudioAdmissionConfig(t *testing.T, baseURL, nativeID, controlID string) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := fmt.Sprintf(`    - id: %s
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: %s
        base_url: %s/v1
        tokenizer: cl100k_base
        models:
          source: inline
          items:
            - canonical_id: %s/model-a
              native_id: model-a
    - id: %s
      kind: custom-openresponses-compatible
      enabled: true
      config:
        backend_prefix: %s
        base_url: %s/v1
        models:
          source: inline
          items:
            - canonical_id: %s/model-a
              native_id: model-a
`, nativeID, nativeID, baseURL, nativeID, controlID, controlID, baseURL, controlID)
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	if text == string(base) {
		t.Fatal("base config did not contain a features section anchor")
	}
	path := filepath.Join(t.TempDir(), "native-audio-admission.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
