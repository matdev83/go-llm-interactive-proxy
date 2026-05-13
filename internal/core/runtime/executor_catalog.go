package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (e *Executor) effectiveFactsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) modelcatalog.EffectiveFacts {
	base := e.backendCapsForAttempt(ctx, be, attempt, c)
	if e == nil || e.CatalogResolver == nil {
		return syntheticBackendOnlyFacts(base, c)
	}
	return e.CatalogResolver.Resolve(ctx, c, attempt, base)
}

func (e *Executor) backendCapsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) lipapi.BackendCaps {
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, c, attempt)
	}
	return execbackend.EffectiveCaps(ctx, be, attempt, c)
}

func syntheticBackendOnlyFacts(base lipapi.BackendCaps, c routing.AttemptCandidate) modelcatalog.EffectiveFacts {
	be := modelcatalog.CloneBackendCaps(base)
	input := strings.TrimSpace(c.Primary.Model)
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceBackendDeclaration,
			MatchKind: modelcatalog.MatchNone,
		},
		BackendCaps:   base,
		EffectiveCaps: be,
		Matched:       false,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchNone, InputModel: input},
	}
}
