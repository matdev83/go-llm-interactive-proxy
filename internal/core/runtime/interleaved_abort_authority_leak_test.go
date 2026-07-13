package runtime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// TestInterleavedAbortExecutorHandoff_ReleasesExecutorLegAuthority reproduces L1b: the L1
// fix populated exec.authority on the retryRecvStream returned by
// openInterleavedExecutorContinuation, which correctly handles the normal Recv-to-EOF and
// error paths but exposed a leak on the abort path. In beginExecutorContinuation, when
// handoffAborted returns non-nil AFTER the executor open succeeded (so exec.authority is
// reserved) but BEFORE the first Recv, abortExecutorHandoff is invoked while s.executor is
// still nil (it is only assigned on the success path), so the normal
// closeActiveInner/finishWithCleanup executor cleanup never runs for this exec stream.
// abortExecutorHandoff closed the inner and marked the stream finished but never released
// exec.authority, leaking the freshly admitted executor-leg reservation.
//
// This stages that exact window: the executor open succeeds and reserves
// "reservation-executor-abort", then handoffAborted returns io.EOF (the combined stream is
// marked finished) before any Recv, routing into the abort branch. The executor-leg
// reservation must be released with ReleaseKindSwallowed (no client-facing output was
// produced before the abort), matching the sibling L1/L8 release sites.
func TestInterleavedAbortExecutorHandoff_ReleasesExecutorLegAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-executor-abort",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")
	_ = ex

	// Wrap the thinker leg in the hidden interleaved stream. A nil recorder makes
	// captureAndPersistThinkerMemo return early, so the handoff reaches
	// openInterleavedExecutorContinuation without requiring a captured memo.
	s := newHiddenInterleavedStream(from, nil, interleavedstate.State{})

	// Mark the combined stream finished so handoffAborted returns io.EOF AFTER the
	// executor open succeeds (populating exec.authority) but BEFORE the first Recv,
	// routing beginExecutorContinuation into the abort branch. s.executor is still nil
	// at this point, so the normal executor cleanup path cannot release the reservation.
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()

	_, err := s.beginExecutorContinuation(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("beginExecutorContinuation error = %v, want io.EOF (handoff aborted)", err)
	}

	if got, want := auth.releaseCalls.Load(), int64(1); got != want {
		t.Fatalf("release calls = %d, want %d (executor-leg reservation must be released on abort, not leaked)", got, want)
	}
	release := auth.lastRelease()
	if release.ReservationID != "reservation-executor-abort" {
		t.Fatalf("released reservation ID = %q, want reservation-executor-abort (the executor-leg reservation)", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindSwallowed {
		t.Fatalf("release kind = %q, want swallowed (no client-facing output was produced before the abort)", release.Kind)
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls = %d, want 0 (no usage was produced before the abort)", auth.settleCalls.Load())
	}
}
