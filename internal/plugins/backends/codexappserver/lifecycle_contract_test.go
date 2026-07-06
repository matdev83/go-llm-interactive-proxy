package codexappserver

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"

var (
	_ leglifecycle.BLegAttempt = (*codexStream)(nil)
	_ leglifecycle.BLegAttempt = (*codexManagedStream)(nil)
)
