package modelcatalog_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDefaultSizeEstimator_textOnly_available(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("hello"),
				lipapi.TextPart(" world"),
			},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available {
		t.Fatalf("expected available estimate, got %+v", got)
	}
	if got.Units != "bytes" {
		t.Fatalf("Units: got %q want bytes", got.Units)
	}
	if got.Input != 11 { // "hello" + " world"
		t.Fatalf("Input: got %d want 11", got.Input)
	}
	if got.Basis != modelcatalog.EstimateBasisCanonicalUTF8 {
		t.Fatalf("Basis: got %q", got.Basis)
	}
}

func TestDefaultSizeEstimator_decisionPayloadCounts(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Decision: &lipapi.DecisionRequest{
		Evidence: json.RawMessage(`"data"`),
		Questions: []lipapi.DecisionQuestion{
			{ID: "yes", Kind: lipapi.DecisionKindNoul, Instructions: json.RawMessage(`"ask"`), TrueCriteria: json.RawMessage(`"ok"`), FalseCriteria: json.RawMessage(`"no"`)},
			{ID: "pick", Kind: lipapi.DecisionKindChoice, Options: []lipapi.DecisionOption{{Name: "a", Description: json.RawMessage(`"one"`)}}},
			{ID: "grade", Kind: lipapi.DecisionKindScore, Levels: []json.RawMessage{json.RawMessage(`"lo"`), json.RawMessage(`"hi"`)}},
		},
	}}
	est := modelcatalog.DefaultSizeEstimator{}
	got := est.EstimateRequestTokens(t.Context(), call)
	// Original JSON bytes: 6 evidence + 5 instruction + 8 noul criteria +
	// 5 option description + 8 score levels.
	if !got.Available || got.Units != "bytes" || got.Input != 32 {
		t.Fatalf("decision estimate = %+v, want 32 available bytes", got)
	}
	call.Decision.Evidence = json.RawMessage(`"data-more"`)
	if got := est.Estimate(t.Context(), call); got.Input != 37 {
		t.Fatalf("larger decision estimate = %+v, want 37 bytes", got)
	}
}

func TestDefaultSizeEstimator_instructions_counted(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("sys")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("u")},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available || got.Input != 4 { // sys + u
		t.Fatalf("got %+v", got)
	}
}

func TestDefaultSizeEstimator_tools_add_bytes(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "fn", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available {
		t.Fatalf("got %+v", got)
	}
	if got.Input <= 1 {
		t.Fatalf("expected tools to add bytes, got %d", got.Input)
	}
	if got.Basis != modelcatalog.EstimateBasisCanonicalUTF8AndTools {
		t.Fatalf("Basis: got %q want tools composite", got.Basis)
	}
}

func TestDefaultSizeEstimator_sessionHints_noContribution_unavailable(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ResumeToken: "token",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if got.Available {
		t.Fatalf("expected unavailable when session contribution missing, got %+v", got)
	}
	if got.Basis != modelcatalog.EstimateBasisSessionContributionUnavailable {
		t.Fatalf("Basis: got %q", got.Basis)
	}
}

func TestDefaultSizeEstimator_sessionHints_withContribution_available(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ResumeToken: "token",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("ab")},
		}},
	}
	ctx := modelcatalog.WithSessionSizeContribution(context.Background(), 100)
	got := est.Estimate(ctx, call)
	if !got.Available {
		t.Fatalf("got %+v", got)
	}
	if got.Input != 2+100 {
		t.Fatalf("Input: got %d want 102", got.Input)
	}
	if got.Basis != modelcatalog.EstimateBasisCanonicalUTF8AndSession {
		t.Fatalf("Basis: got %q", got.Basis)
	}
}

func TestDefaultSizeEstimator_jsonPart_bytes(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	raw := json.RawMessage(`{"k":1}`)
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: raw,
			}},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available || got.Input != int64(len(raw)) {
		t.Fatalf("got %+v", got)
	}
}

func TestDefaultSizeEstimator_countsReasoningParts(t *testing.T) {
	t.Parallel()
	est := modelcatalog.DefaultSizeEstimator{}
	opaque := json.RawMessage(`{"data":"x"}`)
	if !json.Valid(opaque) {
		t.Fatal("opaque must be valid JSON")
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				{
					Kind: lipapi.PartReasoning,
					Reasoning: &lipapi.ReasoningPart{
						Dialect:   lipapi.ReasoningDialectOpenAIChatTextV1,
						Text:      "abcd",
						Signature: "sig",
						Opaque:    opaque,
					},
				},
				lipapi.TextPart("xy"),
			},
		}},
	}
	got := est.Estimate(context.Background(), call)
	if !got.Available {
		t.Fatalf("expected available estimate, got %+v", got)
	}
	want := int64(len("abcd") + len("sig") + len(opaque) + len("xy"))
	if got.Input != want {
		t.Fatalf("Input=%d want %d (reasoning text+signature+opaque must participate in request sizing)", got.Input, want)
	}
}
