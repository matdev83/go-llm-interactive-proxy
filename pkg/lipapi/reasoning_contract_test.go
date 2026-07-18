package lipapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func mustOpaqueJSON(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	b := json.RawMessage(raw)
	if !json.Valid(b) {
		t.Fatalf("test setup requires valid JSON opaque, got %q", raw)
	}
	return append(json.RawMessage(nil), b...)
}

func reasoningPart(dialect lipapi.ReasoningDialect, text, signature string, opaque json.RawMessage) lipapi.Part {
	var op json.RawMessage
	if len(opaque) > 0 {
		op = append(json.RawMessage(nil), opaque...)
	}
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect:   dialect,
			Text:      text,
			Signature: signature,
			Opaque:    op,
		},
	}
}

func assistantReasoningCall(parts ...lipapi.Part) lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleAssistant,
			Parts: parts,
		}},
	}
}

func requireValidationError(t *testing.T, err error) *lipapi.ValidationError {
	t.Helper()
	if err == nil {
		t.Fatal("expected ValidationError")
	}
	var ve *lipapi.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T (%v)", err, err)
	}
	if strings.Contains(strings.ToLower(ve.Message), "unknown part kind") {
		t.Fatalf("Phase 2.1 must emit a reasoning-specific ValidationError, got %q", ve.Message)
	}
	return ve
}

func TestReasoningPart_assistantOnlyOrdered(t *testing.T) {
	t.Parallel()

	t.Run("accepts_ordered_assistant_reasoning", func(t *testing.T) {
		t.Parallel()
		valid := lipapi.Call{
			Messages: []lipapi.Message{
				{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("q")}},
				{
					Role: lipapi.RoleAssistant,
					Parts: []lipapi.Part{
						reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "think-1", "", nil),
						lipapi.TextPart("mid"),
						reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "think-2", "", nil),
						lipapi.TextPart("answer"),
					},
				},
			},
		}
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid ordered assistant reasoning must Validate: %v", err)
		}
	})

	for _, role := range []lipapi.Role{lipapi.RoleUser, lipapi.RoleSystem, lipapi.RoleTool} {
		role := role
		t.Run("rejects_"+string(role), func(t *testing.T) {
			t.Parallel()
			call := lipapi.Call{
				Messages: []lipapi.Message{{
					Role:  role,
					Parts: []lipapi.Part{reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "nope", "", nil)},
				}},
			}
			ve := requireValidationError(t, call.Validate())
			if !strings.Contains(strings.ToLower(ve.Message), "assistant") {
				t.Fatalf("expected assistant-role rejection, field=%q message=%q", ve.Field, ve.Message)
			}
		})
	}
}

