package backendplugin

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"

// CredentialMode is the closed credential posture for a factory export.
type CredentialMode string

const (
	CredentialModeUnspecified CredentialMode = ""
	CredentialModeStatic      CredentialMode = "static"
	CredentialModeWorkload    CredentialMode = "workload"
	CredentialModeOAuthUser   CredentialMode = "oauth_user"
	CredentialModeNone        CredentialMode = "none"
	CredentialModeUnknown     CredentialMode = "unknown"
)

// AccessScope is the closed multi-user access posture for a factory export.
type AccessScope string

const (
	AccessScopeUnspecified AccessScope = ""
	AccessScopeAny         AccessScope = "any"
	AccessScopeLocalOnly   AccessScope = "local_only"
)

// ProcessSharing declares the plugin process model.
type ProcessSharing string

const (
	ProcessSharingUnspecified    ProcessSharing = ""
	ProcessSharingPerInstance    ProcessSharing = "per_instance"
	ProcessSharingSharedArtifact ProcessSharing = "shared_artifact"
)

// Role is a canonical message role.
type Role string

const (
	RoleUnspecified Role = ""
	RoleSystem      Role = "system"
	RoleUser        Role = "user"
	RoleAssistant   Role = "assistant"
	RoleTool        Role = "tool"
)

// PartKind identifies a canonical content part.
type PartKind string

const (
	PartKindUnspecified PartKind = ""
	PartKindText        PartKind = "text"
	PartKindImageRef    PartKind = "image_ref"
	PartKindFileRef     PartKind = "file_ref"
	PartKindReasoning   PartKind = "reasoning"
	PartKindToolCall    PartKind = "tool_call"
	PartKindToolResult  PartKind = "tool_result"
)

// ErrorCode classifies plugin errors.
type ErrorCode string

const (
	ErrorCodeUnspecified        ErrorCode = ""
	ErrorCodeInvalidArgument    ErrorCode = "invalid_argument"
	ErrorCodeUnauthenticated    ErrorCode = "unauthenticated"
	ErrorCodePermissionDenied   ErrorCode = "permission_denied"
	ErrorCodeNotFound           ErrorCode = "not_found"
	ErrorCodeResourceExhausted  ErrorCode = "resource_exhausted"
	ErrorCodeFailedPrecondition ErrorCode = "failed_precondition"
	ErrorCodeAborted            ErrorCode = "aborted"
	ErrorCodeUnavailable        ErrorCode = "unavailable"
	ErrorCodeInternal           ErrorCode = "internal"
	ErrorCodeProviderTransient  ErrorCode = "provider_transient"
	ErrorCodeProviderTerminal   ErrorCode = "provider_terminal"
	ErrorCodeProtocolViolation  ErrorCode = "protocol_violation"
	ErrorCodeCancelled          ErrorCode = "cancelled"
)

// CancelReason classifies cancellation.
type CancelReason string

const (
	CancelReasonUnspecified CancelReason = ""
	CancelReasonClient      CancelReason = "client"
	CancelReasonHost        CancelReason = "host"
	CancelReasonDeadline    CancelReason = "deadline"
	CancelReasonShutdown    CancelReason = "shutdown"
)

// ClientFrameKind is a host-to-plugin Execute frame kind.
type ClientFrameKind string

const (
	ClientFrameUnspecified ClientFrameKind = ""
	ClientFrameStart       ClientFrameKind = "start"
	ClientFrameCancel      ClientFrameKind = "cancel"
	ClientFrameCloseInput  ClientFrameKind = "close_input"
)

// ServerFrameKind is a plugin-to-host Execute frame kind.
type ServerFrameKind string

const (
	ServerFrameUnspecified   ServerFrameKind = ""
	ServerFrameAccepted      ServerFrameKind = "accepted"
	ServerFrameEvent         ServerFrameKind = "event"
	ServerFrameDiagnostic    ServerFrameKind = "diagnostic"
	ServerFrameCancelOutcome ServerFrameKind = "cancel_outcome"
	ServerFrameTerminal      ServerFrameKind = "terminal"
)

