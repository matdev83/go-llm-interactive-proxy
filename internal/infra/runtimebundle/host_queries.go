package runtimebundle

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Host query helpers expose generation, capability, and metering views.
func (h *Host) Ready() bool {
	return h != nil && h.manager != nil && h.executor != nil && h.manager.Active() != nil
}

func (h *Host) activeGen() *runtimehost.Generation {
	if h == nil || h.manager == nil {
		return nil
	}
	return h.manager.Active()
}

func (h *Host) ActiveGenerationID() int64 {
	if g := h.activeGen(); g != nil {
		return g.ID()
	}
	return 0
}

func (h *Host) ActivePublicFingerprint() string {
	if g := h.activeGen(); g != nil {
		return g.Status().Meta.PublicFingerprint
	}
	return ""
}
func (h *Host) ProcessClosed() bool { return h == nil || h.process == nil || h.process.Closed() }
func (h *Host) CanAcquireActive() bool {
	if h == nil || h.manager == nil {
		return false
	}
	lease, ok := h.manager.Acquire()
	if !ok {
		return false
	}
	lease.Release()
	return true
}

func (h *Host) StartALeg(id string) *leglifecycle.ALeg {
	if h == nil || h.process == nil || h.process.ALegLifecycle == nil {
		return nil
	}
	return h.process.ALegLifecycle.StartALeg(id)
}

func (h *Host) Capabilities() controlplane.HostCapabilities {
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

func (h *Host) MeteringQuerier() metering.Querier {
	if h == nil || h.process == nil {
		return nil
	}
	return h.process.MeteringQuerier
}

func (h *Host) ReadinessReport() controlplane.ReadinessReportReader {
	if h == nil || h.manager == nil {
		return nil
	}
	if g := h.manager.Active(); g != nil {
		type readinessProvider interface {
			ReadinessReport() controlplane.ReadinessReportReader
		}
		if p, ok := g.RequestPlane().(readinessProvider); ok {
			return p.ReadinessReport()
		}
	}
	return nil
}

func (h *Host) RefreshSnapshots(ctx context.Context) error {
	if h == nil || h.process == nil || h.process.SnapshotController == nil {
		return fmt.Errorf("runtimebundle: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	return h.process.SnapshotController.Refresh(ctx)
}

func (h *Host) productionOptions() ProductionOptions {
	if h == nil || h.process == nil || h.process.opts == nil {
		return ProductionOptions{}
	}
	return h.process.opts.Production
}

func (h *Host) activeExecutor() *runtime.Executor {
	if h == nil || h.manager == nil {
		return nil
	}
	if g := h.manager.Active(); g != nil {
		if p, ok := g.RequestPlane().(runtimehost.ExecutorProvider); ok && p != nil {
			ex, _ := p.ExecutorView().(*runtime.Executor)
			return ex
		}
	}
	return nil
}

func (h *Host) currentSnapshot() *snapshotgen.RuntimeGeneration {
	if h != nil && h.process != nil && h.process.SnapshotGeneration != nil {
		return h.process.SnapshotGeneration.Current()
	}
	return nil
}

func (h *Host) currentExecutable() *snapshotgen.ExecutableGeneration {
	if h != nil && h.process != nil && h.process.SnapshotGeneration != nil {
		return h.process.SnapshotGeneration.CurrentExecutable()
	}
	return nil
}
