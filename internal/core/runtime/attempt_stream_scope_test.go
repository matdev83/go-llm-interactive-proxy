package runtime_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

type scopeCaptureUsage struct {
	mu     sync.Mutex
	events []usage.Event
}

func (c *scopeCaptureUsage) OnUsage(_ context.Context, ev usage.Event) error {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	return nil
}

type scopeCaptureTraffic struct {
	mu  sync.Mutex
	obs []sdktraffic.Observation
}

func (c *scopeCaptureTraffic) OnObservation(_ context.Context, ev sdktraffic.Observation) error {
	c.mu.Lock()
	c.obs = append(c.obs, ev)
	c.mu.Unlock()
	return nil
}

func scopeEmissionExecutor(t *testing.T, uobs usage.Observer, tobs sdktraffic.Observer) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		UsageObserver:   uobs,
		TrafficObserver: tobs,
	})
	return &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{
							Kind:         lipapi.EventUsageDelta,
							InputTokens:  3,
							RawUsageJSON: `{"usage":true}`,
						},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
}

// TestRuntime_usageEvidence_carriesScope proves the usage observer receives the authoritative
// scope from execctx views and the legacy PrincipalID matches the scope principal id when scope
// is present (requirements 6.4, 6.5, 7.3).
func TestRuntime_usageEvidence_carriesScope(t *testing.T) {
	t.Parallel()
	uobs := &scopeCaptureUsage{}
	ex := scopeEmissionExecutor(t, uobs, nil)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user"),
		TenantID:    scope.Known("t1"),
		Roles:       []string{"admin"},
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	uobs.mu.Lock()
	defer uobs.mu.Unlock()
	if len(uobs.events) == 0 {
		t.Fatal("expected usage events")
	}
	ev := uobs.events[0]
	if !ev.Scope.PrincipalID.Equal(scope.Known("scope-user")) {
		t.Fatalf("usage Scope.PrincipalID: %+v", ev.Scope.PrincipalID)
	}
	if !ev.Scope.TenantID.Equal(scope.Known("t1")) {
		t.Fatalf("usage Scope.TenantID: %+v", ev.Scope.TenantID)
	}
	if ev.PrincipalID != "scope-user" {
		t.Fatalf("legacy PrincipalID %q must match scope principal id", ev.PrincipalID)
	}
}

// TestRuntime_trafficEvidence_carriesScope proves traffic observations carry the authoritative
// scope and legacy PrincipalID matches the scope principal id on the CTP leg where PrincipalID
// is populated (requirements 6.3, 6.4, 6.5, 7.3).
func TestRuntime_trafficEvidence_carriesScope(t *testing.T) {
	t.Parallel()
	tobs := &scopeCaptureTraffic{}
	ex := scopeEmissionExecutor(t, nil, tobs)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user-leak-marker"),
		TenantID:    scope.Known("tenant-leak-marker"),
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	tobs.mu.Lock()
	defer tobs.mu.Unlock()
	if len(tobs.obs) == 0 {
		t.Fatal("expected traffic observations")
	}
	var ctp *sdktraffic.Observation
	for i := range tobs.obs {
		if tobs.obs[i].Leg == sdktraffic.LegCTP {
			ctp = &tobs.obs[i]
			break
		}
	}
	if ctp == nil {
		t.Fatalf("no CTP observation among %d obs", len(tobs.obs))
	}
	if !ctp.Scope.PrincipalID.Equal(scope.Known("scope-user-leak-marker")) {
		t.Fatalf("CTP Scope.PrincipalID: %+v", ctp.Scope.PrincipalID)
	}
	if ctp.PrincipalID != "scope-user-leak-marker" {
		t.Fatalf("CTP legacy PrincipalID %q must match scope principal id", ctp.PrincipalID)
	}
}

// TestRuntime_trafficEvidence_backendPayloadDoesNotLeakScope proves scope is not forwarded into
// backend provider payloads: the PTB observation body (the marshaled backend call) must not
// contain scope principal or tenant values (requirements 7.4, 7.1).
func TestRuntime_trafficEvidence_backendPayloadDoesNotLeakScope(t *testing.T) {
	t.Parallel()
	tobs := &scopeCaptureTraffic{}
	ex := scopeEmissionExecutor(t, nil, tobs)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("leak-principal"),
		TenantID:    scope.Known("leak-tenant"),
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	tobs.mu.Lock()
	defer tobs.mu.Unlock()
	for _, o := range tobs.obs {
		if o.Leg != sdktraffic.LegPTB {
			continue
		}
		if bytes.Contains(o.Body, []byte("leak-principal")) || bytes.Contains(o.Body, []byte("leak-tenant")) {
			t.Fatalf("scope values leaked into backend payload (leg %s): %s", o.Leg, string(o.Body))
		}
		if !o.Scope.PrincipalID.Equal(scope.Known("leak-principal")) {
			t.Fatalf("PTB Scope.PrincipalID: %+v", o.Scope.PrincipalID)
		}
	}
}

