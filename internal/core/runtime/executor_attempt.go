package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// attemptReasonMaxRunes caps persisted attempt lineage text (bounded diagnostic detail).
const attemptReasonMaxRunes = 512

// attemptReasonDetail returns truncated error text suitable for AttemptRecord.Reason persistence.
func attemptReasonDetail(err error) string {
	if err == nil {
		return ""
	}
	return diag.TruncErrDetail(err, attemptReasonMaxRunes)
}

// recordAttemptParams is the argument bundle for [Executor.recordAttempt].
type recordAttemptParams struct {
	ALegID  string
	BLeg    b2bua.BLegRecord
	Cand    routing.AttemptCandidate
	Outcome lipapi.AttemptOutcome
	Reason  string
	// DetailErr is the surfaced failure, when any, for secure-session outcome mapping.
	DetailErr error
}

func (e *Executor) recordAttempt(ctx context.Context, p recordAttemptParams) error {
	now := e.now()
	rec := lipapi.AttemptRecord{
		BLegID:         p.BLeg.BLegID,
		ALegID:         p.ALegID,
		Seq:            p.BLeg.Seq,
		BackendID:      p.Cand.Primary.Backend,
		EffectiveModel: p.Cand.Primary.Model,
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        p.Outcome,
		Reason:         p.Reason,
	}
	if sink, ok := e.CandidateHealth.(policy.RoutingAttemptOutcomeSink); ok {
		sink.OnRoutingAttemptOutcome(p.Cand.Key, p.Outcome)
	}
	return e.Store.RecordAttempt(context.WithoutCancel(ctx), rec)
}

// recordAttemptLogged runs [Executor.recordAttempt]; persistence failure is logged at debug only.
func (e *Executor) recordAttemptLogged(ctx context.Context, p recordAttemptParams, attrOpts diag.AttrOpts) {
	if e == nil {
		return
	}
	if e.Metrics != nil {
		e.Metrics.OnAttemptRecorded(p.Outcome, p.Cand.Primary.Backend)
	}
	if err := e.recordAttempt(ctx, p); err != nil && e.Log != nil {
		base := diag.Attrs(ctx, attrOpts)
		attrs := make([]slog.Attr, 0, len(base)+1)
		attrs = append(attrs, base...)
		attrs = append(attrs, slog.Any("error", err))
		e.Log.LogAttrs(ctx, slog.LevelDebug, "record_attempt_failed", attrs...)
	}
	if m := e.secureSessionForAttempt(); m != nil {
		st, ok := execctx.SecureSessionTurnFromContext(ctx)
		if !ok {
			return
		}
		out := secureAttemptOutcome(st, p.BLeg, p, e.now())
		if err := m.RecordAttemptOutcome(context.WithoutCancel(ctx), out); err != nil && e.Log != nil {
			base := diag.Attrs(ctx, attrOpts)
			attrs := make([]slog.Attr, 0, len(base)+1)
			attrs = append(attrs, base...)
			attrs = append(attrs, slog.Any("error", err))
			e.Log.LogAttrs(ctx, slog.LevelDebug, "secure_session_attempt_outcome_failed", attrs...)
		}
	}
}

func backendCallWithRouteParams(work lipapi.Call, cand routing.AttemptCandidate) (lipapi.Call, error) {
	merged, err := lipapi.MergeRouteQueryIntoGenerationOptions(work.Options, cand.Primary.Params)
	if err != nil {
		return lipapi.Call{}, fmt.Errorf("route generation options: %w", err)
	}
	work.Options = merged
	return work, nil
}
