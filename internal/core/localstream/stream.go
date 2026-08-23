package localstream

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// NewTextStream returns a finite canonical EventStream with exactly one
// assistant text response. The sequence is response_started, message_started,
// text_delta, response_finished. It emits no usage, no B-leg identity, no
// background goroutine, and respects context cancellation via
// lipapi.FixedEventStream. The stream is valid for both streaming and
// non-streaming collection via lipapi.Collect.
func NewTextStream(text string) lipapi.EventStream {
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: text},
		{Kind: lipapi.EventResponseFinished},
	}
	return lipapi.NewFixedEventStream(events)
}

// Events returns the canonical event slice for a text response without
// allocating a stream. Useful for ValidateEventSequence testing.
func Events(text string) []lipapi.Event {
	return []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: text},
		{Kind: lipapi.EventResponseFinished},
	}
}

// CanonicalAssistantMessage returns the canonical assistant message whose
// semantic identity must equal the tagged reply identity. It is the same
// construction used by runtime local-turn reply tagging so that replay
// identity is stable across frontends.
func CanonicalAssistantMessage(text string) lipapi.Message {
	return lipapi.Message{
		Role:  lipapi.RoleAssistant,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: text}},
	}
}

// CanonicalAssistantItem returns the item-authority equivalent of
// CanonicalAssistantMessage. It is used for materialized-history assertions
// where the continuation store uses Items.
func CanonicalAssistantItem(text string) lipapi.Item {
	return lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		Role:   lipapi.RoleAssistant,
		Status: lipapi.ItemStatusCompleted,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: text},
		},
	}
}
