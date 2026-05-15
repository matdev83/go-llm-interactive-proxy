package localstub

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	_ leglifecycle.BLegAttempt = (*lipapi.FixedEventStream)(nil)
	_ leglifecycle.BLegAttempt = (*errorAfterPrefixStream)(nil)
)
