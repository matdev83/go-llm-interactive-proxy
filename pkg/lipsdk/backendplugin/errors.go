package backendplugin

import "errors"

// Sentinel errors for fail-closed public validation and conversion.
var (
	// ErrIncompatibleMajor is returned when host and plugin protocol majors differ
	// or are not ProtocolMajorV1.
	ErrIncompatibleMajor = errors.New("backendplugin: incompatible protocol major version")
	// ErrUnknownRequiredFeature is returned when a required feature is unknown to the peer.
	ErrUnknownRequiredFeature = errors.New("backendplugin: unknown required feature")
	// ErrDuplicateFeature is returned when a feature name appears more than once in an offer.
	ErrDuplicateFeature = errors.New("backendplugin: duplicate feature name")
	// ErrEmptyFeatureName is returned when a feature name is empty after trim.
	ErrEmptyFeatureName = errors.New("backendplugin: empty feature name")
	// ErrConfigureBeforeNegotiate is returned when configure is attempted without a compatible negotiation.
	ErrConfigureBeforeNegotiate = errors.New("backendplugin: configure before successful negotiation")
	// ErrTransportRetriesRequiredDisabled requires DisableAutomaticRetries.
	ErrTransportRetriesRequiredDisabled = errors.New("backendplugin: transport automatic retries must be disabled")
	// ErrInvalidBounds is returned for inconsistent size limits.
	ErrInvalidBounds = errors.New("backendplugin: invalid message or frame bounds")
	// ErrOversizedMessage is returned when a payload exceeds its configured bound.
	ErrOversizedMessage = errors.New("backendplugin: message exceeds configured bound")
	// ErrOversizedRawJSON is returned when raw JSON bytes exceed the configured bound.
	ErrOversizedRawJSON = errors.New("backendplugin: raw JSON exceeds configured bound")
	// ErrUnknownFrameKind is returned for unspecified or unknown frame kinds.
	ErrUnknownFrameKind = errors.New("backendplugin: unknown frame kind")
	// ErrUnknownEventKind is returned for unspecified or unknown event kinds.
	ErrUnknownEventKind = errors.New("backendplugin: unknown event kind")
	// ErrUnknownEnum is returned for unspecified or unknown closed enum values.
	ErrUnknownEnum = errors.New("backendplugin: unknown enum value")
	// ErrUnsupportedPartKind is returned when a content part kind has no
	// canonical<->ABI mapping; unmapped kinds fail closed, never silently drop.
	ErrUnsupportedPartKind = errors.New("backendplugin: unsupported part kind")
	// ErrInvalidRawJSON is returned for invalid or unset RawJSON oneof wire values.
	ErrInvalidRawJSON = errors.New("backendplugin: invalid raw JSON wire value")
	// ErrMultipleTerminals is returned when more than one terminal frame is observed.
	ErrMultipleTerminals = errors.New("backendplugin: multiple terminal frames")
	// ErrEventAfterTerminal is returned for any frame after a terminal.
	ErrEventAfterTerminal = errors.New("backendplugin: frame after terminal")
	// ErrSequenceGap is returned for non-monotonic or missing sequence numbers.
	ErrSequenceGap = errors.New("backendplugin: sequence gap or regression")
	// ErrAcceptedRequired is returned when a sequenced frame arrives before accepted.
	ErrAcceptedRequired = errors.New("backendplugin: accepted frame required first")
	// ErrInvalidDescriptor is returned for an invalid plugin descriptor.
	ErrInvalidDescriptor = errors.New("backendplugin: invalid plugin descriptor")
	// ErrInvalidInvocation is returned for an invalid invocation or usage presence mismatch.
	ErrInvalidInvocation = errors.New("backendplugin: invalid invocation")
	// ErrInvalidFrame is returned when a frame payload shape does not match its kind.
	ErrInvalidFrame = errors.New("backendplugin: invalid frame payload")
	// ErrNegotiationRequired is returned when configure lacks a bound negotiation token.
	ErrNegotiationRequired = errors.New("backendplugin: negotiation token required")
	// ErrUnknownNegotiationToken is returned for an unknown or spent negotiation token.
	ErrUnknownNegotiationToken = errors.New("backendplugin: unknown negotiation token")
	// ErrInstanceExists is returned when configure reuses an active instance id.
	ErrInstanceExists = errors.New("backendplugin: instance already exists")
	// ErrInstanceBusy is returned when close is attempted while execute leases remain.
	ErrInstanceBusy = errors.New("backendplugin: instance has active execute leases")
	// ErrOrderedItemsUnsupported is returned when item authority is requested against an older plugin ABI.
	ErrOrderedItemsUnsupported = errors.New("backendplugin: ordered item ABI not negotiated")
)