func TestReasoningPart_dialectAndPayloadValidation(t *testing.T) {
	t.Parallel()

	t.Run("requires_dialect", func(t *testing.T) {
		t.Parallel()
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart("", "text", "", nil)).Validate())
		if !strings.Contains(strings.ToLower(ve.Message), "dialect") {
			t.Fatalf("expected dialect rejection, got %q", ve.Message)
		}
	})

	t.Run("requires_at_least_one_payload", func(t *testing.T) {
		t.Parallel()
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "", "", nil)).Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "payload") && !(strings.Contains(msg, "text") && strings.Contains(msg, "signature")) {
			t.Fatalf("expected empty-payload rejection, got %q", ve.Message)
		}
	})

	t.Run("opaque_must_be_valid_json", func(t *testing.T) {
		t.Parallel()
		invalid := json.RawMessage(`{not-json`)
		if json.Valid(invalid) {
			t.Fatal("test setup: opaque must be invalid JSON")
		}
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(
			lipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", invalid,
		)).Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "opaque") || !strings.Contains(msg, "json") {
			t.Fatalf("expected opaque JSON rejection, got %q", ve.Message)
		}
	})

	t.Run("reasoning_pointer_required", func(t *testing.T) {
		t.Parallel()
		ve := requireValidationError(t, lipapi.Call{
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleAssistant,
				Parts: []lipapi.Part{{Kind: lipapi.PartReasoning}},
			}},
		}.Validate())
		if !strings.Contains(ve.Message, "Reasoning") {
			t.Fatalf("expected nil Reasoning rejection, got %q", ve.Message)
		}
	})

	t.Run("reasoning_field_only_for_reasoning_kind", func(t *testing.T) {
		t.Parallel()
		ve := requireValidationError(t, lipapi.Call{
			Messages: []lipapi.Message{{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{{
					Kind:      lipapi.PartText,
					Text:      "visible",
					Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: "leak"},
				}},
			}},
		}.Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "reasoning") {
			t.Fatalf("expected cross-kind Reasoning rejection, got %q", ve.Message)
		}
	})

	t.Run("accepts_each_initial_dialect", func(t *testing.T) {
		t.Parallel()
		for _, d := range []lipapi.ReasoningDialect{
			lipapi.ReasoningDialectOpenAIChatTextV1,
			lipapi.ReasoningDialectOpenAIResponsesItemV1,
			lipapi.ReasoningDialectAnthropicThinkingV1,
			lipapi.ReasoningDialectAnthropicRedactedThinkingV1,
		} {
			if err := assistantReasoningCall(reasoningPart(d, "x", "", nil)).Validate(); err != nil {
				t.Fatalf("dialect %q: %v", d, err)
			}
		}
	})

	t.Run("accepts_signature_only", func(t *testing.T) {
		t.Parallel()
		if err := assistantReasoningCall(reasoningPart(lipapi.ReasoningDialectAnthropicThinkingV1, "", "sig_only", nil)).Validate(); err != nil {
			t.Fatalf("signature-only reasoning: %v", err)
		}
	})

	t.Run("accepts_opaque_only", func(t *testing.T) {
		t.Parallel()
		opaque := mustOpaqueJSON(t, `{"data":"x"}`)
		if err := assistantReasoningCall(reasoningPart(lipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", opaque)).Validate(); err != nil {
			t.Fatalf("opaque-only reasoning: %v", err)
		}
	})
}

