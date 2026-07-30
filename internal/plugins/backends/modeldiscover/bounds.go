package modeldiscover

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// MaxInventoryModels bounds remote and static inventory payloads.
const MaxInventoryModels = 10_000

// BoundInventoryModels truncates model rows to MaxInventoryModels and returns warnings.
func BoundInventoryModels(models []modelinventory.Model) ([]modelinventory.Model, []string) {
	return boundInventoryModels(models)
}

func boundInventoryModels(models []modelinventory.Model) ([]modelinventory.Model, []string) {
	if len(models) <= MaxInventoryModels {
		return models, nil
	}
	truncated := make([]modelinventory.Model, MaxInventoryModels)
	copy(truncated, models[:MaxInventoryModels])
	warn := fmt.Sprintf("inventory truncated to %d models", MaxInventoryModels)
	return truncated, []string{warn}
}
