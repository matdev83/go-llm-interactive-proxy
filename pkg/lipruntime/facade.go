package lipruntime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func (r *Runtime) api() hostAPI {
	if r == nil {
		return nil
	}
	return r.host
}

// ExecutorView returns the stable generation-dispatching executor facade.
func (r *Runtime) ExecutorView() lipsdk.ExecutorView {
	if h := r.api(); h != nil {
		return h.ExecutorView()
	}
	return nil
}

// Ready reports whether the runtime has a usable active-generation executor.
func (r *Runtime) Ready() bool { h := r.api(); return h != nil && h.Ready() }

// HasProductionMetering reports whether a production metering recorder was wired
// onto the active generation executor (requirement 12.4 visibility for tests/fixtures).
func (r *Runtime) HasProductionMetering() bool {
	h := r.api()
	return h != nil && h.HasProductionMetering()
}

// HasTrafficObservers reports whether production traffic observers were supplied.
func (r *Runtime) HasTrafficObservers() bool {
	h := r.api()
	return h != nil && h.HasTrafficObservers()
}

// HasUsageObservers reports whether production usage observers were supplied.
func (r *Runtime) HasUsageObservers() bool { h := r.api(); return h != nil && h.HasUsageObservers() }

// HasProductionEvidenceSink reports whether a production EvidenceSink was supplied.
func (r *Runtime) HasProductionEvidenceSink() bool {
	h := r.api()
	return h != nil && h.HasProductionEvidenceSink()
}

// HasProductionRater reports whether a production operator Rater was wired onto
// the active generation executor.
func (r *Runtime) HasProductionRater() bool { h := r.api(); return h != nil && h.HasProductionRater() }

// MeteringQuerier returns the production metering query mount, or nil.
func (r *Runtime) MeteringQuerier() metering.Querier {
	if h := r.api(); h != nil {
		return h.MeteringQuerier()
	}
	return nil
}

// HasProductionMeteringQuerier reports whether a metering Querier was supplied.
func (r *Runtime) HasProductionMeteringQuerier() bool {
	h := r.api()
	return h != nil && h.HasProductionMeteringQuerier()
}

// ReadinessReport returns the active generation readiness report reader, or nil.
func (r *Runtime) ReadinessReport() controlplane.ReadinessReportReader {
	if h := r.api(); h != nil {
		return h.ReadinessReport()
	}
	return nil
}

// SnapshotGenerationID returns the published metadata compatibility generation id.
func (r *Runtime) SnapshotGenerationID() int64 {
	if h := r.api(); h != nil {
		return h.SnapshotGenerationID()
	}
	return 0
}

// SnapshotUsageVersion returns the active usage-authority source-fetch metadata version.
func (r *Runtime) SnapshotUsageVersion() string {
	if h := r.api(); h != nil {
		return h.SnapshotUsageVersion()
	}
	return ""
}

// ExecutableGenerationID returns the active executable generation id, or 0.
func (r *Runtime) ExecutableGenerationID() int64 {
	if h := r.api(); h != nil {
		return h.ExecutableGenerationID()
	}
	return 0
}

// ExecutableGenerationVersion returns the active executable generation version.
func (r *Runtime) ExecutableGenerationVersion() string {
	if h := r.api(); h != nil {
		return h.ExecutableGenerationVersion()
	}
	return ""
}

// ExecutableGenerationState returns executable generation readiness as a public capability state.
func (r *Runtime) ExecutableGenerationState() controlplane.CapabilityState {
	if h := r.api(); h != nil {
		return h.ExecutableGenerationState()
	}
	return controlplane.CapabilityDisabled
}

// ExecutableEvidenceObjectID returns the evaluator object identity used in settlement/admission evidence.
func (r *Runtime) ExecutableEvidenceObjectID() string {
	if h := r.api(); h != nil {
		return h.ExecutableEvidenceObjectID()
	}
	return ""
}

// RefreshSnapshots re-reads injectable source-fetch metadata views and republishes
// an executable generation when sources succeed (subordinate to whole-config reload).
func (r *Runtime) RefreshSnapshots(ctx context.Context) error {
	h := r.api()
	if h == nil {
		return fmt.Errorf("lipruntime: snapshot refresh not available")
	}
	return h.RefreshSnapshots(ctx)
}
