package controlplane

import (
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// ObservedStreamUsageInput is the safe usage observation projected from the
// usage observer or secure-session usage delta paths (requirements 1.4, 14.1,
// 14.2, 17.4).
type ObservedStreamUsageInput struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	CostNanoUnits    int64
	Currency         string
	CostSource       string
}

// ProjectObservedStreamUsage builds a UsageDetail with independent dual-plane
// fields while preserving legacy observed plane/availability projections.
func ProjectObservedStreamUsage(in ObservedStreamUsageInput) cp.UsageDetail {
	return cp.UsageDetail{
		Plane:               cp.UsagePlaneObserved,
		Availability:        cp.UsageAvailabilityObserved,
		Perspective:         cp.UsagePerspectiveOperator,
		Boundary:            cp.UsageBoundaryBackendEgress,
		LifecycleScope:      cp.UsageLifecycleBackendAttempt,
		Provenance:          cp.UsageProvenanceDelegated,
		FactKind:            cp.UsageFactKindDelta,
		Surfaced:            cp.UsageSurfacedUnknown,
		InputTokens:         in.InputTokens,
		OutputTokens:        in.OutputTokens,
		CacheReadTokens:     in.CacheReadTokens,
		CacheWriteTokens:    in.CacheWriteTokens,
		ReasoningTokens:     in.ReasoningTokens,
		TotalTokens:         in.TotalTokens,
		CostNanoUnits:       in.CostNanoUnits,
		Currency:            in.Currency,
		AccountingAuthority: in.CostSource,
		CostSource:          in.CostSource,
	}
}

// UsageDetailFromEventInput carries optional dual-plane overrides for usage
// evidence projected from metering facts or other rich sources.
type UsageDetailFromEventInput struct {
	ObservedStreamUsageInput
	Perspective    cp.UsagePerspective
	Boundary       cp.UsageBoundary
	LifecycleScope cp.UsageLifecycleScope
	Provenance     cp.UsageProvenance
	FactKind       cp.UsageFactKind
	Surfaced       cp.UsageSurfaced
	Plane          cp.UsagePlane
	Availability   cp.UsageAvailability
	PolicyVersion  cp.VersionRef
	RatingVersion  cp.VersionRef
}

// ProjectUsageDetail merges optional dual-plane overrides onto the legacy
// observed-stream defaults.
func ProjectUsageDetail(in UsageDetailFromEventInput) cp.UsageDetail {
	out := ProjectObservedStreamUsage(in.ObservedStreamUsageInput)
	if in.Perspective != "" {
		out.Perspective = in.Perspective
	}
	if in.Boundary != "" {
		out.Boundary = in.Boundary
	}
	if in.LifecycleScope != "" {
		out.LifecycleScope = in.LifecycleScope
	}
	if in.Provenance != "" {
		out.Provenance = in.Provenance
	}
	if in.FactKind != "" {
		out.FactKind = in.FactKind
	}
	if in.Surfaced != "" {
		out.Surfaced = in.Surfaced
	}
	if in.Plane != "" {
		out.Plane = in.Plane
	}
	if in.Availability != "" {
		out.Availability = in.Availability
	}
	if in.PolicyVersion.ID != "" || in.PolicyVersion.Version != "" {
		out.PolicyVersion = in.PolicyVersion
	}
	if in.RatingVersion.ID != "" || in.RatingVersion.Version != "" {
		out.RatingVersion = in.RatingVersion
	}
	return out
}

// UsageRowFromEvent projects a usage query row from a normalized usage event.
func UsageRowFromEvent(ev cp.Event) cp.UsageRow {
	u := ev.Usage
	row := cp.UsageRow{
		Correlation:      ev.Correlation,
		Plane:            u.Plane,
		Availability:     u.Availability,
		Perspective:      u.Perspective,
		Boundary:         u.Boundary,
		LifecycleScope:   u.LifecycleScope,
		Provenance:       u.Provenance,
		FactKind:         u.FactKind,
		Surfaced:         u.Surfaced,
		PolicyVersion:    u.PolicyVersion,
		RatingVersion:    u.RatingVersion,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		ReasoningTokens:  u.ReasoningTokens,
		TotalTokens:      u.TotalTokens,
		CostNanoUnits:    u.CostNanoUnits,
		Currency:         u.Currency,
		EvidenceState:    ev.EvidenceState,
		RedactionState:   ev.RedactionState,
	}
	if row.EvidenceState == "" {
		row.EvidenceState = cp.EvidenceRecorded
	}
	if row.RedactionState == "" {
		row.RedactionState = cp.RedactionNone
	}
	return row
}

// VersionRefFromPolicy maps economics policy snapshot refs onto control-plane
// version refs.
func VersionRefFromPolicy(ref economics.PolicySnapshotRef) cp.VersionRef {
	return cp.VersionRef{
		ID:          firstNonEmpty(ref.PolicyID, ref.ID),
		Version:     ref.Version,
		EffectiveAt: ref.EffectiveAt,
		FetchedAt:   ref.FetchedAt,
	}
}

// VersionRefFromRating maps economics rating snapshot refs onto control-plane
// version refs.
func VersionRefFromRating(ref economics.RatingSnapshotRef) cp.VersionRef {
	return cp.VersionRef{
		ID:          firstNonEmpty(ref.RaterID, ref.ID),
		Version:     ref.Version,
		EffectiveAt: ref.EffectiveAt,
		FetchedAt:   ref.FetchedAt,
	}
}

// ObservedStreamInputFromUsageEvent extracts token/cost fields from a usage
// observer event.
func ObservedStreamInputFromUsageEvent(ev usage.Event) ObservedStreamUsageInput {
	return ObservedStreamUsageInput{
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         ev.Currency,
		CostSource:       ev.CostSource,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
