package sessionclassification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	featurestate "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
)

// This file owns the transport half of the Jev adapter: the failure vocabulary a
// caller classifies, the construction of the decider from one validated generation
// posture, and the single bounded request the decider makes. Every wire detail it
// uses lives in jev_wire.go, and nothing here widens the remote port or the SDK:
// the adapter implements featurestate.RemoteDecider and speaks only the port's own
// types across the boundary (requirements 6.1, 6.7, 6.9, 7.1, 7.5, 11.5;
// design.md 701-714).
//
// A remote failure never fails the user request. Every failure path returns a
// bounded error together with the zero decision, so an unknown session stays
// unknown and the caller's request continues (requirements 4.4, 6.9, 12.8).

// JevFailure is the bounded remote-failure vocabulary this adapter reports. It is
// deliberately free of vendor values, request details, and body content, so a
// caller can decide whether an attempt is worth repeating without inspecting the
// error text (requirements 6.7, 6.9, 7.5).
type JevFailure string

const (
	// JevFailureCredentialMissing reports that the referenced credential is
	// absent or unusable. Repeating the call cannot help without an operator
	// change (requirement 8.3).
	JevFailureCredentialMissing JevFailure = "credential_missing"
	// JevFailureInputRefused reports derived evidence outside the port's bounded
	// contract. It is refused before any request exists (requirements 6.7, 7.2).
	JevFailureInputRefused JevFailure = "input_refused"
	// JevFailureRequestRefused reports a request that could not be built within
	// the egress bounds (requirements 6.7, 7.2).
	JevFailureRequestRefused JevFailure = "request_refused"
	// JevFailureRateLimited reports the vendor's documented 429. The attempt
	// budget in the generation decides whether it is retried (requirements 6.7,
	// 12.8).
	JevFailureRateLimited JevFailure = "rate_limited"
	// JevFailureServerUnavailable reports any 5xx, including the vendor's
	// documented 529 Overloaded (requirements 6.7, 6.9).
	JevFailureServerUnavailable JevFailure = "server_unavailable"
	// JevFailureRequestRejected reports a non-retryable vendor refusal such as
	// the documented 422 Unprocessable Entity (requirements 6.7, 6.9).
	JevFailureRequestRejected JevFailure = "request_rejected"
	// JevFailureRedirectRefused reports a redirect response. The adapter never
	// follows one, so the bearer header is never forwarded to another origin
	// (requirements 6.7, 7.1).
	JevFailureRedirectRefused JevFailure = "redirect_refused"
	// JevFailureMalformedResponse reports a response that is missing a documented
	// member, carries the wrong answer type, or reports a probability the port
	// cannot threshold (requirements 6.9, 12.8).
	JevFailureMalformedResponse JevFailure = "malformed_response"
	// JevFailureResponseOversized reports a response above the read bound. It is
	// a refusal, never a partial accept (requirements 6.7, 7.2).
	JevFailureResponseOversized JevFailure = "response_oversized"
	// JevFailureTimeout reports the configured hard timeout firing (requirement
	// 6.7).
	JevFailureTimeout JevFailure = "timeout"
	// JevFailureCanceled reports the caller's context ending. Caller
	// cancellation stays authoritative (requirement 6.9).
	JevFailureCanceled JevFailure = "canceled"
	// JevFailureTransport reports a dial, TLS, or read failure (requirement 6.9).
	JevFailureTransport JevFailure = "transport_failure"
)

// JevError is the single remote-failure shape this adapter returns. The kind is
// bounded and vendor-free; the cause is either an adapter-written reason or a
// transport error, so no credential and no raw vendor body can appear in it
// (requirements 7.1, 7.5).
type JevError struct {
	// Kind is the bounded failure classification.
	Kind JevFailure
	// Err is the cause, reachable through errors.Is and errors.As.
	Err error
}

func (e *JevError) Error() string {
	return fmt.Sprintf("%s: jev %s: %v", featurestate.ID, e.Kind, e.Err)
}

// Unwrap exposes the cause so a caller can still test for context.DeadlineExceeded,
// context.Canceled, or the port's sentinels.
func (e *JevError) Unwrap() error { return e.Err }

// JevFailureKindOf reports the bounded failure kind carried by err, or the empty
// kind when err did not come from this adapter. It lets a caller classify a
// failure without reading error text (requirements 6.7, 7.5).
func JevFailureKindOf(err error) JevFailure {
	var failure *JevError
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return ""
}

