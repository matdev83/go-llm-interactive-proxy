package featurebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
)

type genFilter struct{ id string }

func (g genFilter) ID() string                      { return g.id }
func (genFilter) Order() int                        { return 0 }
func (genFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (genFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type genFinalizer struct{ id string }

func (g genFinalizer) ID() string { return g.id }
func (genFinalizer) Order() int   { return 0 }
func (genFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

type genCompactionObs struct{ tag string }

func (g genCompactionObs) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

// TestMergeFeatureSurface_GenerationRebuildIsolated proves candidate feature
// merges do not share slice backing arrays across generations.
func TestMergeFeatureSurface_GenerationRebuildIsolated(t *testing.T) {
	t.Parallel()
	genA, errA := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{genCompactionObs{tag: "gen-a"}},
		ToolCallFinalizers:  []toolcall.Finalizer{genFinalizer{id: "gen-a"}},
	})
	if errA != nil {
		t.Fatalf("genA error: %v", errA)
	}
	genB, errB := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{genCompactionObs{tag: "gen-b"}},
		ToolCallFinalizers:  []toolcall.Finalizer{genFinalizer{id: "gen-b"}},
	})
	if errB != nil {
		t.Fatalf("genB error: %v", errB)
	}
	fA := lipfeature.Get(genA.Frozen, lipfeature.PlaneToolCallFinalizers)
	fB := lipfeature.Get(genB.Frozen, lipfeature.PlaneToolCallFinalizers)
	if len(fA) != 1 || fA[0].ID() != "gen-a" {
		t.Fatalf("genA finalizers=%v", fA)
	}
	if len(fB) != 1 || fB[0].ID() != "gen-b" {
		t.Fatalf("genB finalizers=%v", fB)
	}
	oA := lipfeature.Get(genA.Frozen, lipfeature.PlaneCompactionObservers)
	oB := lipfeature.Get(genB.Frozen, lipfeature.PlaneCompactionObservers)
	if len(oA) != 1 {
		t.Fatalf("genA observers=%v", oA)
	}
	if len(oB) != 1 {
		t.Fatalf("genB observers=%v", oB)
	}
}
