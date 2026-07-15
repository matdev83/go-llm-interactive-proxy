package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type countingAdmitRequestProvider struct {
	last atomic.Value // authority.RequestAdmission
}

func (p *countingAdmitRequestProvider) AdmitRequest(_ context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	cp := in
	cp.Exposure.Quantities = append([]metering.Quantity(nil), in.Exposure.Quantities...)
	p.last.Store(cp)
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "qty-req"}, nil
}

func (p *countingAdmitRequestProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (p *countingAdmitRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

type countingAdmitAttemptProvider struct {
	last       atomic.Value // authority.AttemptAdmission
	admitCalls atomic.Int32
}

func (p *countingAdmitAttemptProvider) AdmitAttempt(_ context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	cp := in
	cp.Exposure.Quantities = append([]metering.Quantity(nil), in.Exposure.Quantities...)
	p.last.Store(cp)
	return authority.Decision{
		Kind:         authority.DecisionAllow,
		ProviderID:   "qty-att",
		Reservations: []authority.Reservation{{Handle: "qty-h", Kind: authority.ReservationSpend}},
	}, nil
}

func (p *countingAdmitAttemptProvider) SettleAttempt(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (p *countingAdmitAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type ingressQtyPartHook struct {
	fn func(context.Context, *lipapi.Call, sdk.PartMeta) error
}

func (s *ingressQtyPartHook) ID() string                   { return "ingress-qty-hook" }
func (s *ingressQtyPartHook) Order() int                   { return 1 }
func (s *ingressQtyPartHook) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *ingressQtyPartHook) HandleRequestParts(ctx context.Context, call *lipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

type localCountCallOnly struct {
	fn func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)
}

func (c localCountCallOnly) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, nil
}

func (c localCountCallOnly) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return c.fn(ctx, in)
}

func (c localCountCallOnly) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, nil
}

// Request authority must see non-empty input_token from the immutable FE call via
// existing token counting (not byte/rune heuristics) before transforms (req 4.1).
func TestRequestAdmit_ReceivesFrontendIngressInputToken(t *testing.T) {
	t.Parallel()

	prov := &countingAdmitRequestProvider{}
	ex := &Executor{}
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "qty-req", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	ex.AdminCountService = accountingapp.NewService(
		accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly},
		nil,
		localCountCallOnly{fn: func(_ context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			if len(in.Call.Messages) == 0 {
				t.Fatal("count must use frozen FE call messages")
			}
			return accountingapp.CountResult{InputTokens: 123, TotalTokens: 123, TotalTokensPresent: true}, nil
		}},
	)

	holder := &checkpoint.RequestHolder{}
	maxOut := 64
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{
			ID: "req-fe-qty",
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("count me")},
			}},
			Options: lipapi.GenerationOptions{MaxOutputTokens: &maxOut},
		},
		CheckpointID: "fe-ingress:req-fe-qty",
		StreamID:     "fe-ingress:req-fe-qty",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken); ok {
		t.Fatal("capture must defer input_token until counting")
	}

	ctx := withMeteringHolder(context.Background(), holder)
	_, err = ex.admitRequestAuthorityOnce(ctx, "req-fe-qty", "a-1", "trace-fe-qty", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admitRequest: %v", err)
	}
	got, _ := prov.last.Load().(authority.RequestAdmission)
	in, ok := checkpoint.QuantityComponentValue(got.Exposure.Quantities, metering.ComponentInputToken)
	if !ok || in != 123 {
		t.Fatalf("request admit Exposure.Quantities input_token=%d ok=%v want 123 from FE count; quantities=%+v", in, ok, got.Exposure.Quantities)
	}
	out, ok := checkpoint.QuantityComponentValue(got.Exposure.Quantities, metering.ComponentOutputToken)
	if !ok || out != int64(maxOut) {
		t.Fatalf("conservative output bound must remain %d, got %d ok=%v", maxOut, out, ok)
	}
	if holder.FrontendIngress.Public.CheckpointID != "fe-ingress:req-fe-qty" {
		t.Fatal("deferred counting must preserve CheckpointID")
	}
}

// Attempt authority must admit from BE-ingress quantities frozen after transforms
// and before Open (reqs 2.2, 5.1). Store must precede authorization.
func TestAttemptAdmit_ReceivesBackendIngressQuantitiesBeforeOpen(t *testing.T) {
	t.Parallel()

	attProv := &countingAdmitAttemptProvider{}
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, nil)
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "qty-att", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}
	ex.Preflight = accountingpreflight.NewChecker(authorityAdmissionCountFunc(func(_ context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		// Distinctive post-hook count: only the final BE-shaped call includes the hook marker.
		input := 1
		for _, m := range in.Call.Messages {
			for _, part := range m.Parts {
				if part.Text == "hook-added" {
					input = 777
				}
			}
		}
		return accountingapp.CountResult{InputTokens: input, OutputTokens: 10, TotalTokens: input + 10, TotalTokensPresent: true}, nil
	}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})

	bus := hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			&ingressQtyPartHook{fn: func(_ context.Context, call *lipapi.Call, _ sdk.PartMeta) error {
				call.Messages = append(call.Messages, lipapi.Message{
					Role:  lipapi.RoleSystem,
					Parts: []lipapi.Part{lipapi.TextPart("hook-added")},
				})
				return nil
			}},
		},
	})
	ex.Bus = bus

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-be-qty"},
		CheckpointID: "fe-ingress:req-be-qty",
		StreamID:     "fe-ingress:req-be-qty",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	params := attemptOpenParams{
		ctx:     withMeteringHolder(context.Background(), holder),
		bus:     bus,
		traceID: "trace-be-qty",
		aLegID:  aLegID,
		baseline: lipapi.Call{
			ID:    "req-be-qty",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("be basis")},
			}},
		},
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      routing.NewSeededRng(1),
		budget:   &attemptBudget{max: 3},
	}

	out, err := ex.openPlannedCandidate(params, authorityCandidate(), nil, "", false)
	if err != nil {
		t.Fatalf("openPlannedCandidate: %v", err)
	}
	if out.stream == nil {
		t.Fatal("expected opened stream")
	}
	if backend.openCalls.Load() < 1 {
		t.Fatal("backend must open after successful admit")
	}
	if attProv.admitCalls.Load() < 1 {
		t.Fatal("attempt provider must admit")
	}
	got, _ := attProv.last.Load().(authority.AttemptAdmission)
	if got.Exposure.Boundary != metering.BoundaryBackendIngress {
		t.Fatalf("boundary=%q", got.Exposure.Boundary)
	}
	in, ok := checkpoint.QuantityComponentValue(got.Exposure.Quantities, metering.ComponentInputToken)
	if !ok || in != 777 {
		t.Fatalf("attempt admit input_token=%d ok=%v want 777 from post-hook BE ingress count (pre-hook would be 1); quantities=%+v", in, ok, got.Exposure.Quantities)
	}
	be := holder.BackendIngressFor(out.bleg.BLegID)
	if be == nil {
		t.Fatal("BE ingress must be stored for the attempt")
	}
	if beIn, ok := checkpoint.QuantityComponentValue(be.Public.Quantities, metering.ComponentInputToken); !ok || beIn != 777 {
		t.Fatalf("stored BE quantities input=%d ok=%v", beIn, ok)
	}
	if len(be.Call.Messages) < 2 {
		t.Fatalf("BE freeze must include post-hook messages; got %d", len(be.Call.Messages))
	}
}
