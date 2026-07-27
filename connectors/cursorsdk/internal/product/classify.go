package product

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type FailureCode string

const (
	CodeConfigInvalid         FailureCode = "cursor_sdk_config_invalid"
	CodeKeyMissing            FailureCode = "cursor_sdk_key_missing"
	CodeAuthFailed            FailureCode = "cursor_sdk_auth_failed"
	CodeBridgeMissing         FailureCode = "cursor_sdk_bridge_missing"
	CodeNodeMissing           FailureCode = "cursor_sdk_node_missing"
	CodeBridgeStartFailed     FailureCode = "cursor_sdk_bridge_start_failed"
	CodeBridgeIncompatible    FailureCode = "cursor_sdk_bridge_incompatible"
	CodeBridgeProtocol        FailureCode = "cursor_sdk_bridge_protocol"
	CodeBridgeExited          FailureCode = "cursor_sdk_bridge_exited"
	CodeModelUnknown          FailureCode = "cursor_sdk_model_unknown"
	CodeInventoryUnavailable  FailureCode = "cursor_sdk_inventory_unavailable"
	CodeCapabilityUnsupported FailureCode = "cursor_sdk_capability_unsupported"
	CodeAgentBusy             FailureCode = "cursor_sdk_agent_busy"
	CodeAgentLimit            FailureCode = "cursor_sdk_agent_limit"
	CodeAgentCreateFailed     FailureCode = "cursor_sdk_agent_create_failed"
	CodeRunFailed             FailureCode = "cursor_sdk_run_failed"
	CodeCancelTimeout         FailureCode = "cursor_sdk_cancel_timeout"
	CodeShutdownFailed        FailureCode = "cursor_sdk_shutdown_failed"
)

type ClassifiedFailure struct {
	Code        FailureCode
	Phase       lipapi.OutputPhase
	Recoverable bool
	SafeMessage string
	cause       error
}

func (e *ClassifiedFailure) Error() string {
	if e == nil {
		return "cursorsdk: classified failure"
	}
	if e.SafeMessage != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.SafeMessage)
	}
	return string(e.Code)
}

