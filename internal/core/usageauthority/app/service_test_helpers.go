package app

import (
	"context"
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

	reserveErr            error
	reserveErrors         []error
	settleErr             error
	releaseErr            error
	releaseWaitForContext bool
	releaseContextErr     error
	reserveCalls          []ReserveCommand
	settleCalls           []SettleCommand
	releaseCalls          []ReleaseCommand
	applyUsageCalls       []ApplyUsageCommand

	reserveResult  ReserveResult
	reserveResults []ReserveResult
	settleResult   SettleResult
	releaseResult  ReleaseResult
	limitPage      controlplane.Page[controlplane.AccountingLimitStatusRow]
	limitPages     []controlplane.Page[controlplane.AccountingLimitStatusRow]
	limitCalls     int
	limitQueries   []controlplane.AccountingLimitStatusQuery
	activeLimitRow controlplane.AccountingLimitStatusRow
	activeLimitOK  bool
	activeLimitErr error
	activeQueries  []ActiveLimitQuery
	decisionPage   controlplane.Page[controlplane.AccountingDecisionRow]
	reservations   map[string]ReserveResult
	settlements    map[string]SettleResult
	releases       map[string]ReleaseResult

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
	if idx := len(f.reserveCalls) - 1; idx < len(f.reserveErrors) && f.reserveErrors[idx] != nil {
		return ReserveResult{}, f.reserveErrors[idx]
	}
	if f.reserveErr != nil {
		return ReserveResult{}, f.reserveErr
	}
	if idx := len(f.reserveCalls) - 1; idx < len(f.reserveResults) {
		result := f.reserveResults[idx]
		if result.ReservationID == "" {
			result.ReservationID = cmd.ReservationKey.String()
		}
		if result.Applied && result.ReservedAmount.Unit == "" {
			result.ReservedAmount = cmd.Request
		}
		if result.Applied {
			f.reservations[cmd.ReservationKey.String()] = result
		}
		return result, nil
	}
	if cmd.EstimateOnly {
		return ReserveResult{}, nil
	}
	if f.capacityLimit > 0 {
		amount := cmd.Request.Value
		if amount <= 0 {
			amount = 1
		}
		if f.cumulativeReserved+amount > f.capacityLimit {
			return ReserveResult{}, WrapError(ErrCapacityExceeded, "reserve", &ReservationCapacityError{
				Requested: domain.Amount{Unit: cmd.Request.Unit, Value: amount, Currency: cmd.Request.Currency},
				Remaining: domain.Amount{Unit: cmd.Request.Unit, Value: max(0, f.capacityLimit-f.cumulativeReserved), Currency: cmd.Request.Currency},
			})
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
	if f.settleErr != nil {
		return SettleResult{}, f.settleErr
	}
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

func (f *fakeStateStore) Release(ctx context.Context, cmd ReleaseCommand) (ReleaseResult, error) {
	f.releaseCalls = append(f.releaseCalls, cmd)
	if f.releaseWaitForContext {
		<-ctx.Done()
		f.releaseContextErr = ctx.Err()
		return ReleaseResult{}, ctx.Err()
	}
	if f.releaseErr != nil {
		return ReleaseResult{}, f.releaseErr
	}
	if _, ok := f.releases[cmd.ReleaseKey.String()]; ok {
		return ReleaseResult{}, nil
	}
	result := ReleaseResult{Applied: true, ReservationID: cmd.ReservationID, ReleasedDelta: cmd.Amount}
	if reservation, ok := f.reservations[cmd.ReservationKey.String()]; ok {
		released := cmd.Amount
		if released.Unit == "" {
			released.Unit = reservation.ReservedAmount.Unit
			released.Currency = reservation.ReservedAmount.Currency
		}
		if released.Value > reservation.ReservedAmount.Value {
			released.Value = reservation.ReservedAmount.Value
		}
		if released.Value > f.cumulativeReserved {
			f.cumulativeReserved = 0
		} else {
			f.cumulativeReserved -= released.Value
		}
		delete(f.reservations, cmd.ReservationKey.String())
		result.ReleasedDelta = released
	}
	f.releaseResult = result
	f.releases[cmd.ReleaseKey.String()] = result
	return result, nil
}

func (f *fakeStateStore) ApplyUsage(_ context.Context, cmd ApplyUsageCommand) (ApplyUsageResult, error) {
	f.applyUsageCalls = append(f.applyUsageCalls, cmd)
	return ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (f *fakeStateStore) LimitStatus(_ context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	f.limitQueries = append(f.limitQueries, q)
	if len(f.limitPages) > 0 {
		idx := f.limitCalls
		if q.Cursor.Token != "" {
			idx++
		}
		if idx >= len(f.limitPages) {
			idx = len(f.limitPages) - 1
		}
		f.limitCalls++
		return f.limitPages[idx], nil
	}
	return f.limitPage, nil
}

func (f *fakeStateStore) ActiveLimit(_ context.Context, q ActiveLimitQuery) (controlplane.AccountingLimitStatusRow, bool, error) {
	f.activeQueries = append(f.activeQueries, q)
	if f.activeLimitOK || f.activeLimitErr != nil {
		return f.activeLimitRow, f.activeLimitOK, f.activeLimitErr
	}
	if len(f.limitPage.Items) > 0 {
		return f.limitPage.Items[0], true, nil
	}
	if len(f.limitPages) > 0 && len(f.limitPages[0].Items) > 0 {
		return f.limitPages[0].Items[0], true, nil
	}
	return controlplane.AccountingLimitStatusRow{}, false, nil
}

func (f *fakeStateStore) DecisionHistory(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return f.decisionPage, nil
}

func (f *fakeStateStore) CheckReadiness(context.Context) (domain.AuthorityStatus, error) {
	return f.readiness, f.readinessErr
}
