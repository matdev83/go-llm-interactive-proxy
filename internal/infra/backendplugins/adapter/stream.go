package adapter

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

const defaultMaxStderrBytes = 64 << 10

const maxBufferedUsageEvidence = 1024

// openStream starts one bidirectional execute attempt. It never collects the
// provider response and never restarts after output commitment.
func openStream(
	ctx context.Context,
	session ExecuteSession,
	opt Options,
	call lipapi.Call,
	cand routing.AttemptCandidate,
) (lipapi.ManagedEventStream, error) {
	inv, err := InvocationFromCall(call, cand, opt.Negotiation)
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
	if err := start.ValidateShape(); err != nil {
		return nil, &ClassifiedError{Code: "invalid_invocation", Message: err.Error(), Retryable: false}
	}
	if err := backendplugin.ValidateClientFrameBounds(start); err != nil {
		return nil, &ClassifiedError{Code: "oversized_frame", Message: err.Error(), Retryable: false}
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
		ctx:     execCtx,
		closeCh: make(chan struct{}),
		recv:    hostFrames,
		send:    s.onPluginFrame,
		neg:     opt.Negotiation,
	}

	s.wg.Go(func() {
		defer close(s.done)
		defer close(s.events)
		err := session.Execute(execStream)
		if err != nil || !s.terminalSeen.Load() {
			s.promptCacheMu.Lock()
			s.promptCacheBuffer.Discard()
			s.promptCacheMu.Unlock()
		}
		if err != nil {
			s.cancelMu.Lock()
			wasCanceled := s.cancelRequested || s.forcedAbort || s.closed.Load() || (s.ctx != nil && s.ctx.Err() != nil)
			s.cancelMu.Unlock()

			var fe *ExecuteFailureError
			if wasCanceled && !isProtocolSentinel(err) {
				fe = &ExecuteFailureError{
					Kind:            ExecuteFailureCanceled,
					Err:             err,
					OutputCommitted: s.outputCommitted.Load(),
				}
			} else {
				fe = ClassifyExecuteError(err, s.outputCommitted.Load())
			}
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

type CancellationProgress struct {
	Requested           bool
	Cause               lipapi.CancelCause
	EffectiveDeadline   time.Time
	OutcomeSeen         bool
	OutcomeAcknowledged bool
	OutcomeMode         backendplugin.CancelMode
	OutcomeReason       backendplugin.CancelReason
	OutcomeDetail       string
	ForcedAbort         bool
	TerminalSeen        bool
}

type managedStream struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	opt                 Options
	events              chan lipapi.Event
	errCh               chan error
	hostFrames          chan backendplugin.ClientFrame
	done                chan struct{}
	cancelMu            sync.Mutex
	cancelRequested     bool
	cancelCause         lipapi.CancelCause
	cancelDeadline      time.Time
	outcomeSeen         bool
	outcomeAcknowledged bool
	outcomeMode         backendplugin.CancelMode
	outcomeReason       backendplugin.CancelReason
	outcomeDetail       string
	forcedAbort         bool
	wg                  sync.WaitGroup
	closeOnce           sync.Once
	closed              atomic.Bool
	outputCommitted     atomic.Bool
	terminalSeen        atomic.Bool
	invalidateOnce      sync.Once
	validator           backendplugin.StreamValidator
	stats               streamStats
	maxFrame            int
	mu                  sync.Mutex
	recvErr             error
	stderrBytes         int
	usageMu             sync.Mutex
	usageEvidence       []lipapi.Event
	promptCacheMu       sync.Mutex
	promptCacheBuffer   promptcache.ObservationBuffer
}

func (s *managedStream) CancellationProgress() CancellationProgress {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	return CancellationProgress{
		Requested:           s.cancelRequested,
		Cause:               s.cancelCause,
		EffectiveDeadline:   s.cancelDeadline,
		OutcomeSeen:         s.outcomeSeen,
		OutcomeAcknowledged: s.outcomeAcknowledged,
		OutcomeMode:         s.outcomeMode,
		OutcomeReason:       s.outcomeReason,
		OutcomeDetail:       s.outcomeDetail,
		ForcedAbort:         s.forcedAbort,
		TerminalSeen:        s.terminalSeen.Load(),
	}
}

func (s *managedStream) OutcomeSeen() bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	return s.outcomeSeen
}

func (s *managedStream) ForcedAbort() bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	return s.forcedAbort
}

func (s *managedStream) CancellationFallback() string {
	if backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation) {
		return "negotiated"
	}
	return "legacy"
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

func computeCancelDeadline(ctx context.Context, cancelTimeout time.Duration) (time.Time, int64) {
	now := time.Now()
	var effectiveDeadline time.Time

	if cancelTimeout <= 0 {
		cancelTimeout = 2 * time.Second
	}
	effectiveDeadline = now.Add(cancelTimeout)

	if ctxDeadline, ok := ctx.Deadline(); ok {
		if effectiveDeadline.IsZero() || ctxDeadline.Before(effectiveDeadline) {
			effectiveDeadline = ctxDeadline
		}
	}

	if effectiveDeadline.IsZero() {
		return time.Time{}, 0
	}
	return effectiveDeadline, effectiveDeadline.UnixMilli()
}

func cancelModeToLipapi(mode backendplugin.CancelMode, ack bool) lipapi.CancelMode {
	switch mode {
	case backendplugin.CancelModeProvider:
		return lipapi.CancelModeProvider
	case backendplugin.CancelModeTransport:
		return lipapi.CancelModeTransport
	case backendplugin.CancelModeCloseOnly:
		return lipapi.CancelModeCloseOnly
	case backendplugin.CancelModeNone:
		return lipapi.CancelModeNone
	default:
		if ack {
			return lipapi.CancelModeProvider
		}
		return lipapi.CancelModeTransport
	}
}

func (s *managedStream) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		select {
		case s.hostFrames <- backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCloseInput, InstanceID: s.opt.InstanceID}:
		case <-s.done:
		case <-s.ctx.Done():
		}
		s.cancel()
		s.wg.Wait()
	})
	return nil
}

