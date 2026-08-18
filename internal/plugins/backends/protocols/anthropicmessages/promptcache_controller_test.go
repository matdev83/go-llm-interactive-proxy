package anthropicmessages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func TestCacheControllerPartialReadIsNotRenewed(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotTarget CacheTarget
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "secret-c0" {
			t.Fatalf("x-api-key=%q, want credential-affine key", got)
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&map[string]any{})
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":10}}`))
	}))
	defer server.Close()

	read := int64(100)
	total := int64(100)
	controller, err := NewCacheController(CacheControllerConfig{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		ResolveAPIKey: func(_ context.Context, target CacheTarget) (string, error) {
			mu.Lock()
			gotTarget = target
			mu.Unlock()
			if target.AccountID != "c0" {
				t.Fatalf("target account=%q, want exact credential c0", target.AccountID)
			}
			return "secret-c0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := controller.IssueTarget(CacheTarget{
		ALegID: "a", BLegID: "b", BackendInstanceID: "anthropic", TargetID: "target", GenerationID: "generation", Model: "claude", TTL: "5m",
		Renewal:   RenewalSnapshot{RawRequest: json.RawMessage(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)},
		AccountID: "c0", Evidence: promptcache.CacheEvidence{CacheReadTokens: &read, TotalTokens: &total},
	}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := controller.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "keepwarm:test:1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Result.Status != promptcache.Stale {
		t.Fatalf("partial prefix status=%q, want stale", resp.Result.Status)
	}
	if controller.TargetCount() != 1 {
		t.Fatal("partial result should leave classification to the manager; target unexpectedly removed")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotTarget.AccountID != "c0" {
		t.Fatalf("resolved target=%q, want c0", gotTarget.AccountID)
	}
}

func TestAnthropicRenewalCapabilityRejectsToolAndThinkingShapes(t *testing.T) {
	t.Parallel()
	be := NewBackend(Config{
		BackendID:       "anthropic",
		BaseURL:         "https://example.invalid",
		Credentials:     []credpool.Credential{{ID: "c0", Secret: "secret"}},
		CacheEnrollment: "automatic",
		CacheTTL:        "5m",
	})
	candidate := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude"}}
	plain := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}}}
	profile := be.ResolvePromptCacheProfile(context.Background(), plain, candidate)
	if !profile.RenewalSupported {
		t.Fatal("plain request should be eligible for certified active renewal")
	}

	toolCall := plain
	toolCall.Tools = []lipapi.ToolDef{{Name: "shell", Description: "run shell", Parameters: []byte(`{"type":"object"}`)}}
	toolProfile := be.ResolvePromptCacheProfile(context.Background(), toolCall, candidate)
	if toolProfile.RenewalSupported {
		t.Fatal("tool request must not advertise active renewal")
	}

	formatted := plain
	formatted.Options.ResponseMIMEType = "application/json"
	formattedProfile := be.ResolvePromptCacheProfile(context.Background(), formatted, candidate)
	if formattedProfile.RenewalSupported {
		t.Fatal("response-format request must not advertise active renewal")
	}
}
