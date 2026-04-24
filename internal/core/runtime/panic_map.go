package runtime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// mapBackendPanic maps a recovered backend panic to an orchestration error that participates in
// existing failover and post-output surfacing rules. committed must reflect whether client-visible
// output was already committed for the active attempt.
func mapBackendPanic(pe *safety.PanicError, committed bool, candidateKey string) error {
	if pe == nil {
		return nil
	}
	reason := diag.TruncErrDetail(pe, attemptReasonMaxRunes)
	if committed {
		return &lipapi.UpstreamFailure{
			Phase:        lipapi.PhasePostOutput,
			Recoverable:  false,
			Reason:       reason,
			CandidateKey: candidateKey,
		}
	}
	return lipapi.RecoverablePreOutputError(pe)
}

// mapStreamPanic maps a recovered stream-side panic (recv path, completion gates) using the same
// commit gating as the backend mapper; candidate key is omitted for stream-local failures.
func mapStreamPanic(pe *safety.PanicError, committed bool) error {
	return mapBackendPanic(pe, committed, "")
}
