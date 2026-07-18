package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

type remediatingSetConc struct {
	failRenew  atomic.Bool
	uncertain  atomic.Int32
	renewCalls atomic.Int32
	lastSetID  atomic.Value
	markErr    error
	renewErr   error
}

func (s *remediatingSetConc) AdmitLease(_ context.Context, _ authority.LeaseAdmission) (authority.LeaseDecision, error) {
	ttl := 150 * time.Millisecond
	return authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L-a", Generation: 1, SetID: "set-remed",
		ExpiresAt: time.Now().UTC().Add(ttl), RenewBefore: 100 * time.Millisecond, TTL: ttl,
		FailureBehavior: authority.FailureFailClosed,
		Leases: []authority.LeaseOccupancy{
			{LeaseID: "L-a", Generation: 1, RuleID: "rule-a", ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl},
			{LeaseID: "L-b", Generation: 1, RuleID: "rule-b", ExpiresAt: time.Now().UTC().Add(ttl), TTL: ttl},
		},
	}, nil
}

func (s *remediatingSetConc) RenewLease(_ context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	s.renewCalls.Add(1)
	s.lastSetID.Store(in.SetID)
	if s.failRenew.Load() {
		if s.renewErr != nil {
			return authority.LeaseDecision{}, s.renewErr
		}
		return authority.LeaseDecision{}, context.DeadlineExceeded
	}
	return authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: in.LeaseID, SetID: in.SetID,
		Generation: in.ExpectedGeneration + 1, ExpiresAt: time.Now().UTC().Add(time.Minute), TTL: time.Minute,
		Leases: []authority.LeaseOccupancy{{LeaseID: "L-a"}, {LeaseID: "L-b"}},
	}, nil
}

func (s *remediatingSetConc) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }
func (s *remediatingSetConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
func (s *remediatingSetConc) MarkLeaseSetUncertain(_ context.Context, setID string) error {
	s.uncertain.Add(1)
	s.lastSetID.Store(setID)
	return s.markErr
}

type capturingIntentStore struct {
	appended []terminalwork.WorkRecord
	fail     error
}

func (s *capturingIntentStore) AppendIntent(_ context.Context, rec terminalwork.WorkRecord) error {
	if s.fail != nil {
		return s.fail
	}
	s.appended = append(s.appended, rec)
	return nil
}
func (s *capturingIntentStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return nil
}

func TestPhase6Remediation_FailClosedMarksUncertainAcceptsIntentThenCancels(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRenew.Store(true)
	conc.renewErr = context.DeadlineExceeded
	store := &capturingIntentStore{}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-remed", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil && conc.uncertain.Load() > 0 && len(store.appended) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conc.uncertain.Load() < 1 {
		t.Fatal("must MarkLeaseSetUncertain before cancel")
	}
	if ctx.Err() == nil {
		t.Fatal("must cancel request context")
	}
	if len(store.appended) != 1 || store.appended[0].Kind != sdk.WorkKindReleaseLeaseSet {
		t.Fatalf("want persisted release_lease_set intent, got %+v", store.appended)
	}
	if store.appended[0].LeaseSetID != "set-remed" {
		t.Fatalf("lease_set_id=%q", store.appended[0].LeaseSetID)
	}
	if st.LeaseSetReleaseAcceptErr != nil {
		t.Fatalf("accept should succeed: %v", st.LeaseSetReleaseAcceptErr)
	}
}

func TestPhase6Remediation_FailClosedRecordsAcceptFailure(t *testing.T) {
	t.Parallel()
	conc := &remediatingSetConc{}
	conc.failRenew.Store(true)
	store := &capturingIntentStore{fail: errors.New("intent store down")}
	intents := terminalworkapp.NewIntentService(store, terminalworkapp.IntentServiceConfig{})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency: conc, CleanupTimeout: time.Second,
			},
			TerminalWork: intents,
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-accept-fail", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st.LeaseSetReleaseAcceptErr != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.LeaseSetReleaseAcceptErr == nil {
		t.Fatal("accept failure must be recorded, never ignored")
	}
	if ctx.Err() == nil {
		t.Fatal("must still cancel on fail-closed")
	}
}
