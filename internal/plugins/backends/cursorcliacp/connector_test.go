package cursorcliacp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestCursorSpec_ResolveModel_seededIndex(t *testing.T) {
	t.Parallel()
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "cursor/composer-2", NativeID: "composer-2"},
		{CanonicalID: "cursor/composer-2-fast", NativeID: "composer-2-fast"},
		{CanonicalID: "cursor/gpt-5.2", NativeID: "gpt-5.2"},
		{CanonicalID: "cursor/claude-3.5-sonnet", NativeID: "claude-3.5-sonnet"},
		{CanonicalID: "cursor/grok-4.5-high", NativeID: "cursor-grok-4.5-high"},
	})
	spec := &cursorSpec{
		cfg:   Config{ConnectorConfig: acp.ConnectorConfig{Model: "claude-3.5-sonnet"}},
		index: idx,
	}
	cases := []struct {
		name     string
		in, want string
	}{
		{name: "cursor-colon-native", in: "cursor:composer-2", want: "composer-2"},
		{name: "cursor-slash-canonical", in: "cursor/composer-2-fast", want: "composer-2-fast"},
		{name: "bare-native", in: "composer-2", want: "composer-2"},
		{name: "empty-uses-config", in: "", want: "claude-3.5-sonnet"},
		{name: "cursor-bare-uses-config", in: "cursor", want: "claude-3.5-sonnet"},
		{name: "cursor-colon-auto", in: "cursor:auto", want: "claude-3.5-sonnet"},
		{name: "auto", in: "auto", want: "claude-3.5-sonnet"},
		{name: "trimmed-cursor-colon", in: "  cursor:gpt-5.2  ", want: "gpt-5.2"},
		{name: "cursor-slash-grok", in: "cursor/grok-4.5-high", want: "cursor-grok-4.5-high"},
		{name: "cursor-colon-prefixed-native", in: "cursor:cursor-grok-4.5-high", want: "cursor-grok-4.5-high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := spec.ResolveModel(tc.in)
			if got != tc.want {
				t.Fatalf("ResolveModel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCursorSpec_ResolveModel_unknownWithoutIndex(t *testing.T) {
	t.Parallel()
	spec := &cursorSpec{cfg: Config{}, index: acp.NewModelIndex(nil)}
	got := spec.ResolveModel("composer-2")
	if got != "" {
		t.Fatalf("unknown without seed = %q, want empty", got)
	}
}

func TestCursorSpec_ResolveModel_emptyConfigDefaultWhenSeeded(t *testing.T) {
	t.Parallel()
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "cursor/composer-2", NativeID: "composer-2"},
	})
	spec := &cursorSpec{cfg: Config{}, index: idx}
	got := spec.ResolveModel("")
	if got != "composer-2" {
		t.Fatalf("empty config default = %q, want composer-2", got)
	}
}

func TestCursorSpec_HandshakeProfile(t *testing.T) {
	t.Parallel()
	spec := &cursorSpec{cfg: Config{ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: "/ws"}, MCPServers: json.RawMessage(`[{"name":"srv"}]`)}}
	hp := spec.HandshakeProfile()

	if hp.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", hp.ProtocolVersion)
	}
	if hp.SkipAuthenticate {
		t.Fatal("SkipAuthenticate should be false for Cursor (uses cursor_login)")
	}
	if hp.SessionNewCwd != "/ws" {
		t.Fatalf("SessionNewCwd = %q, want /ws", hp.SessionNewCwd)
	}
	// Verify authenticate params contain cursor_login methodId.
	var ap map[string]any
	if err := json.Unmarshal(hp.AuthenticateParams, &ap); err != nil {
		t.Fatal(err)
	}
	if ap["methodId"] != "cursor_login" {
		t.Fatalf("methodId = %v, want cursor_login", ap["methodId"])
	}
	// Verify client capabilities.
	var caps map[string]any
	if err := json.Unmarshal(hp.ClientCapabilities, &caps); err != nil {
		t.Fatal(err)
	}
	if fs, ok := caps["fs"].(map[string]any); ok {
		if fs["readTextFile"] != false {
			t.Fatal("fs.readTextFile should be false")
		}
	} else {
		t.Fatal("fs capability missing")
	}
	// Verify MCP servers passed through.
	if string(hp.SessionNewMCPServers) != `[{"name":"srv"}]` {
		t.Fatalf("MCPServers = %s, want [{\"name\":\"srv\"}]", hp.SessionNewMCPServers)
	}
}

