package openresponsescompat

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// The Open seam returns [lipapi.NewFixedEventStream] streams for parsed
// non-streaming create responses and [*sseStream] streams for SSE create
// responses; both satisfy the leglifecycle.BLegAttempt contract that every
// official backend B-leg must expose.
var (
	_ leglifecycle.BLegAttempt = (*lipapi.FixedEventStream)(nil)
	_ leglifecycle.BLegAttempt = (*sseStream)(nil)
)
