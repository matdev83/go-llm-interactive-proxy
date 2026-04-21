package stream_test

import (
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestKeepalive_doubleClose(t *testing.T) {
	t.Parallel()
	inner := &delayedStream{
		events: []lipapi.Event{{Kind: lipapi.EventResponseFinished}},
		delay:  time.Hour,
	}
	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{Interval: 50 * time.Millisecond})
	if err := ka.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ka.Close(); err != nil {
		t.Fatalf("second Close must be idempotent, got: %v", err)
	}
}

func TestKeepalive_concurrentClose(t *testing.T) {
	t.Parallel()
	inner := &delayedStream{
		events: []lipapi.Event{{Kind: lipapi.EventResponseFinished}},
		delay:  time.Hour,
	}
	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{Interval: 50 * time.Millisecond})
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_ = ka.Close()
		}()
	}
	wg.Wait()
}
