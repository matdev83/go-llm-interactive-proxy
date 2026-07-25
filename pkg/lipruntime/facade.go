package lipruntime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// HostCapabilities is the public host capability snapshot.
type HostCapabilities = controlplane.HostCapabilities

func (r *Runtime) api() hostAPI {
	if r == nil {
		return nil
	}
	return r.host
}

func (r *Runtime) ExecutorView() lipsdk.ExecutorView {
	if h := r.api(); h != nil {
		return h.ExecutorView()
	}
	return nil
}

func (r *Runtime) Ready() bool { h := r.api(); return h != nil && h.Ready() }

func (r *Runtime) Capabilities() HostCapabilities {
	if h := r.api(); h != nil {
		return h.Capabilities()
	}
	return HostCapabilities{ExecutableState: controlplane.CapabilityDisabled}
}

func (r *Runtime) MeteringQuerier() metering.Querier {
	if h := r.api(); h != nil {
		return h.MeteringQuerier()
	}
	return nil
}

func (r *Runtime) ReadinessReport() controlplane.ReadinessReportReader {
	if h := r.api(); h != nil {
		return h.ReadinessReport()
	}
	return nil
}

func (r *Runtime) RefreshSnapshots(ctx context.Context) error {
	if h := r.api(); h != nil {
		return h.RefreshSnapshots(ctx)
	}
	return fmt.Errorf("lipruntime: snapshot refresh not available")
}
