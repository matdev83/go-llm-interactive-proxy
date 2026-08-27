package featurebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
)

type genFilter struct{ id string }

func (g genFilter) ID() string                      { return g.id }
func (genFilter) Order() int                        { return 0 }
func (genFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (genFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

// TestMergeFeatureSurface_GenerationRebuildIsolated proves candidate feature
// merges do not share slice backing arrays across generations.
func TestMergeFeatureSurface_GenerationRebuildIsolated(t *testing.T) {
	t.Parallel()
	a := featurebundle.MergeBundles(lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{genFilter{id: "gen-a"}},
	})
	b := featurebundle.MergeBundles(lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{genFilter{id: "gen-b"}},
	})
	if len(a.ToolCatalogFilters) != 1 || a.ToolCatalogFilters[0].ID() != "gen-a" {
		t.Fatalf("A filters=%v", a.ToolCatalogFilters)
	}
	if len(b.ToolCatalogFilters) != 1 || b.ToolCatalogFilters[0].ID() != "gen-b" {
		t.Fatalf("B filters=%v", b.ToolCatalogFilters)
	}
	// Mutating one merged surface must not affect the other candidate.
	a.ToolCatalogFilters = append(a.ToolCatalogFilters, genFilter{id: "mutated"})
	if len(b.ToolCatalogFilters) != 1 {
		t.Fatalf("B leaked A mutation: %d", len(b.ToolCatalogFilters))
	}
}
