package runtime

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// errCancelCloseStream returns configurable errors from Cancel and Close so
// cancelLosers / releaseLosers / parallelBridgeStream.Close error filtering can
// be pinned without a full parallel race.
type errCancelCloseStream struct {
	cancelErr error
	closeErr  error
}

func (s *errCancelCloseStream) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (s *errCancelCloseStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Err: s.cancelErr}
}

func (s *errCancelCloseStream) Close() error { return s.closeErr }

func TestCancelLosers_DeadlineExceededSurfaces_CanceledIgnored(t *testing.T) {
	t.Parallel()

	t.Run("Cancel DeadlineExceeded surfaces", func(t *testing.T) {
		t.Parallel()
		legs := []*parallelLeg{{
			cand:   routing.AttemptCandidate{Key: "loser-deadline"},
			stream: &errCancelCloseStream{cancelErr: context.DeadlineExceeded},
		}}
		err := cancelLosers(context.Background(), legs)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancelLosers err = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("Close DeadlineExceeded surfaces", func(t *testing.T) {
		t.Parallel()
		legs := []*parallelLeg{{
			cand:   routing.AttemptCandidate{Key: "loser-close-deadline"},
			stream: &errCancelCloseStream{closeErr: context.DeadlineExceeded},
		}}
		err := cancelLosers(context.Background(), legs)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancelLosers err = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("Cancel context.Canceled ignored", func(t *testing.T) {
		t.Parallel()
		legs := []*parallelLeg{{
			cand:   routing.AttemptCandidate{Key: "loser-canceled"},
			stream: &errCancelCloseStream{cancelErr: context.Canceled},
		}}
		if err := cancelLosers(context.Background(), legs); err != nil {
			t.Fatalf("cancelLosers err = %v, want nil for context.Canceled", err)
		}
	})

	t.Run("Close context.Canceled ignored", func(t *testing.T) {
		t.Parallel()
		legs := []*parallelLeg{{
			cand:   routing.AttemptCandidate{Key: "loser-close-canceled"},
			stream: &errCancelCloseStream{closeErr: context.Canceled},
		}}
		if err := cancelLosers(context.Background(), legs); err != nil {
			t.Fatalf("cancelLosers err = %v, want nil for context.Canceled", err)
		}
	})
}

func TestReleaseLosers_DeadlineExceededSurfacesOnParallelBridgeClose(t *testing.T) {
	t.Parallel()

	legs := []*parallelLeg{{
		cand:   routing.AttemptCandidate{Key: "loser-deadline"},
		stream: &errCancelCloseStream{cancelErr: context.DeadlineExceeded},
	}}
	ex := &Executor{}

	done := make(chan error, 1)
	go func() {
		done <- ex.releaseLosers(context.Background(), nil, legs)
		close(done)
	}()

	bridge := &parallelBridgeStream{
		winner: &parallelLeg{
			stream: &errCancelCloseStream{}, // winner Close is nil
		},
		losersDone:       done,
		loserCleanupWait: time.Second,
	}
	err := bridge.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parallelBridgeStream.Close err = %v, want context.DeadlineExceeded from loser cleanup", err)
	}
}
