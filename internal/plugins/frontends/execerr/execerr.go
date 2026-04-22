// Package execerr classifies errors returned from lipsdk.ExecutorView.Execute for HTTP frontends.
package execerr

import (
	"net/http"

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
	ClientReject
	InternalError
)

type Outcome struct {
	Kind    Kind
	Status  int
	Message string // safe to return to clients on the wire
	// Err is the original error for server-side logging. It is set for ClientReject and InternalError
	// when err != nil; nil only when ClassifyExecute was called with a nil error (see [UnknownExecuteErrorMessage]).
	Err error
}

// ClassifyExecute maps an executor error to HTTP-facing outcome metadata.
// err must be non-nil in normal use (the value returned from Execute). If err is nil, the result is
// InternalError with Message [UnknownExecuteErrorMessage] and Err set to nil—treat that as a programming mistake,
// not a signal that the upstream call succeeded.
func ClassifyExecute(err error) Outcome {
	if err == nil {
		return Outcome{Kind: InternalError, Status: http.StatusInternalServerError, Message: UnknownExecuteErrorMessage, Err: nil}
	}
	if lipapi.IsReject(err) {
		return Outcome{Kind: ClientReject, Status: http.StatusBadRequest, Message: err.Error(), Err: err}
	}
	return Outcome{Kind: InternalError, Status: http.StatusInternalServerError, Message: InternalWireMessage, Err: err}
}
