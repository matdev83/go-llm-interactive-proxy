package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRetryRecvStream_tryReplacement_blockedAfterMandatoryRecorderFailure(t *testing.T) {
	t.Parallel()
	s := &retryRecvStream{
		committed:                   true,
		secureRecvRecordingHardStop: true,
		executor:                    &Executor{SecureSessionRecordingMandatory: true},
		cand:                        routing.AttemptCandidate{Key: "cand-1"},
		traceID:                     "tr-mand",
		aLegID:                      "a-mand",
	}
	_, err := s.tryReplacementIteration(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var uf *lipapi.UpstreamFailure
	if !errors.As(err, &uf) || uf.Phase != lipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("unexpected error: %v", err)
	}
}
