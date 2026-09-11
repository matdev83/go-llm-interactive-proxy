package openaicred_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
)

func TestClassifyHTTPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		header    http.Header
		wantKind  openaicred.FailureKind
		wantRetry string
	}{
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			wantKind: openaicred.FailureAuthInvalid,
		},
		{
			name:      "rate_limited_with_retry_after",
			status:    http.StatusTooManyRequests,
			header:    http.Header{"Retry-After": []string{"45"}},
			wantKind:  openaicred.FailureRateLimited,
			wantRetry: "45",
		},
		{
			name:     "rate_limited_without_retry_after",
			status:   http.StatusTooManyRequests,
			wantKind: openaicred.FailureRateLimited,
		},
		{
			name:     "request_timeout",
			status:   http.StatusRequestTimeout,
			wantKind: openaicred.FailureRetryable,
		},
		{
			name:     "internal_server_error",
			status:   http.StatusInternalServerError,
			wantKind: openaicred.FailureRetryable,
		},
		{
			name:     "bad_gateway",
			status:   http.StatusBadGateway,
			wantKind: openaicred.FailureRetryable,
		},
		{
			name:     "bad_request",
			status:   http.StatusBadRequest,
			wantKind: openaicred.FailureNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k, ra := openaicred.ClassifyHTTPStatus(tt.status, tt.header)
			if k != tt.wantKind || ra != tt.wantRetry {
				t.Fatalf("got kind=%v retryAfter=%q, want kind=%v retryAfter=%q", k, ra, tt.wantKind, tt.wantRetry)
			}
		})
	}
}

func TestClassifyHTTPResponse(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rec.Header().Set("Retry-After", "30")
	rec.WriteHeader(http.StatusTooManyRequests)
	resp := rec.Result()

	k, ra := openaicred.ClassifyHTTPResponse(resp)
	if k != openaicred.FailureRateLimited || ra != "30" {
		t.Fatalf("got kind=%v retryAfter=%q, want kind=%v retryAfter=30", k, ra, openaicred.FailureRateLimited)
	}

	if k, _ := openaicred.ClassifyHTTPResponse(nil); k != openaicred.FailureNone {
		t.Fatalf("nil resp: got %v, want FailureNone", k)
	}
}

func TestExecuteWithCredentialPool_NilPool(t *testing.T) {
	t.Parallel()

	called := false
	stream, err := openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		nil,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
			called = true
			if cred.Secret != "" {
				t.Fatalf("expected empty secret for nil pool, got %q", cred.Secret)
			}
			return lipapi.NewFixedEventStream(nil), nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called || stream == nil {
		t.Fatalf("expected called=true and non-nil stream")
	}
}

func TestExecuteWithCredentialPool_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = openaicred.ExecuteWithCredentialPool(
		ctx,
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
			t.Fatal("attempt must not be called on canceled context")
			return nil, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestExecuteWithCredentialPool_RateLimitCooldownRotation(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	stream, err := openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		10*time.Minute,
		func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
			attempts++
			if attempts == 1 {
				if cred.Secret != "s1" {
					t.Fatalf("first attempt expected s1, got %q", cred.Secret)
				}
				rec := httptest.NewRecorder()
				rec.Header().Set("Retry-After", "120")
				return nil, &openai.Error{
					StatusCode: http.StatusTooManyRequests,
					Response:   rec.Result(),
				}
			}
			if cred.Secret != "s2" {
				t.Fatalf("second attempt expected s2, got %q", cred.Secret)
			}
			return lipapi.NewFixedEventStream(nil), nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil || attempts != 2 {
		t.Fatalf("expected 2 attempts and non-nil stream, got attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_AuthInvalidRotation(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	stream, err := openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
			attempts++
			if attempts == 1 {
				return nil, &openai.Error{
					StatusCode: http.StatusUnauthorized,
					Response:   &http.Response{StatusCode: http.StatusUnauthorized},
				}
			}
			return lipapi.NewFixedEventStream(nil), nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil || attempts != 2 {
		t.Fatalf("expected 2 attempts and non-nil stream, got attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_RetryableReturnsPreOutputError(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}

	serverErr := &openai.Error{
		StatusCode: http.StatusInternalServerError,
		Response:   &http.Response{StatusCode: http.StatusInternalServerError},
	}
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
			return nil, serverErr
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected lipapi.IsRecoverablePreOutput(err) to be true, got %v", err)
	}
}

func TestExecuteWithCredentialPool_ExhaustedReturnsPreOutputError(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkAuthInvalid("k1")

	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
			t.Fatal("attempt must not be called when pool exhausted")
			return nil, nil
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected lipapi.IsRecoverablePreOutput(err) to be true on exhausted pool, got %v", err)
	}
}
