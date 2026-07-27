package protocol

import (
	"fmt"
	"slices"
)

var requiredMethods = []string{
	MethodInitialize,
	MethodHealth,
	MethodModelsList,
	MethodAgentCreate,
	MethodAgentSend,
	MethodRunCancel,
	MethodAgentDispose,
	MethodBridgeShutdown,
}

var eventKinds = []string{
	KindTextDelta,
	KindReasoningDelta,
	KindUsage,
	KindWarning,
	KindActivity,
	KindFinished,
	KindError,
}

var terminalKinds = []string{
	KindFinished,
	KindError,
}

// ProtocolError is a classified bridge protocol failure.
type ProtocolError struct {
	Class   string
	Message string
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "protocol error"
	}
	if e.Message == "" {
		return e.Class
	}
	return e.Class + ": " + e.Message
}

func protoErr(class, message string) *ProtocolError {
	return &ProtocolError{Class: class, Message: message}
}

// IsRequiredMethod reports whether method is a required bridge method.
func IsRequiredMethod(method string) bool {
	return slices.Contains(requiredMethods, method)
}

// RequiredMethods returns a copy of the required method list.
func RequiredMethods() []string {
	return slices.Clone(requiredMethods)
}

// IsEventKind reports whether kind is a required run event kind.
func IsEventKind(kind string) bool {
	return slices.Contains(eventKinds, kind)
}

// IsTerminalKind reports whether kind ends a run.
func IsTerminalKind(kind string) bool {
	return slices.Contains(terminalKinds, kind)
}

// EventKinds returns a copy of the required event kind list.
func EventKinds() []string {
	return slices.Clone(eventKinds)
}

// ValidateFrame checks a decoded frame for schema, type, and shape invariants.
// Unknown optional JSON fields are already ignored by decoding into Frame.
func ValidateFrame(f *Frame) error {
	if f == nil {
		return protoErr(ErrorInvalidJSON, "nil frame")
	}
	if f.SchemaVersion != SchemaVersion {
		return protoErr(ErrorIncompatibleVersion, fmt.Sprintf("schemaVersion=%d", f.SchemaVersion))
	}
	switch f.Type {
	case TypeRequest:
		return validateRequest(f)
	case TypeResponse:
		return validateResponse(f)
	case TypeEvent:
		return validateEvent(f)
	default:
		return protoErr(ErrorUnknownType, f.Type)
	}
}

func validateRequest(f *Frame) error {
	if f.ID == "" {
		return protoErr(ErrorInvalidRequest, "missing id")
	}
	if f.Method == "" {
		return protoErr(ErrorInvalidRequest, "missing method")
	}
	if !IsRequiredMethod(f.Method) {
		return protoErr(ErrorUnknownMethod, f.Method)
	}
	return nil
}

func validateResponse(f *Frame) error {
	if f.ID == "" {
		return protoErr(ErrorResponseMismatch, "missing id")
	}
	if f.Method != "" && !IsRequiredMethod(f.Method) {
		return protoErr(ErrorUnknownMethod, f.Method)
	}
	hasResult := len(f.Result) > 0
	hasError := f.Error != nil
	if !hasResult && !hasError {
		return protoErr(ErrorResponseMismatch, "missing result and error")
	}
	if hasResult && hasError {
		return protoErr(ErrorResponseMismatch, "result and error are mutually exclusive")
	}
	return nil
}

func validateEvent(f *Frame) error {
	if f.RunID == "" {
		return protoErr(ErrorInvalidEvent, "missing runId")
	}
	if f.Seq == nil {
		return protoErr(ErrorInvalidEvent, "missing seq")
	}
	if *f.Seq < 1 {
		return protoErr(ErrorInvalidEvent, "seq must be >= 1")
	}
	if f.Kind == "" {
		return protoErr(ErrorInvalidEvent, "missing kind")
	}
	if !IsEventKind(f.Kind) {
		return protoErr(ErrorUnknownEventKind, f.Kind)
	}
	return nil
}

// MatchResponse verifies a response correlates to an outstanding request.
func MatchResponse(req, resp *Frame) error {
	if req == nil || resp == nil {
		return protoErr(ErrorResponseMismatch, "nil request or response")
	}
	if err := ValidateFrame(req); err != nil {
		return err
	}
	if err := ValidateFrame(resp); err != nil {
		return err
	}
	if req.Type != TypeRequest {
		return protoErr(ErrorResponseMismatch, "expected request")
	}
	if resp.Type != TypeResponse {
		return protoErr(ErrorResponseMismatch, "expected response")
	}
	if req.ID != resp.ID {
		return protoErr(ErrorResponseMismatch, "id mismatch")
	}
	if resp.Method != "" && resp.Method != req.Method {
		return protoErr(ErrorResponseMismatch, "method mismatch")
	}
	return nil
}
