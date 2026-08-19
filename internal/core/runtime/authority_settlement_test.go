package runtime

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestHandleRecvEOFRecoveryAllowsFinalAuthoritySettlement(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-eof-recovery",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})

	start := time.Unix(1, 0)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		executor: ex,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "request-eof-recovery", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-eof-recovery",
			aLegID:   "a-leg-eof-recovery",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(7),
			admissionResult: auth.admitResult,
		}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
		},
		recovery: &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, start)}}
	rs.visibleText.WriteString("hello")
	rs.recovery.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
	rs.markCommitted()

	// handleRecvEOF must defer finalizeTokenAccounting and settle to the downstream drain path
	// in Recv: settle must not have run, recoverDrain must hold the Finish, and the stream
	// must not yet be marked finished.
	if _, err := rs.handleRecvEOF(context.Background()); err != nil {
		t.Fatalf("handleRecvEOF: %v", err)
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls = %d, want 0 (deferred to drain path)", auth.settleCalls.Load())
	}
	if len(rs.recoverDrain) == 0 {
		t.Fatal("recoverDrain should hold the deferred Finish after handleRecvEOF")
	}
	if rs.isFinished() {
		t.Fatal("stream should not be marked finished before the drain path runs")
	}

	// Drain recoverDrain via Recv until the Finish event surfaces; the drain path is what
	// actually finalizes token accounting and settles the authority reservation.
	var finishEv lipapi.Event
	for {
		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind == lipapi.EventResponseFinished {
			finishEv = ev
			break
		}
	}
	if finishEv.Kind != lipapi.EventResponseFinished {
		t.Fatalf("event kind = %q, want response_finished", finishEv.Kind)
	}
	if !rs.isFinished() {
		t.Fatal("stream should be marked finished after the drain path runs")
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	got := auth.lastSettle()
	if got.Kind != authorityapp.SettlementKindFinal {
		t.Fatalf("settle kind = %q, want final", got.Kind)
	}
	if got.Stage != feature.StageIDAttemptLifecycle {
		t.Fatalf("settle stage = %q, want attempt_lifecycle", got.Stage)
	}
	if !got.BackendAttempted {
		t.Fatal("expected final settlement to record backendAttempted=true")
	}
	if !got.OutputCommitted {
		t.Fatal("expected final settlement to record outputCommitted=true after markCommitted")
	}
	if got.FinalUsage.Value != 7 {
		t.Fatalf("final usage = %d, want 7", got.FinalUsage.Value)
	}
}

func TestHandleRecvErrorRecoveryFinishSettlesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-idle-finish",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})

	start := time.Unix(1, 0)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		executor: ex,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "request-idle-finish", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-idle-finish",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(7),
			admissionResult: auth.admitResult,
		}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
		},
		recovery: &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
		}, start)}}
	rs.visibleText.WriteString("hello")
	rs.recovery.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))

	// handleRecvError now defers response_finished authority finalization to the recoverDrain
	// drain path on the next Recv call (single-owner invariant matches handleRecvEOF, and the
	// centralized helper emits the synthesized usage_delta there). It returns a zero event with
	// cont=false so Recv returns to the caller and re-enters at the recoverDrain drain check,
	// rather than finalizing inline or driving a replacement iteration.
	ev, cont, err := rs.handleRecvError(context.Background(), context.Background(), context.DeadlineExceeded, idleContextDeadline{active: true, parent: context.Background()}, ttftContextDeadline{})
	if err != nil {
		t.Fatalf("handleRecvError: %v", err)
	}
	if cont {
		t.Fatal("expected idle recovery to return to the caller so the next Recv drains recoverDrain")
	}
	if ev.Kind != "" {
		t.Fatalf("deferred idle recovery event kind = %q, want empty (finish stays in recoverDrain)", ev.Kind)
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls = %d, want 0 (finalization deferred to the drain path)", auth.settleCalls.Load())
	}
	if len(rs.recoverDrain) == 0 {
		t.Fatal("recoverDrain should hold the deferred finish after handleRecvError")
	}
	if rs.isFinished() {
		t.Fatal("stream should not be marked finished before the drain path runs")
	}

	// Drain recoverDrain via Recv; the drain path finalizes via the centralized helper and emits
	// the synthesized usage_delta before the finish.
	var finishEv lipapi.Event
	var sawSynthesizedUsage bool
	for {
		ev, err := rs.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind == lipapi.EventUsageDelta {
			sawSynthesizedUsage = true
			continue
		}
		if ev.Kind == lipapi.EventResponseFinished {
			finishEv = ev
			break
		}
	}
	if finishEv.Kind != lipapi.EventResponseFinished {
		t.Fatalf("event kind = %q, want response_finished", finishEv.Kind)
	}
	if !sawSynthesizedUsage {
		t.Fatal("expected the drain path to emit the synthesized usage_delta before the finish")
	}
	if !rs.isFinished() {
		t.Fatal("stream should be marked finished after the drain path runs")
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	if !testAttemptSession(rs).authority.Settled() {
		t.Fatal("expected authoritySettled=true after recovery finish")
	}
	if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindFinal {
		t.Fatalf("settle kind = %q, want final", got.Kind)
	}
}

