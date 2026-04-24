// Package execerr classifies errors returned from lipsdk.ExecutorView.Execute for HTTP frontends.
package execerr

import (
	"errors"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// InternalWireMessage is the stable response body for 5xx executor failures (no upstream/raw detail).
const InternalWireMessage = "internal error"

// UnknownExecuteErrorMessage is the wire-safe message when ClassifyExecute is called with a nil error.
// Callers should pass only non-nil errors from ExecutorView.Execute; the nil case exists for defensive completeness.
const UnknownExecuteErrorMessage = "unknown error"

type Kind int

const (
	KindUnspecified Kind = iota
	KindClientReject
	KindInternalError
	// KindSessionDenial is a pre-backend secure-session denial with a stable public code and HTTP status.
	KindSessionDenial
)

type Outcome struct {
	Kind    Kind
	Status  int
	Message string // safe to return to clients on the wire
	// Err is the original error for server-side logging. It is set for KindClientReject and KindInternalError
	// when err != nil; nil only when ClassifyExecute was called with a nil error (see [UnknownExecuteErrorMessage]).
	Err error
	// SessionPublicCode is the lipapi session denial code string when Kind == KindSessionDenial; otherwise empty.
	SessionPublicCode string
}

// ClassifyExecute maps an executor error to HTTP-facing outcome metadata.
// err must be non-nil in normal use (the value returned from Execute). If err is nil, the result is
// InternalError with Message [UnknownExecuteErrorMessage] and Err set to nil—treat that as a programming mistake,
// not a signal that the upstream call succeeded.
func ClassifyExecute(err error) Outcome {
	if err == nil {
		return Outcome{Kind: KindInternalError, Status: http.StatusInternalServerError, Message: UnknownExecuteErrorMessage, Err: nil}
	}
	if lipapi.IsSessionDenial(err) {
		code := lipapi.SessionDenialPublicCode(err)
		var sd *lipapi.SessionDenialError
		msg := "session denied"
		if errors.As(err, &sd) && sd != nil {
			msg = sd.Error()
		}
		var c lipapi.SessionDenialCode
		if code != "" {
			c = lipapi.SessionDenialCode(code)
		}
		return Outcome{
			Kind:              KindSessionDenial,
			Status:            sessionwire.HTTPStatusForSessionDenial(c),
			Message:           msg,
			Err:               err,
			SessionPublicCode: code,
		}
	}
	if lipapi.IsReject(err) {
		return Outcome{Kind: KindClientReject, Status: http.StatusBadRequest, Message: err.Error(), Err: err}
	}
	return Outcome{Kind: KindInternalError, Status: http.StatusInternalServerError, Message: InternalWireMessage, Err: err}
}

// OpenAIWireErrorType maps HTTP status to OpenAI-compatible error.type strings for frontend adapters.
func OpenAIWireErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusServiceUnavailable:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}
