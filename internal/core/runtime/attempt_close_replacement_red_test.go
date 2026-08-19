package runtime

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

type closeReplacementRaceStream struct {
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
}

func (*closeReplacementRaceStream) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (s *closeReplacementRaceStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	s.cancelCalls.Add(1)
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *closeReplacementRaceStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

// TestRetryRecvStreamCloseDuringReplacementOpenDoesNotPublishAttempt is RED until
// a replacement that finishes Open after Close loses publication ownership and is
// cleaned up as an ephemeral attempt.
func TestRetryRecvStreamCloseDuringReplacementOpenDoesNotPublishAttempt(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "replacement-reservation",
			ReservedAmount: authorityInputAmount(8),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	budget := &attemptBudget{max: 3}
	params := authorityOpenParams(t, aLegID, budget)
	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	oldStream := &closeReplacementRaceStream{}
	replacementStream := &closeReplacementRaceStream{}
	replacementOpenEntered := make(chan struct{})
	releaseReplacementOpen := make(chan struct{})
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		close(replacementOpenEntered)
		<-releaseReplacementOpen
		return replacementStream, nil
	}

	oldAdmission := auth.admitResult
	oldAdmission.ReservationID = "old-reservation"
	oldAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(8),
		admissionResult: oldAdmission,
	}
	terminal := newTurnTerminal()
	rs := &retryRecvStream{
		executor: ex,
		bus:      hooks.New(hooks.Config{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: params.baseline,
			aLegID:   aLegID,
			traceID:  "trace-1",
		}),
		terminal: terminal,
		recovery: newRecoveryController(recoveryControllerInput{
			executor: ex,
			bus:      hooks.New(hooks.Config{}),
			aScope:   terminal.aLegScope(),
			budget:   budget,
			sel:      sel,
			session:  &routing.SessionRoutingState{},
			excluded: map[string]struct{}{},
			rng:      routing.NewSeededRng(1),
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{ALegID: aLegID, BLegID: "old-bleg", Seq: 1},
			authorityCandidate(),
			testAuthorityLifecycle(ex, oldAuthority, authorityCandidate()),
		),
	}
	testStoreInner(rs, oldStream)
	oldAttempt := rs.attempt.snapshot()

	const replacementOpenTimeout = 5 * time.Second
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(releaseReplacementOpen) }) }
	defer releaseOpen()

	type replacementResult struct {
		opened bool
		err    error
	}
	resultCh := make(chan replacementResult, 1)
	go func() {
		opened, err := rs.tryReplacementIteration(context.Background())
		resultCh <- replacementResult{opened: opened, err: err}
	}()

	select {
	case <-replacementOpenEntered:
	case <-time.After(replacementOpenTimeout):
		t.Fatal("replacement backend Open did not enter its barrier")
	}

	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !rs.isFinished() {
		t.Fatal("request must be finished after Close terminalizes the old stream")
	}
	if got := oldStream.closeCalls.Load(); got != 1 {
		t.Fatalf("old stream Close calls = %d, want 1", got)
	}

	releaseOpen()
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("tryReplacementIteration: %v", result.err)
		}
	case <-time.After(replacementOpenTimeout):
		t.Fatal("tryReplacementIteration did not return after replacement Open was released")
	}

	if current := rs.attempt.snapshot(); current != oldAttempt || current == nil || current.loadInner() != nil {
		t.Fatal("replacement attempt was published after Close")
	}
	if got := replacementStream.cancelCalls.Load(); got != 1 {
		t.Fatalf("replacement stream Cancel calls = %d, want 1", got)
	}
	if got := replacementStream.closeCalls.Load(); got != 1 {
		t.Fatalf("replacement stream Close calls = %d, want 1", got)
	}
	if !rs.isFinished() {
		t.Fatal("request must remain finished after replacement Open completes")
	}

	settles := auth.settleInputs()
	releases := auth.releaseInputs()
	if got := len(settles) + len(releases); got != 2 {
		t.Fatalf("authority terminal calls = %d, want 2 (old and fresh replacement reservations)", got)
	}
	containsSettle := func(id string) bool {
		for _, input := range settles {
			if input.ReservationID == id {
				return true
			}
		}
		return false
	}
	containsRelease := func(id string) bool {
		for _, input := range releases {
			if input.ReservationID == id {
				return true
			}
		}
		return false
	}
	for _, id := range []string{"old-reservation", "replacement-reservation"} {
		if !containsSettle(id) && !containsRelease(id) {
			t.Fatalf("authority reservation %q was neither settled nor released", id)
		}
	}
}
