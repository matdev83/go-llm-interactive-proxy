package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestTokenCounterSupportsCount(t *testing.T) {
	t.Parallel()
	counter := NewTokenCounter(Config{BaseURL: "https://example.invalid", APIKey: "secret"})

	tests := []struct {
		name string
		in   app.ProviderCountInput
		want app.SupportStatus
	}{
		{
			name: "call kind for anthropic backend",
			in:   app.ProviderCountInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Kind: app.CountKindCall},
			want: app.SupportStatusSupported,
		},
		{
			name: "text kind unsupported",
			in:   app.ProviderCountInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Kind: app.CountKindText},
			want: app.SupportStatusUnsupported,
		},
		{
			name: "output kind unsupported",
			in:   app.ProviderCountInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Kind: app.CountKindOutput},
			want: app.SupportStatusUnsupported,
		},
		{
			name: "other backend unsupported",
			in:   app.ProviderCountInput{Backend: "openai", Model: "claude-3-5-sonnet-latest", Kind: app.CountKindCall},
			want: app.SupportStatusUnsupported,
		},
		{
			name: "empty model unsupported",
			in:   app.ProviderCountInput{Backend: ID, Kind: app.CountKindCall},
			want: app.SupportStatusUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := counter.SupportsCount(context.Background(), tt.in)
			if got.Status != tt.want {
				t.Fatalf("SupportsCount() status = %q, want %q", got.Status, tt.want)
			}
		})
	}
}

func TestTokenCounterCountCallMapsResponse(t *testing.T) {
	t.Parallel()
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	t.Cleanup(srv.Close)

	counter := NewTokenCounter(Config{BaseURL: srv.URL, APIKey: "secret", HTTPClient: srv.Client()})
	result, err := counter.CountCall(context.Background(), app.CountCallInput{
		Backend: ID,
		Model:   "claude-3-5-sonnet-latest",
		CallID:  "call-1",
		Call:    textCall("hello"),
	})
	if err != nil {
		t.Fatalf("CountCall() error = %v", err)
	}
	if gotPath != "/v1/messages/count_tokens" {
		t.Fatalf("path = %q, want count_tokens endpoint", gotPath)
	}
	if gotKey != "secret" {
		t.Fatalf("x-api-key = %q, want secret", gotKey)
	}
	if gotVersion == "" {
		t.Fatal("anthropic-version header is empty")
	}
	if gotBody["model"] != "claude-3-5-sonnet-latest" {
		t.Fatalf("model in request = %v", gotBody["model"])
	}
	if _, ok := gotBody["max_tokens"]; ok {
		t.Fatalf("count_tokens request includes generation-only max_tokens: %#v", gotBody)
	}
	if result.InputTokens != 42 || result.TotalTokens != 42 || result.OutputTokens != 0 {
		t.Fatalf("tokens = input %d output %d total %d, want 42/0/42", result.InputTokens, result.OutputTokens, result.TotalTokens)
	}
	if result.Accounting.Source != lipapi.UsageSourceProviderCountAPI {
		t.Fatalf("source = %q", result.Accounting.Source)
	}
	if result.Accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("authority = %q", result.Accounting.Authority)
	}
	if result.Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("plane = %q", result.Accounting.Plane)
	}
	if result.Accounting.Tokenizer.Type != "provider_count_api" {
		t.Fatalf("tokenizer type = %q", result.Accounting.Tokenizer.Type)
	}
	if result.Accounting.Tokenizer.ModelUsed != "claude-3-5-sonnet-latest" {
		t.Fatalf("model used = %q", result.Accounting.Tokenizer.ModelUsed)
	}
}

func TestTokenCounterUnavailableConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing base url", cfg: Config{APIKey: "secret"}},
		{name: "missing api key", cfg: Config{BaseURL: "https://example.invalid"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			counter := NewTokenCounter(tt.cfg)
			support := counter.SupportsCount(context.Background(), app.ProviderCountInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Kind: app.CountKindCall})
			if support.Status != app.SupportStatusUnavailable {
				t.Fatalf("SupportsCount() status = %q, want unavailable", support.Status)
			}

			svc := app.NewService(app.ServiceConfig{Mode: app.ModeProviderOnly}, counter, nil)
			_, err := svc.CountCall(context.Background(), app.CountCallInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Call: textCall("hello")})
			if !errors.Is(err, app.ErrProviderUnavailable) {
				t.Fatalf("ProviderOnly CountCall() error = %v, want ErrProviderUnavailable", err)
			}
		})
	}
}

