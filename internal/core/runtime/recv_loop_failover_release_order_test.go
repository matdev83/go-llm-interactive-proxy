package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// overlapDetectingAuthority is a UsageAuthorityService stub that tracks active
// (reserved, not-yet-released) reservations and records when an authoritative
// (non-estimate) Admit overlaps an already-active reservation — the exact
// double-counting window the recv-phase failover release ordering must prevent.
// Estimate-only precheck admits do not reserve and are not tracked.
type overlapDetectingAuthority struct {
	mu      sync.Mutex
	active  map[string]bool
	overlap []string

	admitResult  authorityapp.AdmissionResult
	releaseCalls atomic.Int64
	settleCalls  atomic.Int64
}

func newOverlapDetectingAuthority(result authorityapp.AdmissionResult) *overlapDetectingAuthority {
	return &overlapDetectingAuthority{
		active:      map[string]bool{},
		admitResult: result,
	}
}

// prereserve marks a reservation ID as active without an Admit call, modelling a
// prior swallowed attempt's still-live reservation pre-staged on the stream.
func (s *overlapDetectingAuthority) prereserve(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[id] = true
}

func (s *overlapDetectingAuthority) Admit(_ context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	if in.EstimateOnly {
		return s.admitResult, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.active {
		s.overlap = append(s.overlap, id)
		break
	}
	s.active[s.admitResult.ReservationID] = true
	return s.admitResult, nil
}

func (s *overlapDetectingAuthority) Release(_ context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCalls.Add(1)
	delete(s.active, in.ReservationID)
	return authorityapp.ReleaseResult{Applied: true, ReservationID: in.ReservationID}, nil
}

func (s *overlapDetectingAuthority) Settle(_ context.Context, in authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settleCalls.Add(1)
	delete(s.active, in.ReservationID)
	return authorityapp.SettleResult{Applied: true, ReservationID: in.ReservationID}, nil
}

func (s *overlapDetectingAuthority) ApplyUsage(_ context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *overlapDetectingAuthority) overlaps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.overlap))
	copy(out, s.overlap)
	return out
}

func (s *overlapDetectingAuthority) isActive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id]
}

// TestRecvLoopFailoverReleasesBeforeAdmission reproduces the HIGH-severity
// release-ordering bug in tryReplacementIteration: on recv-phase failover the
// swallowed (prior) attempt's reservation was released only AFTER
// tryPlanOpenOnce had already opened a replacement and run authoritative
// admission. While both reservations overlap on the same live window, strict
// quota/rate/budget enforcement can reject the replacement with
// ErrReservationConflict or incorrectly exhaust capacity even though it is the
// same logical request continuing after a swallowed B-leg.
//
// The fix releases the swallowed reservation BEFORE opening/admitting the
// replacement. This test uses an overlap-detecting authority that records any
// authoritative admit occurring while a prior reservation is still active, and
// asserts no overlap exists.
func TestRecvLoopFailoverReleasesBeforeAdmission(t *testing.T) {
	t.Parallel()

	auth := newOverlapDetectingAuthority(authorityapp.AdmissionResult{
		Allowed:        true,
		Reserved:       true,
		ReservationID:  "reservation-repl",
		ReservedAmount: authorityInputAmount(7),
		PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
	})
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	// priorAuthority is the still-reserved capacity from the swallowed prior
	// attempt; it is distinct from the replacement's reservation and must be
	// released before the replacement reserves capacity.
	priorAuthority := attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(5),
		admissionResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-prior",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			RuleIDs:        []string{"rule-1"},
			RuleKind:       "quota",
		},
	}
	priorCand := routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}

	// Pre-register the prior reservation as active on the strict window, matching
	// the live reservation the swallowed attempt holds before failover.
	auth.prereserve("reservation-prior")

	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		executor: ex,
		bus:      hooks.New(hooks.Config{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:    "request-1",
				Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
				Invocation: lipapi.Invocation{
					Operation:    lipapi.OperationOpenAIChatCompletions,
					DeliveryMode: lipapi.DeliveryModeStreaming,
				},
				Messages: testMinimalUserMessages(),
			},
			aLegID:  aLegID,
			traceID: "trace-1",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 3, used: 0}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1}, priorCand, testAuthorityLifecycle(ex, priorAuthority, priorCand)),
	}

	opened, err := rs.tryReplacementIteration(context.Background())
	if err != nil {
		t.Fatalf("tryReplacementIteration: %v", err)
	}
	if !opened {
		t.Fatal("expected replacement to open")
	}

	// The authoritative admit for the replacement must NOT overlap the prior
	// reservation on the live window. Before the fix the prior was released only
	// after the replacement was admitted, so the overlap was recorded.
	if overlaps := auth.overlaps(); len(overlaps) != 0 {
		t.Fatalf("authoritative admit overlapped active prior reservation(s) %v; the swallowed reservation must be released before the replacement is admitted", overlaps)
	}

	// The prior reservation must have been settled exactly once (swallowed),
	// and the replacement's reservation must now be the active one on the stream.
	if got, want := auth.releaseCalls.Load(), int64(0); got != want {
		t.Fatalf("release calls = %d, want %d (incurred prior must settle, not release)", got, want)
	}
	if got, want := auth.settleCalls.Load(), int64(1); got != want {
		t.Fatalf("settle calls = %d, want %d (prior swallowed reservation settled before replacement admit)", got, want)
	}
	if auth.isActive("reservation-prior") {
		t.Fatal("prior reservation must no longer be active after settle")
	}
	if !auth.isActive("reservation-repl") {
		t.Fatal("replacement reservation must be active after successful open")
	}
	if testAttemptSession(rs).authority.stateSnapshot().admissionResult.ReservationID != "reservation-repl" {
		t.Fatalf("stream authority reservation ID = %q, want reservation-repl", testAttemptSession(rs).authority.stateSnapshot().admissionResult.ReservationID)
	}
	if testAttemptSession(rs).authority.Settled() {
		t.Fatal("expected authority settled=false after replacement reset to a fresh reservation")
	}
}