func TestReasoningPart_byteAndCountLimits(t *testing.T) {
	t.Run("dialect_bytes", func(t *testing.T) {
		d := lipapi.ReasoningDialect(strings.Repeat("d", lipapi.MaxReasoningDialectBytes+1))
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(d, "t", "", nil)).Validate())
		if !strings.Contains(strings.ToLower(ve.Message), "dialect") {
			t.Fatalf("expected dialect size rejection, got %q", ve.Message)
		}
	})

	t.Run("text_bytes", func(t *testing.T) {
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(
			lipapi.ReasoningDialectOpenAIChatTextV1,
			strings.Repeat("t", lipapi.MaxReasoningTextBytes+1),
			"",
			nil,
		)).Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "text") || !strings.Contains(msg, "exceed") {
			t.Fatalf("expected reasoning text size rejection, got %q", ve.Message)
		}
	})

	t.Run("signature_bytes", func(t *testing.T) {
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(
			lipapi.ReasoningDialectAnthropicThinkingV1,
			"",
			strings.Repeat("s", lipapi.MaxReasoningSignatureBytes+1),
			nil,
		)).Validate())
		if !strings.Contains(strings.ToLower(ve.Message), "signature") {
			t.Fatalf("expected signature size rejection, got %q", ve.Message)
		}
	})

	t.Run("opaque_bytes", func(t *testing.T) {
		raw := make(json.RawMessage, lipapi.MaxReasoningOpaqueBytes+1)
		raw[0] = '"'
		for i := 1; i < len(raw)-1; i++ {
			raw[i] = 'x'
		}
		raw[len(raw)-1] = '"'
		if !json.Valid(raw) {
			t.Fatal("test setup: oversized opaque must still be valid JSON")
		}
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(
			lipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", raw,
		)).Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "opaque") || !strings.Contains(msg, "exceed") {
			t.Fatalf("expected opaque size rejection, got %q", ve.Message)
		}
	})

	t.Run("per_message_part_count_via_reasoning_alias", func(t *testing.T) {
		if lipapi.MaxReasoningPartsPerMessage != lipapi.MaxPartsPerMessage {
			t.Fatalf("MaxReasoningPartsPerMessage must alias MaxPartsPerMessage (no distinct bound approved), got %d vs %d",
				lipapi.MaxReasoningPartsPerMessage, lipapi.MaxPartsPerMessage)
		}
		parts := make([]lipapi.Part, 0, lipapi.MaxReasoningPartsPerMessage+1)
		for i := 0; i < lipapi.MaxReasoningPartsPerMessage+1; i++ {
			parts = append(parts, reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "x", "", nil))
		}
		ve := requireValidationError(t, lipapi.Call{
			Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: parts}},
		}.Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "at most") || !strings.Contains(msg, "parts") {
			t.Fatalf("expected generic MaxPartsPerMessage envelope rejection (reasoning alias), got %q", ve.Message)
		}
	})

	t.Run("per_call_total_reasoning_bytes", func(t *testing.T) {
		half := lipapi.MaxReasoningBytesPerCall/2 + 1
		ve := requireValidationError(t, lipapi.Call{
			Messages: []lipapi.Message{{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, strings.Repeat("a", half), "", nil),
					lipapi.TextPart("mid"),
					reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, strings.Repeat("b", half), "", nil),
				},
			}},
		}.Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "reasoning") || !strings.Contains(msg, "exceed") {
			t.Fatalf("expected per-call reasoning byte rejection, got %q", ve.Message)
		}
	})

	t.Run("combined_payload_bytes_count_toward_call_limit", func(t *testing.T) {
		sig := strings.Repeat("s", 100)
		text := strings.Repeat("t", lipapi.MaxReasoningBytesPerCall-50)
		opaque := mustOpaqueJSON(t, `{"x":"`+strings.Repeat("o", 40)+`"}`)
		ve := requireValidationError(t, assistantReasoningCall(reasoningPart(
			lipapi.ReasoningDialectAnthropicThinkingV1, text, sig, opaque,
		)).Validate())
		msg := strings.ToLower(ve.Message)
		if !strings.Contains(msg, "reasoning") || !strings.Contains(msg, "exceed") {
			t.Fatalf("expected combined reasoning byte rejection, got %q", ve.Message)
		}
	})
}

func TestCloneCall_deepCopiesReasoningOpaque(t *testing.T) {
	t.Parallel()

	opaque := mustOpaqueJSON(t, `{"k":"v"}`)
	orig := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				reasoningPart(lipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", opaque),
				lipapi.TextPart("hi"),
			},
		}},
	}
	cl := lipapi.CloneCall(orig)
	if cl.Messages[0].Parts[0].Reasoning == nil {
		t.Fatal("clone lost Reasoning")
	}
	if orig.Messages[0].Parts[0].Reasoning == cl.Messages[0].Parts[0].Reasoning {
		t.Fatal("Reasoning pointer must not be shared across CloneCall")
	}
	if &orig.Messages[0].Parts[0].Reasoning.Opaque[0] == &cl.Messages[0].Parts[0].Reasoning.Opaque[0] {
		t.Fatal("Opaque backing array must not be shared across CloneCall")
	}
	cl.Messages[0].Parts[0].Reasoning.Opaque[2] = 'X'
	if bytes.Equal(orig.Messages[0].Parts[0].Reasoning.Opaque, cl.Messages[0].Parts[0].Reasoning.Opaque) {
		t.Fatal("mutating clone Opaque must not mutate original")
	}
	if string(orig.Messages[0].Parts[0].Reasoning.Opaque) != `{"k":"v"}` {
		t.Fatalf("original Opaque mutated: %s", orig.Messages[0].Parts[0].Reasoning.Opaque)
	}
}

