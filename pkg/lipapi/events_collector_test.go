package lipapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCollectWithLimits_nil_event_stream_returns_error(t *testing.T) {
	t.Parallel()
	_, err := lipapi.CollectWithLimits(context.Background(), nil, lipapi.CollectLimits{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, lipapi.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
}

func TestCollect_nil_event_stream_returns_error(t *testing.T) {
	t.Parallel()
	_, err := lipapi.Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, lipapi.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
}

func TestCollectUnbounded_nil_event_stream_returns_error(t *testing.T) {
	t.Parallel()
	_, err := lipapi.CollectUnbounded(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, lipapi.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
}

func TestCollect_happyPathOrderingAndAggregation(t *testing.T) {
	t.Parallel()

	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hel"},
		{Kind: lipapi.EventTextDelta, Delta: "lo"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2},
		{Kind: lipapi.EventWarning, WarningMessage: "x"},
		{Kind: lipapi.EventResponseFinished},
	})

	out, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out.Text.String(); got != "hello" {
		t.Fatalf("text: got %q want %q", got, "hello")
	}
	if out.InputTokens != 3 || out.OutputTokens != 2 {
		t.Fatalf("usage: got in=%d out=%d", out.InputTokens, out.OutputTokens)
	}
	if len(out.Warnings) != 1 || out.Warnings[0] != "x" {
		t.Fatalf("warnings: %#v", out.Warnings)
	}
	if !out.FinishReceived {
		t.Fatal("expected finish")
	}
}

func TestCollect_errorTerminationReturnsError(t *testing.T) {
	t.Parallel()

	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "partial"},
		{Kind: lipapi.EventError, ErrorCode: "upstream", ErrorMessage: "boom"},
	})

	out, err := lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error")
	}
	var se *lipapi.StreamError
	if !errors.As(err, &se) {
		t.Fatalf("want *lipapi.StreamError, got %T: %v", err, err)
	}
	if se.Code != "upstream" || se.Message != "boom" {
		t.Fatalf("unexpected stream error: %+v", se)
	}
	if !errors.Is(err, lipapi.ErrStreamTerminal) {
		t.Fatalf("expected ErrStreamTerminal in chain, got %v", err)
	}
	if out.Text.String() != "partial" {
		t.Fatalf("expected partial text aggregation, got %q", out.Text.String())
	}
	if out.TerminalError == nil {
		t.Fatal("expected terminal error event captured")
	}
}

func TestValidateEventSequence_acceptsErrorTerminal(t *testing.T) {
	t.Parallel()

	err := lipapi.ValidateEventSequence([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventError, ErrorCode: "x", ErrorMessage: "y"},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateEventSequence_rejectsDeltaBeforeMessageStarted(t *testing.T) {
	t.Parallel()

	err := lipapi.ValidateEventSequence([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventTextDelta, Delta: "nope"},
		{Kind: lipapi.EventResponseFinished},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventSequence_rejectsMissingResponseStarted(t *testing.T) {
	t.Parallel()

	err := lipapi.ValidateEventSequence([]lipapi.Event{
		{Kind: lipapi.EventMessageStarted},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollect_duplicateResponseStarted(t *testing.T) {
	t.Parallel()

	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventResponseStarted},
	})

	_, err := lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollect_limitExceededOnText(t *testing.T) {
	t.Parallel()

	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: strings.Repeat("z", 100)},
		{Kind: lipapi.EventResponseFinished},
	})
	_, err := lipapi.CollectWithLimits(context.Background(), stream, lipapi.CollectLimits{
		MaxTextBytes: 50,
	})
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !errors.Is(err, lipapi.ErrCollectLimitExceeded) {
		t.Fatalf("want ErrCollectLimitExceeded: %v", err)
	}
}

func TestCollect_assistantMediaAndFinishReason(t *testing.T) {
	t.Parallel()
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hi"},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "https://example.com/x.png", AssistantMIME: "image/png"},
		{Kind: lipapi.EventAssistantFileRef, AssistantRef: "file-abc", AssistantMIME: "application/pdf", AssistantName: "doc.pdf"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	})
	out, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text.String() != "hi" {
		t.Fatalf("text: %q", out.Text.String())
	}
	if len(out.AssistantMedia) != 2 {
		t.Fatalf("assistant parts: %#v", out.AssistantMedia)
	}
	if out.AssistantMedia[0].Kind != lipapi.PartImageRef || out.AssistantMedia[0].ImageRef != "https://example.com/x.png" {
		t.Fatalf("image part: %#v", out.AssistantMedia[0])
	}
	if out.AssistantMedia[1].Kind != lipapi.PartFileRef || out.AssistantMedia[1].FileRef != "file-abc" {
		t.Fatalf("file part: %#v", out.AssistantMedia[1])
	}
	if out.FinishReason != "stop" {
		t.Fatalf("finish: %q", out.FinishReason)
	}
}

func TestCollect_assistantMediaLimit(t *testing.T) {
	t.Parallel()
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "a"},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "b"},
		{Kind: lipapi.EventResponseFinished},
	})
	_, err := lipapi.CollectWithLimits(context.Background(), stream, lipapi.CollectLimits{
		MaxAssistantMediaParts: 1,
	})
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !errors.Is(err, lipapi.ErrCollectLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_contextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
	})

	_, err := lipapi.Collect(ctx, stream)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
}

func TestCollect_toolCallsInterleavedOrderAndDedup(t *testing.T) {
	t.Parallel()
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "a", ToolName: "fa"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "b", ToolName: "fb"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "b", Delta: "2"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "a", Delta: "1"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "b"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "a", Delta: "3"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "a"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "c"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c", Delta: "4"},
		{Kind: lipapi.EventResponseFinished},
	})
	out, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.ToolCallOrder, ","); got != "a,b,c" {
		t.Fatalf("tool order: %q", got)
	}
	if out.ToolNames["a"] != "fa" {
		t.Fatalf("first tool name wins: %#v", out.ToolNames)
	}
	if out.ToolArgs["a"].String() != "13" || out.ToolArgs["b"].String() != "2" || out.ToolArgs["c"].String() != "4" {
		t.Fatalf("args a=%q b=%q c=%q", out.ToolArgs["a"].String(), out.ToolArgs["b"].String(), out.ToolArgs["c"].String())
	}
}

func TestCollect_toolArgsTotalLimitUsesRunningSum(t *testing.T) {
	t.Parallel()
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "x", Delta: "aa"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "y", Delta: "b"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "x", Delta: "c"},
		{Kind: lipapi.EventResponseFinished},
	})
	_, err := lipapi.CollectWithLimits(context.Background(), stream, lipapi.CollectLimits{
		MaxToolArgsTotalBytes: 3,
	})
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !errors.Is(err, lipapi.ErrCollectLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}
