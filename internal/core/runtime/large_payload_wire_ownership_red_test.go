package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// blockingTrackingStream blocks Recv until unblockRecv is called,
// and records when Cancel and Close are invoked.
type blockingTrackingStream struct {
	cancelCh    chan struct{}
	closeCh     chan struct{}
	blockCh     chan struct{}
	cancelOnce  sync.Once
	closeOnce   sync.Once
	unblockOnce sync.Once
}

func newBlockingTrackingStream() *blockingTrackingStream {
	return &blockingTrackingStream{
		cancelCh: make(chan struct{}, 1),
		closeCh:  make(chan struct{}, 1),
		blockCh:  make(chan struct{}),
	}
}

func (s *blockingTrackingStream) unblockRecv() {
	s.unblockOnce.Do(func() {
		close(s.blockCh)
	})
}

func (s *blockingTrackingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	select {
	case <-s.blockCh:
		return lipapi.Event{}, io.EOF
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	}
}

func (s *blockingTrackingStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	return nil
}

func (s *blockingTrackingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.cancelOnce.Do(func() {
		close(s.cancelCh)
	})
	return lipapi.CancelResult{Mode: lipapi.CancelModeProvider}
}

var _ lipapi.ManagedEventStream = (*blockingTrackingStream)(nil)

// TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome
// verifies Blocker 1 regression requirements:
// 1. Opens a blocking wire stream and returns it from ExecuteLargeBody.
// 2. Invokes real Executor.CancelALeg.
// 3. Asserts the backend stream's Cancel/Close is invoked promptly (compute not leaked).
// 4. Asserts exactly one terminal outcome is produced with LegOutcomeCanceled (no stale loser defaults, no duplicate legs).
func TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	var recordedLegs []billing.CallLegUsageRecord
	var legsMu sync.Mutex
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record billing.CallLegUsageRecord) error {
			legsMu.Lock()
			recordedLegs = append(recordedLegs, record)
			legsMu.Unlock()
			return nil
		},
	}

	backendStream := newBlockingTrackingStream()
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-cancel"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-wire-cancel"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-wire-cancel", "trace-wire-cancel")

	// 1. ExecuteLargeBody returns the open wire stream
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Concurrently read the stream to verify it's active
	recvErrCh := make(chan error, 1)
	go func() {
		_, rErr := res.Stream.Recv(context.Background())
		recvErrCh <- rErr
	}()

	// 2. Invoke real Executor.CancelALeg
	cancelCtx := execview.WithPrincipal(context.Background(), principal)
	err = ex.CancelALeg(cancelCtx, lipapi.ALegCancelRequest{
		ALegID: res.Facts.ALegID,
		Reason: "user cancelled request",
	})
	require.NoError(t, err)

	// 3. Assert the backend stream's Cancel/Close is invoked promptly
	select {
	case <-backendStream.cancelCh:
		// Promptly canceled via Cancel
	case <-backendStream.closeCh:
		// Promptly closed
	case <-time.After(2 * time.Second):
		t.Fatal("backend stream Cancel/Close was not invoked promptly after Executor.CancelALeg")
	}

	// Unblock backend Recv only after CancelALeg has completed, so the cancel path
	// deterministically claims the terminal outcome first by construction.
	backendStream.unblockRecv()

	// Recv should unblock promptly
	select {
	case rErr := <-recvErrCh:
		_ = rErr
	case <-time.After(2 * time.Second):
		t.Fatal("wire stream Recv did not unblock after cancellation")
	}

	// Close the wire stream (simulating wire frontend completion / teardown)
	_ = res.Stream.Close()

	// 4. Assert exactly one terminal outcome is produced
	legsMu.Lock()
	defer legsMu.Unlock()
	require.Len(t, recordedLegs, 1, "expected exactly one terminal leg outcome to be produced")
	assert.Equal(t, billing.LegOutcomeCanceled, recordedLegs[0].Outcome, "terminal outcome must be LegOutcomeCanceled, not stale loser-default")
}

func TestBlocker1_WireOwnershipTransfer_NormalEOF_ProducesSingleWinnerTerminalOutcome(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	var recordedLegs []billing.CallLegUsageRecord
	var legsMu sync.Mutex
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record billing.CallLegUsageRecord) error {
			legsMu.Lock()
			recordedLegs = append(recordedLegs, record)
			legsMu.Unlock()
			return nil
		},
	}

	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return lipapi.CloseOnlyManagedStream{
					Stream: lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventTextDelta, Delta: "hello"},
						{Kind: lipapi.EventResponseFinished},
					}),
				}, nil
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-normal"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-wire-normal"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-wire-normal", "trace-wire-normal")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Consume all events to EOF
	for {
		_, err := res.Stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	_ = res.Stream.Close()

	legsMu.Lock()
	defer legsMu.Unlock()
	require.Len(t, recordedLegs, 1, "expected exactly one terminal leg outcome on normal completion")
	assert.Equal(t, billing.LegOutcomeWinner, recordedLegs[0].Outcome, "normal completion must produce LegOutcomeWinner")
}

func TestBlocker1_WireOwnershipTransfer_EarlyClose_ProducesSingleCanceledTerminalOutcome(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	var recordedLegs []billing.CallLegUsageRecord
	var legsMu sync.Mutex
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record billing.CallLegUsageRecord) error {
			legsMu.Lock()
			recordedLegs = append(recordedLegs, record)
			legsMu.Unlock()
			return nil
		},
	}

	backendStream := newBlockingTrackingStream()
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-early-close"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-wire-early-close"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-wire-early-close", "trace-wire-early-close")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Close before consuming to EOF (early close)
	_ = res.Stream.Close()

	// Backend stream must be closed
	select {
	case <-backendStream.closeCh:
		// closed
	case <-time.After(2 * time.Second):
		t.Fatal("backend stream was not closed after wire stream Close")
	}

	legsMu.Lock()
	defer legsMu.Unlock()
	require.Len(t, recordedLegs, 1, "expected exactly one terminal leg outcome on early close")
	assert.Equal(t, billing.LegOutcomeCanceled, recordedLegs[0].Outcome, "early close must produce LegOutcomeCanceled")
}

func TestBlocker1_WireTakeStream_ResetsTerminalDefaults(t *testing.T) {
	start := time.Now()
	sess := newAttemptSession(attemptSessionInput{
		inner:      lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream(nil)},
		accounting: newAttemptAccountingTracker(start),
	})
	// Set stale loser defaults
	sess.defaultCommand = sdkterminal.CommandParallelLoser
	sess.defaultLegOutcome = billing.LegOutcomeFailed
	sess.releaseKind = "loser"

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	ready.setDefaultEvidence("loser", sdkterminal.CommandParallelLoser, billing.LegOutcomeFailed)

	stream, startedAt, err := ready.WireTakeStream()
	require.NoError(t, err)
	require.NotNil(t, stream)
	assert.Equal(t, start, startedAt)

	// Assert defaults were reset to winner/cancel defaults
	assert.Equal(t, sdkterminal.CommandCancel, sess.defaultCommand, "defaultCommand must be reset to CommandCancel")
	assert.Equal(t, billing.LegOutcomeCanceled, sess.defaultLegOutcome, "defaultLegOutcome must be reset to LegOutcomeCanceled")
	assert.Empty(t, sess.releaseKind, "releaseKind must be reset to empty")
	assert.False(t, sess.streamDisposed, "streamDisposed must not be set to true")

	// Close the forwarding stream
	_ = stream.Close()
}
