package openrouter

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"

var _ leglifecycle.BLegAttempt = (*chatStream)(nil)
var _ leglifecycle.BLegAttempt = (*responsesStream)(nil)
