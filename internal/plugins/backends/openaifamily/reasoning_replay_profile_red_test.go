package openaifamily_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
	legacybackend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	responsesbackend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Kimi/Moonshot and other OpenAI-compatible family backends share openaifamily wiring.
// ResolveFlavor selects chat vs responses replay shape; ParamsForCall must be flavor-exact.

func reasoningPart(dialect lipapi.ReasoningDialect, text string, opaque json.RawMessage) lipapi.Part {
	var op json.RawMessage
	if len(opaque) > 0 {
		op = append(json.RawMessage(nil), opaque...)
	}
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect: dialect,
			Text:    text,
			Opaque:  op,
		},
	}
}

func callWithFlavor(flavor openaicompat.Flavor, dialect lipapi.ReasoningDialect) lipapi.Call {
	ext := map[string]json.RawMessage{}
	switch flavor {
	case openaicompat.FlavorResponses:
		ext["openairesponses.model"] = json.RawMessage(`"gpt-4o-mini"`)
	default:
		ext["openailegacy.model"] = json.RawMessage(`"gpt-4o-mini"`)
	}
	var part lipapi.Part
	switch dialect {
	case lipapi.ReasoningDialectOpenAIResponsesItemV1:
		opaque := json.RawMessage(`{"id":"r1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc"}`)
		part = reasoningPart(dialect, "s", opaque)
	default:
		part = reasoningPart(dialect, "think", nil)
	}
	return lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{part, lipapi.TextPart("answer")}},
		},
		Extensions: ext,
	}
}

func paramsForFlavor(t *testing.T, call lipapi.Call) ([]byte, error) {
	t.Helper()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	switch openaifamily.ResolveFlavor(call) {
	case openaicompat.FlavorResponses:
		p, err := responsesbackend.ParamsForCall(&call, cand)
		if err != nil {
			return nil, err
		}
		return json.Marshal(p)
	case openaicompat.FlavorChat:
		p, err := legacybackend.ParamsForCall(&call, cand)
		if err != nil {
			return nil, err
		}
		return json.Marshal(p)
	default:
		t.Fatalf("unexpected flavor %q", openaifamily.ResolveFlavor(call))
		return nil, nil
	}
}

func TestReasoningReplayProfile_flavorExactDialectSupport_RED(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		call       lipapi.Call
		dialect    lipapi.ReasoningDialect
		wantErr    bool
		bodySubstr []string
	}{
		{
			name:       "FlavorChat_accepts_chat_text_dialect",
			call:       callWithFlavor(openaicompat.FlavorChat, lipapi.ReasoningDialectOpenAIChatTextV1),
			dialect:    lipapi.ReasoningDialectOpenAIChatTextV1,
			bodySubstr: []string{`"reasoning_content":"think"`},
		},
		{
			name:       "FlavorResponses_accepts_responses_item_dialect",
			call:       callWithFlavor(openaicompat.FlavorResponses, lipapi.ReasoningDialectOpenAIResponsesItemV1),
			dialect:    lipapi.ReasoningDialectOpenAIResponsesItemV1,
			bodySubstr: []string{`"type":"reasoning"`, `"id":"r1"`, `"encrypted_content":"enc"`},
		},
		{
			name:    "FlavorChat_rejects_responses_item_dialect",
			call:    callWithFlavor(openaicompat.FlavorChat, lipapi.ReasoningDialectOpenAIResponsesItemV1),
			dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
			wantErr: true,
		},
		{
			name:    "FlavorResponses_rejects_chat_text_dialect",
			call:    callWithFlavor(openaicompat.FlavorResponses, lipapi.ReasoningDialectOpenAIChatTextV1),
			dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotFlavor := openaifamily.ResolveFlavor(tc.call)
			if tc.call.Extensions != nil {
				if _, ok := tc.call.Extensions[openrouterwire.ExtUpstreamFlavor]; ok {
					t.Fatalf("test setup must use extension-derived flavor only")
				}
			}
			if tc.name == "FlavorChat_accepts_chat_text_dialect" && gotFlavor != openaicompat.FlavorChat {
				t.Fatalf("ResolveFlavor = %q, want chat", gotFlavor)
			}
			if tc.name == "FlavorResponses_accepts_responses_item_dialect" && gotFlavor != openaicompat.FlavorResponses {
				t.Fatalf("ResolveFlavor = %q, want responses", gotFlavor)
			}

			raw, err := paramsForFlavor(t, tc.call)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("RED: expected flavor/dialect mismatch error for %s", tc.name)
				}
				msg := err.Error()
				lower := strings.ToLower(msg)
				if strings.Contains(lower, `part kind "reasoning" not supported`) ||
					strings.Contains(lower, `unsupported part kind "reasoning"`) ||
					strings.Contains(lower, `assistant message part kind "reasoning"`) {
					t.Fatalf("RED: dialect mismatch must not be satisfied by generic PartReasoning unsupported; got %v", err)
				}
				if !strings.Contains(lower, "dialect") && !strings.Contains(msg, string(tc.dialect)) {
					t.Fatalf("RED: expected dialect-specific incompatibility mentioning dialect id %q, got %v", tc.dialect, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RED: ParamsForCall must succeed for %s: %v", tc.name, err)
			}
			s := string(raw)
			for _, sub := range tc.bodySubstr {
				if !strings.Contains(s, sub) {
					t.Fatalf("RED: %s body missing %q, got %s", tc.name, sub, s)
				}
			}
		})
	}
}

