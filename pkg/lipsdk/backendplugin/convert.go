package backendplugin

import (
	"fmt"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
)

// ProtocolOfferToNegotiateRequest encodes a host protocol offer.
func ProtocolOfferToNegotiateRequest(o ProtocolOffer) (*backendpluginv1.NegotiateRequest, error) {
	if o.Major == 0 {
		return nil, ErrUnknownEnum
	}
	if _, err := indexFeatures(o.Features); err != nil {
		return nil, err
	}
	return &backendpluginv1.NegotiateRequest{
		HostMajor:               o.Major,
		HostMinor:               o.Minor,
		HostFeatures:            featuresToProto(o.Features),
		DisableTransportRetries: o.DisableTransportRetries,
	}, nil
}

// ProtocolOfferFromNegotiateRequest converts a wire negotiate request into a host offer.
func ProtocolOfferFromNegotiateRequest(p *backendpluginv1.NegotiateRequest) (ProtocolOffer, error) {
	if p == nil {
		return ProtocolOffer{}, ErrInvalidInvocation
	}
	if p.GetHostMajor() == 0 {
		return ProtocolOffer{}, ErrUnknownEnum
	}
	feats := featuresFromProto(p.GetHostFeatures())
	if _, err := indexFeatures(feats); err != nil {
		return ProtocolOffer{}, err
	}
	return ProtocolOffer{
		Major:                   p.GetHostMajor(),
		Minor:                   p.GetHostMinor(),
		Features:                feats,
		DisableTransportRetries: p.GetDisableTransportRetries(),
	}, nil
}

// NegotiationToNegotiateResponse encodes a negotiation outcome.
func NegotiationToNegotiateResponse(n Negotiation) (*backendpluginv1.NegotiateResponse, error) {
	if n.Compatible && n.PluginMajor == 0 {
		return nil, ErrUnknownEnum
	}
	if _, err := indexFeatures(n.PluginFeatures); err != nil {
		return nil, err
	}
	return &backendpluginv1.NegotiateResponse{
		PluginMajor:             n.PluginMajor,
		PluginMinor:             n.PluginMinor,
		PluginFeatures:          featuresToProto(n.PluginFeatures),
		NegotiatedMinor:         n.NegotiatedMinor,
		EnabledFeatures:         append([]string(nil), n.EnabledFeatures...),
		Compatible:              n.Compatible,
		RejectReason:            n.RejectReason,
		DisableTransportRetries: n.TransportPolicy.DisableAutomaticRetries,
		NegotiationToken:        n.NegotiationToken,
	}, nil
}

// NegotiationFromNegotiateResponse converts a wire negotiate response.
func NegotiationFromNegotiateResponse(p *backendpluginv1.NegotiateResponse) (Negotiation, error) {
	if p == nil {
		return Negotiation{}, ErrInvalidInvocation
	}
	if p.GetCompatible() && p.GetPluginMajor() == 0 {
		return Negotiation{}, ErrUnknownEnum
	}
	feats := featuresFromProto(p.GetPluginFeatures())
	if _, err := indexFeatures(feats); err != nil {
		return Negotiation{}, err
	}
	policy := DefaultTransportPolicy()
	policy.DisableAutomaticRetries = p.GetDisableTransportRetries()
	if p.GetCompatible() {
		if err := policy.Validate(); err != nil {
			return Negotiation{}, err
		}
	}
	return Negotiation{
		Compatible:       p.GetCompatible(),
		NegotiatedMinor:  p.GetNegotiatedMinor(),
		EnabledFeatures:  append([]string(nil), p.GetEnabledFeatures()...),
		RejectReason:     p.GetRejectReason(),
		TransportPolicy:  policy,
		PluginMajor:      p.GetPluginMajor(),
		PluginMinor:      p.GetPluginMinor(),
		PluginFeatures:   feats,
		NegotiationToken: p.GetNegotiationToken(),
	}, nil
}

// RawJSONFromProto converts a wire RawJSONValue. Nil means absent.
// Unset oneof and is_null=false fail closed.
func RawJSONFromProto(v *backendpluginv1.RawJSONValue) (RawJSON, error) {
	if v == nil {
		return RawJSONAbsentValue(), nil
	}
	switch x := v.State.(type) {
	case *backendpluginv1.RawJSONValue_IsNull:
		if !x.IsNull {
			return RawJSON{}, fmt.Errorf("%w: is_null=false", ErrInvalidRawJSON)
		}
		return RawJSONNullValue(), nil
	case *backendpluginv1.RawJSONValue_Json:
		return RawJSONFromBytes(x.Json), nil
	case nil:
		return RawJSON{}, fmt.Errorf("%w: unset oneof", ErrInvalidRawJSON)
	default:
		return RawJSON{}, fmt.Errorf("%w: unknown oneof", ErrInvalidRawJSON)
	}
}

// RawJSONToProto encodes RawJSON. Absent encodes as nil.
func RawJSONToProto(r RawJSON) *backendpluginv1.RawJSONValue {
	switch r.State() {
	case RawJSONNull:
		return &backendpluginv1.RawJSONValue{State: &backendpluginv1.RawJSONValue_IsNull{IsNull: true}}
	case RawJSONValue:
		return &backendpluginv1.RawJSONValue{State: &backendpluginv1.RawJSONValue_Json{Json: r.Bytes()}}
	default:
		return nil
	}
}

// UsagePresenceFromProto converts usage presence flags.
func UsagePresenceFromProto(p *backendpluginv1.UsagePresence) UsagePresence {
	if p == nil {
		return UsagePresence{}
	}
	return UsagePresence{
		InputTokens:      p.GetInputTokens(),
		OutputTokens:     p.GetOutputTokens(),
		CacheReadTokens:  p.GetCacheReadTokens(),
		CacheWriteTokens: p.GetCacheWriteTokens(),
		ReasoningTokens:  p.GetReasoningTokens(),
		TotalTokens:      p.GetTotalTokens(),
	}
}

// UsagePresenceToProto encodes usage presence flags.
func UsagePresenceToProto(p UsagePresence) *backendpluginv1.UsagePresence {
	return &backendpluginv1.UsagePresence{
		InputTokens:      p.InputTokens,
		OutputTokens:     p.OutputTokens,
		CacheReadTokens:  p.CacheReadTokens,
		CacheWriteTokens: p.CacheWriteTokens,
		ReasoningTokens:  p.ReasoningTokens,
		TotalTokens:      p.TotalTokens,
	}
}

func optInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optUint32(v *uint32) *uint32 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optInt32(v *int32) *int32 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optString(v *string) *string {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func credentialModeFromProto(v backendpluginv1.CredentialMode) (CredentialMode, error) {
	switch v {
	case backendpluginv1.CredentialMode_CREDENTIAL_MODE_STATIC:
		return CredentialModeStatic, nil
	case backendpluginv1.CredentialMode_CREDENTIAL_MODE_WORKLOAD:
		return CredentialModeWorkload, nil
	case backendpluginv1.CredentialMode_CREDENTIAL_MODE_OAUTH_USER:
		return CredentialModeOAuthUser, nil
	case backendpluginv1.CredentialMode_CREDENTIAL_MODE_NONE:
		return CredentialModeNone, nil
	case backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNKNOWN:
		return CredentialModeUnknown, nil
	default:
		return CredentialModeUnspecified, ErrUnknownEnum
	}
}

func credentialModeToProto(v CredentialMode) (backendpluginv1.CredentialMode, error) {
	switch v {
	case CredentialModeStatic:
		return backendpluginv1.CredentialMode_CREDENTIAL_MODE_STATIC, nil
	case CredentialModeWorkload:
		return backendpluginv1.CredentialMode_CREDENTIAL_MODE_WORKLOAD, nil
	case CredentialModeOAuthUser:
		return backendpluginv1.CredentialMode_CREDENTIAL_MODE_OAUTH_USER, nil
	case CredentialModeNone:
		return backendpluginv1.CredentialMode_CREDENTIAL_MODE_NONE, nil
	case CredentialModeUnknown:
		return backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNKNOWN, nil
	default:
		return backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNSPECIFIED, ErrUnknownEnum
	}
}

func accessScopeFromProto(v backendpluginv1.AccessScope) (AccessScope, error) {
	switch v {
	case backendpluginv1.AccessScope_ACCESS_SCOPE_ANY:
		return AccessScopeAny, nil
	case backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY:
		return AccessScopeLocalOnly, nil
	default:
		return AccessScopeUnspecified, ErrUnknownEnum
	}
}

func accessScopeToProto(v AccessScope) (backendpluginv1.AccessScope, error) {
	switch v {
	case AccessScopeAny:
		return backendpluginv1.AccessScope_ACCESS_SCOPE_ANY, nil
	case AccessScopeLocalOnly:
		return backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY, nil
	default:
		return backendpluginv1.AccessScope_ACCESS_SCOPE_UNSPECIFIED, ErrUnknownEnum
	}
}

func processSharingFromProto(v backendpluginv1.ProcessSharing) (ProcessSharing, error) {
	switch v {
	case backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE:
		return ProcessSharingPerInstance, nil
	case backendpluginv1.ProcessSharing_PROCESS_SHARING_SHARED_ARTIFACT:
		return ProcessSharingSharedArtifact, nil
	default:
		return ProcessSharingUnspecified, ErrUnknownEnum
	}
}

func processSharingToProto(v ProcessSharing) (backendpluginv1.ProcessSharing, error) {
	switch v {
	case ProcessSharingPerInstance:
		return backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE, nil
	case ProcessSharingSharedArtifact:
		return backendpluginv1.ProcessSharing_PROCESS_SHARING_SHARED_ARTIFACT, nil
	default:
		return backendpluginv1.ProcessSharing_PROCESS_SHARING_UNSPECIFIED, ErrUnknownEnum
	}
}

func roleFromProto(v backendpluginv1.Role) (Role, error) {
	switch v {
	case backendpluginv1.Role_ROLE_SYSTEM:
		return RoleSystem, nil
	case backendpluginv1.Role_ROLE_USER:
		return RoleUser, nil
	case backendpluginv1.Role_ROLE_ASSISTANT:
		return RoleAssistant, nil
	case backendpluginv1.Role_ROLE_TOOL:
		return RoleTool, nil
	default:
		return RoleUnspecified, ErrUnknownEnum
	}
}

func roleToProto(v Role) (backendpluginv1.Role, error) {
	switch v {
	case RoleSystem:
		return backendpluginv1.Role_ROLE_SYSTEM, nil
	case RoleUser:
		return backendpluginv1.Role_ROLE_USER, nil
	case RoleAssistant:
		return backendpluginv1.Role_ROLE_ASSISTANT, nil
	case RoleTool:
		return backendpluginv1.Role_ROLE_TOOL, nil
	default:
		return backendpluginv1.Role_ROLE_UNSPECIFIED, ErrUnknownEnum
	}
}

func partKindFromProto(v backendpluginv1.PartKind) (PartKind, error) {
	switch v {
	case backendpluginv1.PartKind_PART_KIND_TEXT:
		return PartKindText, nil
	case backendpluginv1.PartKind_PART_KIND_IMAGE_REF:
		return PartKindImageRef, nil
	case backendpluginv1.PartKind_PART_KIND_FILE_REF:
		return PartKindFileRef, nil
	case backendpluginv1.PartKind_PART_KIND_REASONING:
		return PartKindReasoning, nil
	case backendpluginv1.PartKind_PART_KIND_TOOL_CALL:
		return PartKindToolCall, nil
	case backendpluginv1.PartKind_PART_KIND_TOOL_RESULT:
		return PartKindToolResult, nil
	case backendpluginv1.PartKind_PART_KIND_JSON:
		return PartKindJSON, nil
	default:
		return PartKindUnspecified, ErrUnknownEnum
	}
}

func partKindToProto(v PartKind) (backendpluginv1.PartKind, error) {
	switch v {
	case PartKindText:
		return backendpluginv1.PartKind_PART_KIND_TEXT, nil
	case PartKindImageRef:
		return backendpluginv1.PartKind_PART_KIND_IMAGE_REF, nil
	case PartKindFileRef:
		return backendpluginv1.PartKind_PART_KIND_FILE_REF, nil
	case PartKindReasoning:
		return backendpluginv1.PartKind_PART_KIND_REASONING, nil
	case PartKindToolCall:
		return backendpluginv1.PartKind_PART_KIND_TOOL_CALL, nil
	case PartKindToolResult:
		return backendpluginv1.PartKind_PART_KIND_TOOL_RESULT, nil
	case PartKindJSON:
		return backendpluginv1.PartKind_PART_KIND_JSON, nil
	default:
		return backendpluginv1.PartKind_PART_KIND_UNSPECIFIED, ErrUnknownEnum
	}
}

func errorCodeFromProto(v backendpluginv1.ErrorCode) (ErrorCode, error) {
	switch v {
	case backendpluginv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT:
		return ErrorCodeInvalidArgument, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED:
		return ErrorCodeUnauthenticated, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_PERMISSION_DENIED:
		return ErrorCodePermissionDenied, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_NOT_FOUND:
		return ErrorCodeNotFound, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED:
		return ErrorCodeResourceExhausted, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_FAILED_PRECONDITION:
		return ErrorCodeFailedPrecondition, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_ABORTED:
		return ErrorCodeAborted, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_UNAVAILABLE:
		return ErrorCodeUnavailable, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_INTERNAL:
		return ErrorCodeInternal, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TRANSIENT:
		return ErrorCodeProviderTransient, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TERMINAL:
		return ErrorCodeProviderTerminal, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_PROTOCOL_VIOLATION:
		return ErrorCodeProtocolViolation, nil
	case backendpluginv1.ErrorCode_ERROR_CODE_CANCELLED:
		return ErrorCodeCancelled, nil
	default:
		return ErrorCodeUnspecified, ErrUnknownEnum
	}
}

func errorCodeToProto(v ErrorCode) (backendpluginv1.ErrorCode, error) {
	switch v {
	case ErrorCodeInvalidArgument:
		return backendpluginv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, nil
	case ErrorCodeUnauthenticated:
		return backendpluginv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, nil
	case ErrorCodePermissionDenied:
		return backendpluginv1.ErrorCode_ERROR_CODE_PERMISSION_DENIED, nil
	case ErrorCodeNotFound:
		return backendpluginv1.ErrorCode_ERROR_CODE_NOT_FOUND, nil
	case ErrorCodeResourceExhausted:
		return backendpluginv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, nil
	case ErrorCodeFailedPrecondition:
		return backendpluginv1.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, nil
	case ErrorCodeAborted:
		return backendpluginv1.ErrorCode_ERROR_CODE_ABORTED, nil
	case ErrorCodeUnavailable:
		return backendpluginv1.ErrorCode_ERROR_CODE_UNAVAILABLE, nil
	case ErrorCodeInternal:
		return backendpluginv1.ErrorCode_ERROR_CODE_INTERNAL, nil
	case ErrorCodeProviderTransient:
		return backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TRANSIENT, nil
	case ErrorCodeProviderTerminal:
		return backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TERMINAL, nil
	case ErrorCodeProtocolViolation:
		return backendpluginv1.ErrorCode_ERROR_CODE_PROTOCOL_VIOLATION, nil
	case ErrorCodeCancelled:
		return backendpluginv1.ErrorCode_ERROR_CODE_CANCELLED, nil
	default:
		return backendpluginv1.ErrorCode_ERROR_CODE_UNSPECIFIED, ErrUnknownEnum
	}
}

func cancelReasonFromProto(v backendpluginv1.CancelReason) (CancelReason, error) {
	switch v {
	case backendpluginv1.CancelReason_CANCEL_REASON_CLIENT:
		return CancelReasonClient, nil
	case backendpluginv1.CancelReason_CANCEL_REASON_HOST:
		return CancelReasonHost, nil
	case backendpluginv1.CancelReason_CANCEL_REASON_DEADLINE:
		return CancelReasonDeadline, nil
	case backendpluginv1.CancelReason_CANCEL_REASON_SHUTDOWN:
		return CancelReasonShutdown, nil
	default:
		return CancelReasonUnspecified, ErrUnknownEnum
	}
}

func cancelReasonToProto(v CancelReason) (backendpluginv1.CancelReason, error) {
	switch v {
	case CancelReasonClient:
		return backendpluginv1.CancelReason_CANCEL_REASON_CLIENT, nil
	case CancelReasonHost:
		return backendpluginv1.CancelReason_CANCEL_REASON_HOST, nil
	case CancelReasonDeadline:
		return backendpluginv1.CancelReason_CANCEL_REASON_DEADLINE, nil
	case CancelReasonShutdown:
		return backendpluginv1.CancelReason_CANCEL_REASON_SHUTDOWN, nil
	default:
		return backendpluginv1.CancelReason_CANCEL_REASON_UNSPECIFIED, ErrUnknownEnum
	}
}

func clientFrameKindFromProto(v backendpluginv1.ClientFrameKind) (ClientFrameKind, error) {
	switch v {
	case backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START:
		return ClientFrameStart, nil
	case backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CANCEL:
		return ClientFrameCancel, nil
	case backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CLOSE_INPUT:
		return ClientFrameCloseInput, nil
	default:
		return ClientFrameUnspecified, ErrUnknownEnum
	}
}

func clientFrameKindToProto(v ClientFrameKind) (backendpluginv1.ClientFrameKind, error) {
	switch v {
	case ClientFrameStart:
		return backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START, nil
	case ClientFrameCancel:
		return backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CANCEL, nil
	case ClientFrameCloseInput:
		return backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CLOSE_INPUT, nil
	default:
		return backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_UNSPECIFIED, ErrUnknownEnum
	}
}

func serverFrameKindFromProto(v backendpluginv1.ServerFrameKind) (ServerFrameKind, error) {
	switch v {
	case backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED:
		return ServerFrameAccepted, nil
	case backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_EVENT:
		return ServerFrameEvent, nil
	case backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_DIAGNOSTIC:
		return ServerFrameDiagnostic, nil
	case backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_CANCEL_OUTCOME:
		return ServerFrameCancelOutcome, nil
	case backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL:
		return ServerFrameTerminal, nil
	default:
		return ServerFrameUnspecified, ErrUnknownEnum
	}
}

func serverFrameKindToProto(v ServerFrameKind) (backendpluginv1.ServerFrameKind, error) {
	switch v {
	case ServerFrameAccepted:
		return backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED, nil
	case ServerFrameEvent:
		return backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_EVENT, nil
	case ServerFrameDiagnostic:
		return backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_DIAGNOSTIC, nil
	case ServerFrameCancelOutcome:
		return backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_CANCEL_OUTCOME, nil
	case ServerFrameTerminal:
		return backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL, nil
	default:
		return backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_UNSPECIFIED, ErrUnknownEnum
	}
}

func eventKindFromProto(v backendpluginv1.EventKind) (EventKind, error) {
	switch v {
	case backendpluginv1.EventKind_EVENT_KIND_RESPONSE_STARTED:
		return EventResponseStarted, nil
	case backendpluginv1.EventKind_EVENT_KIND_MESSAGE_STARTED:
		return EventMessageStarted, nil
	case backendpluginv1.EventKind_EVENT_KIND_TEXT_DELTA:
		return EventTextDelta, nil
	case backendpluginv1.EventKind_EVENT_KIND_REASONING_DELTA:
		return EventReasoningDelta, nil
	case backendpluginv1.EventKind_EVENT_KIND_REASONING_SIGNATURE_DELTA:
		return EventReasoningSignatureDelta, nil
	case backendpluginv1.EventKind_EVENT_KIND_REASONING_OPAQUE_DELTA:
		return EventReasoningOpaqueDelta, nil
	case backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_STARTED:
		return EventToolCallStarted, nil
	case backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_ARGS_DELTA:
		return EventToolCallArgsDelta, nil
	case backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_FINISHED:
		return EventToolCallFinished, nil
	case backendpluginv1.EventKind_EVENT_KIND_USAGE_DELTA:
		return EventUsageDelta, nil
	case backendpluginv1.EventKind_EVENT_KIND_WARNING:
		return EventWarning, nil
	case backendpluginv1.EventKind_EVENT_KIND_ERROR:
		return EventError, nil
	case backendpluginv1.EventKind_EVENT_KIND_RESPONSE_FINISHED:
		return EventResponseFinished, nil
	case backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_IMAGE_REF:
		return EventAssistantImageRef, nil
	case backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_FILE_REF:
		return EventAssistantFileRef, nil
	default:
		return EventUnspecified, ErrUnknownEnum
	}
}

func eventKindToProto(v EventKind) (backendpluginv1.EventKind, error) {
	switch v {
	case EventResponseStarted:
		return backendpluginv1.EventKind_EVENT_KIND_RESPONSE_STARTED, nil
	case EventMessageStarted:
		return backendpluginv1.EventKind_EVENT_KIND_MESSAGE_STARTED, nil
	case EventTextDelta:
		return backendpluginv1.EventKind_EVENT_KIND_TEXT_DELTA, nil
	case EventReasoningDelta:
		return backendpluginv1.EventKind_EVENT_KIND_REASONING_DELTA, nil
	case EventReasoningSignatureDelta:
		return backendpluginv1.EventKind_EVENT_KIND_REASONING_SIGNATURE_DELTA, nil
	case EventReasoningOpaqueDelta:
		return backendpluginv1.EventKind_EVENT_KIND_REASONING_OPAQUE_DELTA, nil
	case EventToolCallStarted:
		return backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_STARTED, nil
	case EventToolCallArgsDelta:
		return backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_ARGS_DELTA, nil
	case EventToolCallFinished:
		return backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_FINISHED, nil
	case EventUsageDelta:
		return backendpluginv1.EventKind_EVENT_KIND_USAGE_DELTA, nil
	case EventWarning:
		return backendpluginv1.EventKind_EVENT_KIND_WARNING, nil
	case EventError:
		return backendpluginv1.EventKind_EVENT_KIND_ERROR, nil
	case EventResponseFinished:
		return backendpluginv1.EventKind_EVENT_KIND_RESPONSE_FINISHED, nil
	case EventAssistantImageRef:
		return backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_IMAGE_REF, nil
	case EventAssistantFileRef:
		return backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_FILE_REF, nil
	default:
		return backendpluginv1.EventKind_EVENT_KIND_UNSPECIFIED, ErrUnknownEnum
	}
}

func terminalStatusFromProto(v backendpluginv1.TerminalStatus) (TerminalStatus, error) {
	switch v {
	case backendpluginv1.TerminalStatus_TERMINAL_STATUS_SUCCESS:
		return TerminalSuccess, nil
	case backendpluginv1.TerminalStatus_TERMINAL_STATUS_FAILURE:
		return TerminalFailure, nil
	case backendpluginv1.TerminalStatus_TERMINAL_STATUS_CANCELLED:
		return TerminalCancelled, nil
	default:
		return TerminalUnspecified, ErrUnknownEnum
	}
}

func terminalStatusToProto(v TerminalStatus) (backendpluginv1.TerminalStatus, error) {
	switch v {
	case TerminalSuccess:
		return backendpluginv1.TerminalStatus_TERMINAL_STATUS_SUCCESS, nil
	case TerminalFailure:
		return backendpluginv1.TerminalStatus_TERMINAL_STATUS_FAILURE, nil
	case TerminalCancelled:
		return backendpluginv1.TerminalStatus_TERMINAL_STATUS_CANCELLED, nil
	default:
		return backendpluginv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED, ErrUnknownEnum
	}
}

func capabilityFromProto(p *backendpluginv1.CapabilitySummary) CapabilitySummary {
	if p == nil {
		return CapabilitySummary{}
	}
	return CapabilitySummary{
		Streaming:         p.GetStreaming(),
		Tools:             p.GetTools(),
		Vision:            p.GetVision(),
		Documents:         p.GetDocuments(),
		StructuredOutputs: p.GetStructuredOutputs(),
		Reasoning:         p.GetReasoning(),
		ReasoningReplay:   p.GetReasoningReplay(),
		ParallelToolCalls: p.GetParallelToolCalls(),
	}
}

func capabilityToProto(c CapabilitySummary) *backendpluginv1.CapabilitySummary {
	return &backendpluginv1.CapabilitySummary{
		Streaming:         c.Streaming,
		Tools:             c.Tools,
		Vision:            c.Vision,
		Documents:         c.Documents,
		StructuredOutputs: c.StructuredOutputs,
		Reasoning:         c.Reasoning,
		ReasoningReplay:   c.ReasoningReplay,
		ParallelToolCalls: c.ParallelToolCalls,
	}
}

func transportCapabilityFromProto(p *backendpluginv1.TransportCapabilitySummary) TransportCapabilitySummary {
	if p == nil {
		return TransportCapabilitySummary{}
	}
	return TransportCapabilitySummary{
		Keepalive:           p.GetKeepalive(),
		Cancellation:        p.GetCancellation(),
		BidirectionalStream: p.GetBidirectionalStream(),
	}
}

func transportCapabilityToProto(c TransportCapabilitySummary) *backendpluginv1.TransportCapabilitySummary {
	return &backendpluginv1.TransportCapabilitySummary{
		Keepalive:           c.Keepalive,
		Cancellation:        c.Cancellation,
		BidirectionalStream: c.BidirectionalStream,
	}
}

func featuresFromProto(in []*backendpluginv1.Feature) []Feature {
	if len(in) == 0 {
		return nil
	}
	out := make([]Feature, len(in))
	for i, f := range in {
		if f == nil {
			continue
		}
		out[i] = Feature{Name: f.GetName(), Required: f.GetRequired()}
	}
	return out
}

func featuresToProto(in []Feature) []*backendpluginv1.Feature {
	if len(in) == 0 {
		return nil
	}
	out := make([]*backendpluginv1.Feature, len(in))
	for i, f := range in {
		out[i] = &backendpluginv1.Feature{Name: f.Name, Required: f.Required}
	}
	return out
}

// UsageEvidenceFromProto converts wire usage evidence.
func UsageEvidenceFromProto(p *backendpluginv1.UsageEvidence) (UsageEvidence, error) {
	if p == nil {
		return UsageEvidence{RawUsageJSON: RawJSONAbsentValue()}, nil
	}
	raw, err := RawJSONFromProto(p.GetRawUsageJson())
	if err != nil {
		return UsageEvidence{}, err
	}
	u := UsageEvidence{
		InputTokens:      optInt64(p.InputTokens),
		OutputTokens:     optInt64(p.OutputTokens),
		CacheReadTokens:  optInt64(p.CacheReadTokens),
		CacheWriteTokens: optInt64(p.CacheWriteTokens),
		ReasoningTokens:  optInt64(p.ReasoningTokens),
		TotalTokens:      optInt64(p.TotalTokens),
		Presence:         UsagePresenceFromProto(p.GetPresence()),
		RawUsageJSON:     raw,
	}
	if err := u.ValidatePresence(); err != nil {
		return UsageEvidence{}, err
	}
	return u, nil
}

// UsageEvidenceToProto encodes usage evidence.
func UsageEvidenceToProto(u UsageEvidence) (*backendpluginv1.UsageEvidence, error) {
	if err := u.ValidatePresence(); err != nil {
		return nil, err
	}
	return &backendpluginv1.UsageEvidence{
		InputTokens:      optInt64(u.InputTokens),
		OutputTokens:     optInt64(u.OutputTokens),
		CacheReadTokens:  optInt64(u.CacheReadTokens),
		CacheWriteTokens: optInt64(u.CacheWriteTokens),
		ReasoningTokens:  optInt64(u.ReasoningTokens),
		TotalTokens:      optInt64(u.TotalTokens),
		Presence:         UsagePresenceToProto(u.Presence),
		RawUsageJson:     RawJSONToProto(u.RawUsageJSON),
	}, nil
}

// PluginErrorFromProto converts a classified error.
func PluginErrorFromProto(p *backendpluginv1.PluginError) (*PluginError, error) {
	if p == nil {
		return nil, nil
	}
	code, err := errorCodeFromProto(p.GetCode())
	if err != nil {
		return nil, err
	}
	return &PluginError{
		Code:            code,
		Message:         p.GetMessage(),
		Retryable:       p.GetRetryable(),
		OutputCommitted: p.GetOutputCommitted(),
	}, nil
}

// PluginErrorToProto encodes a classified error.
func PluginErrorToProto(e *PluginError) (*backendpluginv1.PluginError, error) {
	if e == nil {
		return nil, nil
	}
	code, err := errorCodeToProto(e.Code)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.PluginError{
		Code:            code,
		Message:         e.Message,
		Retryable:       e.Retryable,
		OutputCommitted: e.OutputCommitted,
	}, nil
}

func partFromProto(p *backendpluginv1.Part) (Part, error) {
	if p == nil {
		return Part{}, ErrInvalidInvocation
	}
	kind, err := partKindFromProto(p.GetKind())
	if err != nil {
		return Part{}, err
	}
	raw, err := RawJSONFromProto(p.GetToolArgsJson())
	if err != nil {
		return Part{}, err
	}
	return Part{
		Kind:          kind,
		Text:          optString(p.Text),
		ImageRef:      optString(p.ImageRef),
		FileRef:       optString(p.FileRef),
		ReasoningText: optString(p.ReasoningText),
		ToolArgsJSON:  raw,
		ToolCallID:    optString(p.ToolCallId),
		ToolName:      optString(p.ToolName),
	}, nil
}

func partToProto(p Part) (*backendpluginv1.Part, error) {
	kind, err := partKindToProto(p.Kind)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.Part{
		Kind:          kind,
		Text:          optString(p.Text),
		ImageRef:      optString(p.ImageRef),
		FileRef:       optString(p.FileRef),
		ReasoningText: optString(p.ReasoningText),
		ToolArgsJson:  RawJSONToProto(p.ToolArgsJSON),
		ToolCallId:    optString(p.ToolCallID),
		ToolName:      optString(p.ToolName),
	}, nil
}

func messageFromProto(m *backendpluginv1.Message) (Message, error) {
	if m == nil {
		return Message{}, ErrInvalidInvocation
	}
	role, err := roleFromProto(m.GetRole())
	if err != nil {
		return Message{}, err
	}
	parts := make([]Part, 0, len(m.GetParts()))
	for _, p := range m.GetParts() {
		part, err := partFromProto(p)
		if err != nil {
			return Message{}, err
		}
		parts = append(parts, part)
	}
	return Message{Role: role, Parts: parts}, nil
}

func messageToProto(m Message) (*backendpluginv1.Message, error) {
	role, err := roleToProto(m.Role)
	if err != nil {
		return nil, err
	}
	parts := make([]*backendpluginv1.Part, 0, len(m.Parts))
	for _, p := range m.Parts {
		pp, err := partToProto(p)
		if err != nil {
			return nil, err
		}
		parts = append(parts, pp)
	}
	return &backendpluginv1.Message{Role: role, Parts: parts}, nil
}

func toolDefFromProto(t *backendpluginv1.ToolDef) (ToolDef, error) {
	if t == nil {
		return ToolDef{}, ErrInvalidInvocation
	}
	raw, err := RawJSONFromProto(t.GetParametersJson())
	if err != nil {
		return ToolDef{}, err
	}
	return ToolDef{Name: t.GetName(), Description: t.GetDescription(), ParametersJSON: raw}, nil
}

func toolDefToProto(t ToolDef) (*backendpluginv1.ToolDef, error) {
	return &backendpluginv1.ToolDef{
		Name:           t.Name,
		Description:    t.Description,
		ParametersJson: RawJSONToProto(t.ParametersJSON),
	}, nil
}

func generationOptionsFromProto(p *backendpluginv1.GenerationOptions) (GenerationOptions, error) {
	if p == nil {
		return GenerationOptions{ResponseSchemaJSON: RawJSONAbsentValue()}, nil
	}
	raw, err := RawJSONFromProto(p.GetResponseSchemaJson())
	if err != nil {
		return GenerationOptions{}, err
	}
	return GenerationOptions{
		MaxOutputTokens:    optUint32(p.MaxOutputTokens),
		TemperatureMillis:  optInt32(p.TemperatureMillis),
		ReasoningEffort:    optString(p.ReasoningEffort),
		ParallelToolCalls:  optBool(p.ParallelToolCalls),
		ResponseMIMEType:   optString(p.ResponseMimeType),
		ResponseSchemaJSON: raw,
	}, nil
}

func generationOptionsToProto(o GenerationOptions) (*backendpluginv1.GenerationOptions, error) {
	return &backendpluginv1.GenerationOptions{
		MaxOutputTokens:    optUint32(o.MaxOutputTokens),
		TemperatureMillis:  optInt32(o.TemperatureMillis),
		ReasoningEffort:    optString(o.ReasoningEffort),
		ParallelToolCalls:  optBool(o.ParallelToolCalls),
		ResponseMimeType:   optString(o.ResponseMIMEType),
		ResponseSchemaJson: RawJSONToProto(o.ResponseSchemaJSON),
	}, nil
}

// InvocationFromProto converts a wire invocation.
func InvocationFromProto(p *backendpluginv1.Invocation) (Invocation, error) {
	if p == nil {
		return Invocation{}, ErrInvalidInvocation
	}
	opts, err := generationOptionsFromProto(p.GetOptions())
	if err != nil {
		return Invocation{}, err
	}
	inv := Invocation{
		RequestID:        p.GetRequestId(),
		AttemptID:        p.GetAttemptId(),
		ALegID:           p.GetALegId(),
		BLegID:           p.GetBLegId(),
		CanonicalModelID: p.GetCanonicalModelId(),
		NativeModelID:    p.GetNativeModelId(),
		ToolChoice:       optString(p.ToolChoice),
		Options:          opts,
		SafeMetadata:     p.GetSafeMetadata(),
	}
	for _, m := range p.GetInstructions() {
		msg, err := messageFromProto(m)
		if err != nil {
			return Invocation{}, err
		}
		inv.Instructions = append(inv.Instructions, msg)
	}
	for _, m := range p.GetMessages() {
		msg, err := messageFromProto(m)
		if err != nil {
			return Invocation{}, err
		}
		inv.Messages = append(inv.Messages, msg)
	}
	for _, t := range p.GetTools() {
		td, err := toolDefFromProto(t)
		if err != nil {
			return Invocation{}, err
		}
		inv.Tools = append(inv.Tools, td)
	}
	if err := inv.Validate(); err != nil {
		return Invocation{}, err
	}
	return inv, nil
}

// InvocationToProto encodes an invocation.
func InvocationToProto(inv Invocation) (*backendpluginv1.Invocation, error) {
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	opts, err := generationOptionsToProto(inv.Options)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.Invocation{
		RequestId:        inv.RequestID,
		AttemptId:        inv.AttemptID,
		ALegId:           inv.ALegID,
		BLegId:           inv.BLegID,
		CanonicalModelId: inv.CanonicalModelID,
		NativeModelId:    inv.NativeModelID,
		ToolChoice:       optString(inv.ToolChoice),
		Options:          opts,
		SafeMetadata:     inv.SafeMetadata,
	}
	for _, m := range inv.Instructions {
		pm, err := messageToProto(m)
		if err != nil {
			return nil, err
		}
		out.Instructions = append(out.Instructions, pm)
	}
	for _, m := range inv.Messages {
		pm, err := messageToProto(m)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, pm)
	}
	for _, t := range inv.Tools {
		pt, err := toolDefToProto(t)
		if err != nil {
			return nil, err
		}
		out.Tools = append(out.Tools, pt)
	}
	return out, nil
}

