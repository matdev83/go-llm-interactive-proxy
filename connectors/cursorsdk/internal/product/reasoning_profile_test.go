package product

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

func TestReasoningProfile_AnonymousEffortThinkingExactValues(t *testing.T) {
	t.Parallel()
	rows := []protocol.ModelRow{{
		ID:          "claude-anon",
		DisplayName: "Claude Anon",
		Parameters: []protocol.ModelParameter{
			{ID: "effort", Values: []string{"high", "extra-high", "xhigh"}},
		},
		Variants: []protocol.ModelVariant{
			{DisplayName: "High", Params: map[string]any{"effort": "high", "thinking": true}},
			{DisplayName: "Extra High", Params: map[string]any{"effort": "extra-high", "thinking": true}},
		},
	}}
	_, entries, err := normalizeModelRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	cat := NewCatalog()
	cat.Replace(entries)
	p := cat.reasoningProfile("claude-anon")
	if p.Mode != reasoningModeEffort {
		t.Fatalf("mode = %q, want effort", p.Mode)
	}
	if !p.acceptsExact("extra-high") || !p.acceptsExact("high") {
		t.Fatalf("profile = %+v missing exact effort values", p)
	}
	if p.acceptsExact("xhigh") {
		t.Fatal("must not alias xhigh onto effort values")
	}
	if p.acceptsExact("medium") {
		t.Fatal("medium has no thinking=true anonymous variant")
	}
}

func TestReasoningProfile_ExactValuesNoAlias(t *testing.T) {
	t.Parallel()
	rows := mustLoadSanitizedRows(t)
	_, entries, err := normalizeModelRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	cat := NewCatalog()
	cat.Replace(entries)

	gpt := cat.reasoningProfile("gpt-5.3-codex")
	if gpt.Mode != reasoningModeReasoning {
		t.Fatalf("gpt mode = %q, want reasoning", gpt.Mode)
	}
	if !gpt.acceptsExact("xhigh") {
		t.Fatal("gpt must accept exact xhigh")
	}
	if gpt.acceptsExact("extra-high") {
		t.Fatal("gpt must not alias extra-high onto reasoning values")
	}

	claude := cat.reasoningProfile("claude-4.6-sonnet-thinking")
	if claude.Mode != reasoningModeEffort {
		t.Fatalf("claude mode = %q, want effort", claude.Mode)
	}
	if !claude.acceptsExact("extra-high") {
		t.Fatal("claude must accept exact extra-high via thinking variant")
	}
	if !claude.acceptsExact("high") {
		t.Fatal("claude must accept exact high via thinking variant")
	}
	if claude.acceptsExact("xhigh") {
		t.Fatal("claude must not alias xhigh onto effort values")
	}
	if claude.acceptsExact("medium") {
		t.Fatal("medium is advertised on effort param but has no thinking=true variant in fixture")
	}

	composer := cat.reasoningProfile("composer-2-fast")
	if composer.Mode != reasoningModeNone || len(composer.Values) != 0 {
		t.Fatalf("composer profile = %+v, want none", composer)
	}
	if composer.acceptsExact("xhigh") || composer.acceptsExact("extra-high") {
		t.Fatal("boolean-thinking-only must accept no reasoning values")
	}
}

func mustLoadSanitizedRows(t *testing.T) []protocol.ModelRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "models_sanitized.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []protocol.ModelRow `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Models
}
