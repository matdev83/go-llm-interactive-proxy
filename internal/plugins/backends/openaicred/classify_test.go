package openaicred_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/openai/openai-go/v3"
)

func TestClassifyOpenAIAPIError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		makeErr   func() error
		wantKind  openaicred.FailureKind
		wantRetry string
	}{
		{
			name: "unauthorized",
			makeErr: func() error {
				res := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}
				return &openai.Error{StatusCode: http.StatusUnauthorized, Response: res}
			},
			wantKind: openaicred.FailureAuthInvalid,
		},
		{
			name: "unauthorized_wrapped",
			makeErr: func() error {
				res := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}
				inner := &openai.Error{StatusCode: http.StatusUnauthorized, Response: res}
				return errors.Join(errors.New("wrap"), inner)
			},
			wantKind: openaicred.FailureAuthInvalid,
		},
		{
			name: "rate_limit_with_retry_after",
			makeErr: func() error {
				rec := httptest.NewRecorder()
				rec.Header().Set("Retry-After", "120")
				return &openai.Error{
					StatusCode: http.StatusTooManyRequests,
					Response:   rec.Result(),
				}
			},
			wantKind:  openaicred.FailureRateLimited,
			wantRetry: "120",
		},
		{
			name: "bad_request",
			makeErr: func() error {
				return &openai.Error{StatusCode: http.StatusBadRequest, Response: &http.Response{StatusCode: http.StatusBadRequest}}
			},
			wantKind: openaicred.FailureNone,
		},
		{
			name: "plain_error",
			makeErr: func() error {
				return errors.New("plain")
			},
			wantKind: openaicred.FailureNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k, ra := openaicred.ClassifyOpenAIAPIError(tt.makeErr())
			if k != tt.wantKind || ra != tt.wantRetry {
				t.Fatalf("got kind=%v retryAfter=%q want kind=%v retryAfter=%q", k, ra, tt.wantKind, tt.wantRetry)
			}
		})
	}
}
