package openairesponses

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// BenchmarkWriteStreamSSE_textDeltas measures allocations on the streaming encode
// hot path (many text deltas + terminal events).
func BenchmarkWriteStreamSSE_textDeltas(b *testing.B) {
	const nDelta = 200
	events := make([]lipapi.Event, 0, nDelta+3)
	events = append(events,
		lipapi.Event{Kind: lipapi.EventResponseStarted},
		lipapi.Event{Kind: lipapi.EventMessageStarted},
	)
	for i := 0; i < nDelta; i++ {
		events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "xy"})
	}
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})

	call := &lipapi.Call{ID: "bench-call"}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		es := lipapi.NewFixedEventStream(events)
		if err := WriteStreamSSE(context.Background(), rec, call, es, EncodeOptions{
			ResponseID: "resp_bench",
			MessageID:  "msg_bench",
			CreatedAt:  1,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
