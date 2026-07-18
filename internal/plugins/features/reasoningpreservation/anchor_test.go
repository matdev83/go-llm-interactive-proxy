package reasoningpreservation_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func computeAnchor(t *testing.T, msg lipapi.Message) [32]byte {
	t.Helper()
	anchor, err := reasoningpreservation.ComputeAnchor(msg)
	redNotImplemented(t, err, "ComputeAnchor must be implemented")
	if err != nil {
		t.Fatalf("ComputeAnchor: %v", err)
	}
	return anchor
}

func derivePlacements(t *testing.T, nonReasoningCount int, reasoning []lipapi.Part) []reasoningpreservation.PlacedReasoning {
	t.Helper()
	got, err := reasoningpreservation.DerivePlacements(nonReasoningCount, reasoning)
	redNotImplemented(t, err, "DerivePlacements must be implemented")
	if err != nil {
		t.Fatalf("DerivePlacements: %v", err)
	}
	return got
}

func TestComputeAnchor_assistantOnlyExcludesReasoningPreservesOrder(t *testing.T) {
	t.Parallel()
	base := assistantMsg(
		lipapi.TextPart("intro"),
		jsonPart(`{"tool":"search","args":{"q":"weather"}}`),
		lipapi.TextPart("answer"),
	)
	withReasoning := assistantMsg(
		lipapi.TextPart("intro"),
		reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "hidden-thought", "", nil),
		jsonPart(`{"tool":"search","args":{"q":"weather"}}`),
		reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "more-hidden", "", nil),
		lipapi.TextPart("answer"),
	)
	a1 := computeAnchor(t, base)
	a2 := computeAnchor(t, withReasoning)
	if a1 != a2 {
		t.Fatal("reasoning parts must be excluded from anchor")
	}
	reordered := assistantMsg(
		lipapi.TextPart("answer"),
		jsonPart(`{"tool":"search","args":{"q":"weather"}}`),
		lipapi.TextPart("intro"),
	)
	if computeAnchor(t, base) == computeAnchor(t, reordered) {
		t.Fatal("non-reasoning order must affect anchor")
	}
}

func TestComputeAnchor_rejectsNonAssistant(t *testing.T) {
	t.Parallel()
	msg := lipapi.Message{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("hello")},
	}
	_, err := reasoningpreservation.ComputeAnchor(msg)
	redNotImplemented(t, err, "ComputeAnchor must reject non-assistant roles")
	if err == nil {
		t.Fatal("expected non-assistant rejection")
	}
}

func TestComputeAnchor_jsonObjectKeySortStable(t *testing.T) {
	t.Parallel()
	a := assistantMsg(jsonPart(`{"b":2,"a":1,"nested":{"z":9,"m":4}}`))
	b := assistantMsg(jsonPart(`{"nested":{"m":4,"z":9},"a":1,"b":2}`))
	if computeAnchor(t, a) != computeAnchor(t, b) {
		t.Fatal("JSON object key order must not change anchor")
	}
}

func TestComputeAnchor_jsonArrayOrderPreserved(t *testing.T) {
	t.Parallel()
	a := assistantMsg(jsonPart(`{"items":[1,2,3]}`))
	b := assistantMsg(jsonPart(`{"items":[3,2,1]}`))
	if computeAnchor(t, a) == computeAnchor(t, b) {
		t.Fatal("JSON array order must affect anchor")
	}
}

func TestComputeAnchor_numericLexicalPolicy(t *testing.T) {
	t.Parallel()
	one := assistantMsg(jsonPart(`{"n":1}`))
	oneFloat := assistantMsg(jsonPart(`{"n":1.0}`))
	oneExp := assistantMsg(jsonPart(`{"n":1e0}`))
	tenNum := assistantMsg(jsonPart(`{"n":10}`))
	two := assistantMsg(jsonPart(`{"n":2}`))
	tenStr := assistantMsg(jsonPart(`{"n":"10"}`))

	aOne := computeAnchor(t, one)
	aOneFloat := computeAnchor(t, oneFloat)
	aOneExp := computeAnchor(t, oneExp)
	if aOne == aOneFloat || aOne == aOneExp || aOneFloat == aOneExp {
		t.Fatal("1, 1.0, 1e0 must produce distinct anchors (lexical preserve)")
	}
	if computeAnchor(t, tenNum) == computeAnchor(t, two) {
		t.Fatal("distinct numeric values must not collide")
	}
	if computeAnchor(t, tenNum) == computeAnchor(t, tenStr) {
		t.Fatal("number vs string must not normalize to same anchor")
	}
}

func TestComputeAnchor_toolResultPartBoundary(t *testing.T) {
	t.Parallel()
	withTool := assistantMsg(
		lipapi.TextPart("before"),
		lipapi.Part{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Content:    jsonPart(`{"x":1}`).Content,
		},
		lipapi.TextPart("after"),
	)
	withoutTool := assistantMsg(lipapi.TextPart("before"), lipapi.TextPart("after"))
	if computeAnchor(t, withTool) == computeAnchor(t, withoutTool) {
		t.Fatal("tool_result parts must affect anchor")
	}
}

func TestDerivePlacements_indexesInRangeAndEqualIndexOrder(t *testing.T) {
	t.Parallel()
	reasoning := []lipapi.Part{
		reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "first", "", nil),
		reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "second", "", nil),
		reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "third", "", nil),
	}
	const nonReasoningCount = 2
	got := derivePlacements(t, nonReasoningCount, reasoning)
	if len(got) != len(reasoning) {
		t.Fatalf("len=%d want %d", len(got), len(reasoning))
	}
	for i, p := range got {
		if p.BeforeNonReasoningPart < 0 || p.BeforeNonReasoningPart > nonReasoningCount {
			t.Fatalf("placement[%d] index=%d out of [0,%d]", i, p.BeforeNonReasoningPart, nonReasoningCount)
		}
		if p.Part.Reasoning == nil || p.Part.Reasoning.Text != reasoning[i].Reasoning.Text {
			t.Fatalf("placement[%d] must preserve reasoning block order", i)
		}
	}
}

func FuzzComputeAnchor(f *testing.F) {
	seedDir := filepath.Join("testdata", "anchor_seeds")
	entries, err := os.ReadDir(seedDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, readErr := os.ReadFile(filepath.Join(seedDir, e.Name()))
			if readErr != nil {
				continue
			}
			f.Add(b)
		}
	}
	f.Add([]byte("plain assistant text"))
	f.Add([]byte(`{"k":"v","arr":[1,2]}`))
	f.Add([]byte("tool-result payload"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) == 0 {
			return
		}
		msg := assistantMsg(lipapi.TextPart(string(raw)))
		first, err := reasoningpreservation.ComputeAnchor(msg)
		if errors.Is(err, reasoningpreservation.ErrNotImplemented) {
			return
		}
		if err != nil {
			if msg.Role != lipapi.RoleAssistant {
				return
			}
			t.Fatalf("unexpected error: %v", err)
		}
		second, err := reasoningpreservation.ComputeAnchor(msg)
		if err != nil {
			t.Fatalf("second ComputeAnchor: %v", err)
		}
		if first != second {
			t.Fatal("ComputeAnchor must be deterministic")
		}
		if bytes.Equal(first[:], make([]byte, 32)) {
			t.Fatal("anchor must not be all-zero for non-empty assistant content")
		}
	})
}
