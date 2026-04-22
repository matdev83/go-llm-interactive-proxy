package conformance

import (
	"context"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestParity_Anthropic_bundledFrontend(t *testing.T) {
	t.Parallel()
	if !slices.Contains(BundledFrontendIDs(), "anthropic") {
		t.Fatal("expected anthropic in BundledFrontendIDs")
	}
}

// Anthropic parity anchor (design.md ANT-MM-OUT): canonical assistant ref events collect.
func TestParity_Anthropic_canonicalAssistantMediaCollects(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventAssistantFileRef, AssistantRef: "https://files.example.com/a.pdf", AssistantMIME: "application/pdf", AssistantName: "A"},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" || len(col.AssistantMedia) != 1 || col.AssistantMedia[0].FileRef != "https://files.example.com/a.pdf" {
		t.Fatalf("collected: text=%q media=%v", col.Text.String(), col.AssistantMedia)
	}
}
