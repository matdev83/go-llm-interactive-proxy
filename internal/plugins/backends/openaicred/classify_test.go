package openaicred_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/openai/openai-go/v3"
)

func TestClassifyOpenAIAPIError_unauthorized(t *testing.T) {
	t.Parallel()
	res := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}
	err := &openai.Error{StatusCode: http.StatusUnauthorized, Response: res}
	k, ra := openaicred.ClassifyOpenAIAPIError(err)
	if k != openaicred.FailureAuthInvalid || ra != "" {
		t.Fatalf("got kind=%v retryAfter=%q", k, ra)
	}
	wrapped := errors.Join(errors.New("wrap"), err)
	k2, _ := openaicred.ClassifyOpenAIAPIError(wrapped)
	if k2 != openaicred.FailureAuthInvalid {
		t.Fatalf("wrapped: got %v", k2)
	}
}

func TestClassifyOpenAIAPIError_rateLimitWithRetryAfter(t *testing.T) {
	t.Parallel()
	res := httptest.NewRecorder()
	res.Header().Set("Retry-After", "120")
	err := &openai.Error{
		StatusCode: http.StatusTooManyRequests,
		Response:   res.Result(),
	}
	k, ra := openaicred.ClassifyOpenAIAPIError(err)
	if k != openaicred.FailureRateLimited || ra != "120" {
		t.Fatalf("got kind=%v retryAfter=%q", k, ra)
	}
}

func TestClassifyOpenAIAPIError_other(t *testing.T) {
	t.Parallel()
	err := &openai.Error{StatusCode: http.StatusBadRequest, Response: &http.Response{StatusCode: http.StatusBadRequest}}
	k, ra := openaicred.ClassifyOpenAIAPIError(err)
	if k != openaicred.FailureNone || ra != "" {
		t.Fatalf("got kind=%v retryAfter=%q", k, ra)
	}
	if k2, _ := openaicred.ClassifyOpenAIAPIError(errors.New("plain")); k2 != openaicred.FailureNone {
		t.Fatalf("plain: got %v", k2)
	}
}
