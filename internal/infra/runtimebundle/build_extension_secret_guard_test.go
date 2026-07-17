package runtimebundle

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestBuildRuntimeSnapshot_SecretGuardsCloneSortedAndIsolated(t *testing.T) {
	t.Parallel()
	guards := []secretguard.Guard{
		stubSecretGuard{id: "z", ord: 2},
		stubSecretGuard{id: "a", ord: 1},
		stubSecretGuard{id: "b", ord: 1},
	}
	opts := &BuildOptions{
		Extensions: ExtensionsOptions{SecretGuards: guards},
	}
	bus := hooks.New(hooks.Config{})
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{Guards: guards})
	got := snap.SecretGuardPlane().Guards
	if len(got) != 3 {
		t.Fatalf("SecretGuardPlane guards len=%d want 3", len(got))
	}
	wantIDs := []string{"a", "b", "z"}
	for i, id := range wantIDs {
		if got[i].ID() != id {
			t.Fatalf("idx %d want %q got %q (full %#v)", i, id, got[i].ID(), got)
		}
	}
	guards[0] = stubSecretGuard{id: "mutated", ord: 0}
	again := snap.SecretGuardPlane().Guards
	if again[0].ID() != "a" {
		t.Fatalf("snapshot mutated via caller slice; got %q", again[0].ID())
	}
}
