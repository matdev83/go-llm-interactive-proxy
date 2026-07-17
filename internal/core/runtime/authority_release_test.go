package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestExecutorAuthorityReleaseOnSwallowedOpen(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-1",
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, _ := newAuthorityRuntimeTestExecutor(t, auth)
	l := newAuthorityLifecycle(ex.authorityService(), ex.Log, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(12),
		admissionResult: auth.admitResult,
	}, authorityCandidate())
	l.Release(context.Background(), authorityapp.ReleaseKindSwallowed)
	if auth.releaseCalls.Load() != 1 {
		t.Fatalf("release calls = %d, want 1", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls = %d, want 0", auth.settleCalls.Load())
	}
	release := auth.lastRelease()
	if release.Stage != feature.StageIDAttemptLifecycle {
		t.Fatalf("release stage = %q, want attempt_lifecycle", release.Stage)
	}
	if !release.BackendAttempted {
		t.Fatal("expected swallowed release to record backendAttempted=true")
	}
	if release.OutputCommitted {
		t.Fatal("expected swallowed release to record outputCommitted=false")
	}
}

// TestRetryRecvStreamFailedPartialSettleReleasesLosingAndReplacementResetsAuthority
// verifies the authorityLifecycle owner's behavior on a failed partial settle. The owner
// folds the losing-fallback into Settle, so a failed partial settle now releases the
// reservation with ReleaseKindLosing (the unified fallback previously hand-written only at
// the finalizeResponseFinishedAuthority and settleCancellationAuthority sites) and marks
// the lifecycle settled. A subsequent recv-phase replacement then skips re-releasing the
// already-finalized prior reservation and resets the owner to the freshly admitted
// reservation with the settled guard cleared.
func TestRetryRecvStreamFailedPartialSettleReleasesLosingAndReplacementResetsAuthority(t *testing.T) {
	t.Parallel()

	settleErr := errors.New("settle unavailable")
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-2",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		settleErr: settleErr,
		status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	initialAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	initialAuthority.admissionResult.ReservationID = "reservation-1"
	initialAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)
	initialCand := routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}

	rs := &retryRecvStream{
		executor: ex,
		bus:      hooks.New(hooks.Config{}),
		baseline: lipapi.Call{
			ID:    "request-1",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
		budget:    &attemptBudget{max: 3, used: 0},
		aLegID:    aLegID,
		traceID:   "trace-1",
		sel:       sel,
		session:   &routing.SessionRoutingState{},
		excluded:  map[string]struct{}{},
		rng:       routing.NewSeededRng(1),
		bleg:      b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1},
		cand:      initialCand,
		authority: testAuthorityLifecycle(ex, initialAuthority, initialCand),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventUsageDelta, TotalTokens: 4, CostNanoUnits: 11, Currency: "USD"},
		},
	}

	rs.recordPartialTokenAccounting(context.Background(), "partial", errors.New("stream dropped"))
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	// The owner's Settle losing-fallback releases the prior reservation immediately on the
	// failed partial settle (ReleaseKindLosing) and marks the lifecycle settled, so the
	// reservation is not left locked and a later replacement cannot double-release it.
	if auth.releaseCalls.Load() != 1 {
		t.Fatalf("release calls = %d, want 1 (failed partial settle must release losing, not leak)", auth.releaseCalls.Load())
	}
	release := auth.lastRelease()
	if release.ReservationID != "reservation-1" {
		t.Fatalf("release reservation ID = %q, want reservation-1", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindLosing {
		t.Fatalf("release kind = %q, want losing", release.Kind)
	}
	if release.Stage != feature.StageIDAttemptLifecycle {
		t.Fatalf("release stage = %q, want attempt_lifecycle", release.Stage)
	}
	if !release.BackendAttempted {
		t.Fatal("expected losing release to record backendAttempted=true")
	}
	if release.OutputCommitted {
		t.Fatal("expected losing release to record outputCommitted=false")
	}
	if !rs.authority.Settled() {
		t.Fatal("expected authority settled=true after failed partial settle losing-release so later handlers cannot double-release")
	}

	opened, err := rs.tryReplacementIteration(context.Background())
	if err != nil {
		t.Fatalf("tryReplacementIteration: %v", err)
	}
	if !opened {
		t.Fatal("expected replacement to open after failed settle")
	}
	// The prior reservation was already released (losing) and marked settled, so the
	// replacement must NOT release it again.
	if auth.releaseCalls.Load() != 1 {
		t.Fatalf("release calls = %d, want 1 (prior already released; replacement must not double-release)", auth.releaseCalls.Load())
	}
	if rs.authority.stateSnapshot().admissionResult.ReservationID != "reservation-2" {
		t.Fatalf("stream authority reservation ID = %q, want reservation-2", rs.authority.stateSnapshot().admissionResult.ReservationID)
	}
	if rs.authority.Settled() {
		t.Fatal("expected authority settled=false after replacement reset to a fresh reservation")
	}
}

func TestRetryRecvStreamGlobalTTFTTimeoutReleasesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-ttft",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	rs := &retryRecvStream{
		executor: ex,
		baseline: lipapi.Call{ID: "request-ttft", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
		cand:     authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(7),
			admissionResult: auth.admitResult,
		}, authorityCandidate()),
		traceID:    "trace-ttft",
		aLegID:     "a-leg-ttft",
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}

	_, cont, err := rs.handleRecvError(
		context.Background(),
		context.Background(),
		context.DeadlineExceeded,
		idleContextDeadline{},
		ttftContextDeadline{scope: ttftTimeoutGlobal, parent: context.Background()},
	)
	if err == nil {
		t.Fatal("expected global TTFT timeout error")
	}
	if !errors.Is(err, lipapi.ErrTTFTTimeout) {
		t.Fatalf("error = %v, want ErrTTFTTimeout", err)
	}
	if cont {
		t.Fatal("expected global TTFT timeout to stop the stream")
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 (incurred TTFT must settle)", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	settle := auth.lastSettle()
	if settle.Kind != authorityapp.SettlementKindLosing {
		t.Fatalf("settle kind = %q, want losing", settle.Kind)
	}
	if settle.Stage != feature.StageIDAttemptLifecycle {
		t.Fatalf("settle stage = %q, want attempt_lifecycle", settle.Stage)
	}
	if !settle.BackendAttempted {
		t.Fatal("expected global TTFT losing settle to record backendAttempted=true")
	}
	if settle.OutputCommitted {
		t.Fatal("expected global TTFT losing settle to record outputCommitted=false")
	}
	if !rs.authority.Settled() {
		t.Fatal("expected authority settled=true after global TTFT timeout losing-settle so later handlers cannot double-settle")
	}
}

func TestRetryRecvStreamReplacementRefreshesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-2",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	initialAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	initialAuthority.admissionResult.ReservationID = "reservation-1"
	initialAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)

	rs := &retryRecvStream{
		executor: ex,
		bus:      hooks.New(hooks.Config{}),
		baseline: lipapi.Call{
			ID:    "request-1",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
		budget:    &attemptBudget{max: 3, used: 0},
		aLegID:    aLegID,
		traceID:   "trace-1",
		sel:       sel,
		session:   &routing.SessionRoutingState{},
		excluded:  map[string]struct{}{},
		rng:       routing.NewSeededRng(1),
		bleg:      b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1},
		cand:      routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}},
		authority: testAuthorityLifecycle(ex, initialAuthority, routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}),
	}

	opened, err := rs.tryReplacementIteration(context.Background())
	if err != nil {
		t.Fatalf("tryReplacementIteration: %v", err)
	}
	if !opened {
		t.Fatal("expected replacement to open")
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 (incurred prior must settle)", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	settle := auth.lastSettle()
	if settle.ReservationID != "reservation-1" {
		t.Fatalf("settle reservation ID = %q, want reservation-1", settle.ReservationID)
	}
	if settle.Kind != authorityapp.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}
	if rs.authority.stateSnapshot().admissionResult.ReservationID != "reservation-2" {
		t.Fatalf("stream authority reservation ID = %q, want reservation-2", rs.authority.stateSnapshot().admissionResult.ReservationID)
	}
	if rs.authority.Settled() {
		t.Fatal("expected authoritySettled to be false after replacement")
	}
}

