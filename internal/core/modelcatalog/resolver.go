package modelcatalog

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EffectiveFacts is the source-aware capability surface for one candidate (design §CatalogResolver).
type EffectiveFacts struct {
	Facts         ModelFacts
	BackendCaps   lipapi.BackendCaps
	EffectiveCaps lipapi.BackendCaps
	Matched       bool
	Match         MatchResult
	Snapshot      SnapshotRef
}

// CatalogResolverImpl combines overrides, catalog matching, and backend capability intersection.
// Wire it into [github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime.Executor.CatalogResolver].
type CatalogResolverImpl struct {
	matcher        Matcher
	overrides      OverrideResolver
	catalogEnabled bool
	active         ActiveSnapshotProvider
}

// NewCatalogResolver builds a resolver. When catalogEnabled is false, catalog matching is skipped and
// effective capabilities mirror backend declarations. active may be nil (no snapshot) or supply a
// nil index when no valid local catalog is published (Req 1.4).
func NewCatalogResolver(
	m Matcher,
	ovr OverrideResolver,
	catalogEnabled bool,
	active ActiveSnapshotProvider,
) *CatalogResolverImpl {
	if m == nil {
		m = DefaultMatcher{}
	}
	if ovr == nil {
		ovr = NewOverrideResolver(OverrideSet{})
	}
	return &CatalogResolverImpl{
		matcher:        m,
		overrides:      ovr,
		catalogEnabled: catalogEnabled,
		active:         active,
	}
}

// Resolve implements the executor catalog merge contract.
func (c *CatalogResolverImpl) Resolve(
	ctx context.Context,
	candidate routing.AttemptCandidate,
	call lipapi.Call,
	backend lipapi.BackendCaps,
) EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(candidate.Primary.Model)
	be := CloneBackendCaps(backend)

	var idx *SnapshotIndex
	var snapRef SnapshotRef
	if c.active != nil {
		idx, snapRef = c.active.ActiveIndex()
	}

	if !c.catalogEnabled {
		return EffectiveFacts{
			Facts:         ModelFacts{Source: FactSourceBackendDeclaration, MatchKind: MatchNone},
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       false,
			Match:         MatchResult{Kind: MatchNone, InputModel: input},
			Snapshot:      snapRef,
		}
	}

	if f, ok := c.overrides.Resolve(candidate); ok {
		return EffectiveFacts{
			Facts:         f,
			BackendCaps:   backend,
			EffectiveCaps: intersectModelFactsWithBackend(f, be),
			Matched:       true,
			Match:         MatchResult{Kind: MatchNone, InputModel: input},
			Snapshot:      snapRef,
		}
	}

	mr := c.matcher.Match(candidate, idx)
	if mr.Kind == MatchAmbiguous || mr.Kind == MatchNoMatch {
		facts := ModelFacts{Source: FactSourceBackendDeclaration, MatchKind: mr.Kind}
		return EffectiveFacts{
			Facts:         facts,
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       false,
			Match:         mr,
			Snapshot:      snapRef,
		}
	}

	catFacts, ok := idx.FactsByCatalogModelID(mr.MatchedID)
	if !ok {
		return EffectiveFacts{
			Facts:         ModelFacts{Source: FactSourceBackendDeclaration, MatchKind: MatchNoMatch},
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       false,
			Match:         MatchResult{Kind: MatchNoMatch, InputModel: input},
			Snapshot:      snapRef,
		}
	}

	out := catFacts
	out.Source = FactSourceCatalog
	out.MatchKind = mr.Kind
	return EffectiveFacts{
		Facts:         out,
		BackendCaps:   backend,
		EffectiveCaps: intersectModelFactsWithBackend(out, be),
		Matched:       true,
		Match:         mr,
		Snapshot:      snapRef,
	}
}

func intersectModelFactsWithBackend(facts ModelFacts, backend lipapi.BackendCaps) lipapi.BackendCaps {
	out := CloneBackendCaps(backend)
	if out == nil {
		out = lipapi.BackendCaps{}
	}
	if facts.Tools == CapabilityUnsupported {
		delete(out, lipapi.CapabilityTools)
	}
	if facts.StructuredOutputs == CapabilityUnsupported {
		delete(out, lipapi.CapabilityStructuredOutputs)
	}
	if facts.Reasoning == CapabilityUnsupported {
		delete(out, lipapi.CapabilityReasoning)
	}
	if facts.Vision == CapabilityUnsupported {
		delete(out, lipapi.CapabilityVision)
	}
	if facts.Documents == CapabilityUnsupported {
		delete(out, lipapi.CapabilityDocuments)
	}
	return out
}
