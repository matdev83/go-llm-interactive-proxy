package domain

type BackingCapability string

const (
	BackingCapabilityDisabled     BackingCapability = "disabled"
	BackingCapabilityAtomic       BackingCapability = "atomic"
	BackingCapabilityAdvisoryOnly BackingCapability = "advisory_only"
	BackingCapabilityDegraded     BackingCapability = "degraded"
	BackingCapabilityUnavailable  BackingCapability = "unavailable"
)

func (b BackingCapability) IsKnown() bool {
	switch b {
	case BackingCapabilityDisabled, BackingCapabilityAtomic, BackingCapabilityAdvisoryOnly,
		BackingCapabilityDegraded, BackingCapabilityUnavailable:
		return true
	default:
		return false
	}
}

func (b BackingCapability) StrictCapable() bool {
	return b == BackingCapabilityAtomic
}

type AuthorityState string

const (
	AuthorityStateDisabled     AuthorityState = "disabled"
	AuthorityStateReady        AuthorityState = "ready"
	AuthorityStateDegraded     AuthorityState = "degraded"
	AuthorityStateUnavailable  AuthorityState = "unavailable"
	AuthorityStateAdvisoryOnly AuthorityState = "advisory_only"
)

func (s AuthorityState) IsKnown() bool {
	switch s {
	case AuthorityStateDisabled, AuthorityStateReady, AuthorityStateDegraded, AuthorityStateUnavailable, AuthorityStateAdvisoryOnly:
		return true
	default:
		return false
	}
}

type StatusReason string

const (
	StatusReasonNone               StatusReason = "none"
	StatusReasonDisabledByConfig   StatusReason = "disabled_by_config"
	StatusReasonBackingUnavailable StatusReason = "backing_unavailable"
	StatusReasonBackingDegraded    StatusReason = "backing_degraded"
	StatusReasonAdvisoryOnly       StatusReason = "advisory_only"
	StatusReasonValidationFailed   StatusReason = "validation_failed"
)

func (r StatusReason) IsKnown() bool {
	switch r {
	case StatusReasonNone, StatusReasonDisabledByConfig, StatusReasonBackingUnavailable,
		StatusReasonBackingDegraded, StatusReasonAdvisoryOnly, StatusReasonValidationFailed:
		return true
	default:
		return false
	}
}

type AuthorityStatus struct {
	State  AuthorityState
	Reason StatusReason
}

func StatusFromBacking(backing BackingCapability) AuthorityStatus {
	switch backing {
	case BackingCapabilityAtomic:
		return AuthorityStatus{State: AuthorityStateReady, Reason: StatusReasonNone}
	case BackingCapabilityAdvisoryOnly:
		return AuthorityStatus{State: AuthorityStateAdvisoryOnly, Reason: StatusReasonAdvisoryOnly}
	case BackingCapabilityDegraded:
		return AuthorityStatus{State: AuthorityStateDegraded, Reason: StatusReasonBackingDegraded}
	case BackingCapabilityUnavailable:
		return AuthorityStatus{State: AuthorityStateUnavailable, Reason: StatusReasonBackingUnavailable}
	case BackingCapabilityDisabled:
		fallthrough
	default:
		return AuthorityStatus{State: AuthorityStateDisabled, Reason: StatusReasonDisabledByConfig}
	}
}

type AuthorityConfig struct {
	Enabled            bool
	Backing            BackingCapability
	UnknownAttribution UnknownAttribution
	Rules              []Rule
}

func (c AuthorityConfig) Status() AuthorityStatus {
	if !c.Enabled {
		return AuthorityStatus{State: AuthorityStateDisabled, Reason: StatusReasonDisabledByConfig}
	}
	return StatusFromBacking(c.Backing)
}

// UnknownAttribution controls how unknown attribution fields are normalized
// before rule matching and evidence projection.
type UnknownAttribution string

const (
	UnknownAttributionPreserve   UnknownAttribution = "preserve"
	UnknownAttributionUnknown    UnknownAttribution = "unknown"
	UnknownAttributionKnownEmpty UnknownAttribution = "known_empty"
)

func (m UnknownAttribution) IsKnown() bool {
	switch m {
	case "", UnknownAttributionPreserve, UnknownAttributionUnknown, UnknownAttributionKnownEmpty:
		return true
	default:
		return false
	}
}
