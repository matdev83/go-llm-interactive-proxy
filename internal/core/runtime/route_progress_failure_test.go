package runtime

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestFailureHistory_Precedence(t *testing.T) {
	errAdmission := errors.New("admission error")
	errParallel := errors.New("parallel error")

	baseErr := errors.New("base error")

	tests := []struct {
		name     string
		history  candidateFailureHistory
		expected error
	}{
		{
			name: "transport priority over admission",
			history: candidateFailureHistory{
				TransportReject: lipapi.TransportNegotiationResult{
					Kind:      lipapi.NegotiationReject,
					Operation: "op",
					Mode:      lipapi.TransportModeStreaming,
				},
				AdmissionErr:     errAdmission,
				CapabilityReject: lipapi.NegotiationResult{Kind: lipapi.NegotiationReject, Missing: []lipapi.Capability{"streaming"}},
				ContextLimit:     true,
				ParallelFailure:  errParallel,
			},
			expected: lipapi.ErrTransportReject,
		},
		{
			name: "admission priority over capability",
			history: candidateFailureHistory{
				AdmissionErr:     errAdmission,
				CapabilityReject: lipapi.NegotiationResult{Kind: lipapi.NegotiationReject, Missing: []lipapi.Capability{"streaming"}},
				ContextLimit:     true,
				ParallelFailure:  errParallel,
			},
			expected: errAdmission,
		},
		{
			name: "capability priority over context limit",
			history: candidateFailureHistory{
				CapabilityReject: lipapi.NegotiationResult{Kind: lipapi.NegotiationReject, Missing: []lipapi.Capability{"streaming"}},
				ContextLimit:     true,
				ParallelFailure:  errParallel,
			},
			expected: &lipapi.RejectError{Missing: []lipapi.Capability{"streaming"}},
		},
		{
			name: "context limit priority over transform excludes",
			history: candidateFailureHistory{
				ContextLimit:    true,
				ParallelFailure: errParallel,
			},
			expected: lipapi.ErrAllCandidatesContextLimitExceeded,
		},
		{
			name: "transform excludes priority over parallel failure",
			history: candidateFailureHistory{
				TransformExcludes: func() *transformExcludeTracker {
					t := &transformExcludeTracker{}
					t.noteTransform("some_reason")
					return t
				}(),
				ParallelFailure: errParallel,
			},
			expected: lipapi.ErrAllCandidatesExcluded,
		},
		{
			name: "parallel failure fallback",
			history: candidateFailureHistory{
				ParallelFailure: errParallel,
			},
			expected: errParallel,
		},
		{
			name:     "base error fallback",
			history:  candidateFailureHistory{},
			expected: baseErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.history.FinalError(baseErr)
			if !errors.Is(got, tc.expected) && got.Error() != tc.expected.Error() {
				t.Errorf("FinalError() = %v, want %v", got, tc.expected)
			}
		})
	}
}
