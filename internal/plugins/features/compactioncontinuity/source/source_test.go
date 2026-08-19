package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type testCarrierRecognizer struct {
	byID map[string]StructuredCarrier
}

func (r testCarrierRecognizer) Recognize(item lipapi.Item) (StructuredCarrier, bool) {
	c, ok := r.byID[item.ID]
	return c, ok
}

type testRedactor struct {
	seen  []string
	value string
	err   error
}

func (r *testRedactor) Redact(_ context.Context, input string) (string, error) {
	r.seen = append(r.seen, input)
	if r.err != nil {
		return "", r.err
	}
	if r.value != "" {
		return strings.ReplaceAll(input, "secret", r.value), nil
	}
	return input, nil
}

func textItem(id string, role lipapi.Role, text string) lipapi.Item {
	return lipapi.Item{Kind: lipapi.ItemKindMessage, ID: id, Role: role, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}}}
}

func toolResultItem(id, output string) lipapi.Item {
	return lipapi.Item{Kind: lipapi.ItemKindToolResult, ID: id, ToolResult: &lipapi.ToolResultItem{CallID: id + "-call", Name: "shell", Output: output}}
}

func prepareCall(items ...lipapi.Item) lipapi.Call {
	return lipapi.Call{Items: items}
}

func TestPrepare_prioritizesUserAssistantCarrierAndDropsOrdinaryTool(t *testing.T) {
	t.Parallel()
	call := prepareCall(
		textItem("a", lipapi.RoleAssistant, "I will make a plan and inspect the options."),
		toolResultItem("tool", "compiler output: PASS; unrelated log"),
		textItem("u", lipapi.RoleUser, "I choose the Postgres implementation and require encryption."),
	)
	got, err := Prepare(t.Context(), Input{Call: call, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 2 {
		t.Fatalf("entries=%d want user+assistant only: %#v", len(got.Envelope.Entries), got.Envelope.Entries)
	}
	if got.Envelope.Entries[0].Kind != EntryUserDecision {
		t.Fatalf("first entry=%q want user decision", got.Envelope.Entries[0].Kind)
	}
	if got.Envelope.Entries[1].Kind != EntryAssistantPlan {
		t.Fatalf("second entry=%q want assistant plan", got.Envelope.Entries[1].Kind)
	}
}

func TestPrepare_dropsMediaReasoningAndCodeDump(t *testing.T) {
	t.Parallel()
	call := prepareCall(
		textItem("u", lipapi.RoleUser, "```go\nfunc main() { fmt.Println(\"large dump\") }\n```"),
		lipapi.Item{Kind: lipapi.ItemKindMessage, ID: "media", Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartImageRef, ImageRef: "data:image/png;base64,AAAA"}}},
		lipapi.Item{Kind: lipapi.ItemKindReasoning, ID: "reasoning", Reasoning: &lipapi.ReasoningItem{Reasoning: &lipapi.ReasoningPart{Dialect: "test.reasoning", Text: "internal reasoning about a plan"}}},
	)
	got, err := Prepare(t.Context(), Input{Call: call, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 0 {
		t.Fatalf("entries=%d want no media/reasoning/code dump: %#v", len(got.Envelope.Entries), got.Envelope.Entries)
	}
}

func TestPrepare_assistantPlanningRequiresPlanningSignal(t *testing.T) {
	t.Parallel()
	call := prepareCall(
		textItem("plain", lipapi.RoleAssistant, "The answer is 42."),
		textItem("plan", lipapi.RoleAssistant, "Plan: use a bounded adapter, then validate the result."),
	)
	got, err := Prepare(t.Context(), Input{Call: call, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 1 || got.Envelope.Entries[0].ItemID != "plan" {
		t.Fatalf("unexpected assistant entries: %#v", got.Envelope.Entries)
	}
}

func TestPrepare_retainsSmallRelevantToolAsDelimitedUntrusted(t *testing.T) {
	t.Parallel()
	call := prepareCall(toolResultItem("tool", "todo status: implement the billing constraint"))
	cfg := DefaultConfig()
	cfg.MaxUntrustedToolBytes = 256
	got, err := Prepare(t.Context(), Input{Call: call, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 1 {
		t.Fatalf("entries=%d want one bounded tool entry", len(got.Envelope.Entries))
	}
	e := got.Envelope.Entries[0]
	if !e.Untrusted || e.Kind != EntryUntrustedTool || !strings.HasPrefix(e.Text, UntrustedOpen) || !strings.HasSuffix(e.Text, UntrustedClose) {
		t.Fatalf("unexpected untrusted entry: %#v", e)
	}
}

func TestPrepare_dropsOversizedUntrustedTool(t *testing.T) {
	t.Parallel()
	call := prepareCall(toolResultItem("tool", "todo status: "+strings.Repeat("x", 100)))
	cfg := DefaultConfig()
	cfg.MaxUntrustedToolBytes = 16
	got, err := Prepare(t.Context(), Input{Call: call, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 0 {
		t.Fatalf("entries=%d want oversized tool dropped", len(got.Envelope.Entries))
	}
}

func TestPrepare_recognizedCarrierUsesNarrowContract(t *testing.T) {
	t.Parallel()
	call := prepareCall(toolResultItem("carrier", "opaque tool output"))
	recognizer := testCarrierRecognizer{byID: map[string]StructuredCarrier{
		"carrier": {Type: "todo", Version: 1, Payload: `{"items":[{"title":"ship","status":"pending"}]}`},
	}}
	got, err := Prepare(t.Context(), Input{Call: call, Recognizer: recognizer, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 1 {
		t.Fatalf("entries=%d want carrier", len(got.Envelope.Entries))
	}
	e := got.Envelope.Entries[0]
	if e.Kind != EntryStructuredCarrier || e.Carrier.Type != "todo" || e.Untrusted {
		t.Fatalf("unexpected carrier entry: %#v", e)
	}
}

func TestPrepare_redactsRetainedTextBeforeEgress(t *testing.T) {
	t.Parallel()
	redactor := &testRedactor{value: "[redacted]"}
	call := prepareCall(textItem("u", lipapi.RoleUser, "I choose secret storage."))
	got, err := Prepare(t.Context(), Input{Call: call, Redactor: redactor, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(redactor.seen) != 1 || strings.Contains(got.Envelope.Entries[0].Text, "secret") {
		t.Fatalf("redaction did not run: seen=%q entries=%#v", redactor.seen, got.Envelope.Entries)
	}
}

func TestPrepare_redactorErrorDropsCandidateWithoutLeakingInput(t *testing.T) {
	t.Parallel()
	redactor := &testRedactor{err: errors.New("matcher unavailable")}
	call := prepareCall(textItem("u", lipapi.RoleUser, "I choose secret storage."))
	got, err := Prepare(t.Context(), Input{Call: call, Redactor: redactor, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Envelope.Entries) != 0 {
		t.Fatalf("entries=%#v want dropped on redaction failure", got.Envelope.Entries)
	}
}

func TestPrepare_boundsTotalBytesAndEntryText(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxBytes = 40
	cfg.MaxEntryBytes = 20
	call := prepareCall(
		textItem("u1", lipapi.RoleUser, "I choose alpha and require one."),
		textItem("u2", lipapi.RoleUser, "I choose beta and require two."),
	)
	got, err := Prepare(t.Context(), Input{Call: call, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if got.Envelope.Bytes > cfg.MaxBytes {
		t.Fatalf("bytes=%d > max=%d", got.Envelope.Bytes, cfg.MaxBytes)
	}
	for _, e := range got.Envelope.Entries {
		if len(e.Text) > cfg.MaxEntryBytes {
			t.Fatalf("entry text=%d > max=%d", len(e.Text), cfg.MaxEntryBytes)
		}
	}
}

func TestPrepare_highWatermarkIsDeterministic(t *testing.T) {
	t.Parallel()
	call := prepareCall(textItem("u", lipapi.RoleUser, "I choose the bounded source."))
	a, err := Prepare(t.Context(), Input{Call: call, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Prepare(t.Context(), Input{Call: call, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if a.HighWatermark != b.HighWatermark || a.Envelope.Canonical() != b.Envelope.Canonical() {
		t.Fatalf("non-deterministic result: a=%#v b=%#v", a, b)
	}
}

func TestPrepare_incrementalAppendsOnlyNewItems(t *testing.T) {
	t.Parallel()
	first := prepareCall(textItem("u1", lipapi.RoleUser, "I choose alpha."))
	a, err := Prepare(t.Context(), Input{Call: first, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	second := prepareCall(textItem("u1", lipapi.RoleUser, "I choose alpha."), textItem("u2", lipapi.RoleUser, "I require beta."))
	b, err := Prepare(t.Context(), Input{Call: second, Existing: a.Envelope, Previous: a.HighWatermark, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.NewEntries) != 1 || b.NewEntries[0].ItemID != "u2" {
		t.Fatalf("new entries=%#v", b.NewEntries)
	}
	if len(b.Envelope.Entries) != 2 {
		t.Fatalf("envelope entries=%#v", b.Envelope.Entries)
	}
}

func TestPrepare_prefixChangeRewindsIncrementalWatermark(t *testing.T) {
	t.Parallel()
	first, err := Prepare(t.Context(), Input{Call: prepareCall(textItem("u", lipapi.RoleUser, "I choose alpha.")), Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := Prepare(t.Context(), Input{
		Call:     prepareCall(textItem("u", lipapi.RoleUser, "I choose beta."), textItem("u2", lipapi.RoleUser, "I require gamma.")),
		Existing: first.Envelope,
		Previous: first.HighWatermark,
		Config:   DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.NewEntries) != 2 || changed.NewEntries[0].ItemID != "u" {
		t.Fatalf("changed prefix must rewind: %#v", changed.NewEntries)
	}
}

func TestPrepare_watermarkAdvancesAcrossDroppedItems(t *testing.T) {
	t.Parallel()
	got, err := Prepare(t.Context(), Input{Call: prepareCall(toolResultItem("tool", "ordinary output"), textItem("u", lipapi.RoleUser, "I choose alpha.")), Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if got.HighWatermark.ItemCount != 2 {
		t.Fatalf("item count=%d want all walked items", got.HighWatermark.ItemCount)
	}
}

func TestPrepare_emptyCallHasZeroCandidate(t *testing.T) {
	t.Parallel()
	got, err := Prepare(t.Context(), Input{Call: lipapi.Call{Items: []lipapi.Item{}}, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Candidate || len(got.Envelope.Entries) != 0 || got.HighWatermark.ItemCount != 0 {
		t.Fatalf("empty result=%#v", got)
	}
}
