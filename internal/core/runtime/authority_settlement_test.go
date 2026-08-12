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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
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
		executor: ex,
		baseline: lipapi.Call{ID: "request-eof-recovery", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
		cand:     authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(7),
			admissionResult: auth.admitResult,
		}, authorityCandidate()),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
		},
		recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, start),
		traceID:       "trace-eof-recovery",
		aLegID:        "a-leg-eof-recovery",
		accounting:    newAttemptAccountingTracker(start),
	}
	rs.visibleText.WriteString("hello")
	rs.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
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
		executor: ex,
		baseline: lipapi.Call{ID: "request-idle-finish", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
		cand:     authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(7),
			admissionResult: auth.admitResult,
		}, authorityCandidate()),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
		},
		recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
		}, start),
		traceID:    "trace-idle-finish",
		aLegID:     aLegID,
		accounting: newAttemptAccountingTracker(start),
	}
	rs.visibleText.WriteString("hello")
	rs.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))

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
	if !rs.authority.Settled() {
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
		executor: ex,
		baseline: lipapi.Call{ID: "request-close"},
		bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
		cand:     authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(8),
			admissionResult: auth.admitResult,
		}, authorityCandidate()),
		traceID: "trace-close",
		aLegID:  aLegID,
	}
	rs.storeInner(lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}}))

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
	if !rs.authority.Settled() {
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
		executor: ex,
		baseline: lipapi.Call{ID: "request-eof-failure"},
		bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
		cand:     authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(8),
			admissionResult: auth.admitResult,
		}, authorityCandidate()),
		seenEvents:    []lipapi.Event{{Kind: lipapi.EventUsageDelta, TotalTokens: 4, CostNanoUnits: 11, Currency: "USD"}},
		recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: false}, start),
		traceID:       "trace-eof-failure",
		aLegID:        "a-leg-eof-failure",
		accounting:    newAttemptAccountingTracker(start),
	}

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
			executor: ex,
			baseline: lipapi.Call{ID: "request-final", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
			cand:     authorityCandidate(),
			authority: testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate()),
		}
		usageEv, ok, err := rs.finalizeTokenAccounting(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
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
			executor: ex,
			baseline: lipapi.Call{ID: "request-partial"},
			bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
			cand:     authorityCandidate(),
			authority: testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(8),
				admissionResult: auth.admitResult,
			}, authorityCandidate()),
			seenEvents: []lipapi.Event{{Kind: lipapi.EventUsageDelta, TotalTokens: 4, CostNanoUnits: 11, Currency: "USD"}},
		}
		rs.recordPartialTokenAccounting(context.Background(), "partial", errors.New("stream dropped"))
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
		rs.persistCancellationBilling(context.Background(), "client canceled")
		if auth.settleCalls.Load() != 0 {
			t.Fatalf("cancellation settle calls = %d, want 0 (already-settled reservation must not be double-settled)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (already-settled reservation must not be released)", auth.releaseCalls.Load())
		}
		if !rs.authority.Settled() {
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
			executor: ex,
			baseline: lipapi.Call{ID: "request-empty-reconstruct", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
			cand:     authorityCandidate(),
			authority: testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate()),
		}
		usageEv, ok, err := rs.finalizeTokenAccounting(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
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
		if !rs.authority.Settled() {
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
			executor: ex,
			baseline: lipapi.Call{ID: "request-authority-only", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
			cand:     authorityCandidate(),
			authority: testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate()),
		}
		usageEv, ok, err := rs.finalizeTokenAccounting(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
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
		if !rs.authority.Settled() {
			t.Fatal("expected authoritySettled=true after final settle")
		}
	})
}