func TestRetryRecvStreamCloseSettlesAuthorityReservation(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-close",
			ReservedAmount: authorityInputAmount(8),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		executor: ex,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "request-close"},
			traceID:  "trace-close",
			aLegID:   aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(8),
			admissionResult: auth.admitResult,
		}, authorityCandidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testStoreInner(rs, lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}}))

	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	got := auth.lastSettle()
	if got.Kind != authorityapp.SettlementKindCancellation {
		t.Fatalf("settle kind = %q, want cancellation", got.Kind)
	}
	if !got.ClientCanceled {
		t.Fatal("expected client canceled settlement")
	}
	if !testAttemptSession(rs).authority.Settled() {
		t.Fatal("expected authoritySettled=true after Close")
	}
}

func TestHandleRecvEOFWithoutRecoveryPartialSettlesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-eof-failure",
			ReservedAmount: authorityInputAmount(8),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	start := time.Unix(1, 0)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		executor: ex,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "request-eof-failure"},
			traceID:  "trace-eof-failure",
			aLegID:   "a-leg-eof-failure",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(8),
			admissionResult: auth.admitResult,
		}, authorityCandidate())),
		seenEvents: []lipapi.Event{{Kind: lipapi.EventUsageDelta, TotalTokens: 4, CostNanoUnits: 11}},
		recovery:   &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: false}, start)}}

	_, err := rs.handleRecvEOF(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("handleRecvEOF err = %v, want EOF", err)
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("partial settle calls = %d, want 1", auth.settleCalls.Load())
	}
	if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindPartial {
		t.Fatalf("settle kind = %q, want partial", got.Kind)
	}
	if got := auth.lastSettle(); got.Stage != feature.StageIDAttemptLifecycle || !got.BackendAttempted || got.OutputCommitted {
		t.Fatalf("partial settle must project attempt lifecycle with no committed output: %#v", got)
	}
}