func (s *managedStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if s.closed.Load() {
		return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
	}
	if s.terminalSeen.Load() {
		prog := s.CancellationProgress()
		if prog.OutcomeSeen {
			return lipapi.CancelResult{Mode: cancelModeToLipapi(prog.OutcomeMode, prog.OutcomeAcknowledged)}
		}
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}

	if !backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation) {
		s.cancelMu.Lock()
		s.cancelRequested = true
		s.cancelCause = cause
		s.forcedAbort = true
		s.cancelMu.Unlock()

		s.cancel()
		timeout := s.opt.CancelTimeout
		if timeout <= 0 {
			timeout = 500 * time.Millisecond
		}
		select {
		case <-s.done:
			return lipapi.CancelResult{Mode: lipapi.CancelModeTransport}
		case <-ctx.Done():
			return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: ctx.Err()}
		case <-time.After(timeout):
			return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: context.DeadlineExceeded}
		}
	}

	effectiveDeadline, deadlineMS := computeCancelDeadline(ctx, s.opt.CancelTimeout)
	s.cancelMu.Lock()
	firstRequest := !s.cancelRequested
	s.cancelRequested = true
	s.cancelCause = cause
	if firstRequest || s.cancelDeadline.IsZero() || (!effectiveDeadline.IsZero() && effectiveDeadline.Before(s.cancelDeadline)) {
		s.cancelDeadline = effectiveDeadline
	}
	s.cancelMu.Unlock()

	if firstRequest {
		reason := backendplugin.CancelReasonHost
		switch cause.Kind {
		case lipapi.CancelContextDone:
			reason = backendplugin.CancelReasonDeadline
		case lipapi.CancelClientGone:
			reason = backendplugin.CancelReasonClient
		case lipapi.CancelExplicit, lipapi.CancelRaceLoser:
			reason = backendplugin.CancelReasonHost
		default:
			if cause.Detail != "" {
				reason = backendplugin.CancelReason(cause.Detail)
			}
		}

		cancelFrame := backendplugin.ClientFrame{
			Kind:                 backendplugin.ClientFrameCancel,
			InstanceID:           s.opt.InstanceID,
			CancelReason:         reason,
			CancelDeadlineUnixMS: deadlineMS,
		}

		select {
		case s.hostFrames <- cancelFrame:
		case <-s.done:
		case <-s.ctx.Done():
		case <-ctx.Done():
			s.cancelMu.Lock()
			s.forcedAbort = true
			s.cancelMu.Unlock()
			s.cancel()
			<-s.done
			return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: ctx.Err()}
		}
	}

	var timerChan <-chan time.Time
	if !effectiveDeadline.IsZero() {
		graceDuration := time.Until(effectiveDeadline)
		if graceDuration <= 0 {
			s.cancelMu.Lock()
			s.forcedAbort = true
			s.cancelMu.Unlock()
			s.cancel()
			<-s.done
			return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: context.DeadlineExceeded}
		}
		timer := time.NewTimer(graceDuration)
		defer timer.Stop()
		timerChan = timer.C
	}

	select {
	case <-s.done:
	case <-ctx.Done():
		s.cancelMu.Lock()
		s.forcedAbort = true
		s.cancelMu.Unlock()
		s.cancel()
		<-s.done
		return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: ctx.Err()}
	case <-timerChan:
		s.cancelMu.Lock()
		s.forcedAbort = true
		s.cancelMu.Unlock()
		s.cancel()
		<-s.done
		return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: context.DeadlineExceeded}
	}

	prog := s.CancellationProgress()
	if prog.ForcedAbort {
		return lipapi.CancelResult{Mode: lipapi.CancelModeTransport}
	}
	if prog.OutcomeSeen {
		return lipapi.CancelResult{Mode: cancelModeToLipapi(prog.OutcomeMode, prog.OutcomeAcknowledged)}
	}
	return lipapi.CancelResult{Mode: lipapi.CancelModeTransport}
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
	case backendplugin.ServerFrameAccepted, backendplugin.ServerFrameDiagnostic:
		return nil
	case backendplugin.ServerFrameCancelOutcome:
		if !backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation) {
			return ProtocolViolation(backendplugin.ErrInvalidFrame)
		}
		if frame.CancelOutcome == nil {
			return ProtocolViolation(backendplugin.ErrInvalidFrame)
		}
		s.cancelMu.Lock()
		s.outcomeSeen = true
		s.outcomeAcknowledged = frame.CancelOutcome.Acknowledged
		s.outcomeMode = frame.CancelOutcome.Mode
		s.outcomeReason = frame.CancelOutcome.Reason
		s.outcomeDetail = frame.CancelOutcome.Detail
		s.cancelMu.Unlock()
		return nil
	case backendplugin.ServerFramePromptCacheObservation:
		if !backendplugin.PromptCacheNegotiated(s.opt.Negotiation) {
			return ProtocolViolation(backendplugin.ErrPromptCacheUnsupported)
		}
		s.promptCacheMu.Lock()
		err := s.promptCacheBuffer.Add(*frame.PromptCacheObservation)
		s.promptCacheMu.Unlock()
		if err != nil {
			return ProtocolViolation(err)
		}
		return nil
	case backendplugin.ServerFrameAccountingEvidence:
		if !slices.Contains(s.opt.Negotiation.EnabledFeatures, backendplugin.FeatureAccountingEvidence) {
			return ProtocolViolation(backendplugin.ErrInvalidFrame)
		}
		ev, err := accountingEvidenceToEvent(frame.Accounting)
		if err != nil {
			return ProtocolViolation(err)
		}
		s.usageMu.Lock()
		if len(s.usageEvidence) >= maxBufferedUsageEvidence {
			s.usageMu.Unlock()
			return ProtocolViolation(backendplugin.ErrOversizedMessage)
		}
		s.usageEvidence = append(s.usageEvidence, ev)
		s.usageMu.Unlock()
		return nil
	case backendplugin.ServerFrameEvent:
		if err := backendplugin.RequireExactOpenResponsesEventABISupport(s.opt.Negotiation, frame.Event); err != nil {
			return err
		}
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
		s.promptCacheMu.Lock()
		if frame.Terminal != nil && frame.Terminal.Status == backendplugin.TerminalSuccess {
			s.promptCacheBuffer.Commit()
		} else {
			s.promptCacheBuffer.Discard()
		}
		s.promptCacheMu.Unlock()
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

