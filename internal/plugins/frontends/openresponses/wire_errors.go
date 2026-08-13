package openresponses

import (
	"errors"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
)

// WireErrors implements frontendpipe.WireErrors for OpenResponses envelopes.
type WireErrors struct{}

func (WireErrors) WriteBodyTooLarge(w http.ResponseWriter) error {
	writeWireError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "body_too_large", "Request body exceeds max limit")
	return nil
}

func (WireErrors) WriteReadBodyFailed(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Failed to read request body")
	return nil
}

func (WireErrors) WriteExecutorNotConfigured(w http.ResponseWriter) error {
	writeWireError(w, http.StatusNotImplemented, "invalid_request_error", "operation_not_implemented", "OpenResponses responses is not enabled")
	return nil
}

func (WireErrors) WritePreflightCanceled(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "client_closed_request", "Request canceled")
	return nil
}

func (WireErrors) WriteInvalidJSON(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid request")
	return nil
}

func (WireErrors) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error {
	writeWireError(w, d.Status, "server_error", "decode_admission_rejected", d.Message)
	return nil
}

func (WireErrors) WriteInvalidRequest(w http.ResponseWriter) error {
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid request")
	return nil
}

func (WireErrors) WriteEncodeFailed(w http.ResponseWriter) error {
	writeWireError(w, http.StatusInternalServerError, "server_error", "encode_failed", "Failed to encode response")
	return nil
}

func (WireErrors) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error {
	if out.Err != nil {
		status, typ, code, message := classifyExecutionError(out.Err)
		writeWireError(w, status, typ, code, message)
		return nil
	}
	writeWireError(w, out.Status, "server_error", "backend_error", "Backend execution failed")
	return nil
}

func (WireErrors) WriteHookError(w http.ResponseWriter, err error) error {
	var se *frontendpipe.StatusError
	if errors.As(err, &se) {
		writeWireError(w, se.HTTPStatus(), se.Type, se.Code, se.Message)
		return nil
	}
	writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid request")
	return nil
}
