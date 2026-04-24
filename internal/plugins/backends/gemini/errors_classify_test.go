package gemini

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"google.golang.org/genai"
)

func TestClassifyGenaiAPIError_retryInfoInDetails(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("upstream: %w", genai.APIError{
		Code: http.StatusTooManyRequests,
		Details: []map[string]any{{
			"@type":      "type.googleapis.com/google.rpc.RetryInfo",
			"retryDelay": "125s",
		}},
	})
	kind, ra := classifyGenaiAPIError(err)
	if kind != apiFailureRateLimited {
		t.Fatalf("kind: %v", kind)
	}
	if ra != "125" {
		t.Fatalf("retryAfter delta-seconds: got %q want 125", ra)
	}
}

func TestClassifyGenaiAPIError_retryInfoSubsecondRoundsUp(t *testing.T) {
	t.Parallel()
	err := genai.APIError{
		Code: http.StatusTooManyRequests,
		Details: []map[string]any{{
			"@type":      "type.googleapis.com/google.rpc.RetryInfo",
			"retryDelay": "1500ms",
		}},
	}
	_, ra := classifyGenaiAPIError(err)
	if ra != "2" {
		t.Fatalf("retryAfter: got %q want 2", ra)
	}
}

func TestClassifyGenaiAPIError_429WithoutDetailsUsesEmptyRetryAfter(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("wrap: %w", genai.APIError{
		Code:    http.StatusTooManyRequests,
		Message: "rate",
	})
	kind, ra := classifyGenaiAPIError(err)
	if kind != apiFailureRateLimited {
		t.Fatalf("kind: %v", kind)
	}
	if ra != "" {
		t.Fatalf("expected empty retryAfter without details, got %q", ra)
	}
}

func TestClassifyGenaiAPIError_401(t *testing.T) {
	t.Parallel()
	kind, ra := classifyGenaiAPIError(genai.APIError{Code: http.StatusUnauthorized})
	if kind != apiFailureAuthInvalid || ra != "" {
		t.Fatalf("got kind=%v ra=%q", kind, ra)
	}
}

func TestClassifyGenaiAPIError_retryInfoFeedsCredpoolCooldown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	fallback := 60 * time.Second
	err := genai.APIError{
		Code: http.StatusTooManyRequests,
		Details: []map[string]any{{
			"@type":      "type.googleapis.com/google.rpc.RetryInfo",
			"retryDelay": "3600s",
		}},
	}
	_, ra := classifyGenaiAPIError(err)
	if ra != "3600" {
		t.Fatalf("retryAfter: got %q", ra)
	}
	until := credpool.CooldownFromRetryAfterOrFallback(ra, now, fallback)
	want := now.Add(3600 * time.Second)
	if !until.Equal(want) {
		t.Fatalf("cooldown until: got %v want %v", until, want)
	}
}
