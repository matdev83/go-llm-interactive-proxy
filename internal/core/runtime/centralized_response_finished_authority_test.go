package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// TestCentralizedResponseFinishedAuthority is the centralized regression gate for the
// usage-authority leak class on response_finished completion paths. After the refactor
// every response_finished completion path finalizes authority through the single
// finalizeResponseFinishedAuthority chokepoint (settle, with a ReleaseKindLosing fallback
// when settle fails), instead of each site hand-rolling its own inline settle+release.
//
// Each subtest stages a reserved authority whose Settle fails and asserts the reservation
// is RELEASED with ReleaseKindLosing (not leaked until the accounting window resets). The
// settle-success variants assert a single Final settle with no release. The popGateDrainHead
// cases cover the site that leaked before centralization (it had no finalization at all).
// The idle-recovery cases assert the new deferred routing: handleRecvError leaves the finish
// in recoverDrain and returns a continue signal, so the next Recv iteration's recoverDrain
// path finalizes via the helper AND emits the synthesized usage_delta (the client-reporting
// consistency fix). The gated-then-recoverDrain case proves the helper is idempotent
// (tokenAccountingFinalized guard) so a finish flowing through two sites is not double-settled.
func TestCentralizedResponseFinishedAuthority(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-centralized"

	makeFailingSettleAuth := func() *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			settleErr: errors.New("settle boom"),
			status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	makeSuccessSettleAuth := func() *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	// setupStream builds a retryRecvStream with a reserved authority and the supplied stream
	// usage (nil disables reconstruction so finalizeTokenAccounting settles and returns ok=false).
	setupStream := func(t *testing.T, auth *recordingAuthorityService, streamUsage *accountingstream.Reconstructor) (*Executor, *retryRecvStream) {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		if streamUsage != nil {
			ex.StreamUsage = streamUsage
		}
		rs := &retryRecvStream{
			executor: ex,
			bus:      hooks.New(hooks.Config{}),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{ID: "request-centralized", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
				traceID:  "trace-centralized",
				aLegID:   aLegID,
			}),
			attempt:    testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-centralized", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate())),
			accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
		}
		return ex, rs
	}

	streamUsageWithCounts := func() *accountingstream.Reconstructor {
		return accountingstream.New(&stubStreamCounter{
			call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
			output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
		}, accountingstream.Config{})
	}

	// attachPassthroughGate wires a runtime snapshot with the package-local passthrough gate so a
	// response_finished dispatched through handleRecvSuccess routes to handleGatedPath and drains
	// the finish as the emitted out event.
	attachPassthroughGate := func(t *testing.T, ex *Executor, rs *retryRecvStream) {
		t.Helper()
		bus := rs.bus
		if bus == nil {
			bus = hooks.New(hooks.Config{})
		}
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
			CompletionGates: []completion.Gate{gatedLeakPassGate{}},
		})
	}

	assertReleasedAfterFailedSettle := func(t *testing.T, auth *recordingAuthorityService, rs *retryRecvStream) {
		t.Helper()
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (single failed final settle)", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", got.Kind)
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (reservation must be released when final settle failed, not leaked)", auth.releaseCalls.Load())
		}
		rel := auth.lastRelease()
		if rel.Kind != authorityapp.ReleaseKindLosing {
			t.Fatalf("release kind = %q, want %q", rel.Kind, authorityapp.ReleaseKindLosing)
		}
		if rel.ReservationID != reservationID {
			t.Fatalf("release reservation ID = %q, want %q", rel.ReservationID, reservationID)
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after fallback release so later handlers cannot double-release")
		}
	}

	assertSettledNotReleased := func(t *testing.T, auth *recordingAuthorityService, rs *retryRecvStream) {
		t.Helper()
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (single final settle)", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", got.Kind)
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (successful final settle must not release)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after final settle")
		}
	}

	// drainUntilFinish calls Recv until a response_finished surfaces, returning the finish event
	// and the synthesized usage event (when the completion path emitted one before the finish).
	drainUntilFinish := func(t *testing.T, rs *retryRecvStream) (lipapi.Event, lipapi.Event) {
		t.Helper()
		var finish, usageEv lipapi.Event
		for i := range 8 {
			ev, err := rs.Recv(context.Background())
			if err != nil {
				t.Fatalf("Recv %d: %v", i, err)
			}
			if ev.Kind == lipapi.EventUsageDelta && usageEv.Kind == "" {
				usageEv = ev
				continue
			}
			if ev.Kind == lipapi.EventResponseFinished {
				finish = ev
				break
			}
		}
		if finish.Kind != lipapi.EventResponseFinished {
			t.Fatalf("expected to drain a response_finished, last event = %#v", finish)
		}
		return finish, usageEv
	}

	// --- handleResponseFinishedPath (no gates) ---

	t.Run("handleResponseFinishedPath/ok_synthesized_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupStream(t, auth, streamUsageWithCounts())

		ev, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		if cont {
			t.Fatal("expected cont=false on the no-gates completion path")
		}
		if ev.Kind != lipapi.EventUsageDelta {
			t.Fatalf("event kind = %q, want usage_delta (synthesized usage)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("handleResponseFinishedPath/fallthrough_no_stream_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupStream(t, auth, nil)

		ev, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		if cont {
			t.Fatal("expected cont=false on the no-gates completion path")
		}
		if ev.Kind != lipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (fall-through branch)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	// --- recoverDrain (Recv pops a queued finish) ---

	t.Run("recoverDrain/ok_synthesized_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupStream(t, auth, streamUsageWithCounts())
		rs.recoverDrain = []lipapi.Event{{Kind: lipapi.EventResponseFinished}}

		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind != lipapi.EventUsageDelta {
			t.Fatalf("event kind = %q, want usage_delta (synthesized usage from drain ok branch)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("recoverDrain/fallthrough_no_stream_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupStream(t, auth, nil)
		rs.recoverDrain = []lipapi.Event{{Kind: lipapi.EventResponseFinished}}

		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind != lipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (drain fall-through branch)", ev.Kind)
		}
		if !rs.isFinished() {
			t.Fatal("expected stream marked finished after the drain fall-through branch")
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	// --- handleGatedPath (completion gates active, finish drained through the gate) ---

	t.Run("handleGatedPath/ok_synthesized_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		ex, rs := setupStream(t, auth, streamUsageWithCounts())
		attachPassthroughGate(t, ex, rs)

		ev, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		if cont {
			t.Fatal("expected cont=false on the gated completion path")
		}
		if ev.Kind != lipapi.EventUsageDelta {
			t.Fatalf("event kind = %q, want usage_delta (synthesized usage from gated ok branch)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("handleGatedPath/fallthrough_no_stream_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		ex, rs := setupStream(t, auth, nil)
		attachPassthroughGate(t, ex, rs)

		ev, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		if cont {
			t.Fatal("expected cont=false on the gated completion path")
		}
		if ev.Kind != lipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (gated fall-through branch)", ev.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("handleGatedPath/settle_success_not_released", func(t *testing.T) {
		t.Parallel()
		auth := makeSuccessSettleAuth()
		ex, rs := setupStream(t, auth, nil)
		attachPassthroughGate(t, ex, rs)

		ev, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		if cont {
			t.Fatal("expected cont=false on the gated completion path")
		}
		if ev.Kind != lipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (gated fall-through branch)", ev.Kind)
		}
		assertSettledNotReleased(t, auth, rs)
	})

	// --- popGateDrainHead (Recv pops a buffered finish from the gate drain queue) ---
	// This is the site that leaked before centralization: it had no authority finalization at all.

	t.Run("popGateDrainHead/ok_synthesized_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupStream(t, auth, streamUsageWithCounts())
		rs.gateDrain = []lipapi.Event{{Kind: lipapi.EventResponseFinished}}

		// First Recv pops the finish, finalizes via the helper, re-queues the finish, and emits
		// the synthesized usage_delta. The finish itself surfaces on the next Recv (drain fall-through).
		usageEv, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if usageEv.Kind != lipapi.EventUsageDelta {
			t.Fatalf("first Recv event kind = %q, want usage_delta (synthesized usage from gate-drain ok branch)", usageEv.Kind)
		}
		finish, _ := drainUntilFinish(t, rs)
		if finish.Kind != lipapi.EventResponseFinished {
			t.Fatalf("finish event kind = %q, want response_finished", finish.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("popGateDrainHead/fallthrough_no_stream_usage_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupStream(t, auth, nil)
		rs.gateDrain = []lipapi.Event{{Kind: lipapi.EventResponseFinished}}

		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind != lipapi.EventResponseFinished {
			t.Fatalf("event kind = %q, want response_finished (gate-drain fall-through branch)", ev.Kind)
		}
		if !rs.isFinished() {
			t.Fatal("expected stream marked finished after the gate-drain fall-through branch")
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	// --- idle-recovery (handleRecvError DecisionFinishPostOutput defers to recoverDrain) ---

	setupIdleRecoveryStream := func(t *testing.T, auth *recordingAuthorityService, streamUsage *accountingstream.Reconstructor) (*Executor, *retryRecvStream) {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		if streamUsage != nil {
			ex.StreamUsage = streamUsage
		}
		start := time.Unix(1, 0)
		rs := &retryRecvStream{
			executor: ex,
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{ID: "request-centralized-idle", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
				traceID:  "trace-centralized-idle",
				aLegID:   aLegID,
			}),
			attempt:    testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-centralized-idle", Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate())),
			seenEvents: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "hello"}},
			recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:     true,
				IdleTimeout: time.Second,
			}, start),
			accounting: newAttemptAccountingTracker(start),
		}
		rs.visibleText.WriteString("hello")
		rs.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
		return ex, rs
	}

	callIdleRecovery := func(t *testing.T, rs *retryRecvStream) (lipapi.Event, bool, error) {
		t.Helper()
		return rs.handleRecvError(
			context.Background(),
			context.Background(),
			context.DeadlineExceeded,
			idleContextDeadline{active: true, parent: context.Background()},
			ttftContextDeadline{},
		)
	}

	t.Run("idle_recovery/defers_and_emits_synthesized_usage_then_releases", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		_, rs := setupIdleRecoveryStream(t, auth, streamUsageWithCounts())

		// handleRecvError must defer finalization to the recoverDrain drain path on the next Recv
		// call (matching handleRecvEOF): it leaves the finish in recoverDrain and returns a zero
		// event with cont=false so Recv returns to the caller and re-enters at the recoverDrain
		// drain check, rather than finalizing inline or driving a replacement iteration.
		ev, cont, err := callIdleRecovery(t, rs)
		if err != nil {
			t.Fatalf("handleRecvError: %v", err)
		}
		if cont {
			t.Fatal("expected idle recovery to return to the caller (cont=false) so the next Recv re-enters the recoverDrain drain path")
		}
		if ev.Kind != "" {
			t.Fatalf("deferred idle recovery event kind = %q, want empty (finish stays in recoverDrain)", ev.Kind)
		}
		if auth.settleCalls.Load() != 0 || auth.releaseCalls.Load() != 0 {
			t.Fatalf("settle=%d release=%d, want 0/0 (finalization deferred to the drain path)", auth.settleCalls.Load(), auth.releaseCalls.Load())
		}
		if rs.isFinished() {
			t.Fatal("stream must not be marked finished before the drain path runs")
		}

		// The next Recv call drains the finish via recoverDrain, finalizes via the helper, and
		// emits the synthesized usage_delta (the client-reporting consistency fix).
		finish, usageEv := drainUntilFinish(t, rs)
		if usageEv.Kind != lipapi.EventUsageDelta {
			t.Fatalf("expected synthesized usage_delta before the finish, got kind = %q", usageEv.Kind)
		}
		if finish.Kind != lipapi.EventResponseFinished {
			t.Fatalf("finish event kind = %q, want response_finished", finish.Kind)
		}
		assertReleasedAfterFailedSettle(t, auth, rs)
	})

	t.Run("idle_recovery/settle_success_not_released", func(t *testing.T) {
		t.Parallel()
		auth := makeSuccessSettleAuth()
		_, rs := setupIdleRecoveryStream(t, auth, streamUsageWithCounts())

		ev, cont, err := callIdleRecovery(t, rs)
		if err != nil {
			t.Fatalf("handleRecvError: %v", err)
		}
		if cont {
			t.Fatal("expected idle recovery to return to the caller (cont=false) so the next Recv re-enters the recoverDrain drain path")
		}
		if ev.Kind != "" {
			t.Fatalf("deferred idle recovery event kind = %q, want empty (finish stays in recoverDrain)", ev.Kind)
		}

		_, usageEv := drainUntilFinish(t, rs)
		if usageEv.Kind != lipapi.EventUsageDelta {
			t.Fatalf("expected synthesized usage_delta before the finish, got kind = %q", usageEv.Kind)
		}
		assertSettledNotReleased(t, auth, rs)
	})

	// --- idempotency: a finish flowing through handleGatedPath then recoverDrain is not double-finalized ---

	t.Run("gated_then_recoverDrain/not_double_finalized", func(t *testing.T) {
		t.Parallel()
		auth := makeFailingSettleAuth()
		ex, rs := setupStream(t, auth, streamUsageWithCounts())
		attachPassthroughGate(t, ex, rs)

		// handleGatedPath ok branch finalizes via the helper, sets tokenAccountingFinalized=true,
		// and re-queues the finish to recoverDrain for the synthesized usage emission.
		first, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("handleRecvSuccess: %v", err)
		}
		if cont || first.Kind != lipapi.EventUsageDelta {
			t.Fatalf("first event = %#v cont=%v, want synthesized usage_delta cont=false", first, cont)
		}
		if auth.settleCalls.Load() != 1 || auth.releaseCalls.Load() != 1 {
			t.Fatalf("after gated ok branch: settle=%d release=%d, want 1/1", auth.settleCalls.Load(), auth.releaseCalls.Load())
		}

		// The re-queued finish is popped on the next Recv; the tokenAccountingFinalized guard must
		// skip re-finalization so there is no second settle/release.
		finish, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if finish.Kind != lipapi.EventResponseFinished {
			t.Fatalf("finish event kind = %q, want response_finished", finish.Kind)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (idempotent: re-queued finish must not double-settle)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (idempotent: re-queued finish must not double-release)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after the centralized finalization")
		}
	})
}
