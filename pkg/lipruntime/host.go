package lipruntime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// hostAPI is Runtime's sole private host-facing dependency (req 10.1-10.4).
type hostAPI interface {
	ExecutorView() lipsdk.ExecutorView
	Ready() bool
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
	Close(ctx context.Context) error
}

// bundleHost is the only private adapter that may retain [*runtimebundle.Host].
// It delegates queries and Host.Close; it must not call Manager/Process/Coordinator
// shutdown primitives or store them as callbacks.
type bundleHost struct{ h *runtimebundle.Host }

func adaptHost(ctx context.Context, h *runtimebundle.Host) (hostAPI, error) {
	if h == nil || h.Manager == nil || h.Process == nil || h.Executor == nil {
		if h != nil {
			_ = h.Close(context.WithoutCancel(ctx))
		}
		return nil, fmt.Errorf("lipruntime: BuildHost returned incomplete host")
	}
	return bundleHost{h: h}, nil
}

func (b bundleHost) ExecutorView() lipsdk.ExecutorView {
	if b.h == nil {
		return nil
	}
	return b.h.Executor
}
func (b bundleHost) Ready() bool { return b.h.Ready() }
func (b bundleHost) Reload(c context.Context, t sdkreload.Trigger) sdkreload.Result {
	return b.h.Reload(c, t)
}
func (b bundleHost) Status() sdkreload.Status { return b.h.Status() }
func (b bundleHost) Close(ctx context.Context) error {
	if b.h == nil {
		return nil
	}
	return b.h.Close(ctx)
}
