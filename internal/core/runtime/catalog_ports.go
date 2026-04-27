package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// CatalogResolver merges administrator overrides, catalog facts, and backend capability maps
// for one routing candidate. Defined here (consumer package) per ports-and-adapters guidance.
type CatalogResolver interface {
	Resolve(
		ctx context.Context,
		candidate routing.AttemptCandidate,
		call lipapi.Call,
		backend lipapi.BackendCaps,
	) modelcatalog.EffectiveFacts
}

// EligibilityResolver runs context-limit checks after successful capability negotiation.
type EligibilityResolver interface {
	Check(
		ctx context.Context,
		candidate routing.AttemptCandidate,
		call lipapi.Call,
		facts modelcatalog.EffectiveFacts,
	) modelcatalog.EligibilityDecision
}

var (
	_ CatalogResolver     = (*modelcatalog.CatalogResolverImpl)(nil)
	_ EligibilityResolver = (*modelcatalog.EligibilityResolverImpl)(nil)
)
