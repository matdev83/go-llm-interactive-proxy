// Package execerr classifies errors returned from lipsdk.ExecutorView.Execute for HTTP frontends.
package execerr

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// InternalWireMessage is the stable response body for 5xx executor failures (no upstream/raw detail).
const InternalWireMessage = "internal error"

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
	// when err != nil; nil only when ClassifyExecute(nil) was called.
	Err error
}

func ClassifyExecute(err error) Outcome {
	if err == nil {
		return Outcome{Kind: InternalError, Status: http.StatusInternalServerError, Message: "unknown error", Err: nil}
	}
	if lipapi.IsReject(err) {
		return Outcome{Kind: ClientReject, Status: http.StatusBadRequest, Message: err.Error(), Err: err}
	}
	return Outcome{Kind: InternalError, Status: http.StatusInternalServerError, Message: InternalWireMessage, Err: err}
}
