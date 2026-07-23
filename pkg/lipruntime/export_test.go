package lipruntime

import (
	"context"

	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// ReloadQueryForTest is the coordinator/query seam shape used by external facade tests.
type ReloadQueryForTest interface {
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
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
