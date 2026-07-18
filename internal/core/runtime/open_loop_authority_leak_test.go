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

// TestOpenInitialAttempt_RegisterBLegFailureReleasesAuthority reproduces L6: when
// RegisterBLeg fails on the first attempt, the freshly-admitted out.authority
// reservation is orphaned because the error branch closes out.stream but never
// releases out.authority (which is not stored into any holder field on this path).
//
// The seam cancels the A-leg inside backend.Open (the same seam used by
// TestExecutor_RegisterBLegFailureDoesNotDoubleCancelStream) so the backend opens
// (out.opened=true, authority carried in out.authority) and the subsequent
// RegisterBLeg fails with ErrALegCanceled. The freshly-admitted reservation must
// then be released with ReleaseKindSwallowed, mirroring the swallowed-authority
// release sites in executor_recv_loop.go.
func TestOpenInitialAttempt_RegisterBLegFailureReleasesAuthority(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-bleg-leak"
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  reservationID,
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	backend.openFn = func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		if err := coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
			return nil, err
		}
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}
	plan := &routePlanState{
		sel:      sel,
		budget:   &attemptBudget{max: 3},
		excluded: map[string]struct{}{},
		session:  &routing.SessionRoutingState{},
		rng:      routing.NewSeededRng(1),
	}
	prep := &preparedRequest{
		ctx:     context.Background(),
		bus:     hooks.New(hooks.Config{}),
		traceID: "trace-leak",
		aLeg:    b2bua.ALegRecord{ALegID: aLegID},
		aScope:  aScope,
		baseline: lipapi.Call{
			ID:    "request-leak",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
			Messages: testMinimalUserMessages(),
		},
	}

	_, err = ex.openInitialAttempt(prep, plan)
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("openInitialAttempt err = %v, want ErrALegCanceled", err)
	}
	if auth.admitCalls.Load() != 2 {
		t.Fatalf("admit calls = %d, want 2 (estimate-only precheck + authoritative admit)", auth.admitCalls.Load())
	}
	if auth.releaseCalls.Load() != 1 {
		t.Fatalf("release calls = %d, want 1 (out.authority must be released when RegisterBLeg fails)", auth.releaseCalls.Load())
	}
	release := auth.lastRelease()
	if release.ReservationID != reservationID {
		t.Fatalf("release reservation ID = %q, want %q", release.ReservationID, reservationID)
	}
	if release.Kind != authorityapp.ReleaseKindSwallowed {
		t.Fatalf("release kind = %q, want swallowed", release.Kind)
	}
	if auth.settleCalls.Load() != 0 {
		t.Fatalf("settle calls = %d, want 0 (no usage was produced before RegisterBLeg failure)", auth.settleCalls.Load())
	}
}
