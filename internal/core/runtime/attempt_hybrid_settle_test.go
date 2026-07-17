package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type controllableAttemptProvider struct {
	id           string
	admitCalls   atomic.Int32
	settleCalls  atomic.Int32
	releaseCalls atomic.Int32
	failSettle   atomic.Bool
	settleErrMsg atomic.Value // string
	lastSettle   atomic.Value // authority.AttemptSettlement
}

type hybridAuthorityClock struct{ at time.Time }

func (c hybridAuthorityClock) Now() time.Time { return c.at }

func (p *controllableAttemptProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Reservations: []authority.Reservation{{
			Handle: p.id + "-h",
			Kind:   authority.ReservationSpend,
			Money:  &economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		}},
	}, nil
}

func (p *controllableAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	cp := in
	cp.Handles = append([]string(nil), in.Handles...)
	cp.Facts = append([]metering.Fact(nil), in.Facts...)
	p.lastSettle.Store(cp)
	if p.failSettle.Load() {
		msg, _ := p.settleErrMsg.Load().(string)
		if msg == "" {
			msg = "settle failed"
		}
		return authority.Settlement{}, errors.New(msg)
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *controllableAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	p.releaseCalls.Add(1)
	return nil
}

func (p *controllableAttemptProvider) setSettleFail(fail bool, msg string) {
	p.failSettle.Store(fail)
	if msg != "" {
		p.settleErrMsg.Store(msg)
	}
}

func mixedCoordinatorExecutor(builtin, external authority.AttemptProvider) *Executor {
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "usage-authority-attempt", Class: authoritycoord.AttemptPriorityHardSpend, Provider: builtin, Strength: authority.StrengthRequired},
			{ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: external, Strength: authority.StrengthRequired},
		},
	}
	return ex
}

func admitMixed(t *testing.T, ex *Executor, blegID string) attemptAuthorityState {
	t.Helper()
	state, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-hybrid",
		"a-1",
		b2bua.BLegRecord{BLegID: blegID, Seq: 1},
		lipapi.Call{ID: "req-hybrid"},
		authorityCandidate(),
		accountingpreflight.Decision{},
		false,
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return state
}

func TestAuthorityLifecycle_ExternalSettleReceivesFactsOutcomeSurfaced(t *testing.T) {
	t.Parallel()
	builtin := &controllableAttemptProvider{id: "usage-authority-attempt"}
	external := &controllableAttemptProvider{id: "enterprise-attempt"}
	ex := mixedCoordinatorExecutor(builtin, external)
	state := admitMixed(t, ex, "b-facts")
	lifecycle := ex.newAttemptAuthorityLifecycle(state, authorityCandidate())
	lifecycle.outputCommitted.Store(true)

	usage := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 7, TotalTokens: 18,
		CostNanoUnits: 42, Currency: "USD", CostPresent: true, CostSource: "provider",
	}
	if !lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("expected settle applied")
	}
	got, ok := external.lastSettle.Load().(authority.AttemptSettlement)
	if !ok {
		t.Fatal("external settle not recorded")
	}
	if got.Outcome != metering.AttemptOutcomeWinner {
		t.Fatalf("outcome=%q want winner", got.Outcome)
	}
	if got.Surfaced != metering.SurfacedYes {
		t.Fatalf("surfaced=%q want yes", got.Surfaced)
	}
	if len(got.Facts) == 0 {
		t.Fatal("expected Facts on external AttemptSettlement")
	}
	if len(got.Facts[0].Quantities) == 0 {
		t.Fatal("expected quantity evidence on settle Facts")
	}
	if len(got.Rated) == 0 || !got.Rated[0].Money.Present || got.Rated[0].Money.NanoUnits != 42 {
		t.Fatalf("rated=%+v want cost evidence", got.Rated)
	}
	if external.releaseCalls.Load() != 0 {
		t.Fatalf("release after successful settle: %d", external.releaseCalls.Load())
	}
}

