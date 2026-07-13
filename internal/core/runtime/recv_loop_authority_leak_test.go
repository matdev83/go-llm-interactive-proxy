package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// TestRetryRecvStreamRegisterBLegFailureReleasesNewAuthority reproduces L7: when
// tryReplacementIteration reserves a NEW out.authority for a recv-phase
// replacement attempt and RegisterBLeg then fails (the A-leg is canceled
// mid-flight during backend.Open, after out.authority was admitted), the freshly
// admitted reservation must be released. Before the fix the RegisterBLeg error
// branch returned without releasing out.authority, leaking the reservation.
// tryReplacementIteration also releases the prior swallowed s.authority before
// opening the replacement (so the same logical request does not double-count
// capacity under strict enforcement), so on RegisterBLeg failure both the prior
// and the NEW reservation are released; the last release must be the NEW one.
func TestRetryRecvStreamRegisterBLegFailureReleasesNewAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-new",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	ex.ALegLifecycle = coord

	// Cancel the A-leg inside backend.Open so RegisterBLeg fails with
	// ErrALegCanceled after out.authority was reserved. A real fixed stream is
	// returned so the coordinator's cancelAndClose cleanup in RegisterBLeg has a
	// B-leg attempt to tear down.
	backend.openFn = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		if err := coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
			return nil, err
		}
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	// priorAuthority is the still-reserved capacity from the swallowed prior
	// attempt; it is distinct from the NEW out.authority and is released by
	// tryReplacementIteration before the replacement is opened.
	priorAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	priorAuthority.admissionResult.ReservationID = "reservation-prior"
	priorAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)

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
		authority: testAuthorityLifecycle(ex, priorAuthority, routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}),
		aScope:    aScope,
	}

	_, err = rs.tryReplacementIteration(context.Background())
	if err == nil {
		t.Fatal("expected tryReplacementIteration to surface the RegisterBLeg failure")
	}
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("tryReplacementIteration error = %v, want ErrALegCanceled", err)
	}

	// The prior swallowed reservation is released before the replacement is opened,
	// and the NEW out.authority is released on RegisterBLeg failure: 2 releases.
	if got, want := auth.releaseCalls.Load(), int64(2); got != want {
		t.Fatalf("release calls = %d, want %d (prior released before admit + NEW out.authority released on RegisterBLeg failure)", got, want)
	}
	release := auth.lastRelease()
	if release.ReservationID != "reservation-new" {
		t.Fatalf("released reservation ID = %q, want reservation-new (the NEW out.authority, released last on RegisterBLeg failure)", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindSwallowed {
		t.Fatalf("release kind = %q, want swallowed", release.Kind)
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls = %d, want 0 (no usage was produced from the failed replacement)", auth.settleCalls.Load())
	}
}
