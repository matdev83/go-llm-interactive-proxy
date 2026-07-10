package codexappserver

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestBuildCommandSummary_usesSharedFormatter(t *testing.T) {
	t.Parallel()
	item := map[string]any{
		"commandActions":   []any{map[string]any{"command": "/usr/bin/ls -la"}},
		"durationMs":       float64(1500),
		"aggregatedOutput": "file1\nfile2\n",
	}
	summary := buildCommandSummary(item)
	// Tool name is the basename of the first field of the command.
	if !strings.Contains(summary, "Tool: ls\n") {
		t.Fatalf("missing tool name: %s", summary)
	}
	// Input size is the length of the command string (preserved codex semantics).
	if !strings.Contains(summary, "Input size: 15 bytes\n") {
		t.Fatalf("missing/wrong input size: %s", summary)
	}
	// Output size is the length of aggregatedOutput (preserved codex semantics).
	if !strings.Contains(summary, "Output size: 12 bytes\n") {
		t.Fatalf("missing/wrong output size: %s", summary)
	}
	if !strings.Contains(summary, "1.500 s)") {
		t.Fatalf("missing/wrong elapsed: %s", summary)
	}
	if !strings.HasPrefix(summary, "---\n```text\n") {
		t.Fatalf("missing fenced block prefix: %s", summary)
	}
	if !strings.HasSuffix(summary, "```\n") {
		t.Fatalf("missing fenced block suffix: %s", summary)
	}
}

func TestBuildCommandSummary_fallsBackToCommandField(t *testing.T) {
	t.Parallel()
	item := map[string]any{
		"command":          "echo hello",
		"durationMs":       float64(500),
		"aggregatedOutput": "hello\n",
	}
	summary := buildCommandSummary(item)
	if !strings.Contains(summary, "Tool: echo\n") {
		t.Fatalf("missing fallback tool name: %s", summary)
	}
	if !strings.Contains(summary, "Input size: 10 bytes\n") {
		t.Fatalf("missing fallback input size: %s", summary)
	}
	if !strings.Contains(summary, "0.500 s)") {
		t.Fatalf("missing fallback elapsed: %s", summary)
	}
}

func TestBuildCommandSummary_emptyCommandUsesDefaultName(t *testing.T) {
	t.Parallel()
	item := map[string]any{
		"durationMs": float64(0),
	}
	summary := buildCommandSummary(item)
	if !strings.Contains(summary, "Tool: command\n") {
		t.Fatalf("missing default tool name: %s", summary)
	}
	if !strings.Contains(summary, "Input size: 0 bytes\n") {
		t.Fatalf("missing zero input size: %s", summary)
	}
	if !strings.Contains(summary, "0.000 s)") {
		t.Fatalf("missing zero elapsed: %s", summary)
	}
}

func TestBuildFileChangeSummary_usesSharedFormatter(t *testing.T) {
	t.Parallel()
	item := map[string]any{
		"changes": []any{
			map[string]any{"path": "/repo/a.txt"},
			map[string]any{"path": "/repo/b.go"},
		},
	}
	summary := buildFileChangeSummary(item)
	if !strings.Contains(summary, "Tool: fileChange\n") {
		t.Fatalf("missing tool name: %s", summary)
	}
	// Input size is the joined paths length (preserved codex semantics).
	// "/repo/a.txt" + ", " + "/repo/b.go" = 23 bytes.
	if !strings.Contains(summary, "Input size: 23 bytes\n") {
		t.Fatalf("missing/wrong input size: %s", summary)
	}
	// Output size is 0 (preserved codex semantics).
	if !strings.Contains(summary, "Output size: 0 bytes\n") {
		t.Fatalf("missing/wrong output size: %s", summary)
	}
	if !strings.Contains(summary, "0.000 s)") {
		t.Fatalf("missing zero elapsed: %s", summary)
	}
}

func TestBuildItemCompletionSummary_dispatchesByType(t *testing.T) {
	t.Parallel()
	if got := buildItemCompletionSummary(map[string]any{"type": "commandExecution", "command": "ls", "durationMs": float64(10)}); got == "" {
		t.Fatal("commandExecution should produce a summary")
	}
	if got := buildItemCompletionSummary(map[string]any{"type": "fileChange", "changes": []any{}}); got == "" {
		t.Fatal("fileChange should produce a summary")
	}
	if got := buildItemCompletionSummary(map[string]any{"type": "unknown"}); got != "" {
		t.Fatalf("unknown type should produce empty, got %q", got)
	}
}

