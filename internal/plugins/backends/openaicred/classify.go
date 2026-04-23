package openaicred

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
)

// FailureKind classifies an OpenAI HTTP API error for credential-pool handling.
type FailureKind int

const (
	// FailureNone means the error is not a classified OpenAI API HTTP failure.
	FailureNone FailureKind = iota
	FailureRateLimited
	FailureAuthInvalid
)

// ClassifyOpenAIAPIError inspects *openai.Error (including wrapped). On rate limit it
// returns the Retry-After header value when present (may be empty).
func ClassifyOpenAIAPIError(err error) (kind FailureKind, retryAfter string) {
	var apiErr *openai.Error
	if err == nil || !errors.As(err, &apiErr) || apiErr == nil {
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
	default:
		return FailureNone, ""
	}
}