// jevDecider is the one RemoteDecider implementation. Every field is immutable
// after construction, so concurrent turns share it safely, and no credential value
// is stored: only the environment-variable name the generation validated
// (requirements 6.7, 7.1, 8.3).
type jevDecider struct {
	endpoint      string
	model         string
	credentialEnv string
	timeout       time.Duration
	client        *http.Client
}

// JevOption configures one adapter. Options exist because the vendor endpoint is
// a live early-access URL: the default below is the documented one, and an
// alternative must be validated as strictly as the default.
type JevOption func(*jevDecider) error

// withJevEndpoint overrides the documented vendor endpoint. The value is validated
// as an absolute, credential-free https origin, or plaintext http on loopback for
// a hermetic test endpoint, and it is never echoed into a diagnostic
// (requirements 6.7, 7.1, 8.4).
//
// Unexported on purpose: it exists so the hermetic httptest suites in this
// package can aim the adapter at a loopback server, and it must never be sourced
// from operator configuration. Exporting it would permanently offer any in-repo
// caller a validated way to aim the bearer credential at an arbitrary https
// origin, which is not a capability this feature needs. An operator who needs a
// different endpoint should be routed through a separately reviewed change.
func withJevEndpoint(endpoint string) JevOption {
	return func(decider *jevDecider) error {
		if err := jevValidateEndpoint(endpoint); err != nil {
			return fmt.Errorf("%w: %w", featurestate.ErrRemoteNotConfigured, err)
		}
		decider.endpoint = endpoint
		return nil
	}
}

// NewJevDecider builds the Jev adapter for one validated generation posture. It is
// constructed only for the modes that require a remote decision, so a heuristic
// generation can never hold a live endpoint, and the hard timeout it enforces is
// the generation's own validated timeout (requirements 6.1, 6.2, 6.3, 6.4, 6.7,
// 6.10; design.md 703).
func NewJevDecider(policy featurestate.RemotePolicy, options ...JevOption) (featurestate.RemoteDecider, error) {
	if policy.Mode() != featurestate.ModeJev && policy.Mode() != featurestate.ModeHybrid {
		return nil, fmt.Errorf("%w: mode %q never constructs a jev decider",
			featurestate.ErrRemoteNotConfigured, policy.Mode())
	}
	settings := policy.Config()
	decider := &jevDecider{
		endpoint:      jevEndpointDefault,
		model:         jevModelLatest,
		credentialEnv: policy.CredentialReference(),
		timeout:       settings.Timeout,
		client:        jevNewClient(),
	}
	for _, option := range options {
		if err := option(decider); err != nil {
			return nil, err
		}
	}
	return decider, nil
}

// jevNewClient builds the one HTTP client the adapter uses. It is the repository's
// explicit outbound client, not the package-global default, and it refuses every
// redirect: a followed redirect would aim the bearer header at an origin the
// operator never configured, and Go forwards credentials on a same-origin redirect
// too (requirements 6.7, 7.1). The per-call context deadline, not the client's own
// timeout, is what bounds a single attempt.
func jevNewClient() *http.Client {
	client := httpclient.Standard()
	client.CheckRedirect = jevRefuseRedirect
	return client
}

