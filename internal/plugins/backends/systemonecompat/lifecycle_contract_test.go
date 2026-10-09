package systemonecompat

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Successful fixed streams and failed-attempt evidence streams both expose the
// core-owned leglifecycle.BLegAttempt contract; neither owns a second lifecycle.
var (
	_ leglifecycle.BLegAttempt = (*lipapi.FixedEventStream)(nil)
	_ leglifecycle.BLegAttempt = (*failureUsageStream)(nil)
)
