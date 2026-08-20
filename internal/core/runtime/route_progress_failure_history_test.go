package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// TestRouteProgressSharedAuthority asserts that the route plan state progress and
// the recovery controller in the stream are the exact same instance.
func TestRouteProgressSharedAuthority(t *testing.T) {
	ex := TestExecutor()
	// Build a route plan
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "backendA:model-x"},
		Session: lipapi.SessionRef{
			ALegID: "test-aleg",
		},
	}
	preSession := session.SessionView{
		ALegID: "test-aleg",
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		lipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "test-aleg"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{
		identity: ibt,
		call:     ibt.call,
	}
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("failed to build route plan: %v", err)
	}

	// Now check that progress is created
	if plan.progress == nil {
		t.Fatal("expected non-nil plan.progress")
	}

	// Create a recovery input for assemble mimicking executor_assemble_stream
	rc := newRecoveryController(recoveryControllerInput{
		budget:   plan.progress.budget,
		ttft:     plan.progress.ttft,
		sel:      plan.sel,
		session:  plan.progress.session,
		excluded: plan.progress.excluded,
		rng:      plan.rng,
	})

	if rc != plan.progress {
		t.Errorf("expected rc (%p) and plan.progress (%p) to be the exact same instance", rc, plan.progress)
	}
}

// TestErrorPrecedenceMatrix asserts the exact precedence order of failures.
func TestErrorPrecedenceMatrix(t *testing.T) {
	errTransport := &lipapi.TransportRejectError{Operation: "op", Mode: "mode"}
	errAdmission := errors.New("admission err")
	errCapability := &lipapi.RejectError{Missing: []lipapi.Capability{lipapi.CapabilityVision}}
	errContextLimit := lipapi.ErrAllCandidatesContextLimitExceeded
	errTransform := lipapi.ErrAllCandidatesExcluded
	errParallel := errors.New("parallel err")
	errBase := errors.New("base err")

	cases := []struct {
		name      string
		configure func(*candidateFailureHistory)
		expected  error
	}{
		{
			name: "all errors present -> transport wins",
			configure: func(h *candidateFailureHistory) {
				h.TransportReject = lipapi.TransportNegotiationResult{
					Kind:      lipapi.NegotiationReject,
					Operation: "op",
					Mode:      "mode",
				}
				h.AdmissionErr = errAdmission
				h.CapabilityReject = lipapi.NegotiationResult{
					Kind:    lipapi.NegotiationReject,
					Missing: []lipapi.Capability{lipapi.CapabilityVision},
				}
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errTransport,
		},
		{
			name: "admission present -> admission wins over capability",
			configure: func(h *candidateFailureHistory) {
				h.AdmissionErr = errAdmission
				h.CapabilityReject = lipapi.NegotiationResult{
					Kind:    lipapi.NegotiationReject,
					Missing: []lipapi.Capability{lipapi.CapabilityVision},
				}
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errAdmission,
		},
		{
			name: "capability present -> capability wins over context limit",
			configure: func(h *candidateFailureHistory) {
				h.CapabilityReject = lipapi.NegotiationResult{
					Kind:    lipapi.NegotiationReject,
					Missing: []lipapi.Capability{lipapi.CapabilityVision},
				}
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errCapability,
		},
		{
			name: "context limit present -> context limit wins over transform",
			configure: func(h *candidateFailureHistory) {
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errContextLimit,
		},
		{
			name: "transform present -> transform wins over parallel",
			configure: func(h *candidateFailureHistory) {
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errTransform,
		},
		{
			name: "parallel present -> parallel wins over base",
			configure: func(h *candidateFailureHistory) {
				h.ParallelFailure = errParallel
			},
			expected: errParallel,
		},
		{
			name: "none present -> base wins",
			configure: func(h *candidateFailureHistory) {
			},
			expected: errBase,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
			tc.configure(h)
			got := h.FinalError(errBase)
			switch tc.expected {
			case errTransport:
				var gotT *lipapi.TransportRejectError
				if !errors.As(got, &gotT) {
					t.Fatalf("expected TransportRejectError, got %v", got)
				}
			case errCapability:
				var gotC *lipapi.RejectError
				if !errors.As(got, &gotC) {
					t.Fatalf("expected RejectError, got %v", got)
				}
			default:
				if !errors.Is(got, tc.expected) && got.Error() != tc.expected.Error() {
					t.Fatalf("got error %v, want %v", got, tc.expected)
				}
			}
		})
	}
}
