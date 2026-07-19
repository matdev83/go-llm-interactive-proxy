package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

func FuzzStreamObserver_exactReasoningPart(f *testing.F) {
	f.Add([]byte(`{"id":"rs_1","type":"reasoning","summary":[]}`))
	f.Add([]byte(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null}`))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"id":"rs_1","summary":[],"extra":1}`))
	f.Fuzz(func(t *testing.T, opaque []byte) {
		if len(opaque) > 8<<10 {
			opaque = opaque[:8<<10]
		}
		cfg := observeExactConfig(t)
		store := newMemoryStore(t, exactStoreOptions(time.Now))
		obs := openExactObserver(t, cfg, store, "sess-fuzz", nil)
		_ = obs.Observe(context.Background(), lipapi.Event{
			Kind: lipapi.EventReasoningPart,
			Reasoning: &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
				Opaque:  append([]byte(nil), opaque...),
			},
		})
		_ = obs.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
		_ = obs.Finish(context.Background(), response.OutcomeSuccessReleased)
	})
}