func TestExecutorAuthoritySettlementUsesAdmissionUnit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		unit    authoritydomain.AmountUnit
		value   int64
		usage   lipapi.Event
		wantVal int64
	}{
		{
			name:    "requests",
			unit:    authoritydomain.AmountUnitRequests,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 99, InputTokens: 4, OutputTokens: 5, CacheReadTokens: 1, CacheWriteTokens: 2, ReasoningTokens: 3},
			wantVal: 1,
		},
		{
			name:    "input-tokens",
			unit:    authoritydomain.AmountUnitInputTokens,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 99, InputTokens: 4, OutputTokens: 5},
			wantVal: 4,
		},
		{
			name:    "output-tokens",
			unit:    authoritydomain.AmountUnitOutputTokens,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 99, InputTokens: 4, OutputTokens: 5},
			wantVal: 5,
		},
		{
			name:    "cache-read-tokens",
			unit:    authoritydomain.AmountUnitCacheReadTokens,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 99, CacheReadTokens: 6},
			wantVal: 6,
		},
		{
			name:    "cache-write-tokens",
			unit:    authoritydomain.AmountUnitCacheWriteTokens,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 99, CacheWriteTokens: 7},
			wantVal: 7,
		},
		{
			name:    "reasoning-tokens",
			unit:    authoritydomain.AmountUnitReasoningTokens,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 99, ReasoningTokens: 8},
			wantVal: 8,
		},
		{
			name:    "money-nano-ignores-stream-cost",
			unit:    authoritydomain.AmountUnitMoneyNano,
			value:   100,
			usage:   lipapi.Event{CostNanoUnits: 150, Currency: "USD", CostPresent: true},
			wantVal: 100,
		},
		{
			name:    "money-nano-uses-reserved-estimate-currency",
			unit:    authoritydomain.AmountUnitMoneyNano,
			value:   100,
			usage:   lipapi.Event{CostNanoUnits: 150, Currency: "", CostPresent: true},
			wantVal: 100,
		},
		{
			name:    "total-tokens",
			unit:    authoritydomain.AmountUnitTotalTokens,
			value:   10,
			usage:   lipapi.Event{TotalTokens: 12, InputTokens: 4, OutputTokens: 5},
			wantVal: 12,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auth := &recordingAuthorityService{
				admitResult: authorityapp.AdmissionResult{
					Allowed:        true,
					Reserved:       true,
					ReservationID:  "reservation-1",
					ReservedAmount: authoritydomain.Amount{Unit: tc.unit, Value: tc.value},
					PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
				},
				status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
			}
			ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
			state := attemptAuthorityState{
				admissionInput: authorityapp.AdmissionInput{
					Correlation: controlplane.Correlation{
						TraceID:    "trace-1",
						RequestID:  "request-1",
						ALegID:     "a-leg-1",
						BLegID:     "b-leg-1",
						AttemptSeq: 1,
						BackendID:  "backend-1",
						Model:      "model-1",
					},
					Scope: scope.PrincipalScopeView{},
					Request: authoritydomain.Amount{
						Unit:     tc.unit,
						Value:    tc.value,
						Currency: "EUR",
					},
					Spend: authoritydomain.Amount{
						Unit:     authoritydomain.AmountUnitMoneyNano,
						Value:    100,
						Currency: "USD",
					},
					Authority: authoritydomain.AuthorityLevelEstimated,
					ReservationKey: authoritydomain.ReservationKey{
						LogicalRequestID: "request-1",
						ALegID:           "a-leg-1",
						BLegID:           "b-leg-1",
						AttemptID:        "b-leg-1",
						RuleID:           "tenant.requests",
						Sequence:         1,
					},
				},
				admissionResult: auth.admitResult,
			}

			lifecycle := newAuthorityLifecycle(ex.UsageAuthority, ex.Log, state, authorityCandidate())
			lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, tc.usage, false)
			if auth.settleCalls.Load() != 1 {
				t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
			}
			got := auth.lastSettle()
			if got.FinalUsage.Unit != tc.unit {
				t.Fatalf("final usage unit = %q, want %q", got.FinalUsage.Unit, tc.unit)
			}
			if got.FinalUsage.Value != tc.wantVal {
				t.Fatalf("final usage value = %d, want %d", got.FinalUsage.Value, tc.wantVal)
			}
			if tc.unit == authoritydomain.AmountUnitMoneyNano {
				// Stream currency is ignored; reserved/request estimate owns money units.
				wantCurrency := "EUR"
				if got.FinalUsage.Currency != wantCurrency {
					t.Fatalf("final usage currency = %q, want %q", got.FinalUsage.Currency, wantCurrency)
				}
			}
			if got.ReservedUsage.Unit != tc.unit {
				t.Fatalf("reserved usage unit = %q, want %q", got.ReservedUsage.Unit, tc.unit)
			}
			if got.ReservedUsage.Value != tc.value {
				t.Fatalf("reserved usage value = %d, want %d", got.ReservedUsage.Value, tc.value)
			}
			_ = aLegID
		})
	}
}

