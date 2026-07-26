package comparison

// EvidenceClass labels how a cell was produced.
type EvidenceClass string

const (
	EvidenceMeasured  EvidenceClass = "measured"
	EvidenceSynthetic EvidenceClass = "synthetic"
	EvidenceBlocked   EvidenceClass = "blocked"
)

// ConnectorID is a Cursor connector under comparison.
type ConnectorID string

const (
	ConnectorSDK ConnectorID = "cursorsdk"
	ConnectorACP ConnectorID = "cursorcliacp"
)

// Dimension is one cell of the ACP-versus-SDK dogfood matrix (design Phase 2).
type Dimension string

const (
	DimSetup               Dimension = "setup"
	DimInventory           Dimension = "inventory"
	DimTTFT                Dimension = "ttft"
	DimCompletionLatency   Dimension = "completion_latency"
	DimPreOutputFailures   Dimension = "pre_output_failures"
	DimPostOutputFailures  Dimension = "post_output_failures"
	DimCancellation        Dimension = "cancellation"
	DimRestart             Dimension = "restart"
	DimLeaks               Dimension = "leaks"
	DimContinuity          Dimension = "continuity"
	DimPlatformDefects     Dimension = "platform_defects"
	DimUpstreamMaintenance Dimension = "upstream_update_maintenance"
)

// RequiredDimensions is the fixed comparison matrix.
func RequiredDimensions() []Dimension {
	return []Dimension{
		DimSetup,
		DimInventory,
		DimTTFT,
		DimCompletionLatency,
		DimPreOutputFailures,
		DimPostOutputFailures,
		DimCancellation,
		DimRestart,
		DimLeaks,
		DimContinuity,
		DimPlatformDefects,
		DimUpstreamMaintenance,
	}
}

// SafeIncidentClass is a bounded classification without content or secrets.
type SafeIncidentClass string

const (
	IncidentNone              SafeIncidentClass = "none"
	IncidentStartupFailure    SafeIncidentClass = "startup_failure"
	IncidentPreOutputFailure  SafeIncidentClass = "pre_output_failure"
	IncidentPostOutputFailure SafeIncidentClass = "post_output_failure"
	IncidentCancelTimeout     SafeIncidentClass = "cancel_timeout"
	IncidentRestart           SafeIncidentClass = "restart"
	IncidentLeakSuspected     SafeIncidentClass = "leak_suspected"
	IncidentContinuityReset   SafeIncidentClass = "continuity_reset"
	IncidentPlatformBlocked   SafeIncidentClass = "platform_blocked"
	IncidentUpstreamDrift     SafeIncidentClass = "upstream_drift"
)

// NoteCode is a bounded safe operator note (no free prose).
type NoteCode string

const (
	NoteOfflineScaffold NoteCode = "offline_scaffold"
	NoteAwaitingOptIn   NoteCode = "awaiting_opt_in"
)

// ValidNoteCode reports whether n is an allowed note code (empty is allowed).
func ValidNoteCode(n NoteCode) bool {
	switch n {
	case "", NoteOfflineScaffold, NoteAwaitingOptIn:
		return true
	default:
		return false
	}
}

// BlockedReason is a bounded safe blocked-lane code (no free prose).
type BlockedReason string

const (
	BlockedSDKLiveOptIn         BlockedReason = "sdk_live_opt_in_required"
	BlockedACPDogfoodLane       BlockedReason = "acp_dogfood_lane_not_opted_in"
	BlockedMeasuredInputMissing BlockedReason = "measured_input_not_provided"
)

// ValidBlockedReason reports whether r is an allowed blocked reason code.
func ValidBlockedReason(r BlockedReason) bool {
	switch r {
	case BlockedSDKLiveOptIn, BlockedACPDogfoodLane, BlockedMeasuredInputMissing:
		return true
	default:
		return false
	}
}
