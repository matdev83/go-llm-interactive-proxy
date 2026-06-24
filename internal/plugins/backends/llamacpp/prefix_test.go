package llamacpp_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/llamacpp"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/llamacpp"
)

func TestNew_stripsBackendPrefixBeforeUpstream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		candidate, wantUpstream string
	}{
		{"llamacpp/llama-3", "llama-3"},
		{"llamacpp:llama-3", "llama-3"},
		{"llama-3", "llama-3"},
		{"meta/llama-3", "meta/llama-3"},
		{"openai/gpt-4o", "openai/gpt-4o"},
	}
	for _, tc := range cases {
		t.Run(tc.candidate, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var capturedModel string

			srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
				OnRequestBody: func(b []byte) {
					mu.Lock()
					defer mu.Unlock()
					var payload struct {
						Model string `json:"model"`
					}
					_ = json.Unmarshal(b, &payload)
					capturedModel = payload.Model
				},
				AllowMissingBearer: true,
			}))
			t.Cleanup(srv.Close)

			be := llamacpp.New(llamacpp.Config{
				BaseURL:       srv.URL,
				SDKMaxRetries: new(int),
			})
			es, err := be.Open(context.Background(), testCall(), testCandidate(tc.candidate))
			if err != nil {
				t.Fatal(err)
			}
			_ = es.Close()

			mu.Lock()
			defer mu.Unlock()
			if capturedModel != tc.wantUpstream {
				t.Fatalf("upstream model = %q, want %q", capturedModel, tc.wantUpstream)
			}
		})
	}
}
