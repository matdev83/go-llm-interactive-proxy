package adapter

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

const defaultMaxStderrBytes = 64 << 10

// openStream starts one bidirectional execute attempt. It never collects the
// provider response and never restarts after output commitment.
func openStream(
	ctx context.Context,
	session ExecuteSession,
	opt Options,
	call lipapi.Call,
	cand routing.AttemptCandidate,
) (lipapi.ManagedEventStream, error) {
	inv, err := InvocationFromCall(call, cand)
	if err != nil {
		return nil, err
	}
	pending := opt.MaxPendingEvents
	if pending <= 0 {
		pending = 64
	}
	hostFrames := make(chan backendplugin.ClientFrame, 4)
	start := backendplugin.ClientFrame{
		Kind:       backendplugin.ClientFrameStart,
		InstanceID: opt.InstanceID,
		Invocation: &inv,
	}
	select {
	case hostFrames <- start:
	default:
		return nil, &ClassifiedError{Code: "internal", Message: "start frame enqueue failed", Retryable: true}
	}

	execCtx, execCancel := context.WithCancel(ctx)
	s := &managedStream{
		ctx:        execCtx,
		cancel:     execCancel,
		opt:        opt,
		events:     make(chan lipapi.Event, pending),
		errCh:      make(chan error, 1),
		hostFrames: hostFrames,
		done:       make(chan struct{}),
	}
	if opt.MaxStreamFrame > 0 {
		s.maxFrame = opt.MaxStreamFrame
	} else {
		s.maxFrame = int(backendplugin.DefaultMaxStreamFrameBytes)
	}
	s.stats.ProviderAttempts.Add(1)

	execStream := &bridgeExecuteStream{
		ctx:  execCtx,
		recv: hostFrames,
		send: s.onPluginFrame,
	}

	s.wg.Go(func() {
		defer close(s.done)
		defer close(s.events)
		err := session.Execute(execStream)
		if err != nil {
			fe := ClassifyExecuteError(err, s.outputCommitted.Load())
			if fe.InvalidatesGeneration() && opt.InvalidateGeneration != nil {
				s.invalidateOnce.Do(opt.InvalidateGeneration)
			}
			select {
			case s.errCh <- fe.ToClassifiedError():
			default:
			}
		}
	})

	if opt.Stderr != nil {
		s.wg.Go(func() {
			s.drainStderr(opt.Stderr)
		})
	}

	return s, nil
}

type managedStream struct {
	ctx             context.Context
	cancel          context.CancelFunc
	opt             Options
	events          chan lipapi.Event
	errCh           chan error
	hostFrames      chan backendplugin.ClientFrame
	done            chan struct{}
	wg              sync.WaitGroup
	closed          atomic.Bool
	outputCommitted atomic.Bool
	terminalSeen    atomic.Bool
	invalidateOnce  sync.Once
	validator       backendplugin.StreamValidator
	stats           streamStats
	maxFrame        int
	mu              sync.Mutex
	recvErr         error
	stderrBytes     int
}

type streamStats struct {
	ProviderAttempts atomic.Int64
}

func (s *managedStream) Recv(ctx context.Context) (lipapi.Event, error) {
	for {
		// Prefer queued plugin events over a concurrently ready Execute error so
		// commit-then-fail and similar modes deliver output before the failure.
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case ev, ok := <-s.events:
			if ok {
				return ev, nil
			}
			return s.recvAfterEventsClosed()
		default:
		}
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case ev, ok := <-s.events:
			if ok {
				return ev, nil
			}
			return s.recvAfterEventsClosed()
		case err := <-s.errCh:
			select {
			case ev, ok := <-s.events:
				if ok {
					s.stashRecvErr(err)
					return ev, nil
				}
			default:
			}
			if err == nil {
				return lipapi.Event{}, io.EOF
			}
			return lipapi.Event{}, err
		case <-s.ctx.Done():
			select {
			case ev, ok := <-s.events:
				if ok {
					return ev, nil
				}
			default:
			}
			if _, err := s.recvAfterEventsClosed(); err != nil && !errors.Is(err, io.EOF) {
				return lipapi.Event{}, err
			}
			return lipapi.Event{}, s.ctx.Err()
		}
	}
}

func (s *managedStream) stashRecvErr(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvErr == nil {
		s.recvErr = err
	}
}

