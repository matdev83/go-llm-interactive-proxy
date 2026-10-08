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

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Exercise real HTTP admission, generation publication and backend Open/OpenWire,
// not just the static census: steering must win before first-turn wire execution.
func TestModelSystemPrompt_HostReloadFrozenDecisionsAndWireFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"other"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-bootstrap\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"host-live\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	base := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q
routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, t.TempDir(), upstream.URL+"/v1")
	feature := func(pattern, text string) string {
		return fmt.Sprintf("    - id: model-system-prompt\n      enabled: true\n      config:\n        rules:\n          - id: first\n            model_pattern: %q\n            append: %q\n", pattern, text)
	}
	path := filepath.Join(t.TempDir(), "host.yaml")
	require.NoError(t, os.WriteFile(path, []byte(base), 0o600))
	var wire, canonical atomic.Int32
	var captured atomic.Pointer[lipapi.Call]
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath: path, Mandatory: lipsdk.StandardDistributionRequirements(),
		LogWriter: io.Discard, HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(id string, node yaml.Node, client *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(id, node, client, deps)
					if err != nil {
						return res, err
					}
					open, openWire := res.Backend.Open, res.Backend.OpenWire
					res.Backend.Open = func(ctx context.Context, call lipapi.Call, candidate routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						canonical.Add(1)
						clone := lipapi.CloneCall(call)
						captured.Store(&clone)
						return open(ctx, call, candidate)
					}
					res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
						wire.Add(1)
						return openWire(ctx, req)
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)
	type leg struct{ id, session, token string }
	send := func(model string, prior leg, large bool) leg {
		padding := "client truth"
		if large {
			padding += strings.Repeat("x", 1500)
		}
		body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"system","content":"original"},{"role":"user","content":%q}]}`, model, padding)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if prior.id != "" {
			req.Header.Set("X-LIP-Session-ID", prior.session)
			req.Header.Set("X-LIP-Resume-Token", prior.token)
		}
		rec := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "host must remain usable")
		require.Contains(t, rec.Body.String(), "host-live")
		out := leg{rec.Header().Get("X-LIP-A-Leg-ID"), rec.Header().Get("X-LIP-Session-ID"), rec.Header().Get("X-LIP-Resume-Token")}
		require.NotEmpty(t, out.id)
		if prior.id != "" {
			require.Equal(t, prior.id, out.id)
		} else {
			require.NotEmpty(t, out.token)
		}
		return out
	}
	reload := func(raw string) sdkreload.Result {
		manager := runtimebundle.HostManager(host)
		prior := manager.Active()
		candidate := filepath.Join(t.TempDir(), "candidate.yaml")
		require.NoError(t, os.WriteFile(candidate, []byte(raw), 0o600))
		require.NoError(t, replaceTestFile(candidate, path))
		result := host.Reload(t.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: "bootstrap-test"})
		if result.Category == sdkreload.ResultPublished {
			// Wait for actual retirement; publication alone could leave cleanup pending.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := manager.RetireGeneration(ctx, prior)
			require.True(t, err == nil || errors.Is(err, runtimehost.ErrAlreadyClosed), "retirement must finish")
			require.Equal(t, runtimehost.GenClosed, prior.Lifecycle())
		}
		return result
	}
	assertInstructions := func(hidden string) {
		call := captured.Load()
		require.NotNil(t, call)
		var want []lipapi.Message
		if hidden != "" {
			want = []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart(hidden)}}}
		}
		require.Equal(t, want, call.Instructions)
		require.NotEmpty(t, call.Messages)
		require.Equal(t, lipapi.Message{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("original")}}, call.Messages[0])
	}
	// The pre-feature leg has real B-leg history, not fabricated attempt rows.
	preexisting := send("gpt-4o", leg{}, false)
	require.Equal(t, int32(1), canonical.Load())
	require.Equal(t, sdkreload.ResultPublished, reload(base+feature("^gpt-4o$", "generation one")).Category)
	ex := hostActiveExecutor(t, host)
	producer := ex.ConversationBootstrap
	require.NotNil(t, producer)
	var resolves atomic.Int32
	ex.ConversationBootstrap = func(ctx context.Context, id string, resolve func() (coreruntime.InitialModelIntent, error)) error {
		return producer(ctx, id, func() (coreruntime.InitialModelIntent, error) {
			resolves.Add(1)
			return resolve()
		})
	}
	matched := send("gpt-4o", leg{}, true)
	require.Zero(t, wire.Load(), "enabled bootstrap must prevent first-turn wire bypass")
	require.Equal(t, int32(2), canonical.Load(), "one canonical fallback Open")
	require.Equal(t, int32(1), resolves.Load(), "one atomic decision callback on fallback")
	assertInstructions("generation one")
	send("wire-backend:other", matched, true)
	assertInstructions("generation one")
	require.Equal(t, int32(1), resolves.Load(), "later model changes must not evaluate the decision again")
	noMatch := send("wire-backend:other", leg{}, false)
	assertInstructions("")
	ambiguous := send("wire-backend:gpt-4o!wire-backend:other", leg{}, false)
	assertInstructions("")
	send("gpt-4o", preexisting, false)
	assertInstructions("")
	require.Equal(t, int32(3), resolves.Load(), "preexisting allocation must skip resolver")
	cases := []struct {
		name    string
		leg     leg
		outcome conversationview.BootstrapOutcome
		hidden  string
	}{
		{"matched", matched, conversationview.BootstrapMatched, "generation one"},
		{"no-match", noMatch, conversationview.BootstrapNoMatch, ""},
		{"ambiguous", ambiguous, conversationview.BootstrapAmbiguousSkip, ""},
		{"preexisting", preexisting, conversationview.BootstrapPreexistingSkip, ""},
	}
	store := runtimebundle.HostProcess(host).StandardFeatures.ConversationStore()
	bootstrap, ok := store.(conversationview.BootstrapStore)
	require.True(t, ok)
	checkFrozen := func() {
		for _, tc := range cases {
			send("wire-backend:other", tc.leg, true)
			assertInstructions(tc.hidden)
			result, err := bootstrap.BootstrapSteering(t.Context(), tc.leg.id, "model-system-prompt", func() (conversationview.BootstrapDecision, error) {
				t.Error("completed decision evaluated again: " + tc.name)
				return conversationview.BootstrapDecision{}, nil
			})
			require.NoError(t, err)
			require.True(t, result.Reused, tc.name)
			require.Equal(t, tc.outcome, result.Completion.Outcome, tc.name)
		}
	}
	require.Equal(t, sdkreload.ResultPublished, reload(base+feature(".*", "generation two")).Category)
	require.NotSame(t, ex, hostActiveExecutor(t, host))
	checkFrozen()
	send("wire-backend:other", leg{}, true)
	assertInstructions("generation two")
	lastGood := hostActiveExecutor(t, host)
	require.NotEqual(t, sdkreload.ResultPublished, reload(base+feature("[", "invalid candidate")).Category)
	require.Same(t, lastGood, hostActiveExecutor(t, host), "invalid candidate cannot replace last-good generation")
	send("gpt-4o", leg{}, true)
	assertInstructions("generation two")
	require.Equal(t, sdkreload.ResultPublished, reload(base).Category)
	removed := hostActiveExecutor(t, host)
	require.Nil(t, removed.ConversationBootstrap)
	assessor, ok := removed.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.False(t, assessor.AuthorityGate.Census.Ports.SteeringWriterFactoryOccupied)
	checkFrozen()
	require.Zero(t, wire.Load(), "persisted resumed authority still blocks wire after producer removal")
	send("gpt-4o", leg{}, true)
	require.Equal(t, int32(1), wire.Load(), "fresh baseline remains wire-live after removal")
	// Removal must preserve empty markers too, even across re-enablement.
	require.Equal(t, sdkreload.ResultPublished, reload(base+feature(".*", "generation three")).Category)
	checkFrozen()
	require.Equal(t, int32(1), wire.Load())
}
