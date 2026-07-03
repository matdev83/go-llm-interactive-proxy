package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
)

// viewsFor returns authoritative request views from ctx, falling back to the
// stream snapshot when the current recv context does not carry execctx views.
func (s *retryRecvStream) viewsFor(ctx context.Context) (execctx.Views, bool) {
	if v, ok := execctx.FromContext(ctx); ok {
		return v, true
	}
	if s != nil && s.recvViewsOK {
		return s.recvViews, true
	}
	return execctx.Views{}, false
}

// withDecisionEvidence attaches the policy decision evidence seam and stream
// evidence callbacks so stream-stage runners project and emit per-provider
// evidence themselves (requirements 3.3, 3.4, 4.2, 9.1). Runtime only
// establishes the seam; it does not emit aggregate runtime evidence.
func (s *retryRecvStream) withDecisionEvidence(ctx context.Context) context.Context {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil {
		return ctx
	}
	snap := s.executor.RuntimeSnapshot
	emitter := s.executor.policyEvidenceEmitter(snap)
	// Attach the seam whenever a snapshot is present so non-default timeout budgets
	// are enforced on stream stages even without a policy observer. Emitter stays
	// nil for the no-op observer default so no evidence/logs are produced.
	ev := &extensions.DecisionEvidence{
		Emitter:               emitter,
		TimeoutBudget:         snap.TimeoutBudgetSource(),
		TimeoutGuard:          snap.ProviderTimeoutGuard(),
		OutputCommittedSource: s.isCommitted,
	}
	ctx = extensions.WithDecisionEvidence(ctx, ev)
	ctx = hooks.WithToolReactorEvidence(ctx, extensions.NewToolReactorEvidenceFunc(ev))
	ctx = extensions.WithAttemptEvidence(ctx, extensions.NewAttemptEvidenceFunc(ev))
	return ctx
}
