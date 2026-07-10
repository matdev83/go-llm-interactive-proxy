package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// TestPersistCancellationBillingUsageAuthorityLeak locks in the fix for the
// usage-authority reservation leak on the cancellation path.
//
// Root cause: persistCancellationBilling guarded on accounting.usageObserved and
// returned early, skipping settleAttemptAuthority entirely. usageObserved flips to
// true when a backend EventUsageDelta is processed mid-stream (handleRecvSuccess),
// but that mid-stream delta does NOT settle the strict reservation (the final settle
// runs at response_finished via finalizeTokenAccounting). So a cancel after a usage
// delta but before response_finished left the reservation locked until the accounting
// window resets.
//
// The fix settles the reservation with the observed usage (Cancellation kind) when
// usageObserved is true, no-ops when the reservation is already settled, and releases
// with ReleaseKindLosing when the settle fails (mirroring finalizeResponseFinishedAuthority).
func TestPersistCancellationBillingUsageAuthorityLeak(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-cancel-usage-leak"

	// usageDelta stands in for the mid-stream backend EventUsageDelta that
	// handleRecvSuccess observes (flipping accounting.usageObserved=true) without
	// settling the strict reservation.
	usageDelta := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   5,
		OutputTokens:  2,
		TotalTokens:   7,
		CostNanoUnits: 42,
		Currency:      "USD",
	}

	// setupStream stages a retryRecvStream with a reserved authority and a mid-stream
	// usage delta already observed, then returns the stream. It does NOT settle the
	// reservation (the leak precondition: usage observed but not yet finalized).
	setupStream := func(t *testing.T, auth *recordingAuthorityService) *retryRecvStream {
		t.Helper()
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		rs := &retryRecvStream{
			executor: ex,
			baseline: lipapi.Call{
				ID:         "request-cancel-usage-leak",
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions},
			},
			bleg: b2bua.BLegRecord{BLegID: "b-leg-cancel-usage-leak", Seq: 1},
			cand: authorityCandidate(),
			authority: testAuthorityLifecycle(ex, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(7),
				admissionResult: auth.admitResult,
			}, authorityCandidate()),
			seenEvents: []lipapi.Event{usageDelta},
			traceID:    "trace-cancel-usage-leak",
			aLegID:     aLegID,
			accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
		}
		// Simulate handleRecvSuccess having processed the EventUsageDelta: this flips
		// accounting.usageObserved=true (the leak trigger) without settling the reservation.
		rs.accounting.observeUsage(usageDelta)
		if !rs.accounting.usageObserved {
			t.Fatal("test staging: usageObserved must be true after observeUsage")
		}
		return rs
	}

	// cancelAfterUsageAuth builds a recording authority service with the supplied settle
	// error (nil settleErr means settle succeeds).
	cancelAfterUsageAuth := func(settleErr error) *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  reservationID,
				ReservedAmount: authorityInputAmount(7),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
			settleErr: settleErr,
			status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
		}
	}

	t.Run("cancel_after_usage_settles_cancellation", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(nil)
		rs := setupStream(t, auth)

		rs.persistCancellationBilling(context.Background(), "client canceled")

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (cancel after usage must settle the reservation, not leak)", auth.settleCalls.Load())
		}
		got := auth.lastSettle()
		if got.Kind != authorityapp.SettlementKindCancellation {
			t.Fatalf("settle kind = %q, want cancellation", got.Kind)
		}
		if !got.ClientCanceled {
			t.Fatal("expected client canceled settlement")
		}
		if got.ReservationID != reservationID {
			t.Fatalf("settle reservation ID = %q, want %q", got.ReservationID, reservationID)
		}
		if got.FinalUsage.Value != 5 {
			t.Fatalf("final usage = %d, want 5 (observed usage delta input tokens, not the admission estimate)", got.FinalUsage.Value)
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (successful settle must not release)", auth.releaseCalls.Load())
		}
		if !rs.authority.Settled() {
			t.Fatal("expected authoritySettled=true after successful cancellation settle")
		}
	})

	t.Run("cancel_after_usage_failed_settle_releases_losing", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(errors.New("settle boom"))
		rs := setupStream(t, auth)

		rs.persistCancellationBilling(context.Background(), "client canceled")

		if auth.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d, want 1 (single failed cancellation settle)", auth.settleCalls.Load())
		}
		if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindCancellation {
			t.Fatalf("settle kind = %q, want cancellation", got.Kind)
		}
		if auth.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d, want 1 (reservation must be released when settle failed, not leaked)", auth.releaseCalls.Load())
		}
		rel := auth.lastRelease()
		if rel.Kind != authorityapp.ReleaseKindLosing {
			t.Fatalf("release kind = %q, want %q", rel.Kind, authorityapp.ReleaseKindLosing)
		}
		if rel.ReservationID != reservationID {
			t.Fatalf("release reservation ID = %q, want %q", rel.ReservationID, reservationID)
		}
		if !rs.authority.Settled() {
			t.Fatal("expected authoritySettled=true after fallback release so later handlers cannot double-release")
		}
	})

	t.Run("cancel_after_usage_no_double_settle_when_already_settled", func(t *testing.T) {
		t.Parallel()
		auth := cancelAfterUsageAuth(nil)
		rs := setupStream(t, auth)
		// Simulate a prior final settle (e.g. response_finished already finalized the
		// reservation) so authoritySettled is true before the cancellation billing call.
		rs.authority.settled.Store(true)

		rs.persistCancellationBilling(context.Background(), "client canceled")

		if auth.settleCalls.Load() != 0 {
			t.Fatalf("settle calls = %d, want 0 (already-settled reservation must not be double-settled)", auth.settleCalls.Load())
		}
		if auth.releaseCalls.Load() != 0 {
			t.Fatalf("release calls = %d, want 0 (already-settled reservation must not be released)", auth.releaseCalls.Load())
		}
		if !rs.authority.Settled() {
			t.Fatal("expected authoritySettled to remain true")
		}
	})
}
