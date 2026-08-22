package runtime

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// Test-only compatibility shims for deleted R1 methods.
// Production code no longer contains these 5 shims; they remain here
// solely so existing tests that call them continue to compile and
// verify the same TerminalizeAttempt delegation. New tests must call
// TerminalizeAttempt directly.

func (a *attemptSession) AbortBeforeReturn(ctx context.Context, cause error) error {
	if a == nil {
		return cause
	}
	if cause == nil {
		cause = errors.New("runtime: attempt aborted before return")
	}
	outcome := billing.LegOutcomeFailed
	if errors.Is(cause, context.Canceled) {
		outcome = billing.LegOutcomeCanceled
	}
	evidence := attemptEvidence{
		Command:      sdkterminal.CommandBackendOpenFailure,
		LegOutcome:   outcome,
		Usage:        emptyOperatorUsageShell(),
		Err:          cause,
		RecordReason: cause.Error(),
	}
	a.TerminalizeAttempt(ctx, IntentPreReturnAbort, evidence)
	return cause
}

func (a *attemptSession) finishAsReplaced(ctx context.Context) {
	if a != nil && a.finalStreamObs != nil {
		a.finalStreamObs.Finish(ctx, response.OutcomeReplaced)
	}
}

func (tx *attemptTx) Rollback(ctx context.Context, cmd sdkterminal.Command, releaseKind authorityapp.ReleaseKind, outcome billing.LegOutcome, usage lipapi.Event) {
	if tx == nil || tx.completed {
		return
	}
	var err error
	if ctx != nil {
		err = ctx.Err()
	}
	evidence := attemptEvidence{
		Command:     cmd,
		ReleaseKind: releaseKind,
		LegOutcome:  outcome,
		Usage:       usage,
		Err:         err,
		TraceID:     tx.reqFacts.traceID,
		ALegID:      tx.reqFacts.aLegID,
		StartedAt:   tx.openStartedAt,
	}
	tx.rollback(ctx, cmd, evidence)
}

func (tx *attemptTx) RollbackParallelLoser(ctx context.Context, usage lipapi.Event, recvErr error) {
	if tx == nil || tx.completed {
		return
	}
	outcome := lipapi.AttemptCancelled
	reason := "parallel race loser"
	detailErr := error(context.Canceled)
	if recvErr != nil && !errors.Is(recvErr, context.Canceled) && !errors.Is(recvErr, context.DeadlineExceeded) {
		outcome = lipapi.AttemptSwallowedFailure
		reason = attemptReasonDetail(recvErr)
		detailErr = recvErr
	}
	evidence := attemptEvidence{
		Command:       sdkterminal.CommandParallelLoser,
		ReleaseKind:   authorityapp.ReleaseKindLosing,
		LegOutcome:    billing.LegOutcomeFailed,
		Usage:         usage,
		Err:           detailErr,
		RecordOutcome: outcome,
		RecordReason:  reason,
		TraceID:       tx.reqFacts.traceID,
		ALegID:        tx.reqFacts.aLegID,
		StartedAt:     tx.openStartedAt,
	}
	tx.rollback(ctx, sdkterminal.CommandParallelLoser, evidence)
}

func (tx *attemptTx) Abort(ctx context.Context, cmd sdkterminal.Command, releaseKind authorityapp.ReleaseKind, outcome billing.LegOutcome, usage lipapi.Event) {
	tx.Rollback(ctx, cmd, releaseKind, outcome, usage)
}
