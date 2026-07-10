package app

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// principalScope returns a fully populated principal scope used by service tests
// that exercise the policydecision / controlplane projection paths.
func principalScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectService,
		PrincipalID:    scope.Known("principal-1"),
		DisplayName:    scope.Known("principal"),
		AuthMethod:     scope.Known("token"),
		CredentialID:   scope.Known("credential-1"),
		TenantID:       scope.Known("tenant-1"),
		OrganizationID: scope.Known("org-1"),
		WorkspaceID:    scope.Known("workspace-1"),
		ProjectID:      scope.Known(""),
		DepartmentID:   scope.Unknown(),
		CostCenterID:   scope.Known("cost-center-1"),
		Origin:         scope.OriginClient,
		ParentTraceID:  scope.Known("parent-trace"),
	}
}

// fixedClock is a deterministic Clock used to keep admission/settlement
// timestamps reproducible across test runs.
type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

// fakeRuleSource is a minimal RuleSource stub used by service tests to
// script the immutable rule snapshot seen by the app layer.
type fakeRuleSource struct {
	snapshot RuleSnapshot
	err      error
}

func (f *fakeRuleSource) Snapshot(context.Context) (RuleSnapshot, error) {
	return f.snapshot, f.err
}

// fakeEvidenceSink is a recording EvidenceSink used to inspect the
// policydecision.Record and controlplane.Event emitted by admission,
// settlement, and release.
type fakeEvidenceSink struct {
	policy     []policydecision.Record
	accounting []controlplane.Event
}

func (f *fakeEvidenceSink) RecordPolicyDecision(_ context.Context, record policydecision.Record) error {
	f.policy = append(f.policy, record)
	return nil
}

func (f *fakeEvidenceSink) RecordAccountingAuthority(_ context.Context, ev controlplane.Event) error {
	f.accounting = append(f.accounting, ev)
	return nil
}

// fakeStateStore is a StateStore stub that records every Reserve/Settle/Release
// command and supports a capacity-based reservation limiter for rate-style tests.
type fakeStateStore struct {
	readiness    domain.AuthorityStatus
	readinessErr error

	reserveCalls []ReserveCommand
	settleCalls  []SettleCommand
	releaseCalls []ReleaseCommand

	reserveResult ReserveResult
	settleResult  SettleResult
	releaseResult ReleaseResult
	limitPage     controlplane.Page[controlplane.AccountingLimitStatusRow]
	decisionPage  controlplane.Page[controlplane.AccountingDecisionRow]
	reservations  map[string]ReserveResult
	settlements   map[string]SettleResult
	releases      map[string]ReleaseResult

	capacityLimit      int64
	cumulativeReserved int64
}

func newFakeStateStore() *fakeStateStore {
	return &fakeStateStore{
		reservations: make(map[string]ReserveResult),
		settlements:  make(map[string]SettleResult),
		releases:     make(map[string]ReleaseResult),
	}
}

func (f *fakeStateStore) Reserve(_ context.Context, cmd ReserveCommand) (ReserveResult, error) {
	f.reserveCalls = append(f.reserveCalls, cmd)
	if cmd.EstimateOnly {
		return ReserveResult{}, nil
	}
	if f.capacityLimit > 0 {
		amount := cmd.Request.Value
		if amount <= 0 {
			amount = 1
		}
		if f.cumulativeReserved+amount > f.capacityLimit {
			return ReserveResult{}, WrapError(ErrReservationConflict, "reserve", errors.New("strict reservation would exceed remaining capacity"))
		}
		f.cumulativeReserved += amount
		result := f.reserveResult
		if result.ReservationID == "" {
			result.ReservationID = cmd.ReservationKey.String()
		}
		result.Applied = true
		result.ReservedAmount = cmd.Request
		f.reservations[cmd.ReservationKey.String()] = result
		return result, nil
	}
	if f.reserveResult.ReservationID == "" {
		f.reserveResult.ReservationID = "reservation-1"
	}
	f.reservations[cmd.ReservationKey.String()] = f.reserveResult
	return f.reserveResult, nil
}

func (f *fakeStateStore) Settle(_ context.Context, cmd SettleCommand) (SettleResult, error) {
	f.settleCalls = append(f.settleCalls, cmd)
	if _, ok := f.settlements[cmd.SettlementKey.String()]; ok {
		return SettleResult{}, nil
	}
	if f.settleResult.Applied && f.settleResult.ReservationID == "" {
		f.settleResult.ReservationID = cmd.ReservationID
	}
	if !f.settleResult.Applied {
		reserved := cmd.ReservedUsage
		actual := cmd.FinalUsage
		f.settleResult = SettleResult{Applied: true, ReservationID: cmd.ReservationID}
		if actual.Value < reserved.Value {
			f.settleResult.ReleasedDelta = domain.Amount{Unit: reserved.Unit, Value: reserved.Value - actual.Value, Currency: reserved.Currency}
		}
		if actual.Value > reserved.Value {
			f.settleResult.OverageDelta = domain.Amount{Unit: actual.Unit, Value: actual.Value - reserved.Value, Currency: actual.Currency}
		}
	}
	f.settlements[cmd.SettlementKey.String()] = f.settleResult
	return f.settleResult, nil
}

func (f *fakeStateStore) Release(_ context.Context, cmd ReleaseCommand) (ReleaseResult, error) {
	f.releaseCalls = append(f.releaseCalls, cmd)
	if _, ok := f.releases[cmd.ReleaseKey.String()]; ok {
		return ReleaseResult{}, nil
	}
	if !f.releaseResult.Applied {
		f.releaseResult = ReleaseResult{Applied: true, ReservationID: cmd.ReservationID, ReleasedDelta: cmd.Amount}
	}
	f.releases[cmd.ReleaseKey.String()] = f.releaseResult
	return f.releaseResult, nil
}

func (f *fakeStateStore) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return f.limitPage, nil
}

func (f *fakeStateStore) DecisionHistory(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return f.decisionPage, nil
}

func (f *fakeStateStore) CheckReadiness(context.Context) (domain.AuthorityStatus, error) {
	return f.readiness, f.readinessErr
}
