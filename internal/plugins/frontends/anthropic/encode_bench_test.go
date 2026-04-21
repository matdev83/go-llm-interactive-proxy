package anthropic

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type benchAnthropicTokenStream struct {
	n int
	i int
}

func (s *benchAnthropicTokenStream) Recv(ctx context.Context) (lipapi.Event, error) {
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

func (s *benchAnthropicTokenStream) Close() error { return nil }

func BenchmarkWriteStreamSSE_textDeltas(b *testing.B) {
	const n = 256
	ctx := context.Background()
	call := &lipapi.Call{}
	opts := EncodeOptions{MessageID: "msg_bench"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		es := &benchAnthropicTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			b.Fatal(err)
		}
	}
}
