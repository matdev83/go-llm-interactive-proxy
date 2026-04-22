package stream

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkPendingEventQueue_pushPop(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		var q PendingEventQueue
		for range 256 {
			q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "d"})
		}
		for range 256 {
			if _, ok := q.PopFront(); !ok {
				b.Fatal("pop")
			}
		}
	}
}
