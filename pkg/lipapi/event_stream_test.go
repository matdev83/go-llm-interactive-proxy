package lipapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestFixedEventStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect lipapi.ErrNilContext
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestFixedEventStream_Recv_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *lipapi.FixedEventStream
	_, err := s.Recv(context.Background())
	if !errors.Is(err, lipapi.ErrNilFixedEventStream) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectWithLimits_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := lipapi.CollectWithLimits(nil, s, lipapi.CollectLimits{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := lipapi.Collect(nil, s) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectUnbounded_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := lipapi.CollectUnbounded(nil, s) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_usageCostAndUncachedInput(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{
			Kind:            lipapi.EventUsageDelta,
			InputTokens:     10,
			OutputTokens:    3,
			CacheReadTokens: 4,
			CostNanoUnits:   123,
			Currency:        "USD",
			CostSource:      "estimated",
		},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.UncachedInputTokens() != 6 {
		t.Fatalf("uncached = %d want 6", col.UncachedInputTokens())
	}
	if col.CostNanoUnits != 123 || col.Currency != "USD" || col.CostSource != "estimated" {
		t.Fatalf("cost = %d %q %q", col.CostNanoUnits, col.Currency, col.CostSource)
	}
}

func TestCollectedAccumulateUsageMatchesCollectUsage(t *testing.T) {
	t.Parallel()
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 3, CostNanoUnits: 7, Currency: "USD"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 4, CacheWriteTokens: 5, ReasoningTokens: 2, TotalTokens: 20, CostNanoUnits: 11, CostSource: "estimated"},
		{Kind: lipapi.EventResponseFinished},
	}
	var direct lipapi.Collected
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			direct.AccumulateUsage(ev)
		}
	}
	col, err := lipapi.Collect(t.Context(), lipapi.NewFixedEventStream(events))
	if err != nil {
		t.Fatal(err)
	}
	if direct.InputTokens != col.InputTokens || direct.OutputTokens != col.OutputTokens || direct.CacheReadTokens != col.CacheReadTokens || direct.CacheWriteTokens != col.CacheWriteTokens || direct.ReasoningTokens != col.ReasoningTokens || direct.TotalTokens != col.TotalTokens || direct.CostNanoUnits != col.CostNanoUnits || direct.Currency != col.Currency || direct.CostSource != col.CostSource {
		t.Fatalf("direct=%+v collect=%+v", direct, col)
	}
}