func factoryFromProto(p *backendpluginv1.FactoryDescriptor) (FactoryDescriptor, error) {
	if p == nil {
		return FactoryDescriptor{}, ErrInvalidDescriptor
	}
	cm, err := credentialModeFromProto(p.GetCredentialMode())
	if err != nil {
		return FactoryDescriptor{}, err
	}
	as, err := accessScopeFromProto(p.GetAccessScope())
	if err != nil {
		return FactoryDescriptor{}, err
	}
	ps, err := processSharingFromProto(p.GetProcessSharing())
	if err != nil {
		return FactoryDescriptor{}, err
	}
	return FactoryDescriptor{
		Kind:                      p.GetKind(),
		DisplayName:               p.GetDisplayName(),
		Description:               p.GetDescription(),
		CredentialMode:            cm,
		AccessScope:               as,
		RoutePrefixes:             append([]string(nil), p.GetRoutePrefixes()...),
		SupportsCountTokens:       p.GetSupportsCountTokens(),
		SupportsFinalizeBilling:   p.GetSupportsFinalizeBilling(),
		SupportsDynamicInventory:  p.GetSupportsDynamicInventory(),
		SupportsModelAwareProfile: p.GetSupportsModelAwareProfile(),
		ProcessSharing:            ps,
		Experimental:              p.GetExperimental(),
		Deprecated:                p.GetDeprecated(),
		StaticCapabilities:        capabilityFromProto(p.GetStaticCapabilities()),
		TransportCapabilities:     transportCapabilityFromProto(p.GetTransportCapabilities()),
	}, nil
}

