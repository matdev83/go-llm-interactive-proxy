package configreload

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// ReloadCoordinator is the narrow seam onto the runtimehost Coordinator.
// The management adapter never duplicates compile/prepare/publish logic.
type ReloadCoordinator interface {
	Reload(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult
	Status() configreload.ReloadStatus
	// FixedSourcePath is the HTTP-only capability for the fixed startup source.
	// It must not appear on the canonical Status contract.
	FixedSourcePath() string
}