// jevRefuseRedirect stops the client from following a redirect and hands the
// redirect response back, so no second request and no forwarded Authorization
// header ever happens. The caller then classifies the 3xx as a refused redirect.
func jevRefuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Decide makes the single bounded remote decision for one turn. It refuses
// unbounded input before egress, resolves the referenced credential at call time,
// enforces the generation's hard timeout over the whole exchange, follows no
// redirect, reads at most the response bound, and returns either a validated
// decision or a bounded error (requirements 6.7, 6.9, 7.1, 7.2, 7.5).
func (d *jevDecider) Decide(ctx context.Context, in featurestate.RemoteInput) (featurestate.RemoteDecision, error) {
	if err := featurestate.ValidateRemoteInput(in); err != nil {
		return featurestate.RemoteDecision{}, &JevError{
			Kind: JevFailureInputRefused,
			Err:  fmt.Errorf("%w: %w", featurestate.ErrInvalidRemoteInput, err),
		}
	}
	credential, err := d.credential()
	if err != nil {
		return featurestate.RemoteDecision{}, err
	}
	body, err := jevMarshalRequest(in, d.model)
	if err != nil {
		return featurestate.RemoteDecision{}, err
	}
	attempt, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(attempt, http.MethodPost, d.endpoint, bytes.NewReader(body))
	if err != nil {
		return featurestate.RemoteDecision{}, &JevError{
			Kind: JevFailureRequestRefused,
			Err:  errors.New("the jev request could not be assembled"),
		}
	}
	request.Header.Set("Content-Type", jevContentType)
	request.Header.Set("Authorization", jevAuthorizationScheme+" "+credential)
	response, err := d.client.Do(request)
	if err != nil {
		return featurestate.RemoteDecision{}, jevTransportFailure(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return featurestate.RemoteDecision{}, jevStatusFailure(response.StatusCode)
	}
	payload, err := readJevBody(response.Body)
	if err != nil {
		return featurestate.RemoteDecision{}, err
	}
	return decodeJevResponse(payload)
}

// credential resolves the configured environment-variable reference at call time.
// The value is never stored, echoed, or returned; a missing or unusable value is a
// bounded credential failure that makes no request (requirements 7.1, 7.5, 8.3).
func (d *jevDecider) credential() (string, error) {
	value := os.Getenv(d.credentialEnv)
	if strings.TrimSpace(value) == "" {
		return "", &JevError{
			Kind: JevFailureCredentialMissing,
			Err:  errors.New("the configured jev credential reference resolves to no value"),
		}
	}
	return value, nil
}

// jevStatusFailure maps a non-success status onto the bounded failure vocabulary.
// The vendor's error body is never read or echoed, so nothing an endpoint puts in
// it can reach a diagnostic (requirements 6.7, 7.5, 12.8).
func jevStatusFailure(status int) error {
	return &JevError{
		Kind: jevStatusFailureKind(status),
		Err:  fmt.Errorf("the jev endpoint returned status %d", status),
	}
}

// jevStatusFailureKind classifies a non-success status. Only 200 is a success, so
// any other 2xx is also a refusal. The vendor's documented 429 is the rate-limit
// case and every 5xx, including the documented 529 Overloaded, is the
// server-unavailable case.
func jevStatusFailureKind(status int) JevFailure {
	switch {
	case status == http.StatusTooManyRequests:
		return JevFailureRateLimited
	case status >= http.StatusInternalServerError:
		return JevFailureServerUnavailable
	case status >= http.StatusMultipleChoices && status < http.StatusBadRequest && status != http.StatusNotModified:
		return JevFailureRedirectRefused
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return JevFailureCredentialMissing
	default:
		return JevFailureRequestRejected
	}
}

// jevTransportFailure classifies a transport error. The hard timeout and the
// caller's cancellation are distinguished so a caller can tell its own
// cancellation from the generation's budget running out (requirements 6.7, 6.9).
// jevTransportFailureKindFor classifies a transport-level error. A hard timeout
// and a caller cancellation must stay distinguishable all the way to the caller
// because requirements 9.2 and 9.3 label them differently.
func jevTransportFailureKindFor(err error) JevFailure {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return JevFailureTimeout
	case errors.Is(err, context.Canceled):
		return JevFailureCanceled
	default:
		return JevFailureTransport
	}
}

func jevTransportFailure(err error) error {
	return &JevError{Kind: jevTransportFailureKindFor(err), Err: err}
}

// readJevBody reads at most the response bound. It reads one byte past the bound so
// an oversized body is detected rather than silently truncated into a partial
// accept, and it returns nothing at all in that case (requirements 6.7, 7.2;
// design.md "Error Handling").
func readJevBody(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, jevMaxResponseBytes+1))
	if err != nil {
		// The configured hard timeout and the caller's cancellation can fire while
		// the body is still arriving, not only while the headers are pending. A
		// slow vendor streams after a 200, and that body-phase stall is the common
		// failure in practice, so the read error must reach jevTransportFailure
		// rather than being replaced by a fixed message. Otherwise the dominant
		// timeout is reported as a generic transport failure and loses its
		// context.DeadlineExceeded chain, and requirements 9.2/9.3 would label it
		// remote_error instead of remote_timeout. The fixed message is retained for
		// every non-context read error so no vendor body text can reach the error.
		if kind := jevTransportFailureKindFor(err); kind != JevFailureTransport {
			return nil, &JevError{Kind: kind, Err: err}
		}
		return nil, &JevError{
			Kind: JevFailureTransport,
			Err:  fmt.Errorf("the jev response body could not be read: %w", err),
		}
	}
	if len(payload) > jevMaxResponseBytes {
		return nil, &JevError{
			Kind: JevFailureResponseOversized,
			Err:  errors.New("the jev response body is above the bounded response size"),
		}
	}
	return payload, nil
}
