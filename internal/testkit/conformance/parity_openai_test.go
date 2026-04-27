//go:build integration

package conformance

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// OpenAI family parity anchors (see .kiro/specs/llm-api-parity/design.md rows OAR-*, OAC-*).
func TestParity_OpenAI_canonicalAssistantMediaCollects(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "https://cdn.example/a.png", AssistantMIME: "image/png"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	})
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" || len(col.AssistantMedia) != 1 || col.FinishReason != "stop" {
		t.Fatalf("collected: text=%q media=%v finish=%q", col.Text.String(), col.AssistantMedia, col.FinishReason)
	}
}

func TestParity_OpenAI_matrixIncludesBothFrontends(t *testing.T) {
	t.Parallel()
	var sawResponses, sawLegacy bool
	for _, id := range BundledFrontendIDs() {
		switch id {
		case "openai-responses":
			sawResponses = true
		case "openai-legacy":
			sawLegacy = true
		}
	}
	if !sawResponses || !sawLegacy {
		t.Fatalf("bundled frontends: responses=%v legacy=%v", sawResponses, sawLegacy)
	}
}
