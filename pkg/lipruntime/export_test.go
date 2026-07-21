package lipruntime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// ReloadQueryForTest is the coordinator/query seam shape used by external facade tests.
type ReloadQueryForTest interface {
	Reload(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult
	Status() configreload.ReloadStatus
}

// NewReloadControlForTest constructs a public ReloadControl over a test query seam.
// Production composition binds *runtimehost.Coordinator through unexported helpers.
func NewReloadControlForTest(q ReloadQueryForTest) *ReloadControl {
	return newReloadControl(q)
}

// BindReloadQueryForTest attaches a coordinator/query seam to Runtime for tests.
func BindReloadQueryForTest(r *Runtime, q ReloadQueryForTest) {
	if r == nil {
		return
	}
	r.bindReloadQuery(q)
}
