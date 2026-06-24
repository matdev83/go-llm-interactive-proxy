package vllm_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/vllm"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/vllm"
)

func TestNew_stripsBackendPrefixBeforeUpstream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		candidate, wantUpstream string
	}{
		{"vllm/meta-llama/Llama-3-8B-Instruct", "meta-llama/Llama-3-8B-Instruct"},
		{"vllm:meta-llama/Llama-3-8B-Instruct", "meta-llama/Llama-3-8B-Instruct"},
		{"meta-llama/Llama-3-8B-Instruct", "meta-llama/Llama-3-8B-Instruct"},
		{"other-backend/model", "other-backend/model"},
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

			be := vllm.New(vllm.Config{
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
