package parity_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	visibleThinkerReasoning = "plan: ship it quickly"
	visibleExecutorText     = "executor-answer"
)

func visibleThinkerCanonicalStream() []lipapi.Event {
	return []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: visibleThinkerReasoning},
		{Kind: lipapi.EventTextDelta, Delta: visibleExecutorText},
		{Kind: lipapi.EventResponseFinished},
	}
}

func visibleThinkerCall(extensions map[string]json.RawMessage) *lipapi.Call {
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("prompt")},
		}},
		Extensions: extensions,
	}
}

func modelExt(tb testing.TB, key, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{key: raw}
}

func assertVisibleThinkerWireLegality(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, visibleExecutorText) {
		t.Fatalf("executor text missing from wire body: %q", body)
	}
	if strings.Contains(body, visibleThinkerReasoning) {
		t.Fatalf("thinker reasoning must not appear on wire in v1 subset; body=%q", body)
	}
	if strings.Contains(body, interleavedthinking.MemoOpenTag) || strings.Contains(body, interleavedthinking.MemoCloseTag) {
		t.Fatalf("memo wrapper tags must not appear on wire; body=%q", body)
	}
}

func TestVisibleThinkerReasoning_streamEncodesLegally(t *testing.T) {
	t.Parallel()

	type caseDef struct {
		name     string
		call     *lipapi.Call
		encode   func(context.Context, *httptest.ResponseRecorder, *lipapi.Call, lipapi.EventStream) error
		terminal string
	}

	cases := []caseDef{
		{
			name: "anthropic",
			call: visibleThinkerCall(modelExt(t, "anthropic.model", "claude-3-5-haiku-20241022")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return anthropic.WriteStreamSSE(ctx, rec, call, es, anthropic.EncodeOptions{MessageID: "msg_visible_thinker"})
			},
			terminal: "event: message_stop",
		},
		{
			name: "gemini",
			call: visibleThinkerCall(modelExt(t, "gemini.model", "gemini-2.0-flash")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return gemini.WriteStreamSSE(ctx, rec, call, es, gemini.EncodeOptions{})
			},
			terminal: `"candidates"`,
		},
		{
			name: "openailegacy",
			call: visibleThinkerCall(modelExt(t, "openailegacy.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return openailegacy.WriteStreamSSE(ctx, rec, call, es, openailegacy.EncodeOptions{
					CompletionID: "chatcmpl_visible_thinker",
					CreatedAt:    1715620000,
				})
			},
			terminal: "[DONE]",
		},
		{
			name: "openairesponses",
			call: visibleThinkerCall(modelExt(t, "openairesponses.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return openairesponses.WriteStreamSSE(ctx, rec, call, es, openairesponses.EncodeOptions{
					ResponseID: "resp_visible_thinker",
					CreatedAt:  1715620000,
				})
			},
			terminal: "response.completed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := lipapi.NewFixedEventStream(visibleThinkerCanonicalStream())
			rec := httptest.NewRecorder()
			if err := tc.encode(t.Context(), rec, tc.call, es); err != nil {
				t.Fatal(err)
			}
			body := rec.Body.String()
			assertVisibleThinkerWireLegality(t, body)
			if !strings.Contains(body, tc.terminal) {
				t.Fatalf("missing terminal marker %q; body=%q", tc.terminal, body)
			}
		})
	}
}

func TestVisibleThinkerReasoning_nonStreamEncodesLegally(t *testing.T) {
	t.Parallel()

	type caseDef struct {
		name   string
		call   *lipapi.Call
		encode func(context.Context, *httptest.ResponseRecorder, *lipapi.Call, lipapi.EventStream) error
	}

	cases := []caseDef{
		{
			name: "anthropic",
			call: visibleThinkerCall(modelExt(t, "anthropic.model", "claude-3-5-haiku-20241022")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return anthropic.WriteNonStreamJSON(ctx, rec, call, es, anthropic.EncodeOptions{MessageID: "msg_visible_thinker_ns"})
			},
		},
		{
			name: "gemini",
			call: visibleThinkerCall(modelExt(t, "gemini.model", "gemini-2.0-flash")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return gemini.WriteNonStreamJSON(ctx, rec, call, es, gemini.EncodeOptions{})
			},
		},
		{
			name: "openailegacy",
			call: visibleThinkerCall(modelExt(t, "openailegacy.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return openailegacy.WriteNonStreamJSON(ctx, rec, call, es, openailegacy.EncodeOptions{
					CompletionID: "chatcmpl_visible_thinker_ns",
					CreatedAt:    1715620000,
				})
			},
		},
		{
			name: "openairesponses",
			call: visibleThinkerCall(modelExt(t, "openairesponses.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *lipapi.Call, es lipapi.EventStream) error {
				return openairesponses.WriteNonStreamJSON(ctx, rec, call, es, openairesponses.EncodeOptions{
					ResponseID: "resp_visible_thinker_ns",
					MessageID:  "msg_visible_thinker_ns",
					CreatedAt:  1715620000,
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := lipapi.NewFixedEventStream(visibleThinkerCanonicalStream())
			rec := httptest.NewRecorder()
			if err := tc.encode(t.Context(), rec, tc.call, es); err != nil {
				t.Fatal(err)
			}
			assertVisibleThinkerWireLegality(t, rec.Body.String())
		})
	}
}
