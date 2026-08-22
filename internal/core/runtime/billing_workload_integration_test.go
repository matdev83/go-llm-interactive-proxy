package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

type task51ProjectionAuthority struct {
	recordingAuthorityService
	applyCtx context.Context
}

func (s *task51ProjectionAuthority) ApplyUsage(ctx context.Context, cmd app.ApplyUsageCommand) (app.ApplyUsageResult, error) {
	s.applyCtx = ctx
	return app.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0}, nil
}

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
		terminal: newTurnTerminal(), facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:                 "child-a",
			billingCallID:          callID,
			billingCallState:       state,
			billingIdentityStamped: true,
			billingAccountID:       "acct:principal",
			baseline:               lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "child-session"}},
			requestAuth:            requestAuthorityFrom(ctx),
		}),
	}
	bindTestRuntimeOwners(stream, ex)
	stream.terminal.handoffBillingTurn(bare, stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish)
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

func TestTask51BillingLegProjectsFactsWithoutLogger(t *testing.T) {
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	want, err := billing.WorkloadIdentityFromAuxiliaryRole(billing.WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Log = nil
	var got billing.CallLegUsageRecord
	ex.TerminalUsageSink = testTerminalSink{appendLeg: func(_ context.Context, leg billing.CallLegUsageRecord) error {
		got = leg
		return nil
	}}
	parentAuthority := &requestAuthorityState{}
	ctx := withRequestAuthority(execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{
		AuxiliaryRole: string(billing.WorkloadRoleCompactionContinuityExtractor),
	}), parentAuthority)
	facts := testRecvTurnFacts(recvTurnFacts{
		traceID: "trace-no-log", aLegID: "a-no-log", billingCallID: callID,
		billingCallState: newBillingCallState(callID), requestAuth: parentAuthority,
		baseline: lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-no-log"}},
	})
	stream := &retryRecvStream{
		facts: facts,
		attempt: testAttemptSlot(b2bua.BLegRecord{ALegID: "a-no-log", BLegID: "b-no-log", Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	bindTestRuntimeOwners(stream, ex)
	stream.terminal.recordBillingLegForAttempt(ctx, facts.terminalFacts(), stream.attempt.require(), stream.attempt.require().terminalEvidence(), sdkterminal.CommandNormalFinish, lipapi.Event{}, true, facts.billingCallState)
	if got.Workload != want {
		t.Fatalf("leg workload=%+v, want %v with nil logger", got.Workload, want)
	}
}

func TestTask51NilLoggerSettlementProjection(t *testing.T) {
	auth := &task51ProjectionAuthority{}
	authority := newAuthorityLifecycle(auth, nil, attemptAuthorityState{
		admissionResult: app.AdmissionResult{UnreservedRuleIDs: []string{"unreserved"}},
	}, routing.AttemptCandidate{})
	holder := &checkpoint.RequestHolder{}
	requestAuth := &requestAuthorityState{}
	facts := recvTurnFacts{
		traceID: "trace-nil-logger", aLegID: "a-nil-logger", requestAuth: requestAuth, metering: holder,
		baseline: lipapi.Call{ID: "request-nil-logger"},
	}
	terminal := newTurnTerminal()
	var settleCtx context.Context
	terminal.settleRequestAuthority = func(ctx context.Context, _ []metering.Fact) error {
		settleCtx = ctx
		return nil
	}
	stream := &retryRecvStream{
		facts:            facts,
		attempt:          testAttemptSlot(b2bua.BLegRecord{ALegID: facts.aLegID, BLegID: "b-nil-logger", Seq: 1}, routing.AttemptCandidate{}, authority),
		terminal:         terminal,
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	// Both settlement sites receive a bare context. The response owner exists,
	// but deliberately has no logger; facts projection must still carry identity.
	stream.terminal.finishCancellationAuthorityForAttempt(stream.facts.projectContext(context.Background(), stream.responsePipeline.log), stream.attempt.require(), stream.facts.terminalFacts(), stream.responsePipeline)
	if requestAuthorityFrom(auth.applyCtx) != requestAuth || meteringHolderFrom(auth.applyCtx) != holder {
		t.Fatalf("cancellation projection lost facts: requestAuth=%p/%p metering=%p/%p", requestAuthorityFrom(auth.applyCtx), requestAuth, meteringHolderFrom(auth.applyCtx), holder)
	}
	if err := stream.terminal.settleRequestAuthorityWithFrontendEgress(stream.facts.projectContext(context.Background(), stream.responsePipeline.log), lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 1}, stream.facts.terminalFacts(), stream.responsePipeline); err != nil {
		t.Fatalf("settle request authority: %v", err)
	}
	if requestAuthorityFrom(settleCtx) != requestAuth || meteringHolderFrom(settleCtx) != holder {
		t.Fatalf("request settlement projection lost facts: requestAuth=%p/%p metering=%p/%p", requestAuthorityFrom(settleCtx), requestAuth, meteringHolderFrom(settleCtx), holder)
	}
}
