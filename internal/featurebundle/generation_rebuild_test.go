package featurebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

type genSubmitHook struct{ id string }

func (h genSubmitHook) ID() string                      { return h.id }
func (genSubmitHook) Order() int                        { return 0 }
func (genSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (genSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

// TestMergeFeatureSurface_GenerationRebuildIsolated proves candidate feature
// merges do not share hook slice backing arrays across generations.
func TestMergeFeatureSurface_GenerationRebuildIsolated(t *testing.T) {
	t.Parallel()
	a := featurebundle.MergeBundles(lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{genSubmitHook{id: "gen-a"}},
	})
	b := featurebundle.MergeBundles(lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{genSubmitHook{id: "gen-b"}},
	})
	if len(a.SubmitHooks) != 1 || a.SubmitHooks[0].ID() != "gen-a" {
		t.Fatalf("A hooks=%v", a.SubmitHooks)
	}
	if len(b.SubmitHooks) != 1 || b.SubmitHooks[0].ID() != "gen-b" {
		t.Fatalf("B hooks=%v", b.SubmitHooks)
	}
	// Mutating one merged surface must not affect the other candidate.
	a.SubmitHooks = append(a.SubmitHooks, genSubmitHook{id: "mutated"})
	if len(b.SubmitHooks) != 1 {
		t.Fatalf("B leaked A mutation: %d", len(b.SubmitHooks))
	}
}
