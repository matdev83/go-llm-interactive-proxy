package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzDecodeRequest(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"model":"gpt-4o","input":"hello world"}`),
		[]byte(`{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
		[]byte(`{"model":"gpt-4o","previous_response_id":"resp_123"}`),
		[]byte(`{"model":"gpt-4o","input":"test","tools":[{"type":"function","name":"get_weather"}]}`),
		[]byte(`{"model":"gpt-4o","input":"test","tool_choice":"auto"}`),
		[]byte(`{"model":"gpt-4o","input":"test","tool_choice":{"type":"function","function":{"name":"get_weather"}}}`),
		[]byte(`{"model":"gpt-4o","input":"test","temperature":0.7,"top_p":0.9,"max_output_tokens":100}`),
		[]byte(`{"model":"gpt-4o","input":"test","custom:extension_field":{"foo":"bar"}}`),
		[]byte(`{"model":"gpt-4o","input":"test","background":true}`),
		[]byte(`{"model":"gpt-4o","input":"test","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"invalid json`),
		[]byte(`{"model":"gpt-4o","input":"test","model":"dup"}`),
		[]byte("\xFF\xFE\xFD"),
	}

	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxRequestBytes {
			return
		}

		param, call, err := DecodeRequest(data)
		if err != nil {
			if param != nil {
				t.Fatalf("expected nil param on error, got %v", param)
			}
			return
		}

		if param == nil {
			t.Fatal("expected non-nil param on success")
		}

		// Ensure re-encoding request doesn't panic
		_, _ = EncodeRequest(call)
	})
}

func FuzzDecodeItem(f *testing.F) {
	seeds := []string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`,
		`{"type":"item_reference","id":"item_123"}`,
		`{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":{"location":"Paris"}}`,
		`{"type":"function_call_output","call_id":"call_1","output":"sunny"}`,
		`{"type":"reasoning","reasoning":"thinking hard"}`,
		`{"type":"compaction","encapsulated_id":"resp_1","dialect":"v1"}`,
		`{"type":"vendor:custom_item","data":{"key":"val"}}`,
		`{"type":"unknown_type"}`,
		`{}`,
	}

	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}

		var wire WireItem
		if err := json.Unmarshal(data, &wire); err != nil {
			return
		}

		item, err := DecodeItem(wire, DefaultLimits())
		if err != nil {
			return
		}

		_, _ = EncodeItem(item)
	})
}

func FuzzSSEParser(f *testing.F) {
	seeds := []struct {
		eventType string
		delta     string
	}{
		{"response.created", ""},
		{"response.output_text.delta", "hello world"},
		{"response.done", ""},
		{"error", "bad request"},
		{"custom:event", "payload"},
	}

	for _, s := range seeds {
		f.Add(s.eventType, s.delta)
	}

	f.Fuzz(func(t *testing.T, eventType string, delta string) {
		if len(eventType) > 1024 || len(delta) > 64*1024 {
			return
		}

		evt := StreamEvent{
			Type:  eventType,
			Delta: delta,
		}

		out, err := FormatSSEEvent(evt)
		if err != nil {
			return
		}

		if len(out) == 0 {
			t.Fatal("expected non-empty output on success")
		}

		if !bytes.HasSuffix(out, []byte("\n\n")) {
			t.Fatalf("expected output to end with double newline, got %q", out)
		}
	})
}

func FuzzStateMachine(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			return
		}

		env := EnvelopeMetadata{
			ResponseID: "resp_fuzz",
			Model:      "gpt-4o",
			CreatedAt:  time.Unix(1700000000, 0),
		}

		sm := NewStateMachine(env, lipapi.GenerationOptions{})

		for i := 0; i < len(data); i++ {
			byteVal := data[i]
			var event lipapi.Event

			switch byteVal % 5 {
			case 0:
				event = lipapi.Event{Kind: lipapi.EventResponseStarted}
			case 1:
				event = lipapi.Event{Kind: lipapi.EventMessageStarted}
			case 2:
				event = lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "fuzz"}
			case 3:
				event = lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
			case 4:
				event = lipapi.Event{Kind: lipapi.EventResponseFinished}
			}

			// Process event - state machine must never panic
			_, _ = sm.ProcessCanonicalEvent(event)
		}
	})
}

func FuzzResourceBuilder(f *testing.F) {
	seeds := []string{
		"resp_100",
		"resp_200",
		"",
	}

	for _, s := range seeds {
		f.Add(s, int64(1700000000))
	}

	f.Fuzz(func(t *testing.T, respID string, created int64) {
		meta := EnvelopeMetadata{
			ResponseID: respID,
			CreatedAt:  time.Unix(created, 0),
			Model:      "gpt-4o",
			Status:     "completed",
		}

		items := []lipapi.Item{
			{
				ID:     "msg_1",
				Kind:   lipapi.ItemKindMessage,
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleAssistant,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "fuzz answer"},
				},
			},
		}

		usage := UsageStats{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		}

		res, _, err := BuildResponseResource(meta, items, usage, lipapi.GenerationOptions{}, nil)
		if err != nil {
			return
		}

		_, _ = json.Marshal(res)
	})
}

func FuzzErrorMapping(f *testing.F) {
	seeds := []string{
		"decode failed",
		"limit exceeded",
		"sequence violation",
		"unknown error",
		"secret token postgres://admin:secret@host/db",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, errMsg string) {
		err := errors.New(errMsg)
		status, wireEnv, class := MapErrorToWire(err)

		if status < 100 || status > 599 {
			t.Fatalf("invalid HTTP status code: %d", status)
		}

		if wireEnv.Error.Message == "" {
			t.Fatal("expected non-empty wire error message")
		}

		if class == "" {
			t.Fatal("expected non-empty error classification")
		}

		_, _ = json.Marshal(wireEnv)
	})
}
