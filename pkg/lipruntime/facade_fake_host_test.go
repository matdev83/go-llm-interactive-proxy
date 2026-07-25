package lipruntime_test

import (
	"context"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// facadeFakeHost is a public-boundary host double for Runtime facade tests.
type facadeFakeHost struct {
	view       lipsdk.ExecutorView
	ready      bool
	closeErrs  []error
	closeCalls atomic.Int32
	reload     func(context.Context, sdkreload.Trigger) sdkreload.Result
	status     sdkreload.Status
}

func (f *facadeFakeHost) ExecutorView() lipsdk.ExecutorView { return f.view }
func (f *facadeFakeHost) Ready() bool                       { return f.ready }
func (f *facadeFakeHost) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if f.reload != nil {
		return f.reload(ctx, trigger)
	}
	return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
}
func (f *facadeFakeHost) Status() sdkreload.Status { return f.status }
func (f *facadeFakeHost) Close(context.Context) error {
	n := int(f.closeCalls.Add(1))
	if n-1 < len(f.closeErrs) {
		return f.closeErrs[n-1]
	}
	return nil
}
