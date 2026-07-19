package cursorsdk

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
)

var (
	ErrBridgeExited       = errors.New("cursor_sdk_bridge_exited")
	ErrBridgeStartFailed  = errors.New("cursor_sdk_bridge_start_failed")
	ErrBridgeMissing      = errors.New("cursor_sdk_bridge_missing")
	ErrBridgeProtocol     = errors.New("cursor_sdk_bridge_protocol")
	ErrBridgeIncompatible = errors.New("cursor_sdk_bridge_incompatible")
)

type BridgeFault struct {
	Code  FailureCode
	Cause error
	Diag  string
}

func (e *BridgeFault) Error() string {
	if e == nil {
		return "cursorsdk: bridge fault"
	}
	base := string(e.Code)
	if e.Cause != nil {
		base = e.Cause.Error()
	}
	if e.Diag == "" {
		return base
	}
	return base + "; " + e.Diag
}

func (e *BridgeFault) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewBridgeFault(code FailureCode, cause error, diag string) *BridgeFault {
	return &BridgeFault{Code: code, Cause: cause, Diag: diag}
}

func BridgeExited(waitErr error, diag string) *BridgeFault {
	cause := error(ErrBridgeExited)
	if waitErr != nil {
		cause = fmt.Errorf("%w: %v", ErrBridgeExited, waitErr)
	}
	return &BridgeFault{Code: CodeBridgeExited, Cause: cause, Diag: diag}
}

func BridgeStartTransient(cause error, diag string) *BridgeFault {
	root := error(ErrBridgeStartFailed)
	if cause != nil {
		root = fmt.Errorf("%w: %v", ErrBridgeStartFailed, cause)
	}
	return &BridgeFault{Code: CodeBridgeStartFailed, Cause: root, Diag: diag}
}

func BridgeProtocolFault(class, message string) *BridgeFault {
	code := CodeBridgeProtocol
	if class == protocol.ErrorIncompatibleVersion {
		code = CodeBridgeIncompatible
	}
	pe := &protocol.ProtocolError{Class: class, Message: message}
	return &BridgeFault{Code: code, Cause: pe, Diag: message}
}

func wrapStartError(err error) error {
	if err == nil {
		return nil
	}
	var bf *BridgeFault
	if errors.As(err, &bf) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) || isNotExistMessage(err.Error()) {
		return NewBridgeFault(CodeBridgeMissing, fmt.Errorf("%w: %w", ErrBridgeMissing, err), "")
	}
	if isTransientStartMessage(err.Error()) {
		return BridgeStartTransient(err, "")
	}
	return NewBridgeFault(CodeRunFailed, err, "")
}

func isNotExistMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "cannot find the file") ||
		strings.Contains(lower, "the system cannot find") ||
		strings.Contains(lower, "executable is empty")
}

func isTransientStartMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "spawn")
}

func isTransientTransportCode(code FailureCode) bool {
	switch code {
	case CodeBridgeExited, CodeBridgeStartFailed:
		return true
	default:
		return false
	}
}

func mapProtocolClass(class string) (FailureCode, bool) {
	switch class {
	case protocol.ErrorIncompatibleVersion:
		return CodeBridgeIncompatible, true
	case protocol.ErrorFrameTooLarge,
		protocol.ErrorInvalidJSON,
		protocol.ErrorUnknownType,
		protocol.ErrorUnknownMethod,
		protocol.ErrorInvalidRequest,
		protocol.ErrorResponseMismatch,
		protocol.ErrorInvalidEvent,
		protocol.ErrorUnknownEventKind,
		protocol.ErrorSequenceRegression,
		protocol.ErrorDuplicateTerminal:
		return CodeBridgeProtocol, true
	default:
		return CodeBridgeProtocol, class != ""
	}
}
