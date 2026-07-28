package frontendpipe

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
)

// MapAnthropicExecuteError maps executor outcomes to Anthropic wire error.type strings.
func MapAnthropicExecuteError(out execerr.Outcome) (status int, message, errType string) {
	status = out.Status
	message = out.Message
	if out.Kind == execerr.KindInternalError {
		message = execerr.InternalWireMessage
	}
	switch out.Kind {
	case execerr.KindSessionDenial:
		errType = execerr.OpenAIWireErrorType(out.Status)
	case execerr.KindClientReject:
		errType = "invalid_request_error"
	case execerr.KindPolicyDenied:
		errType = "permission_error"
	case execerr.KindPolicyFailure, execerr.KindPolicyMalformed:
		errType = "api_error"
	default:
		errType = "api_error"
	}
	return status, message, errType
}

// MapGeminiExecuteError maps executor outcomes to Gemini error.status strings.
func MapGeminiExecuteError(out execerr.Outcome) (status int, message, gstatus string) {
	status = out.Status
	message = out.Message
	if out.Kind == execerr.KindInternalError {
		message = execerr.InternalWireMessage
	}
	gstatus = MapGeminiWireStatus(out)
	return status, message, gstatus
}

// MapGeminiWireStatus returns the Google RPC status string for a classified executor outcome.
func MapGeminiWireStatus(out execerr.Outcome) string {
	switch out.Kind {
	case execerr.KindPolicyDenied:
		return "PERMISSION_DENIED"
	case execerr.KindPolicyFailure:
		return "UNAVAILABLE"
	case execerr.KindPolicyMalformed, execerr.KindInternalError:
		return "INTERNAL"
	case execerr.KindClientReject:
		switch out.Status {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
			return "INVALID_ARGUMENT"
		case http.StatusForbidden:
			return "PERMISSION_DENIED"
		default:
			return geminiStatusFromHTTP(out.Status)
		}
	case execerr.KindSessionDenial:
		return geminiStatusFromHTTP(out.Status)
	default:
		return geminiStatusFromHTTP(out.Status)
	}
}

func geminiStatusFromHTTP(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case status == http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case status == http.StatusForbidden:
		return "PERMISSION_DENIED"
	case status == http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case status >= 500:
		return "INTERNAL"
	default:
		return "UNKNOWN"
	}
}
