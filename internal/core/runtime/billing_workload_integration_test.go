package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func TestBillingWorkloadIdentitySurvivesBareTerminalContextAndMatchesClosure(t *testing.T) {
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	want, err := billing.WorkloadIdentityFromAuxiliaryRole(billing.WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{}
	var gotLeg billing.CallLegUsageRecord
	var gotCall billing.CallUsageRecord
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(_ context.Context, leg billing.CallLegUsageRecord) error {
			gotLeg = leg
			return nil
		},
		appendCall: func(_ context.Context, call billing.CallUsageRecord) error {
			gotCall = call
			return nil
		},
	}
	base := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal"), Origin: scope.OriginInternal,
	})
	parentAuthority := &requestAuthorityState{}
	trusted := withRequestAuthority(execctx.WithDetachedSession(base, execctx.DetachedSession{
		AuxiliaryRole: string(billing.WorkloadRoleCompactionContinuityExtractor),
	}), parentAuthority)
	// The request-authority carrier is what a bare Recv context retains. The
	// inherited child keeps the parent lease state and only records workload in
	// its per-A-leg side map.
	ctx, err := ex.admitRequestAuthorityOnce(trusted, "child-request", "child-a", "child-trace", scope.PrincipalScopeView{Origin: scope.OriginInternal})
	if err != nil {
		t.Fatal(err)
	}
	if requestAuthorityFrom(ctx) != parentAuthority {
		t.Fatal("inherited auxiliary workload changed parent authority state identity")
	}
	// Simulate a frontend Recv callback that supplies a bare context: detached
	// metadata is gone, while the request-authority carrier remains attached by
	// stream assembly.
	bare := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal"), Origin: scope.OriginInternal,
	})
	bare = withRequestAuthority(bare, requestAuthorityFrom(ctx))
	state := newBillingCallState(callID)
	ex.appendIndependentTerminalLeg(bare, state, "child-a", b2bua.BLegRecord{
		ALegID: "child-a", BLegID: "child-b", Seq: 1,
	}, routing.Primary{Backend: "backend", Model: "extractor"}, time.Unix(1, 0), time.Unix(2, 0), billing.LegOutcomeNeverStarted)
	if gotLeg.Workload != want {
		t.Fatalf("leg workload=%+v, want %+v", gotLeg.Workload, want)
	}
	stream := &retryRecvStream{
		executor: ex, facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:                 "child-a",
			billingCallID:          callID,
			billingCallState:       state,
			billingIdentityStamped: true,
			billingAccountID:       "acct:principal",
			baseline:               lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "child-session"}},
		}),
	}
	stream.appendCallClosureLocked(bare, sdkterminal.CommandNormalFinish)
	if gotCall.Workload != want {
		t.Fatalf("call workload=%+v, want %+v", gotCall.Workload, want)
	}
}

func TestBillingWorkloadIdentityDoesNotTrustCanonicalLineageOrPrimaryScope(t *testing.T) {
	primary := context.Background()
	if got := (&Executor{}).billingWorkloadIdentity(primary); !got.IsZero() {
		t.Fatalf("primary workload=%+v, want legacy zero identity", got)
	}
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{AuxiliaryRole: "provider-model-text"})
	if _, err := (&Executor{}).admitRequestAuthorityOnce(ctx, "request", "a", "trace", scope.PrincipalScopeView{}); err == nil {
		t.Fatal("unsupported trusted role was accepted")
	}
}
