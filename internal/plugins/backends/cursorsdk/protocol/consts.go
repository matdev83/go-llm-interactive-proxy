package protocol

const (
	// SchemaVersion is the bridge schema generation negotiated at initialize.
	SchemaVersion = 1

	// MaxFrameBytes is the hard per-line frame limit (16 MiB).
	MaxFrameBytes = 16 << 20

	// PinnedSDKVersion is the exact @cursor/sdk version the bridge may load.
	PinnedSDKVersion = "1.0.23"

	// MinNodeEngine is the official SDK Node engine lower bound.
	MinNodeEngine = ">=22.13"
)

// Envelope types.
const (
	TypeRequest  = "request"
	TypeResponse = "response"
	TypeEvent    = "event"
)

// Required methods.
const (
	MethodInitialize     = "bridge/initialize"
	MethodHealth         = "bridge/health"
	MethodModelsList     = "models/list"
	MethodAgentCreate    = "agent/create"
	MethodAgentSend      = "agent/send"
	MethodRunCancel      = "run/cancel"
	MethodAgentDispose   = "agent/dispose"
	MethodBridgeShutdown = "bridge/shutdown"
)

// Run event kinds.
const (
	KindTextDelta      = "text_delta"
	KindReasoningDelta = "reasoning_delta"
	KindUsage          = "usage"
	KindWarning        = "warning"
	KindActivity       = "activity"
	KindFinished       = "finished"
	KindError          = "error"
)

// ErrorClass values are stable machine-readable protocol failure classes.
const (
	ErrorFrameTooLarge       = "frame_too_large"
	ErrorInvalidJSON         = "invalid_json"
	ErrorIncompatibleVersion = "incompatible_version"
	ErrorUnknownType         = "unknown_type"
	ErrorUnknownMethod       = "unknown_method"
	ErrorInvalidRequest      = "invalid_request"
	ErrorResponseMismatch    = "response_mismatch"
	ErrorInvalidEvent        = "invalid_event"
	ErrorUnknownEventKind    = "unknown_event_kind"
	ErrorSequenceRegression  = "sequence_regression"
	ErrorDuplicateTerminal   = "duplicate_terminal"
)