func factoryToProto(f FactoryDescriptor) (*backendpluginv1.FactoryDescriptor, error) {
	cm, err := credentialModeToProto(f.CredentialMode)
	if err != nil {
		return nil, err
	}
	as, err := accessScopeToProto(f.AccessScope)
	if err != nil {
		return nil, err
	}
	ps, err := processSharingToProto(f.ProcessSharing)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.FactoryDescriptor{
		Kind:                      f.Kind,
		DisplayName:               f.DisplayName,
		Description:               f.Description,
		CredentialMode:            cm,
		AccessScope:               as,
		RoutePrefixes:             append([]string(nil), f.RoutePrefixes...),
		SupportsCountTokens:       f.SupportsCountTokens,
		SupportsFinalizeBilling:   f.SupportsFinalizeBilling,
		SupportsDynamicInventory:  f.SupportsDynamicInventory,
		SupportsModelAwareProfile: f.SupportsModelAwareProfile,
		ProcessSharing:            ps,
		Experimental:              f.Experimental,
		Deprecated:                f.Deprecated,
		StaticCapabilities:        capabilityToProto(f.StaticCapabilities),
		TransportCapabilities:     transportCapabilityToProto(f.TransportCapabilities),
	}, nil
}

// PluginDescriptorFromProto converts a wire descriptor.
func PluginDescriptorFromProto(p *backendpluginv1.PluginDescriptor) (PluginDescriptor, error) {
	if p == nil {
		return PluginDescriptor{}, ErrInvalidDescriptor
	}
	d := PluginDescriptor{
		ProtocolMajor: p.GetProtocolMajor(),
		ProtocolMinor: p.GetProtocolMinor(),
		PluginID:      p.GetPluginId(),
		Version:       p.GetVersion(),
		BuildID:       p.GetBuildId(),
		Features:      featuresFromProto(p.GetFeatures()),
	}
	for _, f := range p.GetFactories() {
		fd, err := factoryFromProto(f)
		if err != nil {
			return PluginDescriptor{}, err
		}
		d.Factories = append(d.Factories, fd)
	}
	if err := d.Validate(); err != nil {
		return PluginDescriptor{}, err
	}
	return d, nil
}

