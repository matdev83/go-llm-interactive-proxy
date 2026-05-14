package acp

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"

var _ leglifecycle.BLegAttempt = (*promptStream)(nil)
