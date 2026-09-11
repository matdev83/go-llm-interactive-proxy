package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// failAfterEventsStream emits its events and then returns fail (or io.EOF if fail is nil).
type failAfterEventsStream struct {
	events []lipapi.Event
	fail   error
	idx    int
}

func (s *failAfterEventsStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.fail != nil {
		return lipapi.Event{}, s.fail
	}
	return lipapi.Event{}, io.EOF
}

func (s *failAfterEventsStream) Close() error { return nil }

func (s *failAfterEventsStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

var _ lipapi.ManagedEventStream = (*failAfterEventsStream)(nil)

// TestBlocker3_PreOutputRecoverableFailover_Differential verifies Requirement 10:
// When backend A emits a non-committing ResponseStarted event and then returns a RecoverablePreOutputError,
// while backend B succeeds with text output:
// BOTH canonical execution (Execute) and wire execution (ExecuteLargeBody) must failover to backend B.
func TestBlocker3_PreOutputRecoverableFailover_Differential(t *testing.T) {
	respStarted := lipapi.Event{Kind: lipapi.EventResponseStarted}
	textDelta := lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "recovered-on-backend-b"}
	respFinished := lipapi.Event{Kind: lipapi.EventResponseFinished}

	// Verify exact predicates as required by TDD step 1
	require.False(t, lipapi.OutputCommitted(respStarted), "ResponseStarted must not be output-committed")
	require.True(t, lipapi.OutputCommitted(textDelta), "TextDelta must be output-committed")

	recErr := lipapi.RecoverablePreOutputError(errors.New("connection reset by peer before output"))
	require.True(t, lipapi.IsRecoverablePreOutput(recErr), "RecoverablePreOutputError must be recoverable pre-output")

	t.Run("Canonical_FailsOverToBackendB", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)

		var opensA, opensB atomic.Int32
		ex.Backends = map[string]execbackend.Backend{
			"backend-a": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return &failAfterEventsStream{
						events: []lipapi.Event{respStarted},
						fail:   recErr,
					}, nil
				},
			},
			"backend-b": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []lipapi.Event{respStarted, textDelta, respFinished},
					}, nil
				},
			},
		}

		call := &lipapi.Call{
			Route: lipapi.RouteIntent{Selector: "backend-a:gpt-4o|backend-b:gpt-4o"},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hello")},
			}},
		}

		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-canonical"})
		stream, err := ex.Execute(ctx, call)
		require.NoError(t, err)
		defer func() { _ = stream.Close() }()

		var sawTextDelta bool
		for {
			ev, rerr := stream.Recv(context.Background())
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				if lipapi.IsRecoverablePreOutput(rerr) {
					continue
				}
				require.NoError(t, rerr)
			}
			if ev.Kind == lipapi.EventTextDelta && ev.Delta == "recovered-on-backend-b" {
				sawTextDelta = true
			}
		}

		assert.True(t, sawTextDelta, "canonical execution must receive text from backend B")
		assert.Equal(t, int32(1), opensA.Load(), "backend A must have been opened")
		assert.Equal(t, int32(1), opensB.Load(), "backend B must have been opened via failover")
	})

	t.Run("Wire_FailsOverToBackendB", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)

		var opensA, opensB atomic.Int32
		ex.Backends = map[string]execbackend.Backend{
			"backend-a": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return &failAfterEventsStream{
						events: []lipapi.Event{respStarted},
						fail:   recErr,
					}, nil
				},
			},
			"backend-b": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []lipapi.Event{respStarted, textDelta, respFinished},
					}, nil
				},
			},
		}

		src := newTestSource(`{"model":"gpt-4o","prompt":"wire-failover"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
		acc.WireRequest.CandidateModel = "backend-a:gpt-4o|backend-b:gpt-4o"

		principal := execview.PrincipalView{ID: "usr-wire-failover"}
		ctx := execview.WithPrincipal(context.Background(), principal)
		ctx = largebody.WithWireIdentity(ctx, "req-wire-failover", "trace-wire-failover")

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		require.NoError(t, err)
		require.NotNil(t, res.Stream)
		defer func() { _ = res.Stream.Close() }()

		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var sawTextDelta bool
		for {
			ev, rerr := res.Stream.Recv(runCtx)
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				// If wire did not fail over, it surfaces recErr. Break so we can assert on failure.
				break
			}
			if ev.Kind == lipapi.EventTextDelta && ev.Delta == "recovered-on-backend-b" {
				sawTextDelta = true
			}
		}

		assert.True(t, sawTextDelta, "wire execution must receive text from backend B after backend A pre-output failure")
		assert.Equal(t, int32(1), opensA.Load(), "backend A must have been opened")
		assert.Equal(t, int32(1), opensB.Load(), "backend B must have been opened via failover")
	})
}

type blockingFailAfterEventsStream struct {
	events      []lipapi.Event
	fail        error
	idx         int
	recvStarted chan struct{}
	releaseRecv chan struct{}
	startOnce   sync.Once
}

func (s *blockingFailAfterEventsStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	s.startOnce.Do(func() {
		if s.recvStarted != nil {
			close(s.recvStarted)
		}
	})
	if s.releaseRecv != nil {
		select {
		case <-s.releaseRecv:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if s.fail != nil {
		return lipapi.Event{}, s.fail
	}
	return lipapi.Event{}, io.EOF
}

func (s *blockingFailAfterEventsStream) Close() error { return nil }

func (s *blockingFailAfterEventsStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

var _ lipapi.ManagedEventStream = (*blockingFailAfterEventsStream)(nil)

// TestBlocker3_CloseDuringFailover_NoUnownedAttemptEscapes verifies that when client Close fires
// while Recv is mid-failover, no second backend open completes unowned:
// either backend B is never opened, or if opened during the race window, B is terminalized
// with LegOutcomeSwallowed (exactly one terminal outcome per opened attempt, stream closed, no leaked B-leg/exposure).
func TestBlocker3_CloseDuringFailover_NoUnownedAttemptEscapes(t *testing.T) {
	respStarted := lipapi.Event{Kind: lipapi.EventResponseStarted}
	recErr := lipapi.RecoverablePreOutputError(errors.New("connection reset by peer before output"))

	t.Run("CloseFiresWhileBackendBOpening", func(t *testing.T) {
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

		bStream := newBlockingTrackingStream()
		openBStarted := make(chan struct{})
		releaseBOpen := make(chan struct{})
		var opensA, opensB atomic.Int32

		ex.Backends = map[string]execbackend.Backend{
			"backend-a": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return &failAfterEventsStream{
						events: []lipapi.Event{respStarted},
						fail:   recErr,
					}, nil
				},
			},
			"backend-b": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					opensB.Add(1)
					close(openBStarted)
					<-releaseBOpen
					return bStream, nil
				},
			},
		}

		src := newTestSource(`{"model":"gpt-4o","prompt":"wire-failover-close-race"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
		acc.WireRequest.CandidateModel = "backend-a:gpt-4o|backend-b:gpt-4o"

		principal := execview.PrincipalView{ID: "usr-wire-failover-close-race"}
		ctx := execview.WithPrincipal(context.Background(), principal)
		ctx = largebody.WithWireIdentity(ctx, "req-wire-failover-close-race", "trace-wire-failover-close-race")

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		require.NoError(t, err)
		require.NotNil(t, res.Stream)

		recvErrCh := make(chan error, 1)
		go func() {
			ev, rerr := res.Stream.Recv(context.Background())
			if rerr != nil {
				recvErrCh <- rerr
				return
			}
			assert.Equal(t, lipapi.EventResponseStarted, ev.Kind)

			_, rerr = res.Stream.Recv(context.Background())
			recvErrCh <- rerr
		}()

		select {
		case <-openBStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for backend B open to start")
		}

		// Client Close fires while backend B open is in flight
		err = res.Stream.Close()
		require.NoError(t, err)

		// Unblock backend B open so it completes
		close(releaseBOpen)

		select {
		case rerr := <-recvErrCh:
			assert.True(t, errors.Is(rerr, io.EOF), "expected Recv to return io.EOF on closed stream, got %v", rerr)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Recv to return")
		}

		// Backend B stream must be closed upon swallowed terminalization
		select {
		case <-bStream.closeCh:
		case <-time.After(2 * time.Second):
			t.Fatal("backend B stream was not closed upon swallowed terminalization")
		}

		// Assert: exactly 2 terminal leg outcomes produced, both LegOutcomeSwallowed.
		// No second backend attempt escapes unowned.
		legsMu.Lock()
		defer legsMu.Unlock()
		require.Len(t, recordedLegs, 2, "expected exactly 2 terminal leg outcomes (one for A, one for B)")
		assert.Equal(t, billing.LegOutcomeSwallowed, recordedLegs[0].Outcome, "attempt A must be swallowed")
		assert.Equal(t, billing.LegOutcomeSwallowed, recordedLegs[1].Outcome, "attempt B must be swallowed")
	})

	t.Run("CloseFiresBeforeBackendBOpened", func(t *testing.T) {
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

		releaseARecv := make(chan struct{})
		aRecvStarted := make(chan struct{})
		var opensA, opensB atomic.Int32

		ex.Backends = map[string]execbackend.Backend{
			"backend-a": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					opensA.Add(1)
					return &blockingFailAfterEventsStream{
						events:      []lipapi.Event{respStarted},
						fail:        recErr,
						recvStarted: aRecvStarted,
						releaseRecv: releaseARecv,
					}, nil
				},
			},
			"backend-b": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					opensB.Add(1)
					return &failAfterEventsStream{
						events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b"}},
					}, nil
				},
			},
		}

		src := newTestSource(`{"model":"gpt-4o","prompt":"wire-close-before-b"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "backend-a:gpt-4o", "backend-b:gpt-4o")
		acc.WireRequest.CandidateModel = "backend-a:gpt-4o|backend-b:gpt-4o"

		principal := execview.PrincipalView{ID: "usr-wire-close-before-b"}
		ctx := execview.WithPrincipal(context.Background(), principal)
		ctx = largebody.WithWireIdentity(ctx, "req-wire-close-before-b", "trace-wire-close-before-b")

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		require.NoError(t, err)
		require.NotNil(t, res.Stream)

		recvErrCh := make(chan error, 1)
		go func() {
			ev, rerr := res.Stream.Recv(context.Background())
			if rerr != nil {
				recvErrCh <- rerr
				return
			}
			assert.Equal(t, lipapi.EventResponseStarted, ev.Kind)

			_, rerr = res.Stream.Recv(context.Background())
			recvErrCh <- rerr
		}()

		select {
		case <-aRecvStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for backend A second Recv to start")
		}

		// Close fires while A is failing
		err = res.Stream.Close()
		require.NoError(t, err)

		// Release backend A's error
		close(releaseARecv)

		select {
		case rerr := <-recvErrCh:
			assert.True(t, errors.Is(rerr, io.EOF), "expected Recv to return io.EOF, got %v", rerr)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Recv to return")
		}

		// Backend B must never have been opened
		assert.Equal(t, int32(0), opensB.Load(), "backend B must not be opened when stream is already closed")

		// Exactly 1 terminal leg outcome produced for attempt A, swallowed
		legsMu.Lock()
		defer legsMu.Unlock()
		require.Len(t, recordedLegs, 1, "expected exactly 1 terminal leg outcome for attempt A")
		assert.Contains(t, []billing.LegOutcome{billing.LegOutcomeSwallowed, billing.LegOutcomeCanceled}, recordedLegs[0].Outcome, "attempt A must have a terminal outcome")
	})
}
