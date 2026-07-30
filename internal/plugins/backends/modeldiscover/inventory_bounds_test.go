package modeldiscover_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestBoundInventoryModels_truncatesLargeRemotePayload(t *testing.T) {
	t.Parallel()
	models := make([]modelinventory.Model, modeldiscover.MaxInventoryModels+5)
	for i := range models {
		models[i] = modelinventory.Model{
			CanonicalID: "p/m",
			NativeID:    "m",
		}
	}
	bounded, warnings := modeldiscover.BoundInventoryModels(models)
	if len(bounded) != modeldiscover.MaxInventoryModels {
		t.Fatalf("bounded=%d", len(bounded))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "truncated") {
		t.Fatalf("warnings=%v", warnings)
	}
}
