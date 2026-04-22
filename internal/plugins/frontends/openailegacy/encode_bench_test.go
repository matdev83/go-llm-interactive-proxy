package openailegacy

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// benchTokenStream yields n text deltas then one response_finished (no further events).
type benchTokenStream struct {
	n int
	i int
}

func (s *benchTokenStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if s.i < s.n {
		s.i++
		return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"}, nil
	}
	if s.i == s.n {
		s.i++
		return lipapi.Event{Kind: lipapi.EventResponseFinished}, nil
	}
	return lipapi.Event{}, io.EOF
}

func (s *benchTokenStream) Close() error { return nil }

func BenchmarkWriteStreamSSE_textDeltas(b *testing.B) {
	const n = 256
	ctx := context.Background()
	call := &lipapi.Call{}
	opts := EncodeOptions{CompletionID: "chatcmpl_bench", CreatedAt: 1}
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		es := &benchTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			b.Fatal(err)
		}
	}
}
