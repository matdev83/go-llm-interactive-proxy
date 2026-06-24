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