// PluginDescriptorToProto encodes a descriptor.
func PluginDescriptorToProto(d PluginDescriptor) (*backendpluginv1.PluginDescriptor, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	out := &backendpluginv1.PluginDescriptor{
		ProtocolMajor: d.ProtocolMajor,
		ProtocolMinor: d.ProtocolMinor,
		PluginId:      d.PluginID,
		Version:       d.Version,
		BuildId:       d.BuildID,
		Features:      featuresToProto(d.Features),
	}
	for _, f := range d.Factories {
		fp, err := factoryToProto(f)
		if err != nil {
			return nil, err
		}
		out.Factories = append(out.Factories, fp)
	}
	return out, nil
}

// ResolvedProfileFromProto converts a resolved profile.
func ResolvedProfileFromProto(p *backendpluginv1.ResolvedProfile) (ResolvedProfile, error) {
	if p == nil {
		return ResolvedProfile{}, ErrInvalidInvocation
	}
	return ResolvedProfile{
		Capabilities:             capabilityFromProto(p.GetCapabilities()),
		TransportCapabilities:    transportCapabilityFromProto(p.GetTransportCapabilities()),
		ReasoningReplaySupported: p.GetReasoningReplaySupported(),
		RoutePrefixes:            append([]string(nil), p.GetRoutePrefixes()...),
		EnforceMaxOutput:         p.GetEnforceMaxOutput(),
		MaxOutputTokens:          optUint32(p.MaxOutputTokens),
		SupportsCountTokens:      p.GetSupportsCountTokens(),
		SupportsFinalizeBilling:  p.GetSupportsFinalizeBilling(),
		SupportsDynamicInventory: p.GetSupportsDynamicInventory(),
		EvidenceSource:           p.GetEvidenceSource(),
		ProfileVersion:           p.GetProfileVersion(),
	}, nil
}