func TestCursorSpec_CancelProfile(t *testing.T) {
	t.Parallel()
	spec := &cursorSpec{}
	cp := spec.CancelProfile()
	want := []string{"session/cancel", "session/stop", "session/end"}
	if len(cp.Methods) != len(want) {
		t.Fatalf("Methods len = %d, want %d", len(cp.Methods), len(want))
	}
	for i, m := range cp.Methods {
		if m != want[i] {
			t.Fatalf("Methods[%d] = %q, want %q", i, m, want[i])
		}
	}
	if !cp.IncludeRequestID || !cp.IncludeMessageID {
		t.Fatal("IncludeRequestID and IncludeMessageID should be true")
	}
}

func TestCursorServerRequestHandler_permissionAutoAccept(t *testing.T) {
	t.Parallel()
	h := &cursorServerRequestHandler{autoAccept: true}
	result, err := h.HandleServerRequest(context.Background(), "session/request_permission", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("result is not map")
	}
	outcome, ok := m["outcome"].(map[string]any)
	if !ok {
		t.Fatal("outcome missing")
	}
	if outcome["outcome"] != "selected" {
		t.Fatalf("outcome = %v, want selected", outcome["outcome"])
	}
	if outcome["optionId"] != "allow-always" {
		t.Fatalf("optionId = %v, want allow-always", outcome["optionId"])
	}
}

func TestCursorServerRequestHandler_permissionReject(t *testing.T) {
	t.Parallel()
	h := &cursorServerRequestHandler{autoAccept: false}
	result, err := h.HandleServerRequest(context.Background(), "session/request_permission", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	outcomeMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("result is not map")
	}
	outcome, ok := outcomeMap["outcome"].(map[string]any)
	if !ok {
		t.Fatal("outcome missing")
	}
	if outcome["optionId"] != "reject-once" {
		t.Fatalf("optionId = %v, want reject-once", outcome["optionId"])
	}
}

func TestCursorServerRequestHandler_askQuestionSkipped(t *testing.T) {
	t.Parallel()
	h := &cursorServerRequestHandler{}
	result, err := h.HandleServerRequest(context.Background(), "cursor/ask_question", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	outcomeMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("result is not map")
	}
	outcome, ok := outcomeMap["outcome"].(map[string]any)
	if !ok {
		t.Fatal("outcome missing")
	}
	if outcome["outcome"] != "skipped" || outcome["reason"] != "proxy_auto_skip" {
		t.Fatalf("outcome = %v, want skipped/proxy_auto_skip", outcome)
	}
}

func TestCursorServerRequestHandler_createPlanRejected(t *testing.T) {
	t.Parallel()
	h := &cursorServerRequestHandler{}
	result, err := h.HandleServerRequest(context.Background(), "cursor/create_plan", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	outcomeMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("result is not map")
	}
	outcome, ok := outcomeMap["outcome"].(map[string]any)
	if !ok {
		t.Fatal("outcome missing")
	}
	if outcome["outcome"] != "rejected" || outcome["reason"] != "proxy_auto_reject" {
		t.Fatalf("outcome = %v, want rejected/proxy_auto_reject", outcome)
	}
}

func TestCursorServerRequestHandler_unhandledCursorExtensionEmptyResult(t *testing.T) {
	t.Parallel()
	h := &cursorServerRequestHandler{}
	result, err := h.HandleServerRequest(context.Background(), "cursor/unknown_extension", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for unhandled cursor/ method: %v", err)
	}
	if _, ok := result.(map[string]any); !ok {
		t.Fatal("expected empty map result for unhandled cursor/ method")
	}
}