func TestResolveFlavor_upstreamResponsesExtension(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
		},
	}
	if got := openaifamily.ResolveFlavor(call); got != openaicompat.FlavorResponses {
		t.Fatalf("ResolveFlavor = %q, want responses", got)
	}
}

func TestReasoningReplayProfile_kimiMoonshotExactFlavorAndModel_RED(t *testing.T) {
	t.Parallel()
	// Built-in Kimi/Moonshot eligibility applies across OpenAI-compatible families (catalog),
	// but replay encoding must remain exact to ResolveFlavor + candidate model.
	cases := []struct {
		name     string
		backend  string
		model    string
		flavor   openaicompat.Flavor
		dialect  lipapi.ReasoningDialect
		wantBody []string
	}{
		{
			name:     "openrouter_kimi_chat",
			backend:  "openrouter",
			model:    "moonshotai/kimi-k2",
			flavor:   openaicompat.FlavorChat,
			dialect:  lipapi.ReasoningDialectOpenAIChatTextV1,
			wantBody: []string{`"reasoning_content":"think"`, `"model":"moonshotai/kimi-k2"`},
		},
		{
			name:     "openai_legacy_moonshot_chat",
			backend:  "openai-legacy",
			model:    "moonshot-v1-8k",
			flavor:   openaicompat.FlavorChat,
			dialect:  lipapi.ReasoningDialectOpenAIChatTextV1,
			wantBody: []string{`"reasoning_content":"think"`, `"model":"moonshot-v1-8k"`},
		},
		{
			name:     "openrouter_kimi_responses",
			backend:  "openrouter",
			model:    "moonshotai/kimi-k2",
			flavor:   openaicompat.FlavorResponses,
			dialect:  lipapi.ReasoningDialectOpenAIResponsesItemV1,
			wantBody: []string{`"type":"reasoning"`, `"model":"moonshotai/kimi-k2"`},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := callWithFlavor(tc.flavor, tc.dialect)
			cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: tc.backend, Model: tc.model}}
			var (
				raw []byte
				err error
			)
			switch openaifamily.ResolveFlavor(call) {
			case openaicompat.FlavorResponses:
				p, e := responsesbackend.ParamsForCall(&call, cand)
				err = e
				if e == nil {
					raw, err = json.Marshal(p)
				}
			default:
				p, e := legacybackend.ParamsForCall(&call, cand)
				err = e
				if e == nil {
					raw, err = json.Marshal(p)
				}
			}
			if err != nil {
				t.Fatalf("RED: %s ParamsForCall must encode flavor-exact reasoning for model %q: %v", tc.backend, tc.model, err)
			}
			s := string(raw)
			for _, sub := range tc.wantBody {
				if !strings.Contains(s, sub) {
					t.Fatalf("RED: %s body missing %q, got %s", tc.name, sub, s)
				}
			}
		})
	}
}
