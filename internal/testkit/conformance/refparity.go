package conformance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"

	refacp "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/acp"
	refanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/anthropicmessages"
	refbedrock "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/bedrock"
	refgemini "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/gemini"
	refopenaichat "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	refopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
)

const parityText = "conformance-parity"

// NewSuccessRefBackend returns a reference backend whose streaming and non-streaming paths
// both surface parityText as assistant text (ACP keeps the stock emulator which answers "ok").
// Optional onRequestBody observes the raw upstream HTTP body after route/auth checks.
func NewSuccessRefBackend(tb testing.TB, backendID string, onRequestBody func([]byte)) *httptest.Server {
	tb.Helper()
	if backendID == acp.ID {
		srv := httptest.NewServer(refacp.NewHandler(refacp.Config{OnRequestBody: onRequestBody}))
		tb.Cleanup(srv.Close)
		return srv
	}
	inner := parityRefHandler(tb, backendID)
	var h http.Handler = inner
	if onRequestBody != nil {
		h = captureRequestBodyHandler(inner, onRequestBody)
	}
	srv := httptest.NewServer(h)
	tb.Cleanup(srv.Close)
	return srv
}

func captureRequestBodyHandler(inner http.Handler, onBody func([]byte)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			inner.ServeHTTP(w, r)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		onBody(b)
		inner.ServeHTTP(w, r)
	})
}

func parityRefHandler(tb testing.TB, backendID string) http.Handler {
	tb.Helper()
	switch backendID {
	case openairesponses.ID:
		ns := strings.Replace(openAIResponsesNonStreamDefault, `"text": "ok"`, `"text": "`+parityText+`"`, 1)
		ss := strings.Replace(openAIResponsesStreamDefault, "stream-ok", parityText, 1)
		return refopenairesponses.NewHandler(refopenairesponses.Config{
			NonStreamJSON: ns,
			StreamSSE:     ss,
		})
	case openailegacy.ID:
		ns := strings.Replace(openAILegacyNonStreamDefault, `"content":"ok"`, `"content":"`+parityText+`"`, 1)
		ss := strings.Replace(openAILegacyStreamDefault, "stream-ok", parityText, 1)
		return refopenaichat.NewHandler(refopenaichat.Config{
			NonStreamJSON: ns,
			StreamSSE:     ss,
		})
	case anthropic.ID:
		ns := strings.Replace(anthropicNonStreamDefault, `"text":"ok"`, `"text":"`+parityText+`"`, 1)
		return refanthropic.NewHandler(refanthropic.Config{
			NonStreamJSON: ns,
			StreamSSE:     anthropicParityStreamSSE(parityText),
		})
	case gemini.ID:
		ns := strings.Replace(geminiNonStreamDefault, `"text": "ok"`, `"text": "`+parityText+`"`, 1)
		ss := strings.Replace(geminiStreamDefault, "stream-ok", parityText, 1)
		return refgemini.NewHandler(refgemini.Config{
			NonStreamJSON: ns,
			StreamSSE:     ss,
		})
	case bedrock.ID:
		ns := strings.Replace(bedrockConverseDefault, `"text": "ok"`, `"text": "`+parityText+`"`, 1)
		return refbedrock.NewHandler(refbedrock.Config{
			ConverseJSON: ns,
			StreamEvents: bedrockParityStreamEvents(tb, parityText),
		})
	default:
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
}

const openAIResponsesNonStreamDefault = `{
  "id": "resp_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "ok"}
      ]
    }
  ]
}`

const openAIResponsesStreamDefault = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}]}}\n\n" +
	"data: [DONE]\n\n"

const openAILegacyNonStreamDefault = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
}`

const openAILegacyStreamDefault = "data: {\"id\":\"chatcmpl_refbackend_stream\"," +
	"\"object\":\"chat.completion.chunk\",\"created\":1715620000," +
	"\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0," +
	"\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

const anthropicNonStreamDefault = `{
  "id": "msg_refbackend_1",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-haiku-20241022",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`

const geminiNonStreamDefault = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "ok"}]
      }
    }
  ]
}`

const geminiStreamDefault = "data: " +
	"{\"candidates\":[{\"content\":{" +
	"\"role\":\"model\",\"parts\":[{\"text\":\"stream-ok\"}]}}]}\n\n"

const bedrockConverseDefault = `{
  "metrics": { "latencyMs": 1 },
  "output": {
    "message": {
      "role": "assistant",
      "content": [ { "text": "ok" } ]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 1, "outputTokens": 1 }
}`

func anthropicParityStreamSSE(text string) string {
	escaped, _ := json.Marshal(text)
	return "event: message_start\ndata: " +
		`{"type":"message_start","message":{"id":"m_stream","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
		"\n\n" +
		"event: content_block_start\ndata: " +
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
		"\n\n" +
		"event: content_block_delta\ndata: " +
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(escaped) + `}}` +
		"\n\n" +
		"event: content_block_stop\ndata: " +
		`{"type":"content_block_stop","index":0}` +
		"\n\n" +
		"event: message_stop\ndata: " +
		`{"type":"message_stop"}` +
		"\n\n"
}

func bedrockParityStreamEvents(tb testing.TB, text string) []byte {
	tb.Helper()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": text},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	}
	for _, ev := range events {
		payload, err := json.Marshal(ev.payload)
		if err != nil {
			tb.Fatalf("bedrock parity stream: marshal %s: %v", ev.eventType, err)
		}
		msg := eventstream.Message{
			Headers: []eventstream.Header{
				{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
				{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue(ev.eventType)},
				{Name: eventstreamapi.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			},
			Payload: payload,
		}
		if err := enc.Encode(&buf, msg); err != nil {
			tb.Fatalf("bedrock parity stream: encode %s: %v", ev.eventType, err)
		}
	}
	return buf.Bytes()
}