func TestRetryRecvStreamAuthoritySettlementPaths(t *testing.T) {
	t.Parallel()

	t.Run("final", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-final",
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		ex.StreamUsage = accountingstream.New(&stubStreamCounter{
			call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
			output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
		}, accountingstream.Config{})
		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			executor: ex,
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{ID: "request-final", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate())),
		}
		usageEv, ok, err := rs.finalizeTokenAccounting(context.Background(), rs.attempt.snapshot(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("finalizeTokenAccounting: %v", err)
		}
		if !ok {
			t.Fatal("expected usage accounting to finalize")
		}
		if usageEv.TotalTokens == 0 {
			t.Fatal("expected reconstructed usage to be non-zero")
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		settle := auth.lastSettle()
		if settle.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", settle.Kind)
		}
		if settle.FinalUsage.Unit != authoritydomain.AmountUnitInputTokens {
			t.Fatalf("final usage unit = %q, want input_tokens", settle.FinalUsage.Unit)
		}
		if settle.FinalUsage.Value != int64(usageEv.InputTokens) {
			t.Fatalf("final usage = %d, want %d", settle.FinalUsage.Value, usageEv.InputTokens)
		}
	})

	t.Run("partial-and-cancel", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-partial",
				ReservedAmount: authorityInputAmount(8),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			executor: ex,
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{ID: "request-partial"},
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(8),
				admissionResult: auth.admitResult,
			}, authorityCandidate())),
			seenEvents: []lipapi.Event{{Kind: lipapi.EventUsageDelta, TotalTokens: 4, CostNanoUnits: 11}},
		}
		rs.recordPartialTokenAccounting(context.Background(), rs.attempt.snapshot(), "partial", errors.New("stream dropped"))
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("partial settle calls = %d, want 1", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindPartial {
			t.Fatalf("partial settle kind = %q, want partial", got.Kind)
		}

		auth.settleCalls.Store(0)
		auth.lastSettleInput.Store(authorityapp.SettleInput{})
		// After the partial settle above, authoritySettled is true. The cancellation-authority
		// leak fix (persistCancellationBilling -> settleCancellationAuthority) no-ops when the
		// reservation is already settled, so a strict reservation is not double-settled as
		// Cancellation on top of the prior Partial settle. Previously this re-settled, which a
		// strict authority store would reject or double-count; the leak fix removes that.
		rs.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client canceled")
		if auth.settleCalls.Load() != 0 {
			t.Fatalf("cancellation settle calls = %d, want 0 (already-settled reservation must not be double-settled)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (already-settled reservation must not be released)", auth.releaseCalls.Load())
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled to remain true after cancellation billing on an already-settled reservation")
		}
	})

	t.Run("final-settle-uses-admission-estimate-when-reconstruction-empty", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-empty-reconstruct",
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		ex.StreamUsage = accountingstream.New(nil, accountingstream.Config{})

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			executor: ex,
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{ID: "request-empty-reconstruct", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate())),
		}
		usageEv, ok, err := rs.finalizeTokenAccounting(context.Background(), rs.attempt.snapshot(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("finalizeTokenAccounting: %v", err)
		}
		if ok {
			t.Fatal("expected ok=false when reconstruction returns no usage events")
		}
		if usageEv.Kind != "" {
			t.Fatalf("expected empty usage event, got %#v", usageEv)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		settle := auth.lastSettle()
		if settle.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", settle.Kind)
		}
		if settle.FinalUsage.Unit != authoritydomain.AmountUnitInputTokens {
			t.Fatalf("final usage unit = %q, want input_tokens", settle.FinalUsage.Unit)
		}
		if settle.FinalUsage.Value != 7 {
			t.Fatalf("final usage = %d, want admission estimate 7", settle.FinalUsage.Value)
		}
		if settle.EstimatedUsage.Value != 7 {
			t.Fatalf("estimated usage = %d, want 7", settle.EstimatedUsage.Value)
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after final settle")
		}
	})

	t.Run("authority-only-final-without-stream-usage", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-authority-only",
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		ex.StreamUsage = nil

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			executor: ex,
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{ID: "request-authority-only", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate())),
		}
		usageEv, ok, err := rs.finalizeTokenAccounting(context.Background(), rs.attempt.snapshot(), lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("finalizeTokenAccounting: %v", err)
		}
		if ok {
			t.Fatal("expected ok=false when stream usage is disabled")
		}
		if usageEv.Kind != "" {
			t.Fatalf("expected empty usage event, got %#v", usageEv)
		}
		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		settle := auth.lastSettle()
		if settle.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", settle.Kind)
		}
		if settle.ReservationID != "reservation-authority-only" {
			t.Fatalf("settle reservation ID = %q, want reservation-authority-only", settle.ReservationID)
		}
		if !testAttemptSession(rs).authority.Settled() {
			t.Fatal("expected authoritySettled=true after final settle")
		}
	})
}

