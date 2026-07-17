package runtime_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type blockingSecretGuard struct {
	evals *atomic.Int32
}

func (g *blockingSecretGuard) ID() string                           { return "block-all" }
func (g *blockingSecretGuard) Order() int                           { return 0 }
func (g *blockingSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (g *blockingSecretGuard) Evaluate(_ context.Context, call *lipapi.Call, _ secretguard.Meta, _ secretguard.Services) (secretguard.Decision, error) {
	if g.evals != nil {
		g.evals.Add(1)
	}
	needleLen := len(testkit.SyntheticOpenAIAPIKey)
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if len(p.Text) >= needleLen && strings.Contains(p.Text, testkit.SyntheticOpenAIAPIKey) {
				return secretguard.Decision{
					Outcome: secretguard.OutcomeBlock,
					Findings: []secretguard.Finding{{
						SecretRefName:   "OPENAI_API_KEY",
						SourceCategory:  secretguard.SourceCategoryProxyEnv,
						Location:        "messages[].parts[].text",
						OccurrenceCount: 1,
					}},
				}, nil
			}
		}
	}
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type unsupportedJSONTokenBlockGuard struct {
	evals *atomic.Int32
}

func (g *unsupportedJSONTokenBlockGuard) ID() string { return "block-unsupported-json-token" }
func (g *unsupportedJSONTokenBlockGuard) Order() int { return 0 }
func (g *unsupportedJSONTokenBlockGuard) FailureMode() secretguard.FailureMode {
	return secretguard.FailClosed
}
func (g *unsupportedJSONTokenBlockGuard) Evaluate(_ context.Context, call *lipapi.Call, _ secretguard.Meta, _ secretguard.Services) (secretguard.Decision, error) {
	if g.evals != nil {
		g.evals.Add(1)
	}
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if strings.Contains(p.Text, testkit.SyntheticOpenAIAPIKey) {
				return secretguard.Decision{
					Outcome:       secretguard.OutcomeBlock,
					Findings:      []secretguard.Finding{{SecretRefName: "OPENAI_API_KEY", SourceCategory: secretguard.SourceCategoryProxyEnv, Location: "messages[].parts[].text", OccurrenceCount: 1}},
					FailureKind:   "unsupported_json_token",
					FailureReason: "unsupported JSON token encountered",
				}, nil
			}
		}
	}
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type countingTrafficObs struct{ n *atomic.Int32 }

func (c *countingTrafficObs) OnObservation(_ context.Context, _ sdktraffic.Observation) error {
	if c.n != nil {
		c.n.Add(1)
	}
	return nil
}

type countingSubmit struct{ n *atomic.Int32 }

func (c *countingSubmit) ID() string                        { return "count-submit" }
func (c *countingSubmit) Order() int                        { return 0 }
func (c *countingSubmit) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (c *countingSubmit) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	if c.n != nil {
		c.n.Add(1)
	}
	return sdkhooks.SubmitDecision{}, nil
}

type voidWS2 struct{}

func (voidWS2) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{}, nil
}

func secretGuardFingerprintKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 3)
	}
	return k
}

func TestExecutor_secretGuardBlock_zeroDispatch(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		guardEvals   atomic.Int32
		submitCalls  atomic.Int32
		trafficCalls atomic.Int32
		backendOpens atomic.Int32
	)

	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{&countingSubmit{n: &submitCalls}},
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       workspace.NewResolverChain([]lipworkspace.Resolver{voidWS2{}}),
		TrafficObserver: &countingTrafficObs{n: &trafficCalls},
		SecretGuardPlane: extensions.SecretGuardPlane{
			Guards: []secretguard.Guard{&blockingSecretGuard{evals: &guardEvals}},
		},
	})

	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1800, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-sg"})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}

	stream, execErr := ex.Execute(ctx, call)
	if stream != nil {
		_, _ = lipapi.Collect(t.Context(), stream)
	}

	if backendOpens.Load() != 0 {
		t.Fatalf("backend dispatched=%d want 0 after secret-guard block", backendOpens.Load())
	}
	if submitCalls.Load() != 0 {
		t.Fatalf("submit hooks ran=%d want 0 after secret-guard block", submitCalls.Load())
	}
	if trafficCalls.Load() != 0 {
		t.Fatalf("traffic observer calls=%d want 0 after secret-guard block", trafficCalls.Load())
	}
	if guardEvals.Load() == 0 {
		t.Fatal("secret guard Evaluate was never invoked")
	}
	if execErr == nil {
		t.Fatal("expected policy denial error on block")
	}
	if !lipapi.IsPolicyDenied(execErr) {
		t.Fatalf("want policy denied, got %v", execErr)
	}
	if !strings.Contains(execErr.Error(), "start a new session") {
		t.Fatalf("client-safe denial missing session guidance: %v", execErr)
	}
	if strings.Contains(execErr.Error(), testkit.SyntheticOpenAIAPIKey) {
		t.Fatal("error must not contain synthetic secret value")
	}
}

func TestExecutor_secretGuardUnsupportedJSONTokenBlock_zeroDispatch(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		guardEvals   atomic.Int32
		submitCalls  atomic.Int32
		trafficCalls atomic.Int32
		backendOpens atomic.Int32
	)

	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{&countingSubmit{n: &submitCalls}},
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       workspace.NewResolverChain([]lipworkspace.Resolver{voidWS2{}}),
		TrafficObserver: &countingTrafficObs{n: &trafficCalls},
		SecretGuardPlane: extensions.SecretGuardPlane{
			Guards: []secretguard.Guard{&unsupportedJSONTokenBlockGuard{evals: &guardEvals}},
		},
	})

	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1804, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				backendOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-sg-unsupported"})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}

	stream, execErr := ex.Execute(ctx, call)
	if stream != nil {
		_, _ = lipapi.Collect(t.Context(), stream)
	}
	if backendOpens.Load() != 0 {
		t.Fatalf("backend dispatched=%d want 0 after unsupported-json-token block", backendOpens.Load())
	}
	if submitCalls.Load() != 0 {
		t.Fatalf("submit hooks ran=%d want 0 after unsupported-json-token block", submitCalls.Load())
	}
	if trafficCalls.Load() != 0 {
		t.Fatalf("traffic observer calls=%d want 0 after unsupported-json-token block", trafficCalls.Load())
	}
	if guardEvals.Load() == 0 {
		t.Fatal("secret guard Evaluate was never invoked")
	}
	if execErr == nil {
		t.Fatal("expected policy denial error on unsupported-json-token block")
	}
	if !lipapi.IsPolicyDenied(execErr) {
		t.Fatalf("want policy denied, got %v", execErr)
	}
	if !strings.Contains(execErr.Error(), "start a new session") {
		t.Fatalf("client-safe denial missing session guidance: %v", execErr)
	}
	if strings.Contains(execErr.Error(), testkit.SyntheticOpenAIAPIKey) {
		t.Fatal("error must not contain synthetic secret value")
	}
}
