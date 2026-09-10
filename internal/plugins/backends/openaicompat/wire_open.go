package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/endpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// ResolveEndpoint maps a base URL and flavor to an absolute provider target URL.
// It validates the base URL with [endpoint.ParseBaseURL] and applies the canonical
// suffix via [endpoint.Descriptor.Join].
func ResolveEndpoint(baseURL string, flavor Flavor) (string, error) {
	d, err := endpoint.ParseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	op := endpoint.OperationOpenAIChatCompletions
	if flavor == FlavorResponses {
		op = endpoint.OperationOpenAIResponses
	}
	return d.Join(op)
}

// BuildOutboundHeaders constructs backend-owned outbound HTTP request headers.
// It sets required Content-Type, Accept, and Authorization (if secret is non-empty).
// Extra headers are sanitized to strip hop-by-hop framing, stale length/encoding,
// and frontend session/control headers (Requirement 12.2).
func BuildOutboundHeaders(apiSecret string, streaming bool, extraHeaders http.Header) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if streaming {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	if secret := strings.TrimSpace(apiSecret); secret != "" {
		h.Set("Authorization", "Bearer "+secret)
	}
	if extraHeaders != nil {
		for k, vv := range extraHeaders {
			if isRestrictedOutboundHeader(k) {
				continue
			}
			for _, v := range vv {
				h.Add(k, v)
			}
		}
	}
	return h
}

func isRestrictedOutboundHeader(key string) bool {
	canonical := http.CanonicalHeaderKey(key)
	switch canonical {
	// Hop-by-hop headers:
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailers", "Transfer-Encoding", "Upgrade":
		return true
	// Transport / body framing headers (caller/writer controls these):
	case "Content-Length", "Content-Encoding", "Expect":
		return true
	// Backend auth is strictly owned:
	case "Authorization":
		return true
	// Frontend session / control headers must not leak upstream:
	case "X-Session-Id", "X-Resume-Token", "X-Aleg-Id", "X-Bleg-Id",
		"X-Lip-Session-Id", "X-Lip-Resume-Token":
		return true
	default:
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-session-") || strings.HasPrefix(lower, "x-resume-") {
			return true
		}
		return false
	}
}

// ResolveHTTPClient returns client when non-nil; otherwise returns [httpclient.Standard]
// ensuring default TLS, proxy, HTTP/2, and timeout tuning (Requirement 12.1).
func ResolveHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return httpclient.Standard()
}

// PeekFirstEvent peeks the first event from a managed stream and returns a prepended
// stream. If the first Recv fails, the stream is closed and the error returned.
func PeekFirstEvent(ctx context.Context, es lipapi.ManagedEventStream) (lipapi.ManagedEventStream, error) {
	return streampeek.PeekFirst(ctx, es)
}

// HTTPError captures an upstream HTTP response failure (>= 400).
// It implements openaicred.HTTPStatusError so error classification works uniformly.
type HTTPError struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
	ProviderID string
}

func (e *HTTPError) Error() string {
	msg := strings.TrimSpace(string(e.Body))
	if msg == "" {
		msg = e.Status
	}
	if e.ProviderID != "" {
		return fmt.Sprintf("%s: upstream HTTP %d: %s", e.ProviderID, e.StatusCode, msg)
	}
	return fmt.Sprintf("upstream HTTP %d: %s", e.StatusCode, msg)
}

func (e *HTTPError) HTTPStatusCode() int {
	return e.StatusCode
}

func (e *HTTPError) HTTPHeader() http.Header {
	return e.Header
}

var _ openaicred.HTTPStatusError = (*HTTPError)(nil)

// ParseStreamResponse parses an HTTP response into a managed event stream.
// On status >= 400, it reads a bounded error body and returns an [*HTTPError].
func ParseStreamResponse(providerID string, resp *http.Response, flavor Flavor, maxPending int) (lipapi.ManagedEventStream, error) {
	if resp == nil {
		return nil, errors.New("openaicompat: nil http response")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Header:     resp.Header,
			Body:       body,
			ProviderID: providerID,
		}
	}
	dec := ssestream.NewDecoder(resp)
	switch flavor {
	case FlavorResponses:
		s := ssestream.NewStream[responses.ResponseStreamEventUnion](dec, nil)
		return NewResponsesStream(providerID, s, maxPending), nil
	default:
		s := ssestream.NewStream[openai.ChatCompletionChunk](dec, nil)
		return NewChatStream(providerID, s, maxPending), nil
	}
}

// WireOpenPrimitives bundles backend-owned transport configuration and helpers
// for executing wire requests without duplicating credential, endpoint, client,
// or response stream parsing logic (Requirements 10, 12).
type WireOpenPrimitives struct {
	ProviderID        string
	BaseURL           string
	Flavor            Flavor
	Pool              *credpool.Pool
	HTTPClient        *http.Client
	RateLimitFallback time.Duration
	MaxPending        int
}

// ResolveURL resolves the target endpoint URL.
func (p WireOpenPrimitives) ResolveURL() (string, error) {
	return ResolveEndpoint(p.BaseURL, p.Flavor)
}

// BuildHeaders constructs the outbound headers for an attempt.
func (p WireOpenPrimitives) BuildHeaders(apiSecret string, streaming bool) http.Header {
	return BuildOutboundHeaders(apiSecret, streaming, nil)
}

// Client returns the effective HTTP client.
func (p WireOpenPrimitives) Client() *http.Client {
	return ResolveHTTPClient(p.HTTPClient)
}

// Execute executes openFn within the credential-rotation loop.
func (p WireOpenPrimitives) Execute(
	ctx context.Context,
	openFn func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error),
) (lipapi.ManagedEventStream, error) {
	return openaicred.ExecuteWithCredentialPool(ctx, p.ProviderID, p.Pool, p.RateLimitFallback, openFn)
}

// ParseAndPeekStream parses an HTTP response stream and peeks the first event.
func (p WireOpenPrimitives) ParseAndPeekStream(ctx context.Context, resp *http.Response) (lipapi.ManagedEventStream, error) {
	es, err := ParseStreamResponse(p.ProviderID, resp, p.Flavor, p.MaxPending)
	if err != nil {
		return nil, err
	}
	return PeekFirstEvent(ctx, es)
}
