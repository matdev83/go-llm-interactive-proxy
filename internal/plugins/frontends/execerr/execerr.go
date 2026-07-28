// Package execerr classifies errors returned from lipsdk.ExecutorView.Execute for HTTP frontends.
package execerr

import (
	"errors"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
)

// InternalWireMessage is the stable response body for 5xx executor failures (no upstream/raw detail).
const InternalWireMessage = "internal error"

// UnknownExecuteErrorMessage is the wire-safe message when ClassifyExecute is called with a nil error.
// Callers should pass only non-nil errors from ExecutorView.Execute; the nil case exists for defensive completeness.
const UnknownExecuteErrorMessage = "unknown error"

// ContextLimitExceededWireMessage is returned for [lipapi.ErrAllCandidatesContextLimitExceeded] (HTTP 413).
const ContextLimitExceededWireMessage = "request exceeds context limits for all configured model routes"

// PolicyDeniedWireMessage is the stable client-safe wire message for policy denials when the
// decision carries no client message (requirement 5.4).
const PolicyDeniedWireMessage = "request denied by policy"

// PolicyFailureWireMessage is the stable client-safe wire message for policy decision provider
// failures handled through fail-closed behavior (requirement 6.1).
const PolicyFailureWireMessage = "policy decision unavailable"

// PolicyMalformedWireMessage is the stable client-safe wire message for malformed policy
// decisions (requirements 1.5, 6.6).
const PolicyMalformedWireMessage = "policy decision was malformed"

// ClientRejectWireMessage is the stable client-safe wire message for capability rejects
// whose normalized reason is empty (control-only or whitespace input).
const ClientRejectWireMessage = "request rejected"

type Kind int

const (
	KindUnspecified Kind = iota
	KindClientReject
	KindInternalError
	// KindSessionDenial is a pre-backend secure-session denial with a stable public code and HTTP status.
	KindSessionDenial
	// KindPolicyDenied is a policy denial before backend output or an active-stream policy denial
	// after output (requirement 5.1). Distinct from capability, session, backend, and internal errors.
	KindPolicyDenied
	// KindPolicyFailure is a fail-closed policy decision provider failure (requirement 6.1).
	KindPolicyFailure
	// KindPolicyMalformed is a malformed policy decision: unknown stage, outcome, effect, or illegal
	// outcome/effect pair (requirements 1.5, 6.6).
	KindPolicyMalformed
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
	if lipapi.IsPolicyDenied(err) {
		return Outcome{
			Kind:    KindPolicyDenied,
			Status:  http.StatusForbidden,
			Message: clientSafePolicyMessage(err, PolicyDeniedWireMessage),
			Err:     err,
		}
	}
	if lipapi.IsPolicyFailure(err) {
		return Outcome{
			Kind:    KindPolicyFailure,
			Status:  http.StatusServiceUnavailable,
			Message: clientSafePolicyMessage(err, PolicyFailureWireMessage),
			Err:     err,
		}
	}
	if lipapi.IsPolicyMalformed(err) {
		return Outcome{
			Kind:    KindPolicyMalformed,
			Status:  http.StatusInternalServerError,
			Message: clientSafePolicyMessage(err, PolicyMalformedWireMessage),
			Err:     err,
		}
	}
	if lipapi.IsReject(err) {
		msg := ClientRejectWireMessage
		var rej *lipapi.RejectError
		if errors.As(err, &rej) && rej != nil {
			// Bound the reject's own reason, never the wrapped chain text: err.Error()
			// would leak wrapping layers (upstream hosts, tokens, executor internals)
			// to the wire. Same normalizer as the prerequest.IsRejected branch below.
			if bounded := lipapi.NormalizeClientMessage(rej.Error()); bounded != "" {
				msg = bounded
			}
		}
		return Outcome{Kind: KindClientReject, Status: http.StatusBadRequest, Message: msg, Err: err}
	}
	if prerequest.IsRejected(err) {
		msg := "request denied"
		var re *prerequest.RejectError
		if errors.As(err, &re) && re != nil && re.Message != "" {
			// Defense in depth: a bare RejectError not wrapped in a PolicyDecisionError
			// still reaches the wire here. Bound plugin-controlled text with the same
			// canonical normalizer used at PolicyDecisionError construction so the wire
			// path can never emit an unbounded or control-laden deny message.
			if bounded := lipapi.NormalizeClientMessage(re.Message); bounded != "" {
				msg = bounded
			}
		}
		return Outcome{Kind: KindClientReject, Status: http.StatusForbidden, Message: msg, Err: err}
	}
	if lipapi.IsAllCandidatesContextLimitExceeded(err) {
		return Outcome{
			Kind:    KindClientReject,
			Status:  http.StatusRequestEntityTooLarge,
			Message: ContextLimitExceededWireMessage,
			Err:     err,
		}
	}
	return Outcome{Kind: KindInternalError, Status: http.StatusInternalServerError, Message: InternalWireMessage, Err: err}
}

// OpenAIWireErrorType maps HTTP status to OpenAI-compatible error.type strings for frontend adapters.
func OpenAIWireErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusServiceUnavailable, http.StatusInternalServerError:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

// clientSafePolicyMessage returns the client-safe message from a policy decision error, falling
// back to defaultMsg when the error carries no safe message. It never renders Cause, provider IDs,
// stage, raw prompts, or secrets (requirement 5.4); PolicyDecisionError.ClientMessage is the only
// policy field intended for wire rendering.
func clientSafePolicyMessage(err error, defaultMsg string) string {
	if pde := lipapi.PolicyDecisionErrorFrom(err); pde != nil {
		if msg := strings.TrimSpace(pde.ClientMessage); msg != "" {
			return msg
		}
	}
	return defaultMsg
}
