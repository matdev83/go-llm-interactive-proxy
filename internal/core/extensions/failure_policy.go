package extensions

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// FailurePolicy describes default runner behavior when a stage handler errors (design section 17).
type FailurePolicy uint8

const (
	// FailurePolicyUnset means the stage id is not in the legal pipeline.
	FailurePolicyUnset FailurePolicy = iota
	FailurePolicyFailOpen
	FailurePolicyFailClosed
)

// Stage name constants for the legal pipeline (stable ids; align with pkg/lipsdk/feature).
const (
	StageTransportAuth       = feature.StageIDTransportAuth
	StageSessionOpen         = feature.StageIDSessionOpen
	StageSubmit              = feature.StageIDSubmit
	StageToolCatalog         = feature.StageIDToolCatalog
	StageRequestWide         = feature.StageIDRequestWide
	StageRouteHinting        = feature.StageIDRouteHinting
	StageAttemptLifecycle    = feature.StageIDAttemptLifecycle
	StageStreamEventMutation = feature.StageIDStreamEventMutation
	StageToolEventReaction   = feature.StageIDToolEventReaction
	StageCompletionGating    = feature.StageIDCompletionGating
	StageTrafficObservation  = feature.StageIDTrafficObservation
	StageEgressEncoding      = feature.StageIDEgressEncoding
)

// DefaultFailurePolicyForStage returns the documented default for the stage (design section 17).
func DefaultFailurePolicyForStage(stage string) FailurePolicy {
	switch stage {
	case feature.StageIDTransportAuth:
		return FailurePolicyFailClosed
	case feature.StageIDSessionOpen:
		return FailurePolicyFailOpen
	case feature.StageIDSubmit, feature.StageIDToolCatalog, feature.StageIDRequestWide:
		// Pre-backend mutation: compatible with hook-bus per-hook FailureMode; stage default fail-open.
		return FailurePolicyFailOpen
	case feature.StageIDRouteHinting:
		return FailurePolicyFailOpen
	case feature.StageIDAttemptLifecycle:
		return FailurePolicyFailOpen
	case feature.StageIDStreamEventMutation, feature.StageIDToolEventReaction:
		// Per-hook policies apply; documented stage default matches lenient hook-bus handling.
		return FailurePolicyFailOpen
	case feature.StageIDCompletionGating:
		return FailurePolicyFailOpen
	case feature.StageIDTrafficObservation:
		return FailurePolicyFailOpen
	case feature.StageIDEgressEncoding:
		return FailurePolicyFailClosed
	default:
		return FailurePolicyUnset
	}
}

// FailurePolicyLabel returns a stable JSON label for inventory/diagnostics.
func FailurePolicyLabel(p FailurePolicy) string {
	switch p {
	case FailurePolicyFailOpen:
		return "fail_open"
	case FailurePolicyFailClosed:
		return "fail_closed"
	default:
		return "unset"
	}
}
