package conformance

import (
	"context"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestParity_Bedrock_bundledBackend(t *testing.T) {
	t.Parallel()
	if !slices.Contains(BundledBackendIDs(), "bedrock") {
		t.Fatal("expected bedrock in BundledBackendIDs")
	}
}

func TestParity_Bedrock_canonicalAssistantMediaCollects(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "https://cdn.example.com/x.png", AssistantMIME: "image/png"},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.AssistantMedia) != 1 || col.AssistantMedia[0].ImageRef != "https://cdn.example.com/x.png" {
		t.Fatalf("collected: %#v", col.AssistantMedia)
	}
}