func TestRetryRecvStreamAuthorityPartialSettlementUsesAdmissionUnit(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-partial",
			ReservedAmount: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 8},
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	rs := &retryRecvStream{
		executor: ex,
		baseline: lipapi.Call{ID: "request-partial", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:     b2bua.BLegRecord{BLegID: aLegID, Seq: 1},
		cand:     authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{
			admissionInput: authorityapp.AdmissionInput{
				Correlation: controlplane.Correlation{
					TraceID:    "trace-1",
					RequestID:  "request-1",
					ALegID:     "a-leg-1",
					BLegID:     "b-leg-1",
					AttemptSeq: 1,
					BackendID:  "backend-1",
					Model:      "model-1",
				},
				Scope: scope.PrincipalScopeView{},
				Request: authoritydomain.Amount{
					Unit:  authoritydomain.AmountUnitInputTokens,
					Value: 8,
				},
				Spend: authoritydomain.Amount{
					Unit:     authoritydomain.AmountUnitMoneyNano,
					Value:    100,
					Currency: "USD",
				},
				Authority: authoritydomain.AuthorityLevelEstimated,
				ReservationKey: authoritydomain.ReservationKey{
					LogicalRequestID: "request-1",
					ALegID:           "a-leg-1",
					BLegID:           "b-leg-1",
					AttemptID:        "b-leg-1",
					RuleID:           "tenant.requests",
					Sequence:         1,
				},
			},
			admissionResult: auth.admitResult,
		}, authorityCandidate()),
		seenEvents: []lipapi.Event{{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8}},
	}

	rs.recordPartialTokenAccounting(context.Background(), "partial", errors.New("stream dropped"))
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	got := auth.lastSettle()
	if got.Kind != authorityapp.SettlementKindPartial {
		t.Fatalf("partial settle kind = %q, want partial", got.Kind)
	}
	if got.FinalUsage.Unit != authoritydomain.AmountUnitInputTokens {
		t.Fatalf("partial final usage unit = %q, want input_tokens", got.FinalUsage.Unit)
	}
	if got.FinalUsage.Value != 3 {
		t.Fatalf("partial final usage value = %d, want 3", got.FinalUsage.Value)
	}
	if got.ReservedUsage.Unit != authoritydomain.AmountUnitInputTokens {
		t.Fatalf("partial reserved usage unit = %q, want input_tokens", got.ReservedUsage.Unit)
	}
}