func TestTokenCounterCountCallDirectErrorsHaveSentinels(t *testing.T) {
	t.Parallel()
	t.Run("mapping unsupported", func(t *testing.T) {
		t.Parallel()
		counter := NewTokenCounter(Config{BaseURL: "https://example.invalid", APIKey: "secret"})
		badCall := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: []byte(`{"x":1}`)}}}}}
		_, err := counter.CountCall(context.Background(), app.CountCallInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Call: badCall})
		if !errors.Is(err, app.ErrProviderUnsupported) {
			t.Fatalf("CountCall() error = %v, want ErrProviderUnsupported", err)
		}
	})

	t.Run("provider non 2xx unavailable", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		}))
		t.Cleanup(srv.Close)

		counter := NewTokenCounter(Config{BaseURL: srv.URL, APIKey: "secret", HTTPClient: srv.Client()})
		_, err := counter.CountCall(context.Background(), app.CountCallInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Call: textCall("hello")})
		if !errors.Is(err, app.ErrProviderUnavailable) {
			t.Fatalf("CountCall() error = %v, want ErrProviderUnavailable", err)
		}
	})
}

func TestTokenCounterCountCallErrorsAreProviderUnavailableThroughService(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response string
		status   int
	}{
		{name: "non 2xx", status: http.StatusTooManyRequests, response: `{"error":{"message":"rate limited"}}`},
		{name: "malformed", status: http.StatusOK, response: `{"input_tokens":"many"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			t.Cleanup(srv.Close)

			counter := NewTokenCounter(Config{BaseURL: srv.URL, APIKey: "secret", HTTPClient: srv.Client()})
			svc := app.NewService(app.ServiceConfig{Mode: app.ModeProviderOnly}, counter, nil)
			_, err := svc.CountCall(context.Background(), app.CountCallInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Call: textCall("hello")})
			if !errors.Is(err, app.ErrProviderUnavailable) {
				t.Fatalf("CountCall() error = %v, want ErrProviderUnavailable", err)
			}
		})
	}
}

func TestTokenCounterContextCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	counter := NewTokenCounter(Config{BaseURL: srv.URL, APIKey: "secret", HTTPClient: srv.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := counter.CountCall(ctx, app.CountCallInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Call: textCall("hello")})
		errCh <- err
	}()
	<-started
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CountCall() error = %v, want context.Canceled", err)
	}
}

func TestTokenCounterUnsupportedMethods(t *testing.T) {
	t.Parallel()
	counter := NewTokenCounter(Config{BaseURL: "https://example.invalid", APIKey: "secret"})
	if _, err := counter.CountText(context.Background(), app.CountTextInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Text: "hello"}); !errors.Is(err, app.ErrProviderUnsupported) {
		t.Fatalf("CountText() error = %v, want ErrProviderUnsupported", err)
	}
	if _, err := counter.CountOutput(context.Background(), app.CountOutputInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Text: "hello"}); !errors.Is(err, app.ErrProviderUnsupported) {
		t.Fatalf("CountOutput() error = %v, want ErrProviderUnsupported", err)
	}
}

func textCall(text string) lipapi.Call {
	maxOutputTokens := 64
	return lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: text}},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxOutputTokens},
	}
}

var _ app.ProviderCounter = (*TokenCounter)(nil)

func TestTokenCounterDoesNotExposeSecretsInErrors(t *testing.T) {
	t.Parallel()
	secret := "secret-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	t.Cleanup(srv.Close)

	counter := NewTokenCounter(Config{BaseURL: srv.URL, APIKey: secret, HTTPClient: srv.Client()})
	_, err := counter.CountCall(context.Background(), app.CountCallInput{Backend: ID, Model: "claude-3-5-sonnet-latest", Call: textCall("hello")})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked API key: %v", err)
	}
}
