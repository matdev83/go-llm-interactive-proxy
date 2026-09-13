package adapter

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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
			wasCanceled := s.cancelState.interrupted() || s.closed.Load() || (s.ctx != nil && s.ctx.Err() != nil)

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
	ctx               context.Context
	cancel            context.CancelFunc
	opt               Options
	events            chan lipapi.Event
	errCh             chan error
	hostFrames        chan backendplugin.ClientFrame
	done              chan struct{}
	cancelState       cancelState
	wg                sync.WaitGroup
	closeOnce         sync.Once
	closed            atomic.Bool
	outputCommitted   atomic.Bool
	terminalSeen      atomic.Bool
	invalidateOnce    sync.Once
	validator         backendplugin.StreamValidator
	stats             streamStats
	maxFrame          int
	mu                sync.Mutex
	recvErr           error
	stderrBytes       int
	usageMu           sync.Mutex
	usageEvidence     []lipapi.Event
	economicMu        sync.Mutex
	economicEvidence  economicEvidenceBuffer
	promptCacheMu     sync.Mutex
	promptCacheBuffer promptcache.ObservationBuffer
}

// AccountingEvidenceEnabled reports whether this adapter negotiated a typed
// host-only accounting sideband that owns the canonical provider usage path.
// Legacy V1 is deliberately not reported here: its six counters have no
// source-role marker, so each connector must project its own canonical key
// when V1 is authoritative. Keeping V1 false also preserves auxiliary/native
// V1 evidence (for example Codex compaction) alongside primary usage.
func (s *managedStream) AccountingEvidenceEnabled() bool {
	if s == nil {
		return false
	}
	return backendplugin.AccountingEvidenceV2Negotiated(s.opt.Negotiation)
}

func (s *managedStream) CancellationProgress() CancellationProgress {
	prog := s.cancelState.snapshot()
	prog.TerminalSeen = s.terminalSeen.Load()
	return prog
}

func (s *managedStream) CancellationOutcomeSeen() bool {
	return s.cancelState.isOutcomeSeen()
}

func (s *managedStream) CancellationForcedAbort() bool {
	return s.cancelState.forcedAbort()
}

func (s *managedStream) CancellationHandshakeNegotiated() bool {
	return backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation)
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
		return s.cancelAfterTerminal()
	}
	if !backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation) {
		return s.cancelLegacy(ctx, cause)
	}
	return s.cancelNegotiated(ctx, cause)
}

func (s *managedStream) cancelAfterTerminal() lipapi.CancelResult {
	prog := s.CancellationProgress()
	if prog.OutcomeSeen {
		return outcomeCancelResult(prog)
	}
	return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
}

func (s *managedStream) cancelLegacy(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.cancelState.request(cause, time.Time{})
	s.cancelState.markForced()

	s.cancel()
	timeout := s.opt.CancelTimeout
	if timeout <= 0 {
		timeout = legacyCancelWaitFallback
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

func (s *managedStream) cancelNegotiated(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	effectiveDeadline, deadlineMS := computeCancelDeadline(ctx, s.opt.CancelTimeout)
	firstRequest := s.cancelState.request(cause, effectiveDeadline)

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
			return s.forceAbortAndWait(ctx.Err())
		}
	}

	var timerChan <-chan time.Time
	if !effectiveDeadline.IsZero() {
		graceDuration := time.Until(effectiveDeadline)
		if graceDuration <= 0 {
			return s.forceAbortAndWait(context.DeadlineExceeded)
		}
		timer := time.NewTimer(graceDuration)
		defer timer.Stop()
		timerChan = timer.C
	}

	select {
	case <-s.done:
	case <-ctx.Done():
		return s.forceAbortAndWait(ctx.Err())
	case <-timerChan:
		return s.forceAbortAndWait(context.DeadlineExceeded)
	}

	prog := s.CancellationProgress()
	if prog.ForcedAbort {
		return lipapi.CancelResult{Mode: lipapi.CancelModeTransport}
	}
	if prog.OutcomeSeen {
		return outcomeCancelResult(prog)
	}
	return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
}

func (s *managedStream) forceAbortAndWait(err error) lipapi.CancelResult {
	s.cancelState.markForced()
	s.cancel()
	<-s.done
	return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: err}
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
		s.cancelState.observeOutcome(frame.CancelOutcome)
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
		if frame.AccountingV2 != nil {
			if !backendplugin.AccountingEvidenceV2Negotiated(s.opt.Negotiation) {
				return ProtocolViolation(backendplugin.ErrAccountingEvidenceV2Unsupported)
			}
			s.economicMu.Lock()
			err := s.economicEvidence.append(*frame.AccountingV2)
			s.economicMu.Unlock()
			if err != nil {
				return ProtocolViolation(err)
			}
			return nil
		}
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

// DrainAccountingEvidenceV2 returns host-only canonical observations and never
// publishes them on the client-visible event stream.
func (s *managedStream) DrainAccountingEvidenceV2() []backendplugin.AccountingEvidenceV2 {
	if s == nil {
		return nil
	}
	s.economicMu.Lock()
	defer s.economicMu.Unlock()
	return s.economicEvidence.drain()
}

// DrainEconomicEvidence is the neutral naming alias used by shared connector
// support. Both methods drain the same buffer and therefore cannot duplicate a
// provider charge.
func (s *managedStream) DrainEconomicEvidence() []backendplugin.AccountingEvidenceV2 {
	return s.DrainAccountingEvidenceV2()
}

// DrainEconomicEvidenceRecords is the internal core projection. It retains
// the transport coverage disposition while handing the canonical observation
// to runtime without importing the backend-plugin wire package into core.
func (s *managedStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	evidence := s.DrainAccountingEvidenceV2()
	if len(evidence) == 0 {
		return nil
	}
	out := make([]execbackend.EconomicEvidence, len(evidence))
	for i, item := range evidence {
		out[i] = execbackend.EconomicEvidence{
			Observation:    item.Observation.Clone(),
			Coverage:       string(item.Coverage),
			CoverageReason: item.CoverageReason,
		}
	}
	return out
}

// DrainEconomicObservations is the core-facing projection of the validated
// host-only V2 sideband. Coverage is a transport disposition and the
// canonical observation remains the durable economic payload.
func (s *managedStream) DrainEconomicObservations() []metering.Observation {
	evidence := s.DrainAccountingEvidenceV2()
	if len(evidence) == 0 {
		return nil
	}
	out := make([]metering.Observation, 0, len(evidence))
	for _, item := range evidence {
		out = append(out, item.Observation.Clone())
	}
	return out
}

var _ backendplugin.AccountingEvidenceV2Source = (*managedStream)(nil)
var _ backendplugin.EconomicEvidenceSource = (*managedStream)(nil)
var _ execbackend.EconomicEvidenceSource = (*managedStream)(nil)
var _ metering.ObservationSource = (*managedStream)(nil)

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
