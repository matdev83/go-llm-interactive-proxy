package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

var errAuthorityLifecycleSettleFailed = errors.New("settle failed")

// TestAuthorityLifecycle exercises the authorityLifecycle owner in isolation:
// the settle/release/reset contract and the settled idempotency guard. No
// Executor or retryRecvStream is involved, so this pins the owner's behavior
// before Phase 4b/4c migrate scattered call sites onto it.
func TestAuthorityLifecycle(t *testing.T) {
	t.Parallel()
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
		t.Parallel()
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

	t.Run("Settle forwards BoundVersion from admission", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		state := attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(100),
			admissionResult: authorityapp.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r1",
				ReservedAmount: authorityInputAmount(100),
				BoundVersion: economics.PolicySnapshotRef{
					VersionRef: economics.VersionRef{ID: "usage_authority", Version: "v1"},
					PolicyID:   "usage_authority",
				},
			},
		}
		l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
		if applied := l.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false); !applied {
			t.Fatal("Settle = false, want true")
		}
		got := rec.lastSettle().BoundVersion
		if got.Version != "v1" || got.PolicyID != "usage_authority" {
			t.Fatalf("Bound version = %+v, want v1/usage_authority", got)
		}
	})

	t.Run("Settle failure triggers losing release and is idempotent", func(t *testing.T) {
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
		t.Run("empty state", func(t *testing.T) {
			t.Parallel()
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
			t.Parallel()
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

func TestAuthorityLifecycleSerializesConcurrentTerminalOperations(t *testing.T) {
	t.Parallel()
	rec := &recordingAuthorityService{admitResult: authorityapp.AdmissionResult{
		Reserved: true, ReservationID: "r-race", ReservedAmount: authorityInputAmount(10),
	}}
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(10),
		admissionResult: rec.admitResult,
	}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(settle bool) {
			defer wg.Done()
			<-start
			if settle {
				lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, lipapi.Event{}, false)
				return
			}
			lifecycle.Release(context.Background(), authorityapp.ReleaseKindLosing)
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	if calls := rec.settleCalls.Load() + rec.releaseCalls.Load(); calls != 1 {
		t.Fatalf("terminal service calls = %d, want exactly 1", calls)
	}
}

type resetRaceAuthorityService struct {
	entered  chan struct{}
	unblock  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	settles  []authorityapp.SettleInput
	releases []authorityapp.ReleaseInput
}

func (s *resetRaceAuthorityService) Admit(context.Context, authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	return authorityapp.AdmissionResult{}, nil
}

func (s *resetRaceAuthorityService) Settle(_ context.Context, in authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.mu.Lock()
	s.settles = append(s.settles, in)
	s.mu.Unlock()
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.unblock:
	case <-time.After(time.Second):
		return authorityapp.SettleResult{}, context.DeadlineExceeded
	}
	return authorityapp.SettleResult{Applied: true}, nil
}

func (s *resetRaceAuthorityService) Release(_ context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.mu.Lock()
	s.releases = append(s.releases, in)
	s.mu.Unlock()
	return authorityapp.ReleaseResult{Applied: true}, nil
}

func (s *resetRaceAuthorityService) ApplyUsage(context.Context, authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{}, nil
}

func TestAuthorityLifecycleResetSharesPayloadSynchronization(t *testing.T) {
	t.Parallel()
	svc := &resetRaceAuthorityService{entered: make(chan struct{}), unblock: make(chan struct{})}
	oldState := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(1),
		admissionResult: authorityapp.AdmissionResult{Reserved: true, ReservationID: "old-reservation", ReservedAmount: authorityInputAmount(1)},
	}
	lifecycle := newAuthorityLifecycle(svc, nil, oldState, authorityCandidate())
	settled := make(chan bool, 1)
	go func() {
		settled <- lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, lipapi.Event{}, false)
	}()
	<-svc.entered

	newState := oldState
	newState.admissionResult.ReservationID = "new-reservation"
	resetDone := make(chan struct{})
	go func() {
		lifecycle.Reset(newState, routing.AttemptCandidate{Key: "new-candidate"})
		close(resetDone)
	}()
	select {
	case <-resetDone:
		t.Fatal("Reset completed while Settle still owned the lifecycle mutex")
	case <-time.After(20 * time.Millisecond):
	}
	close(svc.unblock)
	if !<-settled {
		t.Fatal("old settlement was not applied")
	}
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("Reset did not complete after settlement released the mutex")
	}

	svc.mu.Lock()
	if len(svc.settles) != 1 || svc.settles[0].ReservationID != "old-reservation" {
		t.Fatalf("settlement payload = %#v, want old reservation only", svc.settles)
	}
	svc.mu.Unlock()
	if got := lifecycle.stateSnapshot().admissionResult.ReservationID; got != "new-reservation" {
		t.Fatalf("lifecycle state after Reset = %q, want new-reservation", got)
	}
	if lifecycle.Settled() {
		t.Fatal("Reset must reopen the replacement lifecycle")
	}
	lifecycle.Release(context.Background(), authorityapp.ReleaseKindLosing)
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.releases) != 1 || svc.releases[0].ReservationID != "new-reservation" {
		t.Fatalf("release payload = %#v, want replacement reservation", svc.releases)
	}
}