// TestRuntime_trafficEvidence_allLegsPrincipalIDMatchesScope proves that on every traffic leg
// carrying a known scope (PTB, BTP, PTC), the legacy PrincipalID is populated from the scope
// principal id and matches Scope.PrincipalID.String() (requirements 6.3, 6.5, 7.3).
func TestRuntime_trafficEvidence_allLegsPrincipalIDMatchesScope(t *testing.T) {
	t.Parallel()
	tobs := &scopeCaptureTraffic{}
	ex := scopeEmissionExecutor(t, nil, tobs)
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user"),
		TenantID:    scope.Known("t1"),
	}
	ctx := scope.WithScope(context.Background(), trusted)
	stream, err := ex.Execute(ctx, &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	tobs.mu.Lock()
	defer tobs.mu.Unlock()
	if len(tobs.obs) == 0 {
		t.Fatal("expected traffic observations")
	}
	checked := 0
	for _, o := range tobs.obs {
		if !o.Scope.PrincipalID.IsKnown() {
			continue
		}
		checked++
		if o.PrincipalID != o.Scope.PrincipalID.String() {
			t.Fatalf("leg %s legacy PrincipalID %q must match scope principal id %q", o.Leg, o.PrincipalID, o.Scope.PrincipalID.String())
		}
		if bytes.Contains(o.Body, []byte("scope-user-leak-marker")) || bytes.Contains(o.Body, []byte("tenant-leak-marker")) {
			t.Fatalf("scope values leaked into payload (leg %s): %s", o.Leg, string(o.Body))
		}
	}
	if checked == 0 {
		t.Fatalf("no observations carried a known scope among %d", len(tobs.obs))
	}
}

// TestRuntime_usageEvidence_syntheticLocalScopeMatchesPrincipal proves that when no trusted
// scope is attached and the executor's existing local-mode synthetic principal is active, the
// usage event carries the synthetic local scope and the legacy PrincipalID matches the scope
// principal id (requirements 1.4, 6.4, 7.3).
func TestRuntime_usageEvidence_syntheticLocalScopeMatchesPrincipal(t *testing.T) {
	t.Parallel()
	uobs := &scopeCaptureUsage{}
	ex := scopeEmissionExecutor(t, uobs, nil)
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	uobs.mu.Lock()
	defer uobs.mu.Unlock()
	if len(uobs.events) == 0 {
		t.Fatal("expected usage events")
	}
	ev := uobs.events[0]
	if !ev.Scope.PrincipalID.IsKnown() {
		t.Fatalf("synthetic local scope must be present: %+v", ev.Scope.PrincipalID)
	}
	if ev.Scope.SubjectKind != scope.SubjectLocal {
		t.Fatalf("SubjectKind: got %v want local", ev.Scope.SubjectKind)
	}
	if ev.PrincipalID != ev.Scope.PrincipalID.String() {
		t.Fatalf("legacy PrincipalID %q must match scope principal id %q", ev.PrincipalID, ev.Scope.PrincipalID.String())
	}
}

// TestRuntime_usageEvidence_recoveryDrainCarriesScope proves stream-recovery synthesized
// response_finished events use the same scoped recv context as the normal stream path.
func TestRuntime_usageEvidence_recoveryDrainCarriesScope(t *testing.T) {
	t.Parallel()
	uobs := &scopeCaptureUsage{}
	ex := scopeEmissionExecutor(t, uobs, nil)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true}
	ex.StreamUsage = accountingstream.New(scopeFixedCounter{}, accountingstream.Config{})
	ex.Backends["openai"] = execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "partial"},
			}), nil
		},
	}
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("recovery-scope-user"),
		TenantID:    scope.Known("t-recovery"),
	}
	stream, err := ex.Execute(scope.WithScope(context.Background(), trusted), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	uobs.mu.Lock()
	defer uobs.mu.Unlock()
	if len(uobs.events) == 0 {
		t.Fatal("expected usage event from recovery drain")
	}
	ev := uobs.events[len(uobs.events)-1]
	if !ev.Scope.PrincipalID.Equal(scope.Known("recovery-scope-user")) {
		t.Fatalf("usage Scope.PrincipalID: %+v", ev.Scope.PrincipalID)
	}
	if ev.PrincipalID != "recovery-scope-user" {
		t.Fatalf("legacy PrincipalID %q must match recovery scope principal", ev.PrincipalID)
	}
}

type scopeFixedCounter struct{}

func (scopeFixedCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 3}, nil
}

func (scopeFixedCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{OutputTokens: 4}, nil
}
