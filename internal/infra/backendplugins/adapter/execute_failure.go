package adapter

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExecuteFailureKind is the stable ExecuteSession failure classification.
type ExecuteFailureKind int

const (
	// ExecuteFailureProvider is a provider-declared error (terminal PluginError).
	ExecuteFailureProvider ExecuteFailureKind = iota + 1
	// ExecuteFailureCanceled is host/context or deadline cancellation.
	ExecuteFailureCanceled
	// ExecuteFailureTransportDeath is transport loss or process death.
	ExecuteFailureTransportDeath
	// ExecuteFailureProtocolViolation is an ABI/stream protocol violation.
	ExecuteFailureProtocolViolation
)

// ExecuteFailure is the classified Execute boundary error.
type ExecuteFailure struct {
	Kind            ExecuteFailureKind
	Err             error
	OutputCommitted bool
}

func (e *ExecuteFailure) Error() string {
	if e == nil {
		return "execute failure"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "execute failure"
}

func (e *ExecuteFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InvalidatesGeneration reports whether the failure requires generation invalidation.
func (e *ExecuteFailure) InvalidatesGeneration() bool {
	if e == nil {
		return false
	}
	return e.Kind == ExecuteFailureTransportDeath || e.Kind == ExecuteFailureProtocolViolation
}

// ToClassifiedError maps the failure to a sanitized ClassifiedError for core.
func (e *ExecuteFailure) ToClassifiedError() *ClassifiedError {
	if e == nil {
		return nil
	}
	code := "transport"
	retryable := !e.OutputCommitted
	switch e.Kind {
	case ExecuteFailureProvider:
		code = "provider"
		retryable = false
	case ExecuteFailureCanceled:
		code = "canceled"
	case ExecuteFailureProtocolViolation:
		code = "protocol"
	case ExecuteFailureTransportDeath:
		code = "transport"
	}
	msg := ""
	if e.Err != nil {
		msg = sanitizeDiagnosticText(e.Err.Error(), 256)
	}
	return &ClassifiedError{
		Code:            code,
		Message:         msg,
		Retryable:       retryable,
		OutputCommitted: e.OutputCommitted,
	}
}

// TransportDeath wraps err as transport/process death.
func TransportDeath(err error) *ExecuteFailure {
	return &ExecuteFailure{Kind: ExecuteFailureTransportDeath, Err: err}
}

// ProtocolViolation wraps err as an ABI protocol violation.
func ProtocolViolation(err error) *ExecuteFailure {
	return &ExecuteFailure{Kind: ExecuteFailureProtocolViolation, Err: err}
}

// ClassifyExecuteError classifies an ExecuteSession error at the openStream boundary.
func ClassifyExecuteError(err error, committed bool) *ExecuteFailure {
	if err == nil {
		return nil
	}
	var ef *ExecuteFailure
	if errors.As(err, &ef) {
		out := *ef
		out.OutputCommitted = committed
		if out.Err == nil {
			out.Err = err
		}
		return &out
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ExecuteFailure{Kind: ExecuteFailureCanceled, Err: err, OutputCommitted: committed}
	}
	if isProtocolSentinel(err) {
		return &ExecuteFailure{Kind: ExecuteFailureProtocolViolation, Err: err, OutputCommitted: committed}
	}
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		switch ce.Code {
		case "frame_too_large", "protocol":
			return &ExecuteFailure{Kind: ExecuteFailureProtocolViolation, Err: err, OutputCommitted: committed}
		case "provider":
			return &ExecuteFailure{Kind: ExecuteFailureProvider, Err: err, OutputCommitted: committed}
		case "canceled":
			return &ExecuteFailure{Kind: ExecuteFailureCanceled, Err: err, OutputCommitted: committed}
		}
	}
	var me backendplugin.ModeError
	if errors.As(err, &me) {
		return &ExecuteFailure{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Canceled:
			return &ExecuteFailure{Kind: ExecuteFailureCanceled, Err: err, OutputCommitted: committed}
		case codes.Unavailable, codes.DeadlineExceeded:
			return &ExecuteFailure{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
		case codes.Unknown, codes.Internal, codes.Aborted:
			if errors.Is(err, io.EOF) || strings.Contains(st.Message(), "EOF") || strings.Contains(st.Message(), "transport is closing") {
				return &ExecuteFailure{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
			}
		}
	}
	if errors.Is(err, io.EOF) {
		return &ExecuteFailure{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
	}
	return &ExecuteFailure{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
}

func isProtocolSentinel(err error) bool {
	return errors.Is(err, backendplugin.ErrMultipleTerminals) ||
		errors.Is(err, backendplugin.ErrEventAfterTerminal) ||
		errors.Is(err, backendplugin.ErrSequenceGap) ||
		errors.Is(err, backendplugin.ErrAcceptedRequired) ||
		errors.Is(err, backendplugin.ErrUnknownFrameKind) ||
		errors.Is(err, backendplugin.ErrInvalidFrame) ||
		errors.Is(err, backendplugin.ErrOversizedMessage) ||
		errors.Is(err, backendplugin.ErrUnknownEventKind) ||
		errors.Is(err, backendplugin.ErrUnknownEnum) ||
		errors.Is(err, backendplugin.ErrInvalidInvocation)
}
