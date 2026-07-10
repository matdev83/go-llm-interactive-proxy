package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var errAuthorityLifecycleSettleFailed = errors.New("settle failed")

// TestAuthorityLifecycle exercises the authorityLifecycle owner in isolation:
// the settle/release/reset contract and the settled idempotency guard. No
// Executor or retryRecvStream is involved, so this pins the owner's behavior
// before Phase 4b/4c migrate scattered call sites onto it.
func TestAuthorityLifecycle(t *testing.T) {
	ctx := context.Background()

	// newActiveLifecycle builds an owner over a reserved admission state
	// (TraceID set, Reserved=true) so Settle/Release are active.
	newActiveLifecycle := func(svc UsageAuthorityService) authorityLifecycle {
		state := attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(100),
			admissionResult: authorityapp.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r1",
				ReservedAmount: authorityInputAmount(100),
			},
		}
		return newAuthorityLifecycle(svc, nil, state, authorityCandidate())
	}

	// newRecorder builds a recordingAuthorityService pre-loaded with a reserved
	// admission result so an owner built over it is active.
	newRecorder := func() *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: authorityapp.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r1",
				ReservedAmount: authorityInputAmount(100),
			},
		}
	}

	t.Run("Settle success settles once and is idempotent", func(t *testing.T) {
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); !applied {
			t.Fatalf("Settle on success = false, want true")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after first Settle = %d, want 1", got)
		}
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after successful Settle = %d, want 0", got)
		}
		if in := rec.lastSettle(); in.Kind != authorityapp.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want %q", in.Kind, authorityapp.SettlementKindFinal)
		}

		// Second Settle must be a no-op: the settled guard is already true.
		if applied := l.Settle(ctx, authorityapp.SettlementKindPartial, lipapi.Event{}, false); applied {
			t.Fatalf("second Settle = true, want false (idempotent)")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after second Settle = %d, want 1 (no further service calls)", got)
		}
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after second Settle = %d, want 0", got)
		}
	})

	t.Run("Settle failure triggers losing release and is idempotent", func(t *testing.T) {
		rec := newRecorder()
		rec.settleErr = errAuthorityLifecycleSettleFailed
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); applied {
			t.Fatalf("Settle on failure = true, want false")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after failing Settle = %d, want 1", got)
		}
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after failing Settle = %d, want 1 (losing fallback)", got)
		}
		if rel := rec.lastRelease(); rel.Kind != authorityapp.ReleaseKindLosing {
			t.Fatalf("fallback release kind = %q, want %q", rel.Kind, authorityapp.ReleaseKindLosing)
		}

		// A following Settle and Release must both be no-ops (settled guard true).
		if applied := l.Settle(ctx, authorityapp.SettlementKindPartial, lipapi.Event{}, false); applied {
			t.Fatalf("Settle after failure = true, want false (idempotent)")
		}
		l.Release(ctx, authorityapp.ReleaseKindSwallowed)
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after follow-up = %d, want 1", got)
		}
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after follow-up = %d, want 1", got)
		}
	})

	t.Run("Settle not-applied without error triggers losing release", func(t *testing.T) {
		svc := &notAppliedAuthorityService{}
		l := newActiveLifecycle(svc)

		if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); applied {
			t.Fatalf("Settle not-applied = true, want false")
		}
		if got := svc.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1", got)
		}
		if got := svc.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls = %d, want 1 (losing fallback for not-applied)", got)
		}
		if rel := svc.lastReleaseInput(); rel.Kind != authorityapp.ReleaseKindLosing {
			t.Fatalf("fallback release kind = %q, want %q", rel.Kind, authorityapp.ReleaseKindLosing)
		}
	})

	t.Run("Release releases once with the right kind and is idempotent", func(t *testing.T) {
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		l.Release(ctx, authorityapp.ReleaseKindSwallowed)
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after first Release = %d, want 1", got)
		}
		if rel := rec.lastRelease(); rel.Kind != authorityapp.ReleaseKindSwallowed {
			t.Fatalf("release kind = %q, want %q", rel.Kind, authorityapp.ReleaseKindSwallowed)
		}

		l.Release(ctx, authorityapp.ReleaseKindLosing)
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after second Release = %d, want 1 (idempotent)", got)
		}
	})

	t.Run("Release after Settle is a no-op", func(t *testing.T) {
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); !applied {
			t.Fatalf("Settle = false, want true")
		}
		l.Release(ctx, authorityapp.ReleaseKindSwallowed)
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after Settle = %d, want 0 (Settle already settled)", got)
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1", got)
		}
	})

	t.Run("Reset clears the settled guard for a fresh settle", func(t *testing.T) {
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); !applied {
			t.Fatalf("first Settle = false, want true")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after first Settle = %d, want 1", got)
		}

		state2 := attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(200),
			admissionResult: authorityapp.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r2",
				ReservedAmount: authorityInputAmount(200),
			},
		}
		cand2 := routing.AttemptCandidate{
			Primary: routing.Primary{Backend: "backend-2", Model: "model-2"},
			Key:     "backend-2:model-2",
		}
		l.Reset(state2, cand2)

		if applied := l.Settle(ctx, authorityapp.SettlementKindPartial, lipapi.Event{}, false); !applied {
			t.Fatalf("Settle after Reset = false, want true (guard cleared)")
		}
		if got := rec.settleCalls.Load(); got != 2 {
			t.Fatalf("settle calls after Reset+Settle = %d, want 2 (fresh settle)", got)
		}
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after Reset+Settle = %d, want 0", got)
		}
		if in := rec.lastSettle(); in.ReservationID != "r2" {
			t.Fatalf("settle ReservationID after Reset = %q, want r2", in.ReservationID)
		}
		if in := rec.lastSettle(); in.Kind != authorityapp.SettlementKindPartial {
			t.Fatalf("settle kind after Reset = %q, want %q", in.Kind, authorityapp.SettlementKindPartial)
		}
	})

	t.Run("Inactive owner no-ops Settle and Release", func(t *testing.T) {
		t.Run("empty state", func(t *testing.T) {
			rec := newRecorder()
			l := newAuthorityLifecycle(rec, nil, attemptAuthorityState{}, authorityCandidate())
			if l.IsActive() {
				t.Fatalf("IsActive on empty state = true, want false")
			}
			if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); applied {
				t.Fatalf("Settle on inactive owner = true, want false")
			}
			l.Release(ctx, authorityapp.ReleaseKindLosing)
			if got := rec.settleCalls.Load(); got != 0 {
				t.Fatalf("settle calls on inactive owner = %d, want 0", got)
			}
			if got := rec.releaseCalls.Load(); got != 0 {
				t.Fatalf("release calls on inactive owner = %d, want 0", got)
			}
		})

		t.Run("reserved false", func(t *testing.T) {
			rec := newRecorder()
			state := attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(100),
				admissionResult: authorityapp.AdmissionResult{Reserved: false},
			}
			l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
			if l.IsActive() {
				t.Fatalf("IsActive with Reserved=false = true, want false")
			}
			if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); applied {
				t.Fatalf("Settle on inactive (Reserved=false) = true, want false")
			}
			l.Release(ctx, authorityapp.ReleaseKindLosing)
			if got := rec.settleCalls.Load(); got != 0 {
				t.Fatalf("settle calls on inactive (Reserved=false) = %d, want 0", got)
			}
			if got := rec.releaseCalls.Load(); got != 0 {
				t.Fatalf("release calls on inactive (Reserved=false) = %d, want 0", got)
			}
		})
	})
}

// notAppliedAuthorityService is a UsageAuthorityService stub whose Settle always
// returns Applied=false with no error, to exercise the owner's not-applied
// fallback branch (distinct from the error branch covered via settleErr on
// recordingAuthorityService).
type notAppliedAuthorityService struct {
	settleCalls  atomic.Int64
	releaseCalls atomic.Int64
	lastRelease  atomic.Value
}

func (s *notAppliedAuthorityService) Admit(context.Context, authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	return authorityapp.AdmissionResult{Reserved: true, ReservationID: "r1"}, nil
}

func (s *notAppliedAuthorityService) Settle(_ context.Context, _ authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.settleCalls.Add(1)
	return authorityapp.SettleResult{Applied: false}, nil
}

func (s *notAppliedAuthorityService) Release(_ context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	s.lastRelease.Store(in)
	return authorityapp.ReleaseResult{Applied: true}, nil
}

func (s *notAppliedAuthorityService) lastReleaseInput() authorityapp.ReleaseInput {
	if v := s.lastRelease.Load(); v != nil {
		if in, ok := v.(authorityapp.ReleaseInput); ok {
			return in
		}
	}
	return authorityapp.ReleaseInput{}
}