func TestAuthorityLifecycleMultiReservationSettleAndReleaseEachReservation(t *testing.T) {
	t.Parallel()

	secondaryAmount := authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 125, Currency: "USD"}
	state := attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(8),
		admissionResult: authorityapp.AdmissionResult{
			Reserved:       true,
			ReservationID:  "reservation-primary",
			ReservedAmount: authorityInputAmount(8),
			RuleIDs:        []string{"tenant.requests", "tenant.spend"},
			Reservations: []authorityapp.AdmissionReservation{
				{
					ReservationID:  "reservation-primary",
					RuleID:         "tenant.requests",
					ReservedAmount: authorityInputAmount(8),
				},
				{
					ReservationID:  "reservation-secondary",
					RuleID:         "tenant.spend",
					ReservedAmount: secondaryAmount,
				},
			},
		},
	}
	state.admissionInput.ReservationKey.RuleID = "tenant.requests"
	state.admissionInput.Spend = secondaryAmount

	usageEv := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   5,
		TotalTokens:   5,
		CostNanoUnits: 40,
		Currency:      "USD",
	}

	t.Run("Settle targets every reservation", func(t *testing.T) {
		t.Parallel()

		rec := &recordingAuthorityService{}
		l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

		if applied := l.Settle(context.Background(), authorityapp.SettlementKindFinal, usageEv, false); !applied {
			t.Fatal("Settle = false, want true")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1 atomic set", got)
		}
		inputs := rec.settleInputs()
		if len(inputs) != 1 || len(inputs[0].Reservations) != 2 {
			t.Fatalf("settle inputs = %#v, want one input with two reservations", inputs)
		}
		reservations := inputs[0].Reservations
		if reservations[0].Reservation.ReservationID != "reservation-primary" {
			t.Fatalf("primary settle reservation id = %q, want reservation-primary", reservations[0].Reservation.ReservationID)
		}
		if reservations[0].Reservation.RuleID != "tenant.requests" {
			t.Fatalf("primary settle rule id = %q, want tenant.requests", reservations[0].Reservation.RuleID)
		}
		if reservations[0].Reservation.ReservationKey.RuleID != "tenant.requests" {
			t.Fatalf("primary settle reservation key rule id = %q, want tenant.requests", reservations[0].Reservation.ReservationKey.RuleID)
		}
		if reservations[0].Reservation.Amount.Unit != authoritydomain.AmountUnitInputTokens || reservations[0].Reservation.Amount.Value != 8 {
			t.Fatalf("primary settle reserved usage = %#v, want 8 input_tokens", reservations[0].Reservation.Amount)
		}
		if reservations[0].FinalUsage.Unit != authoritydomain.AmountUnitInputTokens || reservations[0].FinalUsage.Value != 5 {
			t.Fatalf("primary settle final usage = %#v, want 5 input_tokens", reservations[0].FinalUsage)
		}
		if reservations[1].Reservation.ReservationID != "reservation-secondary" {
			t.Fatalf("secondary settle reservation id = %q, want reservation-secondary", reservations[1].Reservation.ReservationID)
		}
		if reservations[1].Reservation.RuleID != "tenant.spend" {
			t.Fatalf("secondary settle rule id = %q, want tenant.spend", reservations[1].Reservation.RuleID)
		}
		if reservations[1].Reservation.ReservationKey.RuleID != "tenant.spend" {
			t.Fatalf("secondary settle reservation key rule id = %q, want tenant.spend", reservations[1].Reservation.ReservationKey.RuleID)
		}
		if reservations[1].Reservation.Amount != secondaryAmount {
			t.Fatalf("secondary settle reserved usage = %#v, want %#v", reservations[1].Reservation.Amount, secondaryAmount)
		}
		if reservations[1].EstimatedUsage != secondaryAmount {
			t.Fatalf("secondary settle estimated usage = %#v, want %#v", reservations[1].EstimatedUsage, secondaryAmount)
		}
		if reservations[1].FinalUsage.Unit != authoritydomain.AmountUnitMoneyNano || reservations[1].FinalUsage.Value != 40 {
			t.Fatalf("secondary settle final usage = %#v, want 40 money_nano", reservations[1].FinalUsage)
		}
		if reservations[1].FinalUsage.Currency != "USD" {
			t.Fatalf("secondary settle final usage currency = %q, want USD", reservations[1].FinalUsage.Currency)
		}
		if !l.Settled() {
			t.Fatal("expected lifecycle settled after settling every reservation")
		}
	})

	t.Run("Release targets every reservation", func(t *testing.T) {
		t.Parallel()

		rec := &recordingAuthorityService{}
		l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

		l.Release(context.Background(), authorityapp.ReleaseKindSwallowed)
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls = %d, want 1 atomic set", got)
		}
		inputs := rec.releaseInputs()
		if len(inputs) != 1 || len(inputs[0].Reservations) != 2 {
			t.Fatalf("release inputs = %#v, want one input with two reservations", inputs)
		}
		reservations := inputs[0].Reservations
		if reservations[0].Reservation.ReservationID != "reservation-primary" {
			t.Fatalf("primary release reservation id = %q, want reservation-primary", reservations[0].Reservation.ReservationID)
		}
		if reservations[0].Reservation.RuleID != "tenant.requests" {
			t.Fatalf("primary release rule id = %q, want tenant.requests", reservations[0].Reservation.RuleID)
		}
		if reservations[0].Reservation.ReservationKey.RuleID != "tenant.requests" {
			t.Fatalf("primary release reservation key rule id = %q, want tenant.requests", reservations[0].Reservation.ReservationKey.RuleID)
		}
		if reservations[0].Reservation.Amount != authorityInputAmount(8) {
			t.Fatalf("primary release amount = %#v, want 8 requests", reservations[0].Reservation.Amount)
		}
		if reservations[1].Reservation.ReservationID != "reservation-secondary" {
			t.Fatalf("secondary release reservation id = %q, want reservation-secondary", reservations[1].Reservation.ReservationID)
		}
		if reservations[1].Reservation.RuleID != "tenant.spend" {
			t.Fatalf("secondary release rule id = %q, want tenant.spend", reservations[1].Reservation.RuleID)
		}
		if reservations[1].Reservation.ReservationKey.RuleID != "tenant.spend" {
			t.Fatalf("secondary release reservation key rule id = %q, want tenant.spend", reservations[1].Reservation.ReservationKey.RuleID)
		}
		if reservations[1].Reservation.Amount != secondaryAmount {
			t.Fatalf("secondary release amount = %#v, want %#v", reservations[1].Reservation.Amount, secondaryAmount)
		}
		if !l.Settled() {
			t.Fatal("expected lifecycle settled after releasing every reservation")
		}
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

func (s *notAppliedAuthorityService) ApplyUsage(_ context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *notAppliedAuthorityService) lastReleaseInput() authorityapp.ReleaseInput {
	if v := s.lastRelease.Load(); v != nil {
		if in, ok := v.(authorityapp.ReleaseInput); ok {
			return in
		}
	}
	return authorityapp.ReleaseInput{}
}

// TestAuthorityLifecycleSettleReconcilesWhenReservationKeyRuleIDIsCandidateKey
// pins the runtime integration fix for the reservation-key rule-id mismatch
// leak reported by review.
//
// The runtime's attemptAuthorityReservationKey puts the routing candidate key
// (e.g. "backend-1:model-1") into ReservationKey.RuleID because it cannot know
// which authority rules will match until the authority service evaluates the
// request. The authority service reserves each matched strict rule under a
// per-rule key and reports the matched rule ids back via
// AdmissionReservation.RuleID; the authorityLifecycle owner then rebuilds the
// settle/release key using AdmissionReservation.RuleID (the real rule id).
//
// For Settle/Release to find the reservation in the store, the key the owner
// rebuilds at settle time must equal the key the service stored at reserve time.
// This wires the real MemoryStore + Service through the lifecycle owner with a
// ReservationKey.RuleID that is NOT a rule id (mimicking the candidate key),
// admits, then settles, and asserts the reservation is reconciled and the
// strict capacity is released back to zero rather than leaked.
func TestAuthorityLifecycleSettleReconcilesWhenReservationKeyRuleIDIsCandidateKey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "tenant.requests",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeStrict,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 100},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	limitRows, err := authoritystore.LimitRowsFromRules([]authoritydomain.Rule{rule}, now)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "lifecycle-candidate-key-settle",
		Backing:   authoritydomain.BackingCapabilityAtomic,
		LimitRows: limitRows,
		Readiness: authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
	})
	svc := authorityapp.NewService(authorityRuleSource{
		snapshot: authorityapp.RuleSnapshot{
			Status:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
			Rules:     []authoritydomain.Rule{rule},
			FetchedAt: now,
		},
	}, store, nil, nil)

	// Mimic attemptAuthorityReservationKey: the routing candidate key is NOT an
	// authority rule id, but the runtime cannot know the matched rule id until
	// the authority service evaluates the request.
	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-candidate-key",
			RequestID:  "request-candidate-key",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
		},
		Dimensions: authoritydomain.Dimensions{
			Backend: scope.Known("backend-1"),
			Model:   scope.Known("model-1"),
		},
		Request:        authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 3},
		PreflightUsage: authoritydomain.PreflightUsage{InputTokens: 3, TotalTokens: 3},
		Authority:      authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "request-candidate-key",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "b-1",
			RuleID:           "backend-1:model-1",
			Sequence:         1,
		},
	}

	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !admissionResult.Reserved || len(admissionResult.Reservations) != 1 {
		t.Fatalf("admit must reserve exactly one matched strict rule: %#v", admissionResult)
	}
	if admissionResult.Reservations[0].RuleID != "tenant.requests" {
		t.Fatalf("reservation rule id = %q, want tenant.requests", admissionResult.Reservations[0].RuleID)
	}

	limitRow := func(t *testing.T) (reserved, consumed int64) {
		t.Helper()
		page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
			Common:     controlplane.CommonFilters{BackendID: "backend-1"},
			RuleID:     "tenant.requests",
			Unit:       string(authoritydomain.AmountUnitInputTokens),
			Limit:      10,
			Visibility: controlplane.VisibilityDefault,
		})
		if err != nil {
			t.Fatalf("LimitStatus: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("LimitStatus items = %d, want 1: %#v", len(page.Items), page.Items)
		}
		return page.Items[0].Reserved, page.Items[0].Consumed
	}

	if reserved, _ := limitRow(t); reserved != 3 {
		t.Fatalf("after admit, reserved = %d, want 3 (strict capacity must be reserved)", reserved)
	}

	state := attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: admissionResult,
	}
	lifecycle := newAuthorityLifecycle(svc, nil, state, authorityCandidate())

	usageEv := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 3, TotalTokens: 3}
	if applied := lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usageEv, false); !applied {
		t.Fatal("Settle = false, want true (reservation must be reconciled, not leaked)")
	}

	reserved, consumed := limitRow(t)
	if reserved != 0 {
		t.Fatalf("after settle, reserved = %d, want 0 (strict capacity must be released on settle, not leaked)", reserved)
	}
	if consumed != 3 {
		t.Fatalf("after settle, consumed = %d, want 3 (settled usage must be attributed)", consumed)
	}
	if !lifecycle.Settled() {
		t.Fatal("expected lifecycle settled after Settle")
	}
}
