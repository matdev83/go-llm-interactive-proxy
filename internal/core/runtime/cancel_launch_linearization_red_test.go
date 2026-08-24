package runtime_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type openTrackingStream struct {
	cancelCalls atomic.Int64
	closeCalls  atomic.Int64
}

func (s *openTrackingStream) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (s *openTrackingStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	s.cancelCalls.Add(1)
	return lipapi.CancelResult{Mode: lipapi.CancelModeProvider}
}

func (s *openTrackingStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func TestRED_CancelLinearization_DelayedParallelArmLaunchesAfterALegCancel(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	primaryOpenEntered := make(chan struct{})
	var hedgedOpenCalls atomic.Int64
	hedgedOpenStarted := make(chan struct{})

	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				close(primaryOpenEntered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		"hedged": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				hedgedOpenCalls.Add(1)
				select {
				case hedgedOpenStarted <- struct{}{}:
				default:
				}
				return &openTrackingStream{}, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	call := parallelCall("[handicap=1]primary:model!hedged:model")
	aLegID := "aleg-lin-1"
	call.Session.ALegID = aLegID

	go func() {
		_, _ = ex.Execute(context.Background(), call)
	}()

	// Wait until primary arm has entered Open
	select {
	case <-primaryOpenEntered:
	case <-time.After(1 * time.Second):
		t.Fatal("primary Open was not entered")
	}

	// Cancel A-leg explicitly
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	// Wait briefly: without launch permit barrier, parallel worker or hedged branch still calls hedged Open
	time.Sleep(50 * time.Millisecond)

	if got := hedgedOpenCalls.Load(); got > 0 {
		t.Fatalf("delayed parallel branch invoked Backend.Open %d times after explicit A-leg cancellation", got)
	}
}

func TestRED_CancelLinearization_CancelVsSerialOpenInterleavedWindow(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	var openCalls atomic.Int64
	ex.Backends = map[string]execbackend.Backend{
		"serial-b": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openCalls.Add(1)
				return &openTrackingStream{}, nil
			},
		},
	}

	aLegID := "aleg-lin-2"
	call := parallelCall("serial-b:model")
	call.Session.ALegID = aLegID

	// Cancel A-leg BEFORE execute runs
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	_, _ = ex.Execute(context.Background(), call)

	if got := openCalls.Load(); got > 0 {
		t.Fatalf("serial path invoked Backend.Open %d times after explicit A-leg cancel won", got)
	}
}

func TestRED_CancelLinearization_CancelDuringBlockedOpenDoesNotReachContext(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	openStarted := make(chan struct{})
	var openCtxDone atomic.Bool

	ex.Backends = map[string]execbackend.Backend{
		"block-b": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				close(openStarted)
				select {
				case <-ctx.Done():
					openCtxDone.Store(true)
					return nil, ctx.Err()
				case <-time.After(200 * time.Millisecond):
					return &openTrackingStream{}, nil
				}
			},
		},
	}

	aLegID := "aleg-lin-3"
	call := parallelCall("block-b:model")
	call.Session.ALegID = aLegID

	go func() {
		_, _ = ex.Execute(context.Background(), call)
	}()

	select {
	case <-openStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("Backend.Open was not started")
	}

	// Cancel A-leg while Open is blocked
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// In current code, aScope has no launch context registered for in-flight Open, so openCtx is not canceled!
	if !openCtxDone.Load() {
		t.Fatal("openCtx was not canceled during in-flight Backend.Open when A-leg was canceled")
	}
}

func TestCharacterization_CancelLinearization_LateOpenReturnsAfterCancelSettledOnceNotPublished(t *testing.T) {
	t.Parallel()

	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.ALegLifecycle = coord

	openStarted := make(chan struct{})
	unblockOpen := make(chan struct{})
	stream := &openTrackingStream{}

	ex.Backends = map[string]execbackend.Backend{
		"late-b": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				close(openStarted)
				<-unblockOpen
				return stream, nil
			},
		},
	}

	aLegID := "aleg-lin-4"
	call := parallelCall("late-b:model")
	call.Session.ALegID = aLegID

	streamCh := make(chan lipapi.EventStream, 1)
	errCh := make(chan error, 1)
	go func() {
		s, err := ex.Execute(context.Background(), call)
		streamCh <- s
		errCh <- err
	}()

	select {
	case <-openStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("Backend.Open was not started")
	}

	// Cancel A-leg while Open is blocked
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	// Now unblock Open so it returns a stream late
	close(unblockOpen)

	// Wait for Execute to complete
	select {
	case s := <-streamCh:
		err := <-errCh
		if err == nil && s != nil {
			// If stream was returned, collecting should fail or be canceled
			_, colErr := lipapi.Collect(context.Background(), s)
			if colErr == nil {
				t.Fatal("late open stream was unexpectedly published and collectible")
			}
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Execute did not complete after late open unblocked")
	}

	// Stream should have been canceled/closed exactly once
	if stream.cancelCalls.Load() == 0 && stream.closeCalls.Load() == 0 {
		t.Fatal("late returned stream was never terminalized")
	}
}