// TestAuthorityFinalSettlementZeroVsMissingUsage locks in the distinction between a
// completion that reports ZERO usage (a legitimate zero reading that must settle at
// zero) and one that reports NO usage at all (absent, which must fall back to the
// preflight estimate). attemptAuthorityUsageAmount must not conflate present-but-zero
// with missing; otherwise zero-usage/zero-cost completions are reconciled at the
// reserved estimate and over-charge the caller's quota/budget.
func TestAuthorityFinalSettlementZeroVsMissingUsage(t *testing.T) {
	t.Parallel()

	reservedValue := int64(7)

	setup := func(t *testing.T, unit authoritydomain.AmountUnit, currency string) (*recordingAuthorityService, authorityLifecycle) {
		t.Helper()
		auth := &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "reservation-zero-vs-missing",
				ReservedAmount: authoritydomain.Amount{Unit: unit, Value: reservedValue, Currency: currency},
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
		ex, _, _ := newAuthorityRuntimeTestExecutor(t, auth)
		state := attemptAuthorityState{
			admissionInput: authorityapp.AdmissionInput{
				Correlation: controlplane.Correlation{
					TraceID:    "trace-zero-vs-missing",
					RequestID:  "request-zero-vs-missing",
					ALegID:     "a-leg-zero-vs-missing",
					BLegID:     "b-leg-zero-vs-missing",
					AttemptSeq: 1,
					BackendID:  "backend-1",
					Model:      "model-1",
				},
				Scope: scope.PrincipalScopeView{},
				Request: authoritydomain.Amount{
					Unit:     unit,
					Value:    reservedValue,
					Currency: currency,
				},
				Spend: authoritydomain.Amount{
					Unit:     authoritydomain.AmountUnitMoneyNano,
					Value:    100,
					Currency: "USD",
				},
				Authority: authoritydomain.AuthorityLevelEstimated,
				ReservationKey: authoritydomain.ReservationKey{
					LogicalRequestID: "request-zero-vs-missing",
					ALegID:           "a-leg-zero-vs-missing",
					BLegID:           "b-leg-zero-vs-missing",
					AttemptID:        "b-leg-zero-vs-missing",
					RuleID:           "tenant.requests",
					Sequence:         1,
				},
			},
			admissionResult: auth.admitResult,
		}
		return auth, newAuthorityLifecycle(ex.UsageAuthority, ex.Log, state, authorityCandidate())
	}

	t.Run("present-but-zero-tokens-settles-at-zero", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitInputTokens, "")

		// A genuine provider-reported usage delta carrying all-zero token counts: a
		// legitimate zero-usage completion. The scoped reading signals presence.
		usage := lipapi.Event{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  0,
			OutputTokens: 0,
			TotalTokens:  0,
			UsageScopes: []lipapi.ScopedUsageDelta{{
				InputTokens:  0,
				OutputTokens: 0,
				TotalTokens:  0,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneProviderBillable,
					Source:    lipapi.UsageSourceProviderReported,
					Authority: lipapi.UsageAuthorityAuthoritative,
				},
			}},
		}

		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", got.Kind)
		}
		if got.FinalUsage.Unit != authoritydomain.AmountUnitInputTokens {
			t.Fatalf("final usage unit = %q, want input_tokens", got.FinalUsage.Unit)
		}
		if got.FinalUsage.Value != 0 {
			t.Fatalf("final usage value = %d, want 0 (legitimate zero usage must not be reconciled at the reserved estimate %d)",
				got.FinalUsage.Value, reservedValue)
		}
		if got.EstimatedUsage.Value != reservedValue {
			t.Fatalf("estimated usage = %d, want %d (estimate must still be carried for audit)", got.EstimatedUsage.Value, reservedValue)
		}
	})

	t.Run("top-level-authoritative-zero-settles-at-zero", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitInputTokens, "")
		usage := lipapi.Event{
			Kind: lipapi.EventUsageDelta,
			Accounting: lipapi.UsageAccountingMetadata{
				Plane:     lipapi.UsagePlaneProviderBillable,
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
			},
		}
		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false)
		if got := auth.lastSettle().FinalUsage.Value; got != 0 {
			t.Fatalf("final usage value = %d, want authoritative zero", got)
		}
	})

	t.Run("mixed-authoritative-snapshot-preserves-explicit-zero-output", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitOutputTokens, "")
		usage := lipapi.Event{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  5,
			OutputTokens: 0,
			TotalTokens:  5,
			UsagePresence: lipapi.UsagePresence{
				InputTokens:  true,
				OutputTokens: true,
				TotalTokens:  true,
			},
			Accounting: lipapi.UsageAccountingMetadata{
				Plane:     lipapi.UsagePlaneProviderBillable,
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
			},
		}

		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false)
		if got := auth.lastSettle().FinalUsage.Value; got != 0 {
			t.Fatalf("final output usage = %d, want explicit authoritative zero", got)
		}
	})

	t.Run("mixed-snapshot-without-output-presence-falls-back-to-estimate", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitOutputTokens, "")
		usage := lipapi.Event{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  5,
			OutputTokens: 0,
			TotalTokens:  5,
			UsagePresence: lipapi.UsagePresence{
				InputTokens: true,
				TotalTokens: true,
			},
			Accounting: lipapi.UsageAccountingMetadata{
				Plane:     lipapi.UsagePlaneProviderBillable,
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
			},
		}

		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false)
		if got := auth.lastSettle().FinalUsage.Value; got != reservedValue {
			t.Fatalf("final output usage = %d, want reserved estimate %d", got, reservedValue)
		}
	})

	t.Run("money-stream-cost-settles-at-reserved-estimate", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitMoneyNano, "USD")

		// Stream CostPresent/CostNanoUnits are not monetary authority after Phase 8.
		// Residual money-unit reservations settle at the reserved estimate.
		usage := lipapi.Event{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   4,
			OutputTokens:  2,
			TotalTokens:   6,
			CostNanoUnits: 0,
			Currency:      "USD",
			CostPresent:   true,
			CostSource:    string(lipapi.UsageSourceProviderReported),
			UsageScopes: []lipapi.ScopedUsageDelta{{
				InputTokens:  4,
				OutputTokens: 2,
				TotalTokens:  6,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneProviderBillable,
					Source:    lipapi.UsageSourceProviderReported,
					Authority: lipapi.UsageAuthorityAuthoritative,
				},
			}},
		}

		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.FinalUsage.Unit != authoritydomain.AmountUnitMoneyNano {
			t.Fatalf("final usage unit = %q, want money_nano", got.FinalUsage.Unit)
		}
		if got.FinalUsage.Value != reservedValue {
			t.Fatalf("final money value = %d, want reserved estimate %d (stream cost is not authority)",
				got.FinalUsage.Value, reservedValue)
		}
		if got.FinalUsage.Currency != "USD" {
			t.Fatalf("final usage currency = %q, want USD", got.FinalUsage.Currency)
		}
		if got.EstimatedUsage.Value != reservedValue {
			t.Fatalf("estimated usage = %d, want %d (estimate must still be carried for audit)", got.EstimatedUsage.Value, reservedValue)
		}
	})

	t.Run("absent-usage-still-falls-back-to-estimate", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitInputTokens, "")

		// No usage delta at all (the empty event passed when StreamUsage is nil or
		// reconstruction returns no events): the preflight estimate must stand.
		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, lipapi.Event{}, false)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want final", got.Kind)
		}
		if got.FinalUsage.Unit != authoritydomain.AmountUnitInputTokens {
			t.Fatalf("final usage unit = %q, want input_tokens", got.FinalUsage.Unit)
		}
		if got.FinalUsage.Value != reservedValue {
			t.Fatalf("final usage value = %d, want %d (absent usage must fall back to the preflight estimate)",
				got.FinalUsage.Value, reservedValue)
		}
	})

	t.Run("partial-reporting-still-falls-back-to-estimate", func(t *testing.T) {
		t.Parallel()
		auth, lifecycle := setup(t, authoritydomain.AmountUnitInputTokens, "")

		// Usage reported for output tokens but not for the reserved input-tokens
		// unit: the reserved unit's reading is missing (not zero), so the estimate
		// fallback must still apply.
		usage := lipapi.Event{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  0,
			OutputTokens: 5,
			TotalTokens:  5,
			UsageScopes: []lipapi.ScopedUsageDelta{{
				InputTokens:  0,
				OutputTokens: 5,
				TotalTokens:  5,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:  lipapi.UsagePlaneProviderBillable,
					Source: lipapi.UsageSourceProviderReported,
				},
			}},
		}

		lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usage, false)

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.FinalUsage.Unit != authoritydomain.AmountUnitInputTokens {
			t.Fatalf("final usage unit = %q, want input_tokens", got.FinalUsage.Unit)
		}
		if got.FinalUsage.Value != reservedValue {
			t.Fatalf("final usage value = %d, want %d (missing reserved unit must fall back to the estimate)",
				got.FinalUsage.Value, reservedValue)
		}
	})
}

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

	got := attemptAuthorityUsageAmount(merged, authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 999, Currency: "USD"})
	if got.Unit != authoritydomain.AmountUnitMoneyNano {
		t.Fatalf("final amount unit = %q, want money_nano", got.Unit)
	}
	if got.Value != 999 {
		t.Fatalf("final amount value = %d, want reserved estimate 999 (stream cost is not authority)", got.Value)
	}
	if got.Currency != "USD" {
		t.Fatalf("final amount currency = %q, want USD", got.Currency)
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