// TestFormatToolCompletionSummary_isShared ensures the codex summary functions
// route through acp.FormatToolCompletionSummary by checking that the elapsed
// time is derived from ended-started (the shared formatter's contract) rather
// than a hardcoded value. Uses a duration that would break a hardcoded formatter.
func TestFormatToolCompletionSummary_isShared(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	ended := started.Add(3717 * time.Millisecond)
	summary := buildCommandSummary(map[string]any{
		"command":          "run",
		"durationMs":       float64(3717),
		"aggregatedOutput": "",
	})
	// 3717ms → 3.717 s; a hardcoded formatter would not produce this exact value.
	if !strings.Contains(summary, "3.717 s)") {
		t.Fatalf("elapsed not derived from duration: %s", summary)
	}
	// Confirm the shared formatter produces the same elapsed for the same delta.
	ref := acp.FormatToolCompletionSummary("run", 3, 0, started, ended)
	if !strings.Contains(ref, "3.717 s)") {
		t.Fatalf("reference formatter lost elapsed: %s", ref)
	}
}

// TestBuildItemCompletionSummary_nestedItem locks in the real Codex wire shape.
// Per the Codex app-server protocol schema, ItemCompletedNotification *requires*
// the item nested under params["item"] (mirrored by the Python mapper's
// _item_from_params). The existing dispatch test only passes flat params, which
// exercises the params-fallback branch; this test exercises the primary branch.
func TestBuildItemCompletionSummary_nestedItem(t *testing.T) {
	t.Parallel()
	summary := buildItemCompletionSummary(map[string]any{
		"item": map[string]any{
			"type":             "commandExecution",
			"commandActions":   []any{map[string]any{"command": "make build"}},
			"durationMs":       float64(800),
			"aggregatedOutput": "BUILD_OK",
		},
		"threadId": "thread-1",
		"turnId":   "turn-1",
	})
	if !strings.Contains(summary, "Tool: make\n") {
		t.Fatalf("nested item summary missing tool name: %s", summary)
	}
	// Surrounding fields (threadId/turnId) must not leak into the item lookup.
	if !strings.Contains(summary, "Output size: 8 bytes\n") {
		t.Fatalf("nested item summary missing output size: %s", summary)
	}
}

// TestMapNotification_itemCompleted exercises the item/completed branch of
// mapNotification with the real nested-item wire shape — the path that produces
// a fenced tool-completion summary as an EventTextDelta. This branch is not
// reached by any integration test, so mapNotification was only 50% covered.
func TestMapNotification_itemCompleted(t *testing.T) {
	t.Parallel()
	// mapNotification does not touch the embedded base or client fields, so a
	// zero-value codexStream is sufficient for direct unit testing.
	s := &codexStream{}

	evs, err := s.mapNotification(map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": "thread-1",
			"turnId":   "turn-1",
			"item": map[string]any{
				"type":             "commandExecution",
				"commandActions":   []any{map[string]any{"command": "/usr/bin/git status"}},
				"durationMs":       float64(250),
				"aggregatedOutput": "nothing to commit",
			},
		},
	})
	if err != nil {
		t.Fatalf("mapNotification item/completed: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(evs), evs)
	}
	if evs[0].Kind != lipapi.EventTextDelta {
		t.Fatalf("expected EventTextDelta, got %v", evs[0].Kind)
	}
	summary := evs[0].Delta
	if !strings.Contains(summary, "Tool: git\n") {
		t.Fatalf("summary missing tool name from nested item: %s", summary)
	}
	if !strings.HasPrefix(summary, "---\n```text\n") {
		t.Fatalf("summary missing fenced block prefix: %s", summary)
	}
	// Raw stdout must never be streamed — only its size.
	if strings.Contains(summary, "nothing to commit") {
		t.Fatalf("raw command output leaked into summary: %s", summary)
	}
}