func (s *managedStream) DrainPromptCacheObservations() []promptcache.Observation {
	if s == nil {
		return nil
	}
	s.promptCacheMu.Lock()
	defer s.promptCacheMu.Unlock()
	return s.promptCacheBuffer.DrainPromptCacheObservations()
}

func (s *managedStream) DrainUsageEvidence() []lipapi.Event {
	if s == nil {
		return nil
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	out := append([]lipapi.Event(nil), s.usageEvidence...)
	s.usageEvidence = nil
	return out
}

func accountingEvidenceToEvent(e *backendplugin.AccountingEvidence) (lipapi.Event, error) {
	if e == nil {
		return lipapi.Event{}, backendplugin.ErrInvalidFrame
	}
	if err := backendplugin.ValidateAccountingEvidence(*e); err != nil {
		return lipapi.Event{}, err
	}
	ev := lipapi.Event{Kind: lipapi.EventUsageDelta, UsagePresence: e.Presence, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSource(e.Source), Authority: lipapi.UsageAuthority(e.Authority), DedupeKey: e.DedupeKey}}
	if e.InputTokens != nil {
		ev.InputTokens = int(*e.InputTokens)
	}
	if e.OutputTokens != nil {
		ev.OutputTokens = int(*e.OutputTokens)
	}
	if e.CacheReadTokens != nil {
		ev.CacheReadTokens = int(*e.CacheReadTokens)
	}
	if e.CacheWriteTokens != nil {
		ev.CacheWriteTokens = int(*e.CacheWriteTokens)
	}
	if e.ReasoningTokens != nil {
		ev.ReasoningTokens = int(*e.ReasoningTokens)
	}
	if e.TotalTokens != nil {
		ev.TotalTokens = int(*e.TotalTokens)
	}
	return ev, nil
}