func TestCursorServerRequestHandler_unknownMethodError(t *testing.T) {
	t.Parallel()
	h := &cursorServerRequestHandler{}
	_, err := h.HandleServerRequest(context.Background(), "unknown/method", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(err.Error(), "method not handled") {
		t.Fatalf("error = %v, want 'method not handled'", err)
	}
}

func TestParseAgentModelsListing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple listing",
			input: "composer-2 - Cursor Composer 2\ncomposer-2-fast - Cursor Composer 2 Fast\nauto - Automatic selection",
			want:  []string{"composer-2", "composer-2-fast", "auto"},
		},
		{
			name:  "with ANSI escapes",
			input: "\x1b[32mcomposer-2\x1b[0m - Cursor Composer 2\ngpt-5.2 - GPT 5.2",
			want:  []string{"composer-2", "gpt-5.2"},
		},
		{
			name:  "loading lines skipped",
			input: "Loading models...\nmodel-1 - Model One",
			want:  []string{"model-1"},
		},
		{
			name:  "deduplicates",
			input: "model-a - Model A\nmodel-a - Model A duplicate",
			want:  []string{"model-a"},
		},
		{
			name:  "skips lines without separator",
			input: "no separator here\nmodel-b - Model B",
			want:  []string{"model-b"},
		},
		{
			name:  "skips IDs with spaces",
			input: "model with spaces - Some Model\nmodel-c - Model C",
			want:  []string{"model-c"},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseAgentModelsListing(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d models, want %d: got=%v want=%v", len(got), len(tc.want), got, tc.want)
			}
			for i, m := range got {
				if m != tc.want[i] {
					t.Fatalf("got[%d] = %q, want %q", i, m, tc.want[i])
				}
			}
		})
	}
}

func TestCursorSpec_RequiresExplicitWorkspace(t *testing.T) {
	t.Parallel()
	spec := &cursorSpec{}
	if !spec.RequiresExplicitWorkspace() {
		t.Fatal("Cursor requires explicit workspace")
	}
}

func TestCursorSpec_VendorIDAndPrefix(t *testing.T) {
	t.Parallel()
	spec := &cursorSpec{}
	if spec.VendorID() != "cursorcliacp" {
		t.Fatalf("VendorID = %q, want cursorcliacp", spec.VendorID())
	}
	if spec.VendorPrefix() != "cursor" {
		t.Fatalf("VendorPrefix = %q, want cursor", spec.VendorPrefix())
	}
}

func TestCheckExecutable_nonexistentAbsolute(t *testing.T) {
	t.Parallel()
	_, ok := acp.CheckExecutable("/nonexistent/path/to/agent")
	if ok {
		t.Fatal("expected false for nonexistent absolute path")
	}
}

func TestCheckExecutable_emptyString(t *testing.T) {
	t.Parallel()
	_, ok := acp.CheckExecutable("")
	if ok {
		t.Fatal("expected false for empty string")
	}
}

func TestNew_defaultModelApplied(t *testing.T) {
	t.Parallel()
	be, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != ID {
		t.Fatalf("BackendPrefixes = %v, want [%s]", be.BackendPrefixes, ID)
	}
	if be.ModelInventory == nil {
		t.Fatal("ModelInventory should not be nil")
	}
}

func TestNew_customModelDoesNotPanic(t *testing.T) {
	t.Parallel()
	be, err := New(Config{ConnectorConfig: acp.ConnectorConfig{Model: "gpt-5.2", Executable: "/nonexistent/agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if be.ModelInventory == nil {
		t.Fatal("ModelInventory should not be nil even with missing exe")
	}
}

// Compile-time assertion that cursorSpec satisfies the interface.
var _ acp.SubprocessConnectorSpec = (*cursorSpec)(nil)