func TestMapNotification_itemCompletedFileChange(t *testing.T) {
	t.Parallel()
	s := &codexStream{}
	evs, err := s.mapNotification(map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"item": map[string]any{
				"type": "fileChange",
				"changes": []any{
					map[string]any{"path": "/repo/main.go"},
					map[string]any{"path": "/repo/lib.go"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("mapNotification item/completed fileChange: %v", err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventTextDelta {
		t.Fatalf("expected one EventTextDelta, got %v", evs)
	}
	if !strings.Contains(evs[0].Delta, "Tool: fileChange\n") {
		t.Fatalf("fileChange summary missing tool name: %s", evs[0].Delta)
	}
}

func TestMapNotification_itemCompletedUnknownTypeEmitsNothing(t *testing.T) {
	t.Parallel()
	s := &codexStream{}
	evs, err := s.mapNotification(map[string]any{
		"method": "item/completed",
		"params": map[string]any{"item": map[string]any{"type": "plan"}},
	})
	if err != nil {
		t.Fatalf("mapNotification: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("unknown item type should emit nothing, got %v", evs)
	}
}

// TestMapNotification_dispatch is a table-driven check of the remaining
// notification branches (text/reasoning deltas, terminal, suppressed methods,
// nil params, unknown method) to keep mapNotification coverage stable.
func TestMapNotification_dispatch(t *testing.T) {
	t.Parallel()
	s := &codexStream{}

	cases := []struct {
		name              string
		probe             map[string]any
		wantN             int
		wantKind          lipapi.EventKind
		wantDeltaContains string
	}{
		{
			name:              "agent message delta",
			probe:             map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"delta": "hi"}},
			wantN:             1,
			wantKind:          lipapi.EventTextDelta,
			wantDeltaContains: "hi",
		},
		{
			name:  "agent message empty delta suppressed",
			probe: map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"delta": ""}},
			wantN: 0,
		},
		{
			name:  "reasoning empty delta suppressed",
			probe: map[string]any{"method": "item/reasoning/summaryTextDelta", "params": map[string]any{"delta": ""}},
			wantN: 0,
		},
		{
			name:              "reasoning summary text delta",
			probe:             map[string]any{"method": "item/reasoning/summaryTextDelta", "params": map[string]any{"delta": "thinking"}},
			wantN:             1,
			wantKind:          lipapi.EventReasoningDelta,
			wantDeltaContains: "thinking",
		},
		{
			name:              "reasoning text delta",
			probe:             map[string]any{"method": "item/reasoning/textDelta", "params": map[string]any{"delta": "pondering"}},
			wantN:             1,
			wantKind:          lipapi.EventReasoningDelta,
			wantDeltaContains: "pondering",
		},
		{
			name:     "turn completed finishes",
			probe:    map[string]any{"method": "turn/completed", "params": map[string]any{}},
			wantN:    1,
			wantKind: lipapi.EventResponseFinished,
		},
		{
			name:  "item/started suppressed",
			probe: map[string]any{"method": "item/started", "params": map[string]any{}},
			wantN: 0,
		},
		{
			name:  "item/commandExecution/outputDelta suppressed",
			probe: map[string]any{"method": "item/commandExecution/outputDelta", "params": map[string]any{"delta": "raw stdout"}},
			wantN: 0,
		},
		{
			name:  "unknown method suppressed",
			probe: map[string]any{"method": "something/new", "params": map[string]any{}},
			wantN: 0,
		},
		{
			name:     "nil params tolerated for terminal",
			probe:    map[string]any{"method": "turn/completed"},
			wantN:    1,
			wantKind: lipapi.EventResponseFinished,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evs, err := s.mapNotification(tc.probe)
			if err != nil {
				t.Fatalf("mapNotification: %v", err)
			}
			if len(evs) != tc.wantN {
				t.Fatalf("expected %d events, got %d: %v", tc.wantN, len(evs), evs)
			}
			if tc.wantN == 1 {
				if evs[0].Kind != tc.wantKind {
					t.Fatalf("expected kind %v, got %v", tc.wantKind, evs[0].Kind)
				}
				if tc.wantDeltaContains != "" && !strings.Contains(evs[0].Delta, tc.wantDeltaContains) {
					t.Fatalf("delta %q missing %q", evs[0].Delta, tc.wantDeltaContains)
				}
			}
		})
	}
}

func TestMapNotification_reasoningSummaryTextDelta_stripsEmptyHTMLCommentMarker(t *testing.T) {
	t.Parallel()
	s := &codexStream{}

	evs, err := s.mapNotification(map[string]any{
		"method": "item/reasoning/summaryTextDelta",
		"params": map[string]any{"delta": "**Planning Phase 1**\n\n<!-- -->"},
	})
	if err != nil {
		t.Fatalf("mapNotification: %v", err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventReasoningDelta {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Delta != "**Planning Phase 1**\n\n" {
		t.Fatalf("delta = %q", evs[0].Delta)
	}
}

func TestMapNotification_reasoningSummaryTextDelta_suppressesSplitCommentClose(t *testing.T) {
	t.Parallel()
	s := &codexStream{}

	evs, err := s.mapNotification(map[string]any{
		"method": "item/reasoning/summaryTextDelta",
		"params": map[string]any{"delta": " -->"},
	})
	if err != nil {
		t.Fatalf("mapNotification: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("expected split comment close to be suppressed, got %+v", evs)
	}
}