// ResolvedProfileToProto encodes a resolved profile.
func ResolvedProfileToProto(p ResolvedProfile) *backendpluginv1.ResolvedProfile {
	return &backendpluginv1.ResolvedProfile{
		Capabilities:             capabilityToProto(p.Capabilities),
		TransportCapabilities:    transportCapabilityToProto(p.TransportCapabilities),
		ReasoningReplaySupported: p.ReasoningReplaySupported,
		RoutePrefixes:            append([]string(nil), p.RoutePrefixes...),
		EnforceMaxOutput:         p.EnforceMaxOutput,
		MaxOutputTokens:          optUint32(p.MaxOutputTokens),
		SupportsCountTokens:      p.SupportsCountTokens,
		SupportsFinalizeBilling:  p.SupportsFinalizeBilling,
		SupportsDynamicInventory: p.SupportsDynamicInventory,
		EvidenceSource:           p.EvidenceSource,
		ProfileVersion:           p.ProfileVersion,
	}
}

// ListModelsResponseFromProto converts inventory.
func ListModelsResponseFromProto(p *backendpluginv1.ListModelsResponse) (ListModelsResponse, error) {
	if p == nil {
		return ListModelsResponse{}, nil
	}
	out := ListModelsResponse{
		InventorySource:    p.GetInventorySource(),
		FetchedUnixMS:      p.GetFetchedUnixMs(),
		RefreshAfterUnixMS: optInt64(p.RefreshAfterUnixMs),
		ErrorCode:          p.GetErrorCode(),
	}
	for _, m := range p.GetModels() {
		if m == nil {
			continue
		}
		out.Models = append(out.Models, ModelDescriptor{
			CanonicalModelID: m.GetCanonicalModelId(),
			NativeModelID:    m.GetNativeModelId(),
			DisplayName:      m.GetDisplayName(),
			RoutePrefix:      m.GetRoutePrefix(),
			FactoryKind:      m.GetFactoryKind(),
			Capabilities:     capabilityFromProto(m.GetCapabilities()),
		})
	}
	if err := out.Validate(DefaultMaxModelsPerResponse); err != nil {
		return ListModelsResponse{}, err
	}
	return out, nil
}

// ListModelsResponseToProto encodes inventory.
func ListModelsResponseToProto(r ListModelsResponse) *backendpluginv1.ListModelsResponse {
	out := &backendpluginv1.ListModelsResponse{
		InventorySource:    r.InventorySource,
		FetchedUnixMs:      r.FetchedUnixMS,
		RefreshAfterUnixMs: optInt64(r.RefreshAfterUnixMS),
		ErrorCode:          r.ErrorCode,
	}
	for _, m := range r.Models {
		out.Models = append(out.Models, &backendpluginv1.ModelDescriptor{
			CanonicalModelId: m.CanonicalModelID,
			NativeModelId:    m.NativeModelID,
			DisplayName:      m.DisplayName,
			RoutePrefix:      m.RoutePrefix,
			FactoryKind:      m.FactoryKind,
			Capabilities:     capabilityToProto(m.Capabilities),
		})
	}
	return out
}

// CountTokensResponseFromProto converts count results.
func CountTokensResponseFromProto(p *backendpluginv1.CountTokensResponse) (CountTokensResponse, error) {
	if p == nil {
		return CountTokensResponse{}, nil
	}
	return CountTokensResponse{
		InputTokens:     optInt64(p.InputTokens),
		Presence:        UsagePresenceFromProto(p.GetPresence()),
		EvidenceQuality: p.GetEvidenceQuality(),
	}, nil
}

// CountTokensResponseToProto encodes count results.
func CountTokensResponseToProto(r CountTokensResponse) *backendpluginv1.CountTokensResponse {
	return &backendpluginv1.CountTokensResponse{
		InputTokens:     optInt64(r.InputTokens),
		Presence:        UsagePresenceToProto(r.Presence),
		EvidenceQuality: r.EvidenceQuality,
	}
}

// FinalizeBillingResponseFromProto converts finalize results.
func FinalizeBillingResponseFromProto(p *backendpluginv1.FinalizeBillingResponse) (FinalizeBillingResponse, error) {
	if p == nil {
		return FinalizeBillingResponse{}, nil
	}
	usage, err := UsageEvidenceFromProto(p.GetUsage())
	if err != nil {
		return FinalizeBillingResponse{}, err
	}
	return FinalizeBillingResponse{Usage: usage, EvidenceQuality: p.GetEvidenceQuality()}, nil
}

// FinalizeBillingResponseToProto encodes finalize results.
func FinalizeBillingResponseToProto(r FinalizeBillingResponse) (*backendpluginv1.FinalizeBillingResponse, error) {
	usage, err := UsageEvidenceToProto(r.Usage)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.FinalizeBillingResponse{Usage: usage, EvidenceQuality: r.EvidenceQuality}, nil
}

// CancelOutcomeFromProto converts cancel outcomes.
func CancelOutcomeFromProto(p *backendpluginv1.CancelOutcome) (*CancelOutcome, error) {
	if p == nil {
		return nil, nil
	}
	reason, err := cancelReasonFromProto(p.GetReason())
	if err != nil {
		return nil, err
	}
	return &CancelOutcome{Acknowledged: p.GetAcknowledged(), Detail: p.GetDetail(), Reason: reason}, nil
}

