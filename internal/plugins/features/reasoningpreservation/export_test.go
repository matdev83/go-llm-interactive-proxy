package reasoningpreservation

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

// StreamObserverIsInert reports whether Open returned the package-private inert no-op observer.
// Test-only export (export_test.go); not part of the public API surface.
func StreamObserverIsInert(o response.StreamObserver) bool {
	switch o.(type) {
	case inertStreamObserver, *inertStreamObserver:
		return true
	default:
		return false
	}
}

func HandleCompletedPollCandidateForTest(ctx context.Context, cfg Config, cs CompressionStore, svc CompressionServices, tel *Telemetry, res CompressionPollAttemptResult, call *lipapi.Call) AdoptionResult {
	return handleCompletedPollCandidate(ctx, cfg, cs, svc, tel, res, call)
}
