package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

type recordingAttemptProvider struct {
	id           string
	admitCalls   atomic.Int32
	settleCalls  atomic.Int32
	releaseCalls atomic.Int32
	lastSettle   atomic.Value // []string
	lastSettleIn atomic.Value // authority.AttemptSettlement
}

func (p *recordingAttemptProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	return authority.Decision{
		Kind:         authority.DecisionAllow,
		Reservations: []authority.Reservation{{Handle: p.id + "-h", Kind: authority.ReservationSpend}},
	}, nil
}

func (p *recordingAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	p.lastSettle.Store(append([]string(nil), in.Handles...))
	p.lastSettleIn.Store(in)
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (p *recordingAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	p.releaseCalls.Add(1)
	return nil
}

// Issue (1): external AttemptProviders must admit even when built-in UsageAuthority is disabled.
func TestAdmitAttemptAuthority_InvokesExternalProvidersWhenUsageAuthorityNil(t *testing.T) {
	t.Parallel()

	external := &recordingAttemptProvider{id: "enterprise-attempt"}
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID:       "enterprise-attempt",
			Class:    authoritycoord.AttemptPriorityHardSpend,
			Provider: external,
			Strength: authority.StrengthRequired,
		}},
	}

	state, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-ext",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-ext", Seq: 1},
		lipapi.Call{ID: "req-ext"},
		authorityCandidate(),
		accountingpreflight.Decision{},
		false,
	)
	if err != nil {
		t.Fatalf("admitAttemptAuthority: %v", err)
	}
	if external.admitCalls.Load() != 1 {
		t.Fatalf("external AdmitAttempt calls=%d want 1 (UsageAuthority nil must not skip AttemptCoordinator)", external.admitCalls.Load())
	}
	if !state.admissionResult.Reserved {
		t.Fatal("expected reserved admission from external attempt provider")
	}
	if !state.viaCoordinator {
		t.Fatal("expected viaCoordinator state when admitted through AttemptCoordinator")
	}
}

// Issue (2): mixed built-in + external reservations settle/release through owning providers
// via AttemptCoordinator rather than flattened raw handles into UsageAuthority only.
func TestAuthorityLifecycle_CoordinatorSettleAndReleaseViaOwningProviders(t *testing.T) {
	t.Parallel()

	builtin := &recordingAttemptProvider{id: "usage-authority-attempt"}
	external := &recordingAttemptProvider{id: "enterprise-attempt"}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "usage-authority-attempt", Class: authoritycoord.AttemptPriorityHardSpend, Provider: builtin, Strength: authority.StrengthRequired},
			{ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: external, Strength: authority.StrengthRequired},
		},
	}
	ex := &Executor{}
	ex.AttemptCoordinator = coord

	state, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-mixed",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-mixed", Seq: 1},
		lipapi.Call{ID: "req-mixed"},
		authorityCandidate(),
		accountingpreflight.Decision{},
		false,
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	lifecycle := ex.newAttemptAuthorityLifecycle(state, authorityCandidate())
	if applied := lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, lipapi.Event{}, false); !applied {
		t.Fatal("expected coordinator settle to apply")
	}
	if builtin.settleCalls.Load() != 1 || external.settleCalls.Load() != 1 {
		t.Fatalf("settle calls builtin=%d external=%d want 1 each", builtin.settleCalls.Load(), external.settleCalls.Load())
	}
	builtinHandles, _ := builtin.lastSettle.Load().([]string)
	externalHandles, _ := external.lastSettle.Load().([]string)
	if len(builtinHandles) != 1 || builtinHandles[0] != "usage-authority-attempt-h" {
		t.Fatalf("builtin settle handles=%v", builtinHandles)
	}
	if len(externalHandles) != 1 || externalHandles[0] != "enterprise-attempt-h" {
		t.Fatalf("external settle handles=%v", externalHandles)
	}

	// Fresh admit + release path (loser / pre-open cleanup).
	state2, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-mixed-2",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-mixed-2", Seq: 2},
		lipapi.Call{ID: "req-mixed-2"},
		authorityCandidate(),
		accountingpreflight.Decision{},
		false,
	)
	if err != nil {
		t.Fatalf("admit2: %v", err)
	}
	lifecycle2 := ex.newAttemptAuthorityLifecycle(state2, authorityCandidate())
	lifecycle2.backendAttempted.Store(false)
	lifecycle2.Release(context.Background(), authorityapp.ReleaseKindLosing)
	if builtin.releaseCalls.Load() < 1 || external.releaseCalls.Load() < 1 {
		t.Fatalf("release calls builtin=%d external=%d want >=1 each", builtin.releaseCalls.Load(), external.releaseCalls.Load())
	}
}