// CancelOutcomeToProto encodes cancel outcomes.
func CancelOutcomeToProto(c *CancelOutcome) (*backendpluginv1.CancelOutcome, error) {
	if c == nil {
		return nil, nil
	}
	reason, err := cancelReasonToProto(c.Reason)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.CancelOutcome{Acknowledged: c.Acknowledged, Detail: c.Detail, Reason: reason}, nil
}

// TerminalFromProto converts terminals.
func TerminalFromProto(p *backendpluginv1.Terminal) (*Terminal, error) {
	if p == nil {
		return nil, nil
	}
	status, err := terminalStatusFromProto(p.GetStatus())
	if err != nil {
		return nil, err
	}
	pe, err := PluginErrorFromProto(p.GetError())
	if err != nil {
		return nil, err
	}
	return &Terminal{Status: status, Error: pe}, nil
}

// TerminalToProto encodes terminals.
func TerminalToProto(t *Terminal) (*backendpluginv1.Terminal, error) {
	if t == nil {
		return nil, nil
	}
	status, err := terminalStatusToProto(t.Status)
	if err != nil {
		return nil, err
	}
	pe, err := PluginErrorToProto(t.Error)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.Terminal{Status: status, Error: pe}, nil
}

// CanonicalEventFromProto converts stream events.
func CanonicalEventFromProto(p *backendpluginv1.CanonicalEvent) (*CanonicalEvent, error) {
	if p == nil {
		return nil, nil
	}
	kind, err := eventKindFromProto(p.GetKind())
	if err != nil {
		return nil, err
	}
	var usage *UsageEvidence
	if p.Usage != nil {
		u, err := UsageEvidenceFromProto(p.GetUsage())
		if err != nil {
			return nil, err
		}
		usage = &u
	}
	pe, err := PluginErrorFromProto(p.GetError())
	if err != nil {
		return nil, err
	}
	return &CanonicalEvent{
		Kind:         kind,
		MessageIndex: optInt32(p.MessageIndex),
		Delta:        optString(p.Delta),
		Signature:    optString(p.Signature),
		Opaque:       append([]byte(nil), p.GetOpaque()...),
		ToolCallID:   optString(p.ToolCallId),
		ToolName:     optString(p.ToolName),
		Usage:        usage,
		Warning:      optString(p.Warning),
		Error:        pe,
		ImageRef:     optString(p.ImageRef),
		FileRef:      optString(p.FileRef),
	}, nil
}

// CanonicalEventToProto encodes stream events.
func CanonicalEventToProto(e *CanonicalEvent) (*backendpluginv1.CanonicalEvent, error) {
	if e == nil {
		return nil, nil
	}
	kind, err := eventKindToProto(e.Kind)
	if err != nil {
		return nil, err
	}
	var usage *backendpluginv1.UsageEvidence
	if e.Usage != nil {
		usage, err = UsageEvidenceToProto(*e.Usage)
		if err != nil {
			return nil, err
		}
	}
	pe, err := PluginErrorToProto(e.Error)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.CanonicalEvent{
		Kind:         kind,
		MessageIndex: optInt32(e.MessageIndex),
		Delta:        optString(e.Delta),
		Signature:    optString(e.Signature),
		Opaque:       append([]byte(nil), e.Opaque...),
		ToolCallId:   optString(e.ToolCallID),
		ToolName:     optString(e.ToolName),
		Usage:        usage,
		Warning:      optString(e.Warning),
		Error:        pe,
		ImageRef:     optString(e.ImageRef),
		FileRef:      optString(e.FileRef),
	}, nil
}

// ClientFrameFromProto converts host-to-plugin frames.
func ClientFrameFromProto(p *backendpluginv1.ExecuteClientFrame) (ClientFrame, error) {
	if p == nil {
		return ClientFrame{}, ErrInvalidFrame
	}
	kind, err := clientFrameKindFromProto(p.GetKind())
	if err != nil {
		return ClientFrame{}, err
	}
	frame := ClientFrame{
		Kind:                 kind,
		Sequence:             p.GetSequence(),
		InstanceID:           p.GetInstanceId(),
		CancelDeadlineUnixMS: p.GetCancelDeadlineUnixMs(),
	}
	if p.CancelReason != backendpluginv1.CancelReason_CANCEL_REASON_UNSPECIFIED {
		reason, err := cancelReasonFromProto(p.GetCancelReason())
		if err != nil {
			return ClientFrame{}, err
		}
		frame.CancelReason = reason
	}
	if p.Invocation != nil {
		inv, err := InvocationFromProto(p.GetInvocation())
		if err != nil {
			return ClientFrame{}, err
		}
		frame.Invocation = &inv
	}
	if err := frame.ValidateShape(); err != nil {
		return ClientFrame{}, err
	}
	return frame, nil
}

// ClientFrameToProto encodes host-to-plugin frames.
func ClientFrameToProto(f ClientFrame) (*backendpluginv1.ExecuteClientFrame, error) {
	if err := f.ValidateShape(); err != nil {
		return nil, err
	}
	kind, err := clientFrameKindToProto(f.Kind)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.ExecuteClientFrame{
		Kind:                 kind,
		Sequence:             f.Sequence,
		InstanceId:           f.InstanceID,
		CancelDeadlineUnixMs: f.CancelDeadlineUnixMS,
	}
	if f.CancelReason != CancelReasonUnspecified {
		reason, err := cancelReasonToProto(f.CancelReason)
		if err != nil {
			return nil, err
		}
		out.CancelReason = reason
	}
	if f.Invocation != nil {
		inv, err := InvocationToProto(*f.Invocation)
		if err != nil {
			return nil, err
		}
		out.Invocation = inv
	}
	return out, nil
}

// ServerFrameFromProto converts plugin-to-host frames.
func ServerFrameFromProto(p *backendpluginv1.ExecuteServerFrame) (ServerFrame, error) {
	if p == nil {
		return ServerFrame{}, ErrInvalidFrame
	}
	kind, err := serverFrameKindFromProto(p.GetKind())
	if err != nil {
		return ServerFrame{}, err
	}
	ev, err := CanonicalEventFromProto(p.GetEvent())
	if err != nil {
		return ServerFrame{}, err
	}
	co, err := CancelOutcomeFromProto(p.GetCancelOutcome())
	if err != nil {
		return ServerFrame{}, err
	}
	term, err := TerminalFromProto(p.GetTerminal())
	if err != nil {
		return ServerFrame{}, err
	}
	frame := ServerFrame{
		Kind:          kind,
		Sequence:      p.GetSequence(),
		Event:         ev,
		Diagnostic:    p.GetDiagnostic(),
		CancelOutcome: co,
		Terminal:      term,
	}
	if err := frame.ValidateShape(); err != nil {
		return ServerFrame{}, err
	}
	return frame, nil
}

// ServerFrameToProto encodes plugin-to-host frames.
func ServerFrameToProto(f ServerFrame) (*backendpluginv1.ExecuteServerFrame, error) {
	if err := f.ValidateShape(); err != nil {
		return nil, err
	}
	kind, err := serverFrameKindToProto(f.Kind)
	if err != nil {
		return nil, err
	}
	ev, err := CanonicalEventToProto(f.Event)
	if err != nil {
		return nil, err
	}
	co, err := CancelOutcomeToProto(f.CancelOutcome)
	if err != nil {
		return nil, err
	}
	term, err := TerminalToProto(f.Terminal)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.ExecuteServerFrame{
		Kind:          kind,
		Sequence:      f.Sequence,
		Event:         ev,
		Diagnostic:    f.Diagnostic,
		CancelOutcome: co,
		Terminal:      term,
	}, nil
}

