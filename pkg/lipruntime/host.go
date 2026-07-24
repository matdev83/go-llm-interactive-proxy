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

// hostAPI is Runtime's sole private host-facing dependency (req 10.1-10.4).
type hostAPI interface {
	ExecutorView() lipsdk.ExecutorView
	Ready() bool
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
	HasTrafficObservers() bool
	HasUsageObservers() bool
	HasProductionEvidenceSink() bool
	HasProductionMetering() bool
	HasProductionRater() bool
	MeteringQuerier() metering.Querier
	HasProductionMeteringQuerier() bool
	ReadinessReport() controlplane.ReadinessReportReader
	SnapshotGenerationID() int64
	SnapshotUsageVersion() string
	ExecutableGenerationID() int64
	ExecutableGenerationVersion() string
	ExecutableGenerationState() controlplane.CapabilityState
	ExecutableEvidenceObjectID() string
	RefreshSnapshots(ctx context.Context) error
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
func (b bundleHost) Status() sdkreload.Status          { return b.h.Status() }
func (b bundleHost) HasTrafficObservers() bool         { return b.h.HasProductionTrafficObservers() }
func (b bundleHost) HasUsageObservers() bool           { return b.h.HasProductionUsageObservers() }
func (b bundleHost) HasProductionEvidenceSink() bool   { return b.h.HasProductionEvidenceSink() }
func (b bundleHost) HasProductionMetering() bool       { return b.h.ActiveHasProductionMetering() }
func (b bundleHost) HasProductionRater() bool          { return b.h.ActiveHasProductionRater() }
func (b bundleHost) MeteringQuerier() metering.Querier { return b.h.MeteringQuerier() }
func (b bundleHost) HasProductionMeteringQuerier() bool {
	return b.h.HasProductionMeteringQuerier()
}
func (b bundleHost) ReadinessReport() controlplane.ReadinessReportReader {
	return b.h.ReadinessReport()
}
func (b bundleHost) SnapshotGenerationID() int64         { return b.h.SnapshotGenerationID() }
func (b bundleHost) SnapshotUsageVersion() string        { return b.h.SnapshotUsageVersion() }
func (b bundleHost) ExecutableGenerationID() int64       { return b.h.ExecutableGenerationID() }
func (b bundleHost) ExecutableGenerationVersion() string { return b.h.ExecutableGenerationVersion() }
func (b bundleHost) ExecutableGenerationState() controlplane.CapabilityState {
	return b.h.ExecutableGenerationState()
}
func (b bundleHost) ExecutableEvidenceObjectID() string { return b.h.ExecutableEvidenceObjectID() }
func (b bundleHost) RefreshSnapshots(ctx context.Context) error {
	if b.h == nil {
		return fmt.Errorf("lipruntime: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("lipruntime: nil context")
	}
	return b.h.RefreshSnapshots(ctx)
}
func (b bundleHost) Close(ctx context.Context) error {
	if b.h == nil {
		return nil
	}
	return b.h.Close(ctx)
}
