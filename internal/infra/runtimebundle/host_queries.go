package runtimebundle

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Ready reports whether the host has a usable active-generation executor.
func (h *ReloadHost) Ready() bool {
	return h != nil && h.Manager != nil && h.Executor != nil && h.Manager.Active() != nil
}

// MeteringQuerier returns the production metering query mount, or nil.
func (h *ReloadHost) MeteringQuerier() metering.Querier {
	if h == nil || h.Process == nil {
		return nil
	}
	return h.Process.MeteringQuerier
}

// HasProductionMeteringQuerier reports whether a metering Querier is mounted.
func (h *ReloadHost) HasProductionMeteringQuerier() bool {
	return h.MeteringQuerier() != nil
}

// ReadinessReport returns the active generation readiness report reader, or nil.
func (h *ReloadHost) ReadinessReport() controlplane.ReadinessReportReader {
	if h == nil || h.Manager == nil {
		return nil
	}
	g := h.Manager.Active()
	if g == nil {
		return nil
	}
	type readinessProvider interface {
		ReadinessReport() controlplane.ReadinessReportReader
	}
	if p, ok := g.RequestPlane().(readinessProvider); ok {
		return p.ReadinessReport()
	}
	return nil
}

// SnapshotGenerationID returns the published metadata compatibility generation id.
func (h *ReloadHost) SnapshotGenerationID() int64 {
	if cur := h.currentSnapshot(); cur != nil {
		return cur.ID
	}
	return 0
}

// SnapshotUsageVersion returns the active usage-authority source-fetch metadata version.
func (h *ReloadHost) SnapshotUsageVersion() string {
	if cur := h.currentSnapshot(); cur != nil {
		return cur.Usage.Version
	}
	return ""
}

// ExecutableGenerationID returns the active executable generation id, or 0.
func (h *ReloadHost) ExecutableGenerationID() int64 {
	if exec := h.currentExecutable(); exec != nil {
		return exec.ID
	}
	return 0
}

// ExecutableGenerationVersion returns the active executable generation version.
func (h *ReloadHost) ExecutableGenerationVersion() string {
	if exec := h.currentExecutable(); exec != nil {
		return exec.Version
	}
	return ""
}

// ExecutableGenerationState returns executable generation readiness as a public capability state.
func (h *ReloadHost) ExecutableGenerationState() controlplane.CapabilityState {
	exec := h.currentExecutable()
	if exec == nil {
		return controlplane.CapabilityDisabled
	}
	switch exec.State {
	case economics.SnapshotReady, economics.SnapshotStale, "":
		return controlplane.CapabilityReady
	case economics.SnapshotDegraded:
		return controlplane.CapabilityDegraded
	case economics.SnapshotUnavailable:
		return controlplane.CapabilityUnavailable
	case economics.SnapshotDisabled:
		return controlplane.CapabilityDisabled
	default:
		return controlplane.CapabilityUnavailable
	}
}

// ExecutableEvidenceObjectID returns the evaluator object identity used in settlement/admission evidence.
func (h *ReloadHost) ExecutableEvidenceObjectID() string {
	if exec := h.currentExecutable(); exec != nil {
		return exec.EvidenceObjectID()
	}
	return ""
}

// RefreshSnapshots re-reads injectable source-fetch metadata views and republishes
// an executable generation when sources succeed (subordinate to whole-config reload).
func (h *ReloadHost) RefreshSnapshots(ctx context.Context) error {
	if h == nil || h.Process == nil || h.Process.SnapshotController == nil {
		return fmt.Errorf("runtimebundle: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	return h.Process.SnapshotController.Refresh(ctx)
}

func (h *ReloadHost) snapshotPublisher() *snapshotgen.Publisher {
	if h == nil || h.Process == nil {
		return nil
	}
	return h.Process.SnapshotGeneration
}

func (h *ReloadHost) currentSnapshot() *snapshotgen.RuntimeGeneration {
	if pub := h.snapshotPublisher(); pub != nil {
		return pub.Current()
	}
	return nil
}

func (h *ReloadHost) currentExecutable() *snapshotgen.ExecutableGeneration {
	if pub := h.snapshotPublisher(); pub != nil {
		return pub.CurrentExecutable()
	}
	return nil
}
