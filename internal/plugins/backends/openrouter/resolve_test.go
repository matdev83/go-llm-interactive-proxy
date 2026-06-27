package openrouter

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func TestProviderRawFromCandidate(t *testing.T) {
	t.Parallel()

	t.Run("empty when no params", func(t *testing.T) {
		if got := providerRawFromCandidate(routing.AttemptCandidate{}); got != nil {
			t.Fatalf("expected nil, got %s", string(got))
		}
	})

	t.Run("empty when provider param missing", func(t *testing.T) {
		cand := routing.AttemptCandidate{Primary: routing.Primary{Params: url.Values{"foo": {"bar"}}}}
		if got := providerRawFromCandidate(cand); got != nil {
			t.Fatalf("expected nil, got %s", string(got))
		}
	})

	t.Run("builds order and disable fallbacks", func(t *testing.T) {
		cand := routing.AttemptCandidate{Primary: routing.Primary{Params: url.Values{"provider": {"deepinfra/turbo"}}}}
		raw := providerRawFromCandidate(cand)
		if raw == nil {
			t.Fatal("expected non-nil raw")
		}
		var got struct {
			Order          []string `json:"order"`
			AllowFallbacks *bool    `json:"allow_fallbacks"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v raw=%s", err, string(raw))
		}
		if len(got.Order) != 1 || got.Order[0] != "deepinfra/turbo" {
			t.Fatalf("order = %v, want [deepinfra/turbo]", got.Order)
		}
		if got.AllowFallbacks == nil {
			t.Fatalf("allow_fallbacks missing from provider JSON, raw=%s", string(raw))
		}
		if *got.AllowFallbacks {
			t.Fatalf("allow_fallbacks = true, want false")
		}
	})

	t.Run("uses first value when multiple", func(t *testing.T) {
		cand := routing.AttemptCandidate{Primary: routing.Primary{Params: url.Values{"provider": {"a", "b"}}}}
		raw := providerRawFromCandidate(cand)
		var got struct {
			Order []string `json:"order"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Order) != 1 || got.Order[0] != "a" {
			t.Fatalf("order = %v, want [a]", got.Order)
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		cand := routing.AttemptCandidate{Primary: routing.Primary{Params: url.Values{"provider": {"  deepinfra/turbo  "}}}}
		raw := providerRawFromCandidate(cand)
		if raw == nil {
			t.Fatal("expected non-nil raw")
		}
		var got struct {
			Order []string `json:"order"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v raw=%s", err, string(raw))
		}
		if len(got.Order) != 1 || got.Order[0] != "deepinfra/turbo" {
			t.Fatalf("order = %v, want [deepinfra/turbo]", got.Order)
		}
	})

	t.Run("empty when provider is whitespace only", func(t *testing.T) {
		cand := routing.AttemptCandidate{Primary: routing.Primary{Params: url.Values{"provider": {"   "}}}}
		if got := providerRawFromCandidate(cand); got != nil {
			t.Fatalf("expected nil for whitespace-only provider, got %s", string(got))
		}
	})
}