// RuntimePolicyFromProto converts runtime policy.
func RuntimePolicyFromProto(p *backendpluginv1.RuntimePolicy) RuntimePolicy {
	if p == nil {
		return RuntimePolicy{}
	}
	return RuntimePolicy{
		MaxRequestBytes:         p.GetMaxRequestBytes(),
		MaxStreamFrameBytes:     p.GetMaxStreamFrameBytes(),
		MaxPendingEvents:        p.GetMaxPendingEvents(),
		RequestTimeoutMS:        p.GetRequestTimeoutMs(),
		CancelDeadlineMS:        p.GetCancelDeadlineMs(),
		DiagnosticsVerbosity:    p.GetDiagnosticsVerbosity(),
		MaxConcurrentExecutions: p.GetMaxConcurrentExecutions(),
		LocalOnly:               p.GetLocalOnly(),
		AllowedEnvNames:         append([]string(nil), p.GetAllowedEnvNames()...),
		DisableTransportRetries: p.GetDisableTransportRetries(),
	}
}

// RuntimePolicyToProto encodes runtime policy.
func RuntimePolicyToProto(p RuntimePolicy) *backendpluginv1.RuntimePolicy {
	return &backendpluginv1.RuntimePolicy{
		MaxRequestBytes:         p.MaxRequestBytes,
		MaxStreamFrameBytes:     p.MaxStreamFrameBytes,
		MaxPendingEvents:        p.MaxPendingEvents,
		RequestTimeoutMs:        p.RequestTimeoutMS,
		CancelDeadlineMs:        p.CancelDeadlineMS,
		DiagnosticsVerbosity:    p.DiagnosticsVerbosity,
		MaxConcurrentExecutions: p.MaxConcurrentExecutions,
		LocalOnly:               p.LocalOnly,
		AllowedEnvNames:         append([]string(nil), p.AllowedEnvNames...),
		DisableTransportRetries: p.DisableTransportRetries,
	}
}

// ConfigureRequestFromProto converts configure wire input.
// Negotiation is domain-only and must be supplied by the host after Negotiate.
func ConfigureRequestFromProto(p *backendpluginv1.ConfigureRequest, neg Negotiation) (ConfigureRequest, error) {
	if p == nil {
		return ConfigureRequest{}, ErrInvalidInvocation
	}
	secrets := SecretBundle{}
	if p.GetSecrets() != nil {
		secrets.Values = make(map[string][]byte, len(p.GetSecrets().GetValues()))
		for k, v := range p.GetSecrets().GetValues() {
			secrets.Values[k] = append([]byte(nil), v...)
		}
	}
	req := ConfigureRequest{
		InstanceID:       p.GetInstanceId(),
		FactoryKind:      p.GetFactoryKind(),
		ConfigYAML:       append([]byte(nil), p.GetConfigYaml()...),
		Secrets:          secrets,
		RuntimePolicy:    RuntimePolicyFromProto(p.GetRuntimePolicy()),
		Negotiation:      neg,
		NegotiationToken: p.GetNegotiationToken(),
	}
	if err := req.Validate(); err != nil {
		return ConfigureRequest{}, err
	}
	return req, nil
}

// ConfigureRequestToProto encodes configure wire fields (Negotiation is not on the wire).
func ConfigureRequestToProto(r ConfigureRequest) *backendpluginv1.ConfigureRequest {
	var secrets *backendpluginv1.SecretBundle
	if len(r.Secrets.Values) > 0 {
		secrets = &backendpluginv1.SecretBundle{Values: make(map[string][]byte, len(r.Secrets.Values))}
		for k, v := range r.Secrets.Values {
			secrets.Values[k] = append([]byte(nil), v...)
		}
	}
	return &backendpluginv1.ConfigureRequest{
		InstanceId:       r.InstanceID,
		FactoryKind:      r.FactoryKind,
		ConfigYaml:       append([]byte(nil), r.ConfigYAML...),
		Secrets:          secrets,
		RuntimePolicy:    RuntimePolicyToProto(r.RuntimePolicy),
		NegotiationToken: r.NegotiationToken,
	}
}

// CountTokensRequestFromProto converts count requests.
func CountTokensRequestFromProto(p *backendpluginv1.CountTokensRequest) (CountTokensRequest, error) {
	if p == nil {
		return CountTokensRequest{}, ErrInvalidInvocation
	}
	inv, err := InvocationFromProto(p.GetInvocation())
	if err != nil {
		return CountTokensRequest{}, err
	}
	return CountTokensRequest{InstanceID: p.GetInstanceId(), ModelID: p.GetModelId(), Invocation: inv}, nil
}

// CountTokensRequestToProto encodes count requests.
func CountTokensRequestToProto(r CountTokensRequest) (*backendpluginv1.CountTokensRequest, error) {
	inv, err := InvocationToProto(r.Invocation)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.CountTokensRequest{InstanceId: r.InstanceID, ModelId: r.ModelID, Invocation: inv}, nil
}

// FinalizeBillingRequestFromProto converts finalize requests.
func FinalizeBillingRequestFromProto(p *backendpluginv1.FinalizeBillingRequest) (FinalizeBillingRequest, error) {
	if p == nil {
		return FinalizeBillingRequest{}, ErrInvalidInvocation
	}
	req := FinalizeBillingRequest{
		InstanceID:     p.GetInstanceId(),
		ALegID:         p.GetALegId(),
		BLegID:         p.GetBLegId(),
		ModelID:        p.GetModelId(),
		Reason:         p.GetReason(),
		IdempotencyKey: p.GetIdempotencyKey(),
	}
	if err := req.Validate(); err != nil {
		return FinalizeBillingRequest{}, err
	}
	return req, nil
}

// FinalizeBillingRequestToProto encodes finalize requests.
func FinalizeBillingRequestToProto(r FinalizeBillingRequest) *backendpluginv1.FinalizeBillingRequest {
	return &backendpluginv1.FinalizeBillingRequest{
		InstanceId:     r.InstanceID,
		ALegId:         r.ALegID,
		BLegId:         r.BLegID,
		ModelId:        r.ModelID,
		Reason:         r.Reason,
		IdempotencyKey: r.IdempotencyKey,
	}
}

// HealthResponseFromProto converts health.
func HealthResponseFromProto(p *backendpluginv1.HealthResponse) HealthResponse {
	if p == nil {
		return HealthResponse{}
	}
	return HealthResponse{Serving: p.GetServing(), Detail: p.GetDetail()}
}

// HealthResponseToProto encodes health.
func HealthResponseToProto(h HealthResponse) *backendpluginv1.HealthResponse {
	return &backendpluginv1.HealthResponse{Serving: h.Serving, Detail: h.Detail}
}

// GracefulShutdownFromProto converts shutdown request/response.
func GracefulShutdownRequestFromProto(p *backendpluginv1.GracefulShutdownRequest) GracefulShutdownRequest {
	if p == nil {
		return GracefulShutdownRequest{}
	}
	return GracefulShutdownRequest{DrainTimeoutMS: p.GetDrainTimeoutMs()}
}

// GracefulShutdownRequestToProto encodes shutdown request.
func GracefulShutdownRequestToProto(r GracefulShutdownRequest) *backendpluginv1.GracefulShutdownRequest {
	return &backendpluginv1.GracefulShutdownRequest{DrainTimeoutMs: r.DrainTimeoutMS}
}

// GracefulShutdownResponseFromProto converts shutdown response.
func GracefulShutdownResponseFromProto(p *backendpluginv1.GracefulShutdownResponse) GracefulShutdownResponse {
	if p == nil {
		return GracefulShutdownResponse{}
	}
	return GracefulShutdownResponse{Accepted: p.GetAccepted()}
}

// GracefulShutdownResponseToProto encodes shutdown response.
func GracefulShutdownResponseToProto(r GracefulShutdownResponse) *backendpluginv1.GracefulShutdownResponse {
	return &backendpluginv1.GracefulShutdownResponse{Accepted: r.Accepted}
}
