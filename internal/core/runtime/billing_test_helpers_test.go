package runtime

import (
	"context"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testAttemptSlot(bleg b2bua.BLegRecord, cand routing.AttemptCandidate, authority authorityLifecycle, accounting ...attemptAccountingTracker) attemptSlot {
	in := attemptSessionInput{
		bleg:           bleg,
		cand:           cand,
		authority:      authority,
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	}
	if len(accounting) > 0 {
		in.accounting = accounting[0]
	}
	return attemptSlot{current: newAttemptSession(in)}
}

// testAttemptSession installs a complete default fixture for direct retry-stream
// tests. Production assembly and replacement install complete sessions before
// exposing a stream, so production code never creates a partial attempt.
func testAttemptSession(s *retryRecvStream) *attemptSession {
	if s == nil {
		return nil
	}
	if s.terminal == nil {
		s.terminal = newTurnTerminal()
	}
	if attempt := s.attempt.snapshot(); attempt != nil {
		return attempt
	}
	attempt := newAttemptSession(attemptSessionInput{})
	s.attempt.install(attempt)
	return attempt
}

// installTestTurnTerminal is the explicit fixture constructor for direct
// retryRecvStream tests. Production assembly always installs this owner before
// exposure; tests must do so before any concurrent Recv/Close or terminal use.
func installTestTurnTerminal(s *retryRecvStream) {
	if s != nil && s.terminal == nil {
		s.terminal = newTurnTerminal()
	}
}

// bindTestRuntimeOwners composes the concrete response and terminal owners
// for a direct fixture. The executor is used only as a construction source;
// neither owner retains the broad object.
func bindTestRuntimeOwners(s *retryRecvStream, e *Executor) {
	if s == nil || e == nil {
		return
	}
	installTestTurnTerminal(s)
	deps := newResponsePipelineForExecutor(e)
	if s.responsePipeline == nil {
		s.responsePipeline = deps
	} else {
		p := s.responsePipeline
		p.bus = deps.bus
		p.log = deps.log
		p.now = deps.now
		p.runtimeSnapshot = deps.runtimeSnapshot
		p.extensionMetrics = deps.extensionMetrics
		p.policyDiagnostics = deps.policyDiagnostics
		p.policyEvidenceEmitter = deps.policyEvidenceEmitter
		p.streamUsage = deps.streamUsage
		p.secureSessionRecorder = deps.secureSessionRecorder
		p.secureSessionMetrics = deps.secureSessionMetrics
		p.secureRecordingMandatory = deps.secureRecordingMandatory
		p.backends = deps.backends
		p.detector = deps.detector
		p.compactionObservers = deps.compactionObservers
		p.compactionPreservers = deps.compactionPreservers
		p.compactionServices = deps.compactionServices
		p.keepwarm = deps.keepwarm
		p.completionBufferLimits = deps.completionBufferLimits
	}
	bindTurnTerminalRuntime(s.terminal, e)
	if attempt := s.attempt.snapshot(); attempt != nil {
		attempt.recordAttemptLoggedFn = e.recordAttemptLogged
	}
	if s.recovery != nil {
		s.recovery.bindOpener(e, s.responsePipeline.bus, s.terminal.aLegScope())
		s.recovery.attemptFactory = func(opened replacementOpenResult, facts requestTerminalFacts) *attemptSession {
			fs, maxArgs := e.resolveToolCallFinalizers()
			return newAttemptSession(attemptSessionInput{
				inner: opened.stream, bleg: opened.bleg, cand: opened.cand,
				authority:             e.newAttemptAuthorityLifecycle(opened.authority, opened.cand),
				accounting:            newAttemptAccountingTracker(e.now()),
				toolFinal:             newToolCallAssembler(fs, maxArgs, facts.call.Tools),
				promptCacheSource:     promptCacheObservationSource(opened.stream),
				promptCacheController: promptCacheControllerFor(e.Backends[opened.cand.Primary.Backend]),
				finalStreamObs:        &extensions.FinalStreamObservationSession{Log: e.Log, Metrics: e.ExtensionMetrics},
				recordAttemptLoggedFn: e.recordAttemptLogged,
			})
		}
		s.recovery.postOpenLeg = e.appendPostOpenTerminalLeg
	}
}

func testStoreInner(s *retryRecvStream, inner lipapi.ManagedEventStream) {
	testAttemptSession(s).storeInner(inner)
}

type task51SingleEventStream struct {
	event lipapi.Event
	done  bool
}

type task51ErrorStream struct{ err error }

func (s *task51ErrorStream) Recv(context.Context) (lipapi.Event, error) { return lipapi.Event{}, s.err }
func (s *task51ErrorStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}
func (s *task51ErrorStream) Close() error { return nil }

func (s *task51SingleEventStream) Recv(context.Context) (lipapi.Event, error) {
	if s.done {
		return lipapi.Event{}, io.EOF
	}
	s.done = true
	return s.event, nil
}

func (s *task51SingleEventStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}
func (s *task51SingleEventStream) Close() error { return nil }

func testRecvOne(ctx context.Context, s *retryRecvStream, ev lipapi.Event) (lipapi.Event, error) {
	testStoreInner(s, &task51SingleEventStream{event: ev})
	return s.Recv(ctx)
}

func testRecvEOF(ctx context.Context, s *retryRecvStream) (lipapi.Event, error) {
	testStoreInner(s, &task51ErrorStream{err: io.EOF})
	return s.Recv(ctx)
}

func testRecvError(ctx context.Context, s *retryRecvStream, err error) (lipapi.Event, error) {
	testStoreInner(s, &task51ErrorStream{err: err})
	return s.Recv(ctx)
}

type exposureAdmissionFunc func(context.Context, BillingExposureAdmissionInput) (billing.CallExposure, error)

func (f exposureAdmissionFunc) Admit(ctx context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return f(ctx, in)
}

type creditGateFunc func(context.Context, string) error

func (f creditGateFunc) Check(ctx context.Context, accountID string) error {
	return f(ctx, accountID)
}

func testBillingIdentity() BillingIdentity {
	return BillingIdentity{
		AccountID: func(context.Context, lipapi.Call) string { return "acct" },
		CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "pricing:test", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "policy:test", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) billing.VersionRef {
			return billing.VersionRef{ID: "operator:test", Version: "1"}
		},
	}
}

