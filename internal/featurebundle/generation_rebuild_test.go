package featurebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

type genOpener struct{ id string }

func (g genOpener) ID() string { return g.id }
func (genOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

// TestMergeFeatureSurface_GenerationRebuildIsolated proves candidate feature
// merges do not share slice backing arrays across generations.
func TestMergeFeatureSurface_GenerationRebuildIsolated(t *testing.T) {
	t.Parallel()
	a := featurebundle.MergeBundles(lipfeature.FeatureBundle{
		SchemaVersion:  lipfeature.SchemaVersionV1,
		SessionOpeners: []session.Opener{genOpener{id: "gen-a"}},
	})
	b := featurebundle.MergeBundles(lipfeature.FeatureBundle{
		SchemaVersion:  lipfeature.SchemaVersionV1,
		SessionOpeners: []session.Opener{genOpener{id: "gen-b"}},
	})
	if len(a.SessionOpeners) != 1 || a.SessionOpeners[0].ID() != "gen-a" {
		t.Fatalf("A openers=%v", a.SessionOpeners)
	}
	if len(b.SessionOpeners) != 1 || b.SessionOpeners[0].ID() != "gen-b" {
		t.Fatalf("B openers=%v", b.SessionOpeners)
	}
	// Mutating one merged surface must not affect the other candidate.
	a.SessionOpeners = append(a.SessionOpeners, genOpener{id: "mutated"})
	if len(b.SessionOpeners) != 1 {
		t.Fatalf("B leaked A mutation: %d", len(b.SessionOpeners))
	}
}
