package openairesponses

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWriteStreamSSE_AllocBudget_textOnly(t *testing.T) {
	const n = 200
	events := make([]lipapi.Event, 0, n+3)
	events = append(events,
		lipapi.Event{Kind: lipapi.EventResponseStarted},
		lipapi.Event{Kind: lipapi.EventMessageStarted},
	)
	for i := 0; i < n; i++ {
		events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
	}
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})

	ctx := context.Background()
	call := &lipapi.Call{ID: "alloc-call"}
	opts := EncodeOptions{ResponseID: "resp_alloc", MessageID: "msg_alloc", CreatedAt: 1}
	allocs := testing.AllocsPerRun(5, func() {
		rec := httptest.NewRecorder()
		es := lipapi.FixedEventStream(events)
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			t.Fatal(err)
		}
	})
	const maxAllocs = 250_000
	if int(allocs) > maxAllocs {
		t.Fatalf("allocs per run=%g (n=%d deltas), want <= %d", allocs, n, maxAllocs)
	}
}
