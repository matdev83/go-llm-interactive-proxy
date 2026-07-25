package runtimebundle

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func (h *ReloadHost) Ready() bool {
	return h != nil && h.Manager != nil && h.Executor != nil && h.Manager.Active() != nil
}

func (h *ReloadHost) Capabilities() controlplane.HostCapabilities {
	if h == nil {
		return controlplane.HostCapabilities{ExecutableState: controlplane.CapabilityDisabled}
	}
	ex, prod := h.activeExecutor(), h.productionOptions()
	caps := controlplane.HostCapabilities{
		ProductionMetering: ex != nil && ex.MeteringRecorder != nil, TrafficObservers: len(prod.TrafficObservers) > 0,
		UsageObservers: len(prod.UsageObservers) > 0, ProductionEvidenceSink: prod.EvidenceSink != nil,
		ProductionRater: ex != nil && ex.EconomicsRater != nil, ProductionMeteringQuerier: h.MeteringQuerier() != nil,
		ExecutableState: controlplane.CapabilityDisabled,
	}
	if cur := h.currentSnapshot(); cur != nil {
		caps.SnapshotGenerationID, caps.SnapshotUsageVersion = cur.ID, cur.Usage.Version
	}
	if exec := h.currentExecutable(); exec != nil {
		caps.ExecutableGenerationID, caps.ExecutableVersion = exec.ID, exec.Version
		caps.ExecutableEvidenceObjectID = exec.EvidenceObjectID()
		switch exec.State {
		case economics.SnapshotReady, economics.SnapshotStale, "":
			caps.ExecutableState = controlplane.CapabilityReady
		case economics.SnapshotDegraded:
			caps.ExecutableState = controlplane.CapabilityDegraded
		case economics.SnapshotUnavailable:
			caps.ExecutableState = controlplane.CapabilityUnavailable
		case economics.SnapshotDisabled:
			caps.ExecutableState = controlplane.CapabilityDisabled
		default:
			caps.ExecutableState = controlplane.CapabilityUnavailable
		}
	}
	return caps
}

func (h *ReloadHost) MeteringQuerier() metering.Querier {
	if h == nil || h.Process == nil {
		return nil
	}
	return h.Process.MeteringQuerier
}

func (h *ReloadHost) ReadinessReport() controlplane.ReadinessReportReader {
	if h == nil || h.Manager == nil {
		return nil
	}
	if g := h.Manager.Active(); g != nil {
		type readinessProvider interface {
			ReadinessReport() controlplane.ReadinessReportReader
		}
		if p, ok := g.RequestPlane().(readinessProvider); ok {
			return p.ReadinessReport()
		}
	}
	return nil
}

func (h *ReloadHost) RefreshSnapshots(ctx context.Context) error {
	if h == nil || h.Process == nil || h.Process.SnapshotController == nil {
		return fmt.Errorf("runtimebundle: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	return h.Process.SnapshotController.Refresh(ctx)
}

func (h *ReloadHost) productionOptions() ProductionOptions {
	if h == nil || h.Process == nil || h.Process.opts == nil {
		return ProductionOptions{}
	}
	return h.Process.opts.Production
}

func (h *ReloadHost) activeExecutor() *runtime.Executor {
	if h == nil || h.Manager == nil {
		return nil
	}
	if g := h.Manager.Active(); g != nil {
		if p, ok := g.RequestPlane().(runtimehost.ExecutorProvider); ok && p != nil {
			ex, _ := p.ExecutorView().(*runtime.Executor)
			return ex
		}
	}
	return nil
}

func (h *ReloadHost) currentSnapshot() *snapshotgen.RuntimeGeneration {
	if h != nil && h.Process != nil && h.Process.SnapshotGeneration != nil {
		return h.Process.SnapshotGeneration.Current()
	}
	return nil
}
func (h *ReloadHost) currentExecutable() *snapshotgen.ExecutableGeneration {
	if h != nil && h.Process != nil && h.Process.SnapshotGeneration != nil {
		return h.Process.SnapshotGeneration.CurrentExecutable()
	}
	return nil
}