func (s *managedStream) drainStderr(r io.Reader) {
	maxBytes := s.opt.MaxStderrBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxStderrBytes
	}
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.mu.Lock()
			remain := maxBytes - s.stderrBytes
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
		full := s.stderrBytes >= maxBytes
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
	ctx       context.Context
	closeCh   chan struct{}
	closeOnce sync.Once
	recv      <-chan backendplugin.ClientFrame
	send      func(backendplugin.ServerFrame) error
	neg       backendplugin.Negotiation
}

var (
	_ backendplugin.ExecuteStream               = (*bridgeExecuteStream)(nil)
	_ backendplugin.OptionalExecuteStreamCloser = (*bridgeExecuteStream)(nil)
	_ backendplugin.OptionalNegotiatedStream    = (*bridgeExecuteStream)(nil)
)

func (b *bridgeExecuteStream) Negotiation() backendplugin.Negotiation { return b.neg }

func (b *bridgeExecuteStream) Context() context.Context { return b.ctx }

// Close unblocks a host Execute pump when the gRPC server finishes before the
// adapter's client-frame channel has been closed.
func (b *bridgeExecuteStream) Close() error {
	b.closeOnce.Do(func() { close(b.closeCh) })
	return nil
}

func (b *bridgeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	select {
	case <-b.ctx.Done():
		return backendplugin.ClientFrame{}, b.ctx.Err()
	case <-b.closeCh:
		return backendplugin.ClientFrame{}, io.EOF
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
