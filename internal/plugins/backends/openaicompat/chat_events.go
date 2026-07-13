package openaicompat

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
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

	if comp.JSON.Usage.Valid() {
		events = append(events, openaiusage.ChatUsageEvent(comp.Usage))
	}

	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return events
}

// firstReasoningField returns the reasoning text from the first key (in order)
// whose extra field carries a usable raw JSON string. The boolean is true when
// such a field was found, so callers preserve short-circuit semantics.
//
// The guard is on [respjson.Field.Raw] rather than [respjson.Field.Valid]:
// openai-go's decoder records decoded extra string fields with an "invalid"
// status yet still populates Raw, so Valid() would reject real reasoning
// payloads. Omitted (Raw == "") and JSON null (Raw == "null") values are
// skipped so later keys (e.g. reasoning_summary) are tried. When
// requireNonSpace is true, whitespace-only values are skipped, which
// "reasoning_summary" needs because it can carry placeholder space.
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

// ReasoningTextFromMessage extracts reasoning text from the "reasoning",
// "reasoning_content", or "reasoning_summary" extra fields of a
// ChatCompletionMessage.
func ReasoningTextFromMessage(msg openai.ChatCompletionMessage) string {
	if msg.JSON.ExtraFields == nil {
		return ""
	}
	if s, ok := firstReasoningField(msg.JSON.ExtraFields, false, "reasoning", "reasoning_content"); ok {
		return s
	}
	s, _ := firstReasoningField(msg.JSON.ExtraFields, true, "reasoning_summary")
	return s
}

// ReasoningTextFromChunkDelta extracts reasoning text from a streaming chunk
// delta's "reasoning", "reasoning_content", or "reasoning_summary" extra
// fields.
func ReasoningTextFromChunkDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	if delta.JSON.ExtraFields == nil {
		return ""
	}
	if s, ok := firstReasoningField(delta.JSON.ExtraFields, false, "reasoning", "reasoning_content"); ok {
		return s
	}
	s, _ := firstReasoningField(delta.JSON.ExtraFields, true, "reasoning_summary")
	return s
}