func (s *managedStream) recvAfterEventsClosed() (lipapi.Event, error) {
	s.mu.Lock()
	err := s.recvErr
	s.mu.Unlock()
	if err != nil {
		return lipapi.Event{}, err
	}
	select {
	case err := <-s.errCh:
		if err != nil {
			return lipapi.Event{}, err
		}
	default:
	}
	return lipapi.Event{}, io.EOF
}

func (s *managedStream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	select {
	case s.hostFrames <- backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCloseInput}:
	default:
	}
	s.cancel()
	s.wg.Wait()
	return nil
}

func (s *managedStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	mode := lipapi.CancelModeProvider
	reason := backendplugin.CancelReasonHost
	switch cause.Kind {
	case lipapi.CancelContextDone:
		mode = lipapi.CancelModeTransport
		reason = backendplugin.CancelReasonDeadline
	case lipapi.CancelClientGone:
		mode = lipapi.CancelModeTransport
		reason = backendplugin.CancelReasonClient
	case lipapi.CancelExplicit, lipapi.CancelRaceLoser:
		mode = lipapi.CancelModeProvider
		reason = backendplugin.CancelReasonHost
	}
	timer := time.NewTimer(s.opt.CancelTimeout)
	defer timer.Stop()
	select {
	case s.hostFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		CancelReason: reason,
	}:
		return lipapi.CancelResult{Mode: mode}
	case <-ctx.Done():
		return lipapi.CancelResult{Mode: mode, Err: ctx.Err()}
	case <-timer.C:
		s.cancel()
		return lipapi.CancelResult{Mode: mode, Err: context.DeadlineExceeded}
	}
}

func (s *managedStream) onPluginFrame(frame backendplugin.ServerFrame) error {
	if s.terminalSeen.Load() {
		return backendplugin.ErrEventAfterTerminal
	}
	if s.maxFrame > 0 {
		if err := backendplugin.ValidateServerFrameSize(frame, uint64(s.maxFrame)); err != nil {
			if errors.Is(err, backendplugin.ErrOversizedMessage) {
				return ProtocolViolation(err)
			}
			return err
		}
	}
	if err := s.validator.Push(frame); err != nil {
		return err
	}
	switch frame.Kind {
	case backendplugin.ServerFrameAccepted, backendplugin.ServerFrameDiagnostic, backendplugin.ServerFrameCancelOutcome:
		return nil
	case backendplugin.ServerFrameEvent:
		ev, err := eventToLipapi(frame.Event)
		if err != nil {
			return err
		}
		if lipapi.OutputCommitted(ev) {
			s.outputCommitted.Store(true)
		}
		select {
		case s.events <- ev:
			return nil
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	case backendplugin.ServerFrameTerminal:
		if !s.terminalSeen.CompareAndSwap(false, true) {
			return backendplugin.ErrMultipleTerminals
		}
		if frame.Terminal != nil && frame.Terminal.Error != nil {
			cerr := sanitizePluginError(frame.Terminal.Error)
			if s.outputCommitted.Load() {
				cerr.OutputCommitted = true
				cerr.Retryable = false
			}
			s.mu.Lock()
			s.recvErr = cerr
			s.mu.Unlock()
		}
		return nil
	default:
		return backendplugin.ErrUnknownFrameKind
	}
}

func (s *managedStream) drainStderr(r io.Reader) {
	max := s.opt.MaxStderrBytes
	if max <= 0 {
		max = defaultMaxStderrBytes
	}
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.mu.Lock()
			remain := max - s.stderrBytes
			if remain > 0 {
				if n > remain {
					n = remain
				}
				s.stderrBytes += n
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
		s.mu.Lock()
		full := s.stderrBytes >= max
		s.mu.Unlock()
		if full {
			_, _ = io.Copy(io.Discard, r)
			return
		}
	}
}

func (s *managedStream) OutputCommitted() bool { return s.outputCommitted.Load() }
func (s *managedStream) Attempts() int64       { return s.stats.ProviderAttempts.Load() }

type bridgeExecuteStream struct {
	ctx  context.Context
	recv <-chan backendplugin.ClientFrame
	send func(backendplugin.ServerFrame) error
}

func (b *bridgeExecuteStream) Context() context.Context { return b.ctx }

func (b *bridgeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	select {
	case <-b.ctx.Done():
		return backendplugin.ClientFrame{}, b.ctx.Err()
	case fr, ok := <-b.recv:
		if !ok {
			return backendplugin.ClientFrame{}, io.EOF
		}
		return fr, nil
	}
}

func (b *bridgeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	return b.send(frame)
}