func TestAuthorityLifecycle_ExternalSettleFailureBeforeOutputReleasesOnlyUnfinished(t *testing.T) {
	t.Parallel()
	builtin := &controllableAttemptProvider{id: "usage-authority-attempt"}
	external := &controllableAttemptProvider{id: "enterprise-attempt"}
	external.setSettleFail(true, "enterprise settle boom")
	ex := mixedCoordinatorExecutor(builtin, external)
	state := admitMixed(t, ex, "b-pre-out")
	lifecycle := ex.newAttemptAuthorityLifecycle(state, authorityCandidate())
	lifecycle.backendAttempted.Store(true)
	lifecycle.outputCommitted.Store(false)

	_ = lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, lipapi.Event{Kind: lipapi.EventUsageDelta}, false)
	if builtin.settleCalls.Load() != 1 {
		t.Fatalf("builtin settle calls=%d want 1", builtin.settleCalls.Load())
	}
	if external.settleCalls.Load() != 1 {
		t.Fatalf("external settle calls=%d want 1", external.settleCalls.Load())
	}
	if builtin.releaseCalls.Load() != 0 {
		t.Fatalf("successful builtin settle must not be released; releases=%d", builtin.releaseCalls.Load())
	}
	if external.releaseCalls.Load() != 1 {
		t.Fatalf("unfinished external must release before output; releases=%d", external.releaseCalls.Load())
	}
	if !lifecycle.Settled() {
		t.Fatal("terminal must close after settling builtin and releasing unfinished external")
	}
}

func TestAuthorityLifecycle_ExternalSettleFailureAfterOutputRetriesUnfinishedOnly(t *testing.T) {
	t.Parallel()
	builtin := &controllableAttemptProvider{id: "usage-authority-attempt"}
	external := &controllableAttemptProvider{id: "enterprise-attempt"}
	external.setSettleFail(true, "transient")
	ex := mixedCoordinatorExecutor(builtin, external)
	state := admitMixed(t, ex, "b-retry")
	lifecycle := ex.newAttemptAuthorityLifecycle(state, authorityCandidate())
	lifecycle.outputCommitted.Store(true)

	usage := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 1, TotalTokens: 4}
	if lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("first settle should fail open for retry")
	}
	if builtin.settleCalls.Load() != 1 || external.settleCalls.Load() != 1 {
		t.Fatalf("first settle calls builtin=%d external=%d", builtin.settleCalls.Load(), external.settleCalls.Load())
	}
	if builtin.releaseCalls.Load()+external.releaseCalls.Load() != 0 {
		t.Fatal("after output committed, settle failure must not release")
	}

	external.setSettleFail(false, "")
	if !lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("retry must apply after external recovers")
	}
	if builtin.settleCalls.Load() != 1 {
		t.Fatalf("builtin must not re-settle; calls=%d", builtin.settleCalls.Load())
	}
	if external.settleCalls.Load() != 2 {
		t.Fatalf("external retry calls=%d want 2", external.settleCalls.Load())
	}
	if external.releaseCalls.Load() != 0 {
		t.Fatalf("no release after successful settle; releases=%d", external.releaseCalls.Load())
	}
}

func TestAuthorityLifecycle_CoordinatorPreservesBuiltinReservationMetadata(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "attempt-input",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeStrict,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 100},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	rows, err := authoritystore.LimitRowsFromRules([]authoritydomain.Rule{rule}, now)
	if err != nil {
		t.Fatalf("limit rows: %v", err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "hybrid-metadata",
		Backing:   authoritydomain.BackingCapabilityAtomic,
		LimitRows: rows,
		Readiness: authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
	})
	svc := authorityapp.NewService(authorityRuleSource{snapshot: authorityapp.RuleSnapshot{
		Status:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
		Rules:     []authoritydomain.Rule{rule},
		FetchedAt: now,
	}}, store, nil, hybridAuthorityClock{at: now})
	req, att := buildDefaultCoordinators(svc, nil)
	_ = req
	external := &controllableAttemptProvider{id: "enterprise-attempt"}
	att.Slots = append(att.Slots, authoritycoord.AttemptSlot{
		ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: external, Strength: authority.StrengthRequired,
	})
	ex := &Executor{}
	ex.UsageAuthority = svc
	ex.AttemptCoordinator = att

	state, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-metadata",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-metadata", Seq: 1},
		lipapi.Call{ID: "req-metadata"},
		authorityCandidate(),
		accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 3}},
		false,
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got := state.admissionResult.Reservations; len(got) != 1 || got[0].RuleID != rule.ID || got[0].ReservedAmount != (authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 3}) {
		t.Fatalf("built-in reservation metadata = %#v", got)
	}

	lifecycle := ex.newAttemptAuthorityLifecycle(state, authorityCandidate())
	lifecycle.outputCommitted.Store(true)
	usage := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 2, TotalTokens: 2}
	if !lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("settle must reconcile built-in reservation and settle external provider")
	}
	if external.settleCalls.Load() != 1 {
		t.Fatalf("external settle calls=%d want 1", external.settleCalls.Load())
	}
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{BackendID: "backend-1"},
		RuleID: rule.ID, Unit: string(rule.Unit), Limit: 10, Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("limit status: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Reserved != 0 || page.Items[0].Consumed != 2 {
		t.Fatalf("settled limit status=%#v want reserved=0 consumed=2", page.Items)
	}
}
