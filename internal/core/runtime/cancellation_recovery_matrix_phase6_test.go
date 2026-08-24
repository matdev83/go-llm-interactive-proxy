package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type phase6MatrixStream struct {
	events      []lipapi.Event
	eventIdx    int
	recvErr     error
	blockRecv   chan struct{}
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
}

func (s *phase6MatrixStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.blockRecv != nil {
		select {
		case <-s.blockRecv:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if s.eventIdx < len(s.events) {
		ev := s.events[s.eventIdx]
		s.eventIdx++
		return ev, nil
	}
	if s.recvErr != nil {
		return lipapi.Event{}, s.recvErr
	}
	return lipapi.Event{}, io.EOF
}

func (s *phase6MatrixStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.cancelCalls.Add(1)
	return lipapi.CancelResult{Mode: lipapi.CancelModeTransport}
}

func (s *phase6MatrixStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

// TestPhase6_OpenCancel_NoRecoverableFailover_Serial proves that when A-leg is canceled
// during backend Open (even if Open returns an error wrapping ErrRecoverablePreOutput),
// the executor does NOT swallow the error and failover to candidate 2.
func TestPhase6_OpenCancel_NoRecoverableFailover_Serial(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	authIDCh, sendAuthID := captureAuthoritativeID()

	var badOpens, okOpens atomic.Int64
	openStarted := make(chan struct{}, 1)

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				sendAuthID(call)
				badOpens.Add(1)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return nil, lipapi.RecoverablePreOutputError(ctx.Err())
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				okOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "unexpected replacement"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-1",
		Route: lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Session: lipapi.SessionRef{
			ALegID: "aleg-cancel-open-test",
		},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	execDone := make(chan error, 1)
	go func() {
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			execDone <- err
			return
		}
		defer func() { _ = stream.Close() }()
		_, recvErr := stream.Recv(context.Background())
		execDone <- recvErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("bad Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	select {
	case err = <-execDone:
	case <-time.After(time.Second):
		t.Fatal("Execute did not finish after cancel")
	}

	if err == nil {
		t.Fatal("expected error on canceled execution, got nil")
	}

	if okOpens.Load() != 0 {
		t.Fatalf("expected 0 opens for 'ok' backend after cancellation, got %d", okOpens.Load())
	}
	if badOpens.Load() != 1 {
		t.Fatalf("expected 1 open for 'bad' backend, got %d", badOpens.Load())
	}
}

// TestPhase6_RecvCancel_NoRecoverableFailover_Serial proves that when cancellation occurs
// during Recv (or intentional legacy transport death), core does not open replacement candidates.
func TestPhase6_RecvCancel_NoRecoverableFailover_Serial(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	authIDCh, sendAuthID := captureAuthoritativeID()

	var badOpens, okOpens atomic.Int64
	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})

	badStream := &phase6MatrixStream{
		blockRecv: blockRecv,
		recvErr:   lipapi.RecoverablePreOutputError(errors.New("pre-output failure")),
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				sendAuthID(call)
				badOpens.Add(1)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return badStream, nil
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				okOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "failover"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-2",
		Route: lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Session: lipapi.SessionRef{
			ALegID: "aleg-cancel-recv-test",
		},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := stream.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("bad Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case err = <-recvDone:
	case <-time.After(time.Second):
		t.Fatal("Recv did not finish after cancel")
	}

	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}

	if okOpens.Load() != 0 {
		t.Fatalf("expected 0 opens for 'ok' backend, got %d", okOpens.Load())
	}
}

// TestPhase6_ParallelRace_Cancel_NoFailoverOrNewBLegs proves that cancelling a parallel
// race cleanly tears down all active arms and does not open subsequent candidate failover groups.
func TestPhase6_ParallelRace_Cancel_NoFailoverOrNewBLegs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	authIDCh, sendAuthID := captureAuthoritativeID()

	var p1Opens, p2Opens, fallbackOpens atomic.Int64
	p1Started := make(chan struct{}, 1)
	p2Started := make(chan struct{}, 1)
	blockP1 := make(chan struct{})
	blockP2 := make(chan struct{})

	stream1 := &phase6MatrixStream{blockRecv: blockP1}
	stream2 := &phase6MatrixStream{blockRecv: blockP2}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 4
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"p1": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				sendAuthID(call)
				p1Opens.Add(1)
				select {
				case p1Started <- struct{}{}:
				default:
				}
				return stream1, nil
			},
		},
		"p2": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				sendAuthID(call)
				p2Opens.Add(1)
				select {
				case p2Started <- struct{}{}:
				default:
				}
				return stream2, nil
			},
		},
		"fallback": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				fallbackOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "fallback"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-3",
		Route: lipapi.RouteIntent{Selector: "p1:m!p2:m|fallback:m"},
		Session: lipapi.SessionRef{
			ALegID: "aleg-parallel-cancel-test",
		},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	execDone := make(chan error, 1)
	go func() {
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			execDone <- err
			return
		}
		defer func() { _ = stream.Close() }()
		_, rErr := stream.Recv(context.Background())
		execDone <- rErr
	}()

	select {
	case <-p1Started:
	case <-time.After(time.Second):
		t.Fatal("p1 Open was not started")
	}
	select {
	case <-p2Started:
	case <-time.After(time.Second):
		t.Fatal("p2 Open was not started")
	}

	// Drain the authoritative ID capture; only one value is buffered, ensure at least one was captured.
	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockP1)
	close(blockP2)

	select {
	case err = <-execDone:
	case <-time.After(time.Second):
		t.Fatal("Execute did not complete after cancel")
	}

	if err == nil {
		t.Fatal("expected cancellation error on parallel race, got nil")
	}

	if p1Opens.Load() != 1 {
		t.Fatalf("expected 1 open for 'p1' parallel arm, got %d", p1Opens.Load())
	}
	if p2Opens.Load() != 1 {
		t.Fatalf("expected 1 open for 'p2' parallel arm, got %d", p2Opens.Load())
	}
	if fallbackOpens.Load() != 0 {
		t.Fatalf("expected 0 fallback opens, got %d", fallbackOpens.Load())
	}
}

