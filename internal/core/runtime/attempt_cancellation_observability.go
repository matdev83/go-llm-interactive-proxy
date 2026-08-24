package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type cancellationTelemetrySource interface {
	CancellationOutcomeSeen() bool
	CancellationForcedAbort() bool
	CancellationHandshakeNegotiated() bool
}

// deriveAttemptCancellation classifies whether terminalization represents a
// cancellation and derives its canonical cause using the first applicable source.
func deriveAttemptCancellation(intent attemptTerminalIntent, evidence attemptEvidence, pendingCancelCause *lipapi.CancelCause, contextsCanceled bool) (bool, lipapi.CancelCause) {
	isCancel := intent == IntentCancellation || intent == IntentTimeout || intent == IntentParallelLoser ||
		(evidence.CancelCause != nil && evidence.CancelCause.Kind != "") ||
		(pendingCancelCause != nil && pendingCancelCause.Kind != "") ||
		evidence.RecordOutcome == lipapi.AttemptCancelled || contextsCanceled

	var cause lipapi.CancelCause
	if evidence.CancelCause != nil && evidence.CancelCause.Kind != "" {
		cause = *evidence.CancelCause
	} else if pendingCancelCause != nil && pendingCancelCause.Kind != "" {
		cause = *pendingCancelCause
	} else if intent == IntentParallelLoser {
		cause = lipapi.CancelCause{Kind: lipapi.CancelRaceLoser, Detail: "parallel race loser"}
	} else if intent == IntentTimeout {
		cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "timeout"}
	} else if intent == IntentCancellation {
		cause = lipapi.CancelCause{Kind: lipapi.CancelClientGone}
	} else if contextsCanceled {
		cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone}
	} else if evidence.Err != nil {
		cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: evidence.Err.Error()}
	} else if evidence.RecordReason != "" {
		cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: evidence.RecordReason}
	} else if isCancel {
		cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone}
	}
	return isCancel, cause
}

func (a *attemptSession) recordCancellationTelemetry(cause lipapi.CancelCause, res lipapi.CancelResult, innerStream lipapi.ManagedEventStream, requested bool) CancellationObservation {
	actualMode, fallback, outcomeSeen, forcedAbort := res.Mode, string(CancellationFallbackNone), false, false
	if actualMode == "" {
		actualMode = lipapi.CancelModeNone
	}
	if telSource, ok := innerStream.(cancellationTelemetrySource); ok {
		fallback, outcomeSeen, forcedAbort = string(CancellationFallbackLegacy), telSource.CancellationOutcomeSeen(), telSource.CancellationForcedAbort()
		if telSource.CancellationHandshakeNegotiated() {
			fallback = string(CancellationFallbackNegotiated)
		}
	}
	termObs := newCancellationObservation(cause.Kind, actualMode, string(CancellationPhaseTerminal), fallback)
	if requested {
		a.recordCancellation(newCancellationObservation(cause.Kind, lipapi.CancelModeNone, string(CancellationPhaseRequested), string(CancellationFallbackNone)))
		if outcomeSeen || actualMode == lipapi.CancelModeProvider {
			a.recordCancellation(newCancellationObservation(cause.Kind, actualMode, string(CancellationPhaseOutcome), fallback))
		}
		if forcedAbort || errors.Is(res.Err, context.DeadlineExceeded) {
			a.recordCancellation(newCancellationObservation(cause.Kind, actualMode, string(CancellationPhaseForced), fallback))
		}
		a.recordCancellation(termObs)
	}
	return termObs
}

func (a *attemptSession) logAttemptCanceled(cctx context.Context, evidence attemptEvidence, obs CancellationObservation) {
	if a == nil || a.finalStreamObs == nil || a.finalStreamObs.Log == nil {
		return
	}
	traceID, aLegID := strings.TrimSpace(evidence.TraceID), strings.TrimSpace(evidence.ALegID)
	if traceID == "" {
		traceID = strings.TrimSpace(a.traceID)
	}
	if aLegID == "" {
		aLegID = strings.TrimSpace(a.bleg.ALegID)
	}
	logCtx := diag.EnsureCallDiag(cctx, traceID, aLegID)
	diag.LogDecision(logCtx, a.finalStreamObs.Log, "attempt_canceled",
		diag.AttrOpts{CallID: traceID, BLegID: a.bleg.BLegID},
		slog.String("cause_class", string(obs.CauseClass)),
		slog.String("cancel_mode", string(obs.Mode)),
		slog.String("phase", string(CancellationPhaseTerminal)),
		slog.String("fallback", string(obs.Fallback)),
		slog.String("backend", a.cand.Primary.Backend),
		slog.String("model", a.cand.Primary.Model),
	)
}

func storedCancelResult(sess *attemptSession) (lipapi.CancelResult, bool) {
	if sess == nil {
		return lipapi.CancelResult{}, false
	}
	sess.innerMu.Lock()
	cr := sess.cancelResult
	sess.innerMu.Unlock()
	return cr, cr.Mode != ""
}
