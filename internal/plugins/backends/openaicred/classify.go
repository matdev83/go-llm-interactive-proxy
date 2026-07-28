package openaicred

import (
	"errors"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/transporterr"
	"github.com/openai/openai-go/v3"
)

// FailureKind classifies an OpenAI HTTP API error for credential-pool handling.
type FailureKind int

const (
	// FailureNone means the error is not a classified OpenAI API HTTP failure.
	FailureNone FailureKind = iota
	FailureRateLimited
	FailureAuthInvalid
	// FailureRetryable means a transient upstream (5xx, 408) or transport failure
	// (timeout, connection reset/refused) worth a pre-output failover.
	FailureRetryable
)

// ClassifyOpenAIAPIError inspects *openai.Error (including wrapped). On rate limit it
// returns the Retry-After header value when present (may be empty). Transport-level
// failures are classified via [transporterr.IsRetryable].
func ClassifyOpenAIAPIError(err error) (kind FailureKind, retryAfter string) {
	var apiErr *openai.Error
	if err == nil {
		return FailureNone, ""
	}
	if !errors.As(err, &apiErr) || apiErr == nil {
		if transporterr.IsRetryable(err) {
			return FailureRetryable, ""
		}
		return FailureNone, ""
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		return FailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		if apiErr.Response != nil {
			return FailureRateLimited, strings.TrimSpace(apiErr.Response.Header.Get("Retry-After"))
		}
		return FailureRateLimited, ""
	case http.StatusRequestTimeout:
		return FailureRetryable, ""
	default:
		if apiErr.StatusCode >= 500 {
			return FailureRetryable, ""
		}
		return FailureNone, ""
	}
}