func TestRetryRecvStreamSwallowedFailureReleasesAuthorityOnReplacement(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-2",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	initialAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	initialAuthority.admissionResult.ReservationID = "reservation-1"
	initialAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)

	rs := &retryRecvStream{
		executor: ex,
		bus:      hooks.New(hooks.Config{}),
		baseline: lipapi.Call{
			ID:    "request-1",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
		budget:    &attemptBudget{max: 3, used: 0},
		aLegID:    aLegID,
		traceID:   "trace-1",
		sel:       sel,
		session:   &routing.SessionRoutingState{},
		excluded:  map[string]struct{}{},
		rng:       routing.NewSeededRng(1),
		bleg:      b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1},
		cand:      routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}},
		authority: testAuthorityLifecycle(ex, initialAuthority, routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}),
	}

	recvErr := &lipapi.UpstreamFailure{
		Phase:       lipapi.PhasePreOutput,
		Recoverable: true,
		Reason:      "recv dropped",
	}
	_, cont, err := rs.handleRecvError(context.Background(), context.Background(), recvErr, idleContextDeadline{}, ttftContextDeadline{})
	if err != nil {
		t.Fatalf("handleRecvError: %v", err)
	}
	if !cont {
		t.Fatal("expected recoverable pre-output recv failure to continue")
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls after swallowed recv = %d, want 0", auth.settleCalls.Load())
	}
	if rs.authority.Settled() {
		t.Fatal("expected authoritySettled to remain false after swallowed recv failure")
	}

	opened, err := rs.tryReplacementIteration(context.Background())
	if err != nil {
		t.Fatalf("tryReplacementIteration: %v", err)
	}
	if !opened {
		t.Fatal("expected replacement to open")
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 (incurred swallowed prior must settle)", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	settle := auth.lastSettle()
	if settle.ReservationID != "reservation-1" {
		t.Fatalf("settle reservation ID = %q, want reservation-1", settle.ReservationID)
	}
	if settle.Kind != authorityapp.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}
	if rs.authority.stateSnapshot().admissionResult.ReservationID != "reservation-2" {
		t.Fatalf("stream authority reservation ID = %q, want reservation-2", rs.authority.stateSnapshot().admissionResult.ReservationID)
	}
	if rs.authority.Settled() {
		t.Fatal("expected authoritySettled to be false after replacement")
	}
}

// TestRetryRecvStreamReplacementErrorReleasesSwallowedAuthority reproduces the Bugbot
// finding at executor_recv_loop.go:86-88. After a swallowed recv-phase failure the prior
// attempt's authority reservation stays reserved on the stream and is only released by
// tryReplacementIteration on its success path. When Recv calls tryReplacementIteration and
// it returns a non-nil error, Recv must release the swallowed reservation (with
// ReleaseKindSwallowed) before surfacing the error, otherwise the reserved capacity leaks.
func TestRetryRecvStreamReplacementErrorReleasesSwallowedAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-2",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	// initialAuthority represents the still-reserved capacity from a prior attempt whose
	// recv-phase failure was swallowed (inner dropped, authoritySettled=false).
	initialAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	initialAuthority.admissionResult.ReservationID = "reservation-1"
	initialAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)

	rs := &retryRecvStream{
		executor: ex,
		bus:      hooks.New(hooks.Config{}),
		baseline: lipapi.Call{
			ID:    "request-1",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
		budget:     &attemptBudget{max: 3, used: 0},
		aLegID:     aLegID,
		traceID:    "trace-1",
		sel:        sel,
		session:    &routing.SessionRoutingState{},
		excluded:   map[string]struct{}{},
		rng:        routing.NewSeededRng(1),
		bleg:       b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1},
		cand:       routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}},
		authority:  testAuthorityLifecycle(ex, initialAuthority, routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}),
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}

	// A pre-canceled context forces tryReplacementIteration to error at its ctx.Err() guard
	// before any new stream/authority is admitted, leaving the swallowed reservation active.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = rs.Recv(ctx)
	if err == nil {
		t.Fatal("expected Recv to surface the replacement error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv error = %v, want context.Canceled", err)
	}

	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 (incurred swallowed must settle on replacement error)", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	settle := auth.lastSettle()
	if settle.ReservationID != "reservation-1" {
		t.Fatalf("settle reservation ID = %q, want reservation-1", settle.ReservationID)
	}
	if settle.Kind != authorityapp.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}
	if settle.Stage != feature.StageIDAttemptLifecycle {
		t.Fatalf("settle stage = %q, want attempt_lifecycle", settle.Stage)
	}
	if !settle.BackendAttempted {
		t.Fatal("expected replacement-error swallowed settle to record backendAttempted=true")
	}
	if settle.OutputCommitted {
		t.Fatal("expected replacement-error swallowed settle to record outputCommitted=false")
	}
	if !rs.isFinished() {
		t.Fatal("expected stream to be marked finished after terminal replacement error")
	}
}