func TestReasoning_equalityViaDeepEqualAndClone(t *testing.T) {
	t.Parallel()

	opaque := mustOpaqueJSON(t, `{"a":1}`)
	left := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "r", "sig", opaque)
	right := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "r", "sig", mustOpaqueJSON(t, `{"a":1}`))
	if !reflect.DeepEqual(left, right) {
		t.Fatal("equivalent reasoning parts must DeepEqual")
	}
	right.Reasoning.Opaque[2] = '9'
	if reflect.DeepEqual(left, right) {
		t.Fatal("distinct Opaque bytes must not DeepEqual")
	}

	orig := assistantReasoningCall(left, lipapi.TextPart("visible"))
	cl := lipapi.CloneCall(orig)
	if !reflect.DeepEqual(orig.Messages[0].Parts[0].Reasoning.Text, cl.Messages[0].Parts[0].Reasoning.Text) {
		t.Fatal("clone must preserve reasoning text for equality comparisons")
	}
	cl.Messages[0].Parts[0].Reasoning.Text = "mutated"
	if orig.Messages[0].Parts[0].Reasoning.Text != "r" {
		t.Fatal("equality helpers/callers must observe independent Reasoning values after CloneCall")
	}
}

func TestRequiredCapabilities_hardReasoningReplay(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("q")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "think", "", nil),
					lipapi.TextPart("a"),
				},
			},
		},
		Options: lipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	got := lipapi.RequiredCapabilities(call)
	if !slices.Contains(got, lipapi.CapabilityReasoningReplay) {
		t.Fatalf("expected CapabilityReasoningReplay in %v", got)
	}
	if !slices.Contains(got, lipapi.CapabilityReasoning) {
		t.Fatalf("expected soft CapabilityReasoning still derived from effort in %v", got)
	}

	without := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("q")}}},
		Options:  lipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	gotWithout := lipapi.RequiredCapabilities(without)
	if slices.Contains(gotWithout, lipapi.CapabilityReasoningReplay) {
		t.Fatalf("CapabilityReasoningReplay must not appear without historical reasoning: %v", gotWithout)
	}
}

func TestNegotiate_reasoningReplayNotSoftDowngradable_characterization(t *testing.T) {
	t.Parallel()

	res := lipapi.Negotiate(
		[]lipapi.Capability{lipapi.CapabilityReasoningReplay, lipapi.CapabilityReasoning},
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityReasoning),
	)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("missing reasoning_replay must hard-reject, got kind=%s missing=%v downgraded=%v", res.Kind, res.Missing, res.Downgraded)
	}
	if !slices.Contains(res.Missing, lipapi.CapabilityReasoningReplay) {
		t.Fatalf("expected reasoning_replay in Missing, got %v", res.Missing)
	}
	if slices.Contains(res.Downgraded, lipapi.CapabilityReasoningReplay) {
		t.Fatalf("reasoning_replay must never appear in Downgraded: %v", res.Downgraded)
	}
	if slices.Contains(res.Downgraded, lipapi.CapabilityReasoning) {
		t.Fatal("soft reasoning must not soft-downgrade when a hard missing capability forces reject")
	}

	_ = lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}}
}

func TestApplyNegotiatedDowngrades_doesNotStripHistoricalReasoning_characterization(t *testing.T) {
	t.Parallel()

	par := true
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "keep-me", "", nil),
				lipapi.TextPart("a"),
			},
		}},
		Options: lipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &par,
		},
	}
	soft := lipapi.Negotiate(
		[]lipapi.Capability{lipapi.CapabilityReasoning, lipapi.CapabilityParallelToolCalls},
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
	)
	if soft.Kind != lipapi.NegotiationDowngrade {
		t.Fatalf("precondition soft downgrade: %s", soft.Kind)
	}
	lipapi.ApplyNegotiatedDowngrades(&call, soft)
	if call.Options.ReasoningEffort != "" {
		t.Fatalf("soft reasoning effort should strip: %q", call.Options.ReasoningEffort)
	}
	if call.Messages[0].Parts[0].Kind != lipapi.PartReasoning || call.Messages[0].Parts[0].Reasoning == nil || call.Messages[0].Parts[0].Reasoning.Text != "keep-me" {
		t.Fatalf("historical reasoning parts must survive ApplyNegotiatedDowngrades: %+v", call.Messages[0].Parts[0])
	}

	call2 := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "keep-2", "", nil),
				lipapi.TextPart("a"),
			},
		}},
	}
	lipapi.ApplyNegotiatedDowngrades(&call2, lipapi.NegotiationResult{
		Kind:       lipapi.NegotiationDowngrade,
		Downgraded: []lipapi.Capability{lipapi.CapabilityReasoningReplay},
	})
	if call2.Messages[0].Parts[0].Reasoning == nil || call2.Messages[0].Parts[0].Reasoning.Text != "keep-2" {
		t.Fatal("ApplyNegotiatedDowngrades must never strip historical reasoning parts")
	}
}