// Quantity settlement is covered by lifecycle request/token tests.
// Quantity zero/missing behavior is covered by request/token lifecycle tests.
func TestMergeUsageEventsForClientPreservesMoneyCost(t *testing.T) {
	t.Parallel()

	usage := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   4,
		OutputTokens:  2,
		TotalTokens:   6,
		CostNanoUnits: 150,
		Currency:      "USD",
		CostSource:    "provider_reported",
		CostPresent:   true,
		UsageScopes: []lipapi.ScopedUsageDelta{
			{
				InputTokens:  4,
				OutputTokens: 2,
				TotalTokens:  6,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneProviderBillable,
					Source:    lipapi.UsageSourceProviderReported,
					Authority: lipapi.UsageAuthorityAuthoritative,
				},
			},
			{
				InputTokens:  4,
				OutputTokens: 2,
				TotalTokens:  6,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneClientVisible,
					Source:    lipapi.UsageSourceLocalTokenizer,
					Authority: lipapi.UsageAuthorityEstimated,
				},
			},
		},
	}

	merged := mergeUsageEventsForClient([]lipapi.Event{usage}, true)
	if len(merged.UsageScopes) != 1 {
		t.Fatalf("merged usage scopes = %d, want 1 client-visible scope", len(merged.UsageScopes))
	}
	if merged.CostNanoUnits != 150 {
		t.Fatalf("merged cost = %d, want 150", merged.CostNanoUnits)
	}
	if merged.Currency != "USD" {
		t.Fatalf("merged currency = %q, want USD", merged.Currency)
	}
	if merged.CostSource != "provider_reported" {
		t.Fatalf("merged cost source = %q, want provider_reported", merged.CostSource)
	}
	if !merged.CostPresent {
		t.Fatal("merged CostPresent = false, want true")
	}

	got := attemptAuthorityUsageAmount(merged, authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 999})
	if got.Unit != authoritydomain.AmountUnitInputTokens {
		t.Fatalf("final amount unit = %q, want money_nano", got.Unit)
	}
	if got.Value != 4 {
		t.Fatalf("final amount value = %d, want observed input quantity 4", got.Value)
	}
}

func TestMergeUsageEventsAggregatesLaterScopeCounters(t *testing.T) {
	t.Parallel()

	merged := mergeUsageEventsForClient([]lipapi.Event{{
		Kind: lipapi.EventUsageDelta,
		UsageScopes: []lipapi.ScopedUsageDelta{
			{
				InputTokens:   0,
				UsagePresence: lipapi.UsagePresence{InputTokens: true},
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneProviderBillable,
					Source:    lipapi.UsageSourceProviderReported,
					Authority: lipapi.UsageAuthorityAuthoritative,
				},
			},
			{
				OutputTokens:  12,
				UsagePresence: lipapi.UsagePresence{OutputTokens: true},
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneProviderBillable,
					Source:    lipapi.UsageSourceProviderReported,
					Authority: lipapi.UsageAuthorityAuthoritative,
				},
			},
		},
	}}, false)
	if merged.InputTokens != 0 || merged.OutputTokens != 12 {
		t.Fatalf("aggregated counters = in=%d out=%d, want in=0 out=12", merged.InputTokens, merged.OutputTokens)
	}
	if !merged.UsagePresence.InputTokens || !merged.UsagePresence.OutputTokens {
		t.Fatalf("presence = %+v, want input and output present", merged.UsagePresence)
	}
	got := attemptAuthorityUsageAmount(merged, authoritydomain.Amount{Unit: authoritydomain.AmountUnitOutputTokens, Value: 99})
	if got.Value != 12 {
		t.Fatalf("settled output = %d, want 12 from later scope", got.Value)
	}
}
