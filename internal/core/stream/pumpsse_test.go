package stream_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type sliceStream struct {
	evs []lipapi.Event
	i   int
}

func (s *sliceStream) Recv(context.Context) (lipapi.Event, error) {
	if s.i >= len(s.evs) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.evs[s.i]
	s.i++
	return ev, nil
}

func (s *sliceStream) Close() error { return nil }

func TestPumpSSE_keepaliveWarningAndTerminalEvent(t *testing.T) {
	t.Parallel()
	es := &sliceStream{evs: []lipapi.Event{
		{Kind: lipapi.EventWarning, WarningCode: stream.KeepaliveEventCode},
		{Kind: lipapi.EventTextDelta, Delta: "hi"},
		{Kind: lipapi.EventResponseFinished},
	}}
	rec := httptest.NewRecorder()
	var deltas []string
	err := stream.PumpSSE(context.Background(), rec, es, errors.New("eof"), func(ev lipapi.Event) (bool, error) {
		if ev.Kind == lipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
		return ev.Kind == lipapi.EventResponseFinished, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0] != "hi" {
		t.Fatalf("deltas = %v", deltas)
	}
	if !contains(rec.Body.String(), ": keepalive") {
		t.Fatalf("missing keepalive comment: %q", rec.Body.String())
	}
}

func TestPumpSSE_eofWithoutDone(t *testing.T) {
	t.Parallel()
	es := &sliceStream{evs: nil}
	rec := httptest.NewRecorder()
	want := errors.New("custom eof")
	err := stream.PumpSSE(context.Background(), rec, es, want, func(lipapi.Event) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v want %v", err, want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
