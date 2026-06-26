package openaicompat

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
)

// ChatCompletionEvents converts a non-streaming ChatCompletion response into
// a canonical event slice. The caller owns the returned slice.
func ChatCompletionEvents(comp openai.ChatCompletion) []lipapi.Event {
	events := []lipapi.Event{{Kind: lipapi.EventResponseStarted}}

	for _, choice := range comp.Choices {
		msg := choice.Message
		sawMsg := false

		if reasoning := ReasoningTextFromMessage(msg); reasoning != "" {
			if !sawMsg {
				sawMsg = true
				events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			events = append(events, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: reasoning})
		}

		if len(msg.ToolCalls) > 0 {
			if !sawMsg {
				sawMsg = true
				events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			for _, tc := range msg.ToolCalls {
				fn := tc.AsFunction()
				if fn.ID == "" {
					continue
				}
				events = append(events, lipapi.Event{
					Kind:       lipapi.EventToolCallStarted,
					ToolCallID: fn.ID,
					ToolName:   fn.Function.Name,
				})
				if fn.Function.Arguments != "" {
					events = append(events, lipapi.Event{
						Kind:       lipapi.EventToolCallArgsDelta,
						ToolCallID: fn.ID,
						Delta:      fn.Function.Arguments,
					})
				}
				events = append(events, lipapi.Event{
					Kind:       lipapi.EventToolCallFinished,
					ToolCallID: fn.ID,
				})
			}
		}

		if msg.Content != "" {
			if !sawMsg {
				sawMsg = true
				events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: msg.Content})
		}
	}

	if comp.JSON.Usage.Valid() && (comp.Usage.PromptTokens > 0 || comp.Usage.CompletionTokens > 0 || comp.Usage.TotalTokens > 0) {
		events = append(events, openaiusage.ChatUsageEvent(comp.Usage))
	}

	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return events
}

// ReasoningTextFromMessage extracts reasoning text from the "reasoning" or
// "reasoning_content" extra fields of a ChatCompletionMessage.
func ReasoningTextFromMessage(msg openai.ChatCompletionMessage) string {
	if msg.JSON.ExtraFields == nil {
		return ""
	}
	if f, ok := msg.JSON.ExtraFields["reasoning"]; ok && f.Valid() {
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil {
			return s
		}
	}
	if f, ok := msg.JSON.ExtraFields["reasoning_content"]; ok && f.Valid() {
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil {
			return s
		}
	}
	return ""
}

// ReasoningTextFromChunkDelta extracts reasoning text from a streaming chunk delta.
func ReasoningTextFromChunkDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	if delta.JSON.ExtraFields == nil {
		return ""
	}
	if f, ok := delta.JSON.ExtraFields["reasoning"]; ok && f.Raw() != "" {
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil {
			return s
		}
	}
	if f, ok := delta.JSON.ExtraFields["reasoning_content"]; ok && f.Raw() != "" {
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil {
			return s
		}
	}
	return ""
}