// TestPhase6_PostCommit_CancelPreservesNoRetry proves that cancellation or transport error
// after client-visible output was committed never attempts failover.
func TestPhase6_PostCommit_CancelPreservesNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var firstOpens, secondOpens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"first": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				firstOpens.Add(1)
				return &phase6MatrixStream{
					events: []lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventTextDelta, Delta: "committed_text"},
					},
					recvErr: lipapi.RecoverablePreOutputError(errors.New("crash after commit")),
				}, nil
			},
		},
		"second": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				secondOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventTextDelta, Delta: "replayed"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-4",
		Route: lipapi.RouteIntent{Selector: "first:m|second:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var committedSeen bool
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
		if lipapi.OutputCommitted(ev) {
			committedSeen = true
		}
	}
	if !committedSeen {
		t.Fatal("expected committed output before failure")
	}

	if secondOpens.Load() != 0 {
		t.Fatalf("expected 0 opens for second backend after committed output, got %d", secondOpens.Load())
	}
	if firstOpens.Load() != 1 {
		t.Fatalf("expected 1 open for first backend, got %d", firstOpens.Load())
	}
}

// TestPhase6_RequestContextCancel_NoRecoverableFailover proves that when request context
// is canceled during backend Open or Recv, the executor does not attempt failover.
func TestPhase6_RequestContextCancel_NoRecoverableFailover(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var badOpens, okOpens atomic.Int64
	openStarted := make(chan struct{}, 1)

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				badOpens.Add(1)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				okOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "unexpected replacement"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-ctx-cancel",
		Route: lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	reqCtx, reqCancel := context.WithCancel(context.Background())
	execDone := make(chan error, 1)
	go func() {
		stream, err := ex.Execute(reqCtx, call)
		if err != nil {
			execDone <- err
			return
		}
		defer func() { _ = stream.Close() }()
		_, recvErr := stream.Recv(reqCtx)
		execDone <- recvErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("bad Open was not started")
	}

	reqCancel()

	select {
	case err = <-execDone:
	case <-time.After(time.Second):
		t.Fatal("Execute did not finish after request context cancellation")
	}

	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	if okOpens.Load() != 0 {
		t.Fatalf("expected 0 opens for 'ok' backend after request context cancel, got %d", okOpens.Load())
	}
}

// TestPhase6_DeadlineExpired_NoRecoverableFailover proves that when request deadline expires
// during execution, no replacement attempt is opened.
func TestPhase6_DeadlineExpired_NoRecoverableFailover(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var badOpens, okOpens atomic.Int64
	openStarted := make(chan struct{}, 1)

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				badOpens.Add(1)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				okOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "unexpected replacement"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-deadline",
		Route: lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer reqCancel()

	execDone := make(chan error, 1)
	go func() {
		stream, err := ex.Execute(reqCtx, call)
		if err != nil {
			execDone <- err
			return
		}
		defer func() { _ = stream.Close() }()
		_, recvErr := stream.Recv(reqCtx)
		execDone <- recvErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("bad Open was not started")
	}

	select {
	case err = <-execDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not finish after deadline expiry")
	}

	if err == nil {
		t.Fatal("expected error on expired deadline, got nil")
	}

	if okOpens.Load() != 0 {
		t.Fatalf("expected 0 opens for 'ok' backend after deadline expiry, got %d", okOpens.Load())
	}
}

// TestPhase6_ParallelRace_LoserCancellation_NotRetried proves that in a parallel race,
// the loser is cleanly canceled as CancelRaceLoser and no retry/failover is opened for the loser.
func TestPhase6_ParallelRace_LoserCancellation_NotRetried(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var p2RetryOpens atomic.Int64
	p1Stream := &phase6MatrixStream{
		events: []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "winner p1"},
			{Kind: lipapi.EventResponseFinished},
		},
	}
	p2Block := make(chan struct{})
	p2Stream := &phase6MatrixStream{blockRecv: p2Block}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 4
	ex.Backends = map[string]execbackend.Backend{
		"p1": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return p1Stream, nil
			},
		},
		"p2": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return p2Stream, nil
			},
		},
		"p2_retry": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				p2RetryOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "unexpected p2 retry"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		ID:    "call-parallel-loser",
		Route: lipapi.RouteIntent{Selector: "p1:m!p2:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()

	var b strings.Builder
	for {
		ev, rErr := stream.Recv(context.Background())
		if rErr != nil {
			break
		}
		if ev.Kind == lipapi.EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
	textReceived := b.String()

	close(p2Block)

	if textReceived != "winner p1" {
		t.Fatalf("expected 'winner p1', got %q", textReceived)
	}
	if p2RetryOpens.Load() != 0 {
		t.Fatalf("expected 0 opens for p2_retry, got %d", p2RetryOpens.Load())
	}
}