// EventKind identifies a canonical stream event.
type EventKind string

const (
	EventUnspecified             EventKind = ""
	EventResponseStarted         EventKind = "response_started"
	EventMessageStarted          EventKind = "message_started"
	EventTextDelta               EventKind = "text_delta"
	EventReasoningDelta          EventKind = "reasoning_delta"
	EventReasoningSignatureDelta EventKind = "reasoning_signature_delta"
	EventReasoningOpaqueDelta    EventKind = "reasoning_opaque_delta"
	EventToolCallStarted         EventKind = "tool_call_started"
	EventToolCallArgsDelta       EventKind = "tool_call_args_delta"
	EventToolCallFinished        EventKind = "tool_call_finished"
	EventUsageDelta              EventKind = "usage_delta"
	EventWarning                 EventKind = "warning"
	EventError                   EventKind = "error"
	EventResponseFinished        EventKind = "response_finished"
	EventAssistantImageRef       EventKind = "assistant_image_ref"
	EventAssistantFileRef        EventKind = "assistant_file_ref"
)

// TerminalStatus is the execute terminal outcome.
type TerminalStatus string

const (
	TerminalUnspecified TerminalStatus = ""
	TerminalSuccess     TerminalStatus = "success"
	TerminalFailure     TerminalStatus = "failure"
	TerminalCancelled   TerminalStatus = "cancelled"
)

// CredentialModeFromLipsdk maps public lipsdk registration modes into ABI modes.
func CredentialModeFromLipsdk(m lipsdk.BackendCredentialMode) (CredentialMode, error) {
	switch m {
	case lipsdk.CredentialStatic:
		return CredentialModeStatic, nil
	case lipsdk.CredentialWorkload:
		return CredentialModeWorkload, nil
	case lipsdk.CredentialOAuthUser:
		return CredentialModeOAuthUser, nil
	case lipsdk.CredentialNone:
		return CredentialModeNone, nil
	case lipsdk.CredentialUnknown:
		return CredentialModeUnknown, nil
	default:
		return CredentialModeUnspecified, ErrUnknownEnum
	}
}

// AccessScopeFromLipsdk maps public lipsdk access scopes into ABI scopes.
func AccessScopeFromLipsdk(s lipsdk.BackendAccessScope) (AccessScope, error) {
	switch s {
	case lipsdk.BackendAccessAny:
		return AccessScopeAny, nil
	case lipsdk.BackendAccessLocalOnly:
		return AccessScopeLocalOnly, nil
	default:
		return AccessScopeUnspecified, ErrUnknownEnum
	}
}

// Validate rejects unspecified and unknown canonical event kinds (fail closed).
func (k EventKind) Validate() error {
	switch k {
	case EventResponseStarted, EventMessageStarted, EventTextDelta, EventReasoningDelta,
		EventReasoningSignatureDelta, EventReasoningOpaqueDelta, EventToolCallStarted,
		EventToolCallArgsDelta, EventToolCallFinished, EventUsageDelta, EventWarning,
		EventError, EventResponseFinished, EventAssistantImageRef, EventAssistantFileRef:
		return nil
	default:
		return ErrUnknownEventKind
	}
}

func (m CredentialMode) Validate() error {
	switch m {
	case CredentialModeStatic, CredentialModeWorkload, CredentialModeOAuthUser, CredentialModeNone, CredentialModeUnknown:
		return nil
	default:
		return ErrUnknownEnum
	}
}

func (s AccessScope) Validate() error {
	switch s {
	case AccessScopeAny, AccessScopeLocalOnly:
		return nil
	default:
		return ErrUnknownEnum
	}
}

func (p ProcessSharing) Validate() error {
	switch p {
	case ProcessSharingPerInstance, ProcessSharingSharedArtifact:
		return nil
	default:
		return ErrUnknownEnum
	}
}