func TestReasoningFixtures_validate(t *testing.T) {
	t.Parallel()

	type fixtureExpect struct {
		name             string
		assistantIdx     int
		reasoningKinds   int
		firstDialect     lipapi.ReasoningDialect
		requireOpaque    bool
		requireSignature bool
	}
	cases := []fixtureExpect{
		{name: "text_reasoning.json", assistantIdx: 1, reasoningKinds: 1, firstDialect: lipapi.ReasoningDialectOpenAIChatTextV1},
		{name: "signed_thinking.json", assistantIdx: 1, reasoningKinds: 1, firstDialect: lipapi.ReasoningDialectAnthropicThinkingV1, requireSignature: true},
		{name: "redacted_opaque_thinking.json", assistantIdx: 1, reasoningKinds: 1, firstDialect: lipapi.ReasoningDialectAnthropicRedactedThinkingV1, requireOpaque: true},
		{name: "multiple_blocks.json", assistantIdx: 1, reasoningKinds: 2, firstDialect: lipapi.ReasoningDialectOpenAIResponsesItemV1, requireOpaque: true},
		{name: "interleaved_tool_calls.json", assistantIdx: 1, reasoningKinds: 2, firstDialect: lipapi.ReasoningDialectAnthropicThinkingV1, requireSignature: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("testdata", "reasoning", tc.name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if !json.Valid(raw) {
				t.Fatal("fixture file must be valid JSON")
			}
			var call lipapi.Call
			if err := json.Unmarshal(raw, &call); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			if tc.assistantIdx >= len(call.Messages) || call.Messages[tc.assistantIdx].Role != lipapi.RoleAssistant {
				t.Fatalf("fixture missing assistant message at %d", tc.assistantIdx)
			}
			var reasoningCount int
			var first *lipapi.ReasoningPart
			for _, p := range call.Messages[tc.assistantIdx].Parts {
				if p.Kind != lipapi.PartReasoning {
					continue
				}
				reasoningCount++
				if p.Reasoning == nil {
					t.Fatal("reasoning kind requires Reasoning payload in fixture")
				}
				if first == nil {
					first = p.Reasoning
				}
				if p.Reasoning.Opaque != nil && !json.Valid(p.Reasoning.Opaque) {
					t.Fatalf("fixture opaque must be valid JSON: %s", p.Reasoning.Opaque)
				}
			}
			if reasoningCount != tc.reasoningKinds {
				t.Fatalf("reasoning parts=%d want %d", reasoningCount, tc.reasoningKinds)
			}
			if first == nil || first.Dialect != tc.firstDialect {
				t.Fatalf("first dialect=%q want %q", first, tc.firstDialect)
			}
			if tc.requireSignature && first.Signature == "" {
				t.Fatal("fixture must carry signature")
			}
			if tc.requireOpaque && len(first.Opaque) == 0 {
				t.Fatal("fixture must carry opaque")
			}

			if err := call.Validate(); err != nil {
				t.Fatalf("fixture must Validate once reasoning contracts are implemented: %v", err)
			}
			req := lipapi.RequiredCapabilities(call)
			if !slices.Contains(req, lipapi.CapabilityReasoningReplay) {
				t.Fatalf("fixture must require reasoning_replay, got %v", req)
			}
		})
	}
}
