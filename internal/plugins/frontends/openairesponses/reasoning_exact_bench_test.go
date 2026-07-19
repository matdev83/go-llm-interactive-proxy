package openairesponses

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkWriteStreamSSE_exactReasoningPart(b *testing.B) {
	opaque := json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`)
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningPart, Reasoning: &lipapi.ReasoningPart{
			Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		}},
		{Kind: lipapi.EventResponseFinished},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "stub:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		es := lipapi.NewFixedEventStream(events)
		rec := httptest.NewRecorder()
		if err := WriteStreamSSE(context.Background(), rec, call, es, EncodeOptions{ResponseID: "r", CreatedAt: 1}); err != nil {
			b.Fatal(err)
		}
	}
}