func testRecvTurnFacts(f recvTurnFacts) recvTurnFacts {
	if f.billingCallState == nil {
		f.billingCallState = newBillingCallState(f.billingCallID)
	}
	return f
}

func withTestRecvFacts(s *retryRecvStream, update func(recvTurnFacts) recvTurnFacts) *retryRecvStream {
	if s == nil {
		return nil
	}
	// Test callers invoke this while constructing a stream, before any lock or
	// terminal state is used. Keep the original stream identity so this helper
	// does not copy mutex-bearing retryRecvStream state.
	s.facts = update(testRecvTurnFacts(s.facts))
	return s
}

// stampStreamIdentity wires the concrete response and terminal owners for a
// direct fixture. The optional executor is only a construction source; it is
// never retained by retryRecvStream or either owner.
func stampStreamIdentity(s *retryRecvStream, executors ...*Executor) *retryRecvStream {
	if s == nil {
		return nil
	}
	if len(executors) > 0 && executors[0] != nil {
		bindTestRuntimeOwners(s, executors[0])
	} else {
		installTestTurnTerminal(s)
	}
	return withTestRecvFacts(s, func(f recvTurnFacts) recvTurnFacts {
		f.billingAccountID = "acct"
		f.billingCustomerPricing = billing.VersionRef{ID: "pricing:test", Version: "1"}
		f.billingChargePolicy = billing.VersionRef{ID: "policy:test", Version: "1"}
		f.billingIdentityStamped = true
		return f
	})
}
