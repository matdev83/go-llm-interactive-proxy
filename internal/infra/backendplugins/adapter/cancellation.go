package adapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

const (
	// defaultCancelGrace is the fallback grace timeout when CancelTimeout is not configured for negotiated cancellation.
	defaultCancelGrace = 2 * time.Second

	// legacyCancelWaitFallback is the fallback wait duration for legacy streams without negotiated cancellation.
	legacyCancelWaitFallback = 500 * time.Millisecond
)

type cancelState struct {
	mu           sync.Mutex
	requested    bool
	cause        lipapi.CancelCause
	deadline     time.Time
	outcomeSeen  bool
	acknowledged bool
	mode         backendplugin.CancelMode
	reason       backendplugin.CancelReason
	detail       string
	forced       bool
}

func (s *cancelState) request(cause lipapi.CancelCause, deadline time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := !s.requested
	s.requested = true
	s.cause = cause
	if first || s.deadline.IsZero() || (!deadline.IsZero() && deadline.Before(s.deadline)) {
		s.deadline = deadline
	}
	return first
}

func (s *cancelState) observeOutcome(o *backendplugin.CancelOutcome) {
	if o == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomeSeen = true
	s.acknowledged = o.Acknowledged
	s.mode = o.Mode
	s.reason = o.Reason
	s.detail = o.Detail
}

func (s *cancelState) markForced() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forced = true
}

func (s *cancelState) snapshot() CancellationProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return CancellationProgress{
		Requested:           s.requested,
		Cause:               s.cause,
		EffectiveDeadline:   s.deadline,
		OutcomeSeen:         s.outcomeSeen,
		OutcomeAcknowledged: s.acknowledged,
		OutcomeMode:         s.mode,
		OutcomeReason:       s.reason,
		OutcomeDetail:       s.detail,
		ForcedAbort:         s.forced,
	}
}

func (s *cancelState) isOutcomeSeen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcomeSeen
}

func (s *cancelState) forcedAbort() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forced
}

func (s *cancelState) interrupted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested || s.forced
}

func computeCancelDeadline(ctx context.Context, cancelTimeout time.Duration) (time.Time, int64) {
	if cancelTimeout <= 0 {
		cancelTimeout = defaultCancelGrace
	}
	effectiveDeadline := time.Now().Add(cancelTimeout)

	if ctxDeadline, ok := ctx.Deadline(); ok {
		if ctxDeadline.Before(effectiveDeadline) {
			effectiveDeadline = ctxDeadline
		}
	}

	return effectiveDeadline, effectiveDeadline.UnixMilli()
}

func outcomeCancelResult(prog CancellationProgress) lipapi.CancelResult {
	mode := prog.OutcomeMode
	switch mode {
	case backendplugin.CancelModeProvider,
		backendplugin.CancelModeTransport,
		backendplugin.CancelModeCloseOnly,
		backendplugin.CancelModeNone:
	default:
		mode = lipapi.CancelModeNone
	}
	res := lipapi.CancelResult{Mode: mode}
	if !prog.OutcomeAcknowledged {
		if prog.OutcomeDetail != "" {
			res.Err = fmt.Errorf("backend plugin cancel failed: %s", prog.OutcomeDetail)
		} else {
			res.Err = errors.New("backend plugin cancel failed")
		}
	}
	return res
}