func (e *ClassifiedFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func ClassifyFailure(err error, committedOutput bool, apiKey string) *ClassifiedFailure {
	if err == nil {
		return nil
	}
	code, recoverableTransport := inspectFailureCode(err)
	phase := lipapi.PhasePreOutput
	if committedOutput {
		phase = lipapi.PhasePostOutput
	}
	recoverable := recoverableTransport && phase == lipapi.PhasePreOutput
	return &ClassifiedFailure{
		Code:        code,
		Phase:       phase,
		Recoverable: recoverable,
		SafeMessage: faultSafeMessage(err, code, apiKey),
		cause:       err,
	}
}

func MapOrchestrationError(cf *ClassifiedFailure) error {
	if cf == nil {
		return nil
	}
	if cf.Recoverable && cf.Phase == lipapi.PhasePreOutput {
		return lipapi.RecoverablePreOutputError(cf)
	}
	return cf
}

func ClassifyAndMap(err error, committedOutput bool, apiKey string) error {
	return MapOrchestrationError(ClassifyFailure(err, committedOutput, apiKey))
}

func faultSafeMessage(err error, code FailureCode, apiKey string) string {
	var bf *BridgeFault
	if errors.As(err, &bf) && bf != nil {
		base := string(code)
		if bf.Cause != nil {
			base = bf.Cause.Error()
		}
		if bf.Diag != "" {
			base = base + "; " + bf.Diag
		}
		msg := sanitizeWarningMessage(base, apiKey)
		msg = strings.TrimPrefix(msg, string(code)+": ")
		msg = strings.TrimPrefix(msg, "cursorsdk: ")
		return msg
	}
	msg := sanitizeWarningMessage(err.Error(), apiKey)
	msg = strings.TrimPrefix(msg, string(code)+": ")
	msg = strings.TrimPrefix(msg, "cursorsdk: ")
	return msg
}

func inspectFailureCode(err error) (FailureCode, bool) {
	if err == nil {
		return CodeRunFailed, false
	}

	var bf *BridgeFault
	if errors.As(err, &bf) && bf != nil && bf.Code != "" {
		return bf.Code, isTransientTransportCode(bf.Code)
	}

	var pe *protocol.ProtocolError
	if errors.As(err, &pe) && pe != nil {
		if code, ok := mapProtocolClass(pe.Class); ok {
			return code, false
		}
	}

	switch {
	case errors.Is(err, ErrBridgeExited):
		return CodeBridgeExited, true
	case errors.Is(err, ErrBridgeStartFailed):
		return CodeBridgeStartFailed, true
	case errors.Is(err, ErrBridgeMissing):
		return CodeBridgeMissing, false
	case errors.Is(err, ErrBridgeIncompatible):
		return CodeBridgeIncompatible, false
	case errors.Is(err, ErrBridgeProtocol):
		return CodeBridgeProtocol, false
	case errors.Is(err, ErrAgentBusy):
		return CodeAgentBusy, false
	case errors.Is(err, ErrAgentLimit), errors.Is(err, ErrRunLimit):
		return CodeAgentLimit, false
	case errors.Is(err, errCancelTimeout):
		return CodeCancelTimeout, false
	case errors.Is(err, os.ErrNotExist):
		return CodeBridgeMissing, false
	}

	return inspectFailureCodeText(err.Error())
}

func inspectFailureCodeText(raw string) (FailureCode, bool) {
	msg := strings.ToLower(raw)

	if code, ok := matchProtocolCodeToken(msg); ok {
		return code, false
	}

	switch {
	case strings.Contains(msg, "executable is empty"),
		strings.Contains(msg, "no such file"),
		strings.Contains(msg, "cannot find the file"),
		strings.Contains(msg, "the system cannot find"):
		return CodeBridgeMissing, false
	case strings.Contains(msg, "node") && (strings.Contains(msg, "not found") || strings.Contains(msg, "missing")):
		return CodeNodeMissing, false
	case strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "invalid_api_key"),
		strings.Contains(msg, "invalid api key"):
		if strings.Contains(msg, "missing") || strings.Contains(msg, "empty") {
			return CodeKeyMissing, false
		}
		return CodeAuthFailed, false
	case strings.Contains(msg, "model_unknown"),
		strings.Contains(msg, "unknown model"),
		strings.Contains(msg, "model not found"):
		return CodeModelUnknown, false
	case strings.Contains(msg, "capability_unsupported"),
		strings.Contains(msg, string(CodeCapabilityUnsupported)):
		return CodeCapabilityUnsupported, false
	case strings.Contains(msg, "cancel_timeout"),
		strings.Contains(msg, "cancel timed out"):
		return CodeCancelTimeout, false
	case strings.Contains(msg, "agent_busy"):
		return CodeAgentBusy, false
	case strings.Contains(msg, "agent_limit"),
		strings.Contains(msg, "run_limit"):
		return CodeAgentLimit, false
	case strings.Contains(msg, "config"):
		return CodeConfigInvalid, false
	default:
		return CodeRunFailed, false
	}
}

func matchProtocolCodeToken(msg string) (FailureCode, bool) {
	switch {
	case strings.Contains(msg, protocol.ErrorIncompatibleVersion):
		return CodeBridgeIncompatible, true
	case strings.Contains(msg, protocol.ErrorSequenceRegression),
		strings.Contains(msg, protocol.ErrorFrameTooLarge),
		strings.Contains(msg, protocol.ErrorInvalidJSON),
		strings.Contains(msg, protocol.ErrorUnknownEventKind),
		strings.Contains(msg, protocol.ErrorDuplicateTerminal),
		strings.Contains(msg, protocol.ErrorInvalidEvent),
		strings.Contains(msg, protocol.ErrorUnknownType),
		strings.Contains(msg, protocol.ErrorUnknownMethod),
		strings.Contains(msg, protocol.ErrorInvalidRequest),
		strings.Contains(msg, protocol.ErrorResponseMismatch),
		strings.Contains(msg, "bridge_protocol"),
		strings.Contains(msg, "run subscription buffer full"):
		return CodeBridgeProtocol, true
	default:
		return "", false
	}
}
