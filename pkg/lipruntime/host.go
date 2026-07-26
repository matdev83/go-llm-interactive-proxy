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

// hostAPI is the unexported host seam satisfied by bundleHost.
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

// bundleHost adapts a ready runtimebundle.Host; h is non-nil after adaptHost.
type bundleHost struct{ h *runtimebundle.Host }

// adaptHost validates readiness and closes partial hosts on failure.
func adaptHost(ctx context.Context, h *runtimebundle.Host) (hostAPI, error) {
	if h == nil || !h.Ready() || h.ExecutorView() == nil {
		if h != nil {
			_ = h.Close(context.WithoutCancel(ctx))
		}
		return nil, fmt.Errorf("lipruntime: BuildHost returned incomplete host")
	}
	return bundleHost{h: h}, nil
}

// bundleHost delegates executor, capability, and readiness queries to the bundle.
func (b bundleHost) ExecutorView() lipsdk.ExecutorView { return b.h.ExecutorView() }
func (b bundleHost) Ready() bool                       { return b.h.Ready() }
func (b bundleHost) Capabilities() HostCapabilities    { return b.h.Capabilities() }
func (b bundleHost) MeteringQuerier() metering.Querier { return b.h.MeteringQuerier() }
func (b bundleHost) ReadinessReport() controlplane.ReadinessReportReader {
	return b.h.ReadinessReport()
}

// RefreshSnapshots requires a bound host and non-nil context.
func (b bundleHost) RefreshSnapshots(ctx context.Context) error {
	if b.h == nil {
		return fmt.Errorf("lipruntime: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("lipruntime: nil context")
	}
	return b.h.RefreshSnapshots(ctx)
}

// Reload and Status forward to the bound bundle coordinator seam.
func (b bundleHost) Reload(c context.Context, t sdkreload.Trigger) sdkreload.Result {
	return b.h.Reload(c, t)
}
func (b bundleHost) Status() sdkreload.Status { return b.h.Status() }

// Close forwards shutdown to the underlying runtimebundle host.
func (b bundleHost) Close(ctx context.Context) error { return b.h.Close(ctx) }
