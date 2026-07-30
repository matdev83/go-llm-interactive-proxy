package openailegacy

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
)

// CompletionEvents converts a non-streaming ChatCompletion response into canonical events.
func CompletionEvents(comp openai.ChatCompletion) []lipapi.Event {
	events := []lipapi.Event{{Kind: lipapi.EventResponseStarted}}

	for _, choice := range comp.Choices {
		msg := choice.Message
		sawMsg := false

		if reasoning := reasoningTextFromMessage(msg); reasoning != "" {
			if !sawMsg {
				sawMsg = true
				events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			events = append(events, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: reasoning})
		}

		if msg.Content != "" {
			if !sawMsg {
				sawMsg = true
				events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: msg.Content})
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
	}

	finishReason := ""
	for _, choice := range comp.Choices {
		if fr := string(choice.FinishReason); fr != "" {
			finishReason = fr
			break
		}
	}

	if comp.JSON.Usage.Valid() {
		events = append(events, openaiusage.ChatUsageEvent(comp.Usage))
	}

	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: finishReason})
	return events
}

func firstReasoningField(fields map[string]respjson.Field, requireNonSpace bool, keys ...string) (string, bool) {
	for _, key := range keys {
		f, ok := fields[key]
		if !ok {
			continue
		}
		raw := f.Raw()
		if raw == "" || raw == respjson.Null {
			continue
		}
		var s string
		if json.Unmarshal([]byte(raw), &s) != nil {
			continue
		}
		if requireNonSpace && strings.TrimSpace(s) == "" {
			continue
		}
		return s, true
	}
	return "", false
}

func reasoningTextFromMessage(msg openai.ChatCompletionMessage) string {
	if msg.JSON.ExtraFields == nil {
		return ""
	}
	if s, ok := firstReasoningField(msg.JSON.ExtraFields, false, "reasoning", "reasoning_content"); ok {
		return s
	}
	s, _ := firstReasoningField(msg.JSON.ExtraFields, true, "reasoning_summary")
	return s
}
