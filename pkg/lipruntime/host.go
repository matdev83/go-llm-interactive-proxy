package lipruntime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type hostAPI interface {
	ExecutorView() lipsdk.ExecutorView
	Ready() bool
	Capabilities() HostCapabilities
	MeteringQuerier() metering.Querier
	ReadinessReport() controlplane.ReadinessReportReader
	RefreshSnapshots(context.Context) error
	Reload(context.Context, sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
	Close(context.Context) error
}

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
func (b bundleHost) Ready() bool                       { return b.h.Ready() }
func (b bundleHost) Capabilities() HostCapabilities    { return b.h.Capabilities() }
func (b bundleHost) MeteringQuerier() metering.Querier { return b.h.MeteringQuerier() }
func (b bundleHost) ReadinessReport() controlplane.ReadinessReportReader {
	return b.h.ReadinessReport()
}
func (b bundleHost) RefreshSnapshots(ctx context.Context) error {
	if b.h == nil {
		return fmt.Errorf("lipruntime: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("lipruntime: nil context")
	}
	return b.h.RefreshSnapshots(ctx)
}
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
