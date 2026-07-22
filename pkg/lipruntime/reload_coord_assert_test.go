package lipruntime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

// Ensure the public facade seam matches the production coordinator (req 16.1).
var _ reloadQuery = (*runtimehost.Coordinator)(nil)
