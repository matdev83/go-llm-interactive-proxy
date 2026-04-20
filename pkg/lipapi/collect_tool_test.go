package lipapi_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCollect_toolStartedRecordsNameAndOrder(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_a", ToolName: "fn_a"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_a", Delta: `{"x":1}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_a"},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].ID != "call_a" || tcs[0].Name != "fn_a" || tcs[0].Arguments != `{"x":1}` {
		t.Fatalf("got %+v", tcs)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text %q", col.Text.String())
	}
}

func TestCollect_argsDeltaBeforeStartedStillOrders(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: "1"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_b", ToolName: "late"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: "2"},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].Name != "late" || tcs[0].Arguments != "12" {
		t.Fatalf("got %+v", tcs)
	}
}

func TestCollect_twoToolsPreservesOrder(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "a"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c2", ToolName: "b"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c2", Delta: "y"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: "x"},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 2 || tcs[0].ID != "c1" || tcs[1].ID != "c2" {
		t.Fatalf("got %+v", tcs)
	}
	if tcs[0].Arguments != "x" || tcs[1].Arguments != "y" {
		t.Fatalf("args %+v", tcs)
	}
}

func TestCollect_toolStartedMissingIDErrors(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolName: "x"},
		{Kind: lipapi.EventResponseFinished},
	})
	_, err := lipapi.Collect(context.Background(), es)
	if err == nil {
		t.Fatal("expected error")
	}
}
