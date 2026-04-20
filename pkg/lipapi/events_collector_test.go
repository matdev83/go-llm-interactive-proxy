package lipapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCollect_happyPathOrderingAndAggregation(t *testing.T) {
	t.Parallel()

	stream := lipapi.FixedEventStream([]lipapi.Event{
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

	stream := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "partial"},
		{Kind: lipapi.EventError, ErrorCode: "upstream", ErrorMessage: "boom"},
	})

	out, err := lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected error: %v", err)
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

	stream := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventResponseStarted},
	})

	_, err := lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollect_contextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
	})

	_, err := lipapi.Collect(ctx, stream)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
}
