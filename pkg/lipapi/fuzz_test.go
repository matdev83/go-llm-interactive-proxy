package lipapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzCallValidateJSON(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"x"}]}]}`))
	f.Add([]byte(`{"Messages":[{"Role":"assistant","Parts":[{"Kind":"reasoning","Reasoning":{"Dialect":"openai.chat.reasoning_text.v1","Text":"think"}},{"Kind":"text","Text":"hi"}]}]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 512<<10)
		var c lipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			return
		}
		_ = c.Validate()
	})
}

func FuzzSemanticExtensionValidation(f *testing.F) {
	f.Add([]byte(`{"kind":"hint"}`))
	f.Add([]byte(`{"request":{"messages":[]}}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, lipapi.MaxSemanticExtensionDataBytes)
		call := lipapi.Call{
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "fuzz"}}}},
			SemanticExtensions: []lipapi.SemanticExtension{{
				Namespace: "lip", Type: "fuzz_hint", Implementor: "fuzzer", Direction: "request",
				Presence: lipapi.SemanticExtensionValue, Data: raw,
			}},
		}
		err := call.Validate()
		if err == nil {
			if !json.Valid(raw) {
				t.Fatalf("call.Validate() accepted invalid JSON data: %q", string(raw))
			}
			if len(raw) == 0 || len(raw) > lipapi.MaxSemanticExtensionDataBytes {
				t.Fatalf("call.Validate() accepted raw data length %d outside valid 1..%d byte range", len(raw), lipapi.MaxSemanticExtensionDataBytes)
			}
			var parsed any
			if unmarshalErr := json.Unmarshal(raw, &parsed); unmarshalErr != nil {
				t.Fatalf("call.Validate() accepted raw data that fails json.Unmarshal: %v", unmarshalErr)
			}
		} else {
			if len(raw) == 0 || !json.Valid(raw) {
				return // expected validation failure
			}
		}
	})
}

func FuzzMergeRouteQueryGenerationOptions(f *testing.F) {
	f.Add("temperature=0.5&max_output_tokens=3")
	f.Add("parallel_tool_calls=1")

	f.Fuzz(func(t *testing.T, q string) {
		q = testkit.CapString(q, 16<<10)
		v, err := url.ParseQuery(q)
		if err != nil {
			return
		}
		_, _ = lipapi.MergeRouteQueryIntoGenerationOptions(lipapi.GenerationOptions{}, v)
	})
}

func FuzzCollectWithLimitsProgram(f *testing.F) {
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0, 1, 2, 3, 4, 5})

	f.Fuzz(func(t *testing.T, b []byte) {
		b = testkit.CapBytes(b, 4096)
		evs := collectFuzzEvents(b)
		ctx := context.Background()
		_, _ = lipapi.CollectWithLimits(ctx, lipapi.NewFixedEventStream(evs), lipapi.CollectLimits{
			MaxTextBytes:             1 << 20,
			MaxReasoningBytes:        1 << 20,
			MaxToolArgsTotalBytes:    1 << 20,
			MaxWarnings:              1000,
			MaxAggregatePayloadBytes: 2 << 20,
		})
		// Also fuzz aggregate as standalone dimension with zero per-dimension limits.
		_, _ = lipapi.CollectWithLimits(ctx, lipapi.NewFixedEventStream(evs), lipapi.CollectLimits{
			MaxAggregatePayloadBytes: 1 << 20,
		})
	})
}

func collectFuzzEvents(b []byte) []lipapi.Event {
	evs := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
	}
	const chunk = 48
	for i := 0; i < len(b); i += chunk {
		j := min(i+chunk, len(b))
		if i >= j {
			break
		}
		switch b[i] % 4 {
		case 0:
			evs = append(evs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: string(b[i:j])})
		case 1:
			evs = append(evs, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: string(b[i:j])})
		case 2:
			evs = append(evs, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "fuzz-call", Delta: string(b[i:j])})
		default:
			// Always emit a structurally valid exact part inside a legal sequence.
			opaque := json.RawMessage(`{"v":1}`)
			if alt, err := json.Marshal(map[string]any{"v": 1, "n": len(b[i:j])}); err == nil {
				opaque = alt
			}
			evs = append(evs, lipapi.Event{
				Kind: lipapi.EventReasoningPart,
				Reasoning: &lipapi.ReasoningPart{
					Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
					Opaque:  opaque,
				},
			})
		}
	}
	evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return evs
}
