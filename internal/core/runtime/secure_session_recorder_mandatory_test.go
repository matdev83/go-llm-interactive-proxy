package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRetryRecvStream_tryReplacement_blockedAfterMandatoryRecorderFailure(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SecureSessionRecordingMandatory = true
	s := &retryRecvStream{
		terminal:         newTurnTerminal(),
		recovery:         &recoveryController{},
		responsePipeline: &responsePipeline{recordingOutcome: responseRecordingMandatoryPostCommitFailure},
		attempt:          testAttemptSlot(b2bua.BLegRecord{}, routing.AttemptCandidate{Key: "cand-1"}, authorityLifecycle{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "tr-mand",
			aLegID:  "a-mand",
		}),
	}
	bindTestRuntimeOwners(s, ex)
	s.terminal.markCommitted(s.attempt.snapshot())
	_, err := s.tryReplacementIteration(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var uf *lipapi.UpstreamFailureError
	if !errors.As(err, &uf) || uf.Phase != lipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("unexpected error: %v", err)
	}
}
