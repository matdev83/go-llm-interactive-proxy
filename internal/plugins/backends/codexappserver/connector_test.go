package codexappserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestStripOpenAIModelPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"openai/gpt-5.4", "gpt-5.4"},
		{"gpt-5.3-codex", "gpt-5.3-codex"},
		{"", ""},
		{"auto", "auto"},
	}
	for _, tc := range cases {
		got := stripOpenAIModelPrefix(tc.in)
		if got != tc.want {
			t.Fatalf("stripOpenAIModelPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsAutoModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"auto", true},
		{"AUTO", true},
		{"  auto  ", true},
		{"", true},
		{"gpt-5.4", false},
		{"openai/auto", false},
	}
	for _, tc := range cases {
		got := isAutoModel(tc.in)
		if got != tc.want {
			t.Fatalf("isAutoModel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBuildCodexCommand(t *testing.T) {
	t.Parallel()
	cmd := buildCodexCommand("/usr/bin/codex", []string{"k=v", "k2=v2"}, []string{"--debug"})
	want := []string{
		"/usr/bin/codex",
		"--dangerously-bypass-approvals-and-sandbox",
		"--search",
		"app-server",
		"-c", "k=v",
		"-c", "k2=v2",
		"--stdio",
		"--debug",
	}
	if len(cmd) != len(want) {
		t.Fatalf("cmd len = %d, want %d: got=%v", len(cmd), len(want), cmd)
	}
	for i, v := range cmd {
		if v != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, v, want[i])
		}
	}
}

func TestBuildCodexCommand_noOverridesNoExtra(t *testing.T) {
	t.Parallel()
	cmd := buildCodexCommand("codex", nil, nil)
	want := []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "--search", "app-server", "--stdio"}
	if len(cmd) != len(want) {
		t.Fatalf("cmd = %v, want %v", cmd, want)
	}
}

func TestBuildCodexCommandWithVerbosityReplacesStaticOverride(t *testing.T) {
	t.Parallel()
	cmd := buildCodexCommandWithVerbosity("codex", []string{"model_verbosity=low", "foo=bar"}, "high", nil)
	want := []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "--search", "app-server", "-c", "foo=bar", "-c", "model_verbosity=high", "--stdio"}
	if strings.Join(cmd, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cmd = %v, want %v", cmd, want)
	}
}

func TestBuildCodexCommandWithVerbosityPreservesOverridesWhenUnset(t *testing.T) {
	t.Parallel()
	cmd := buildCodexCommandWithVerbosity("codex", []string{"model_verbosity=low"}, "", nil)
	if !strings.Contains(strings.Join(cmd, " "), "model_verbosity=low") {
		t.Fatalf("unset verbosity must preserve static override: %v", cmd)
	}
}

func TestClearInvalidDefaultVerbosity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"high", "high"},
		{"HIGH", "high"},
		{"", ""},
		{"bogus", ""},
		{"  medium  ", "medium"},
	}
	for _, tc := range cases {
		cfg := Config{DefaultVerbosity: lipapi.VerbosityLevel(tc.in)}
		clearInvalidDefaultVerbosity(&cfg)
		if got := string(cfg.DefaultVerbosity); got != tc.want {
			t.Fatalf("clearInvalidDefaultVerbosity(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCodexServerRequestHandler_acceptApproval(t *testing.T) {
	t.Parallel()
	h := &codexServerRequestHandler{}
	methods := []string{
		"execCommandApproval",
		"applyPatchApproval",
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
	}
	for _, method := range methods {
		result, err := h.HandleServerRequest(context.Background(), method, nil, nil)
		if err != nil {
			t.Fatalf("method %q: unexpected error: %v", method, err)
		}
		m, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("method %q: result not map", method)
		}
		if m["decision"] != "accept" {
			t.Fatalf("method %q: decision = %v, want accept", method, m["decision"])
		}
	}
}

func TestCodexServerRequestHandler_permissionsApproval(t *testing.T) {
	t.Parallel()
	h := &codexServerRequestHandler{}
	params := json.RawMessage(`{"permissions":{"net":{"github.com":"allow"}}}`)
	result, err := h.HandleServerRequest(context.Background(), "item/permissions/requestApproval", nil, params)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("result not map")
	}
	perms, ok := m["permissions"].(map[string]any)
	if !ok {
		t.Fatal("permissions missing or wrong type")
	}
	if perms["net"] == nil {
		t.Fatal("expected net permission to be echoed back")
	}
}

func TestCodexServerRequestHandler_unknownMethodDecline(t *testing.T) {
	t.Parallel()
	h := &codexServerRequestHandler{}
	result, err := h.HandleServerRequest(context.Background(), "unknown/method", nil, nil)
	if err != nil {
		t.Fatalf("unknown method should return decline result, not error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("result not map")
	}
	if m["decision"] != "decline" {
		t.Fatalf("decision = %v, want decline", m["decision"])
	}
}

func TestCheckExecutable_nonexistentAbsolute(t *testing.T) {
	t.Parallel()
	_, ok := acp.CheckExecutable("/nonexistent/path/to/codex")
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

func TestDefaultInventoryModels(t *testing.T) {
	t.Parallel()
	models := defaultInventoryModels(nil)
	if len(models) == 0 {
		t.Fatal("default inventory is empty; expected auto + shipped fallback slugs")
	}
	for _, m := range models {
		if !strings.HasPrefix(m.CanonicalID, "openai/") {
			t.Fatalf("CanonicalID %q should have prefix openai/", m.CanonicalID)
		}
	}
	// The "auto" routing sentinel is always first; the rest come from the
	// auto-discovered catalog's shipped fallback snapshot.
	if models[0].NativeID != autoModelSentinel {
		t.Fatalf("first model = %q, want %q", models[0].NativeID, autoModelSentinel)
	}
	if len(models) < 2 {
		t.Fatalf("default inventory has only %d models, want auto + catalog slugs", len(models))
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
	be, err := New(Config{ConnectorConfig: acp.ConnectorConfig{Model: "gpt-5.4", Executable: "/nonexistent/codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if be.ModelInventory == nil {
		t.Fatal("ModelInventory should not be nil even with missing exe")
	}
}

func TestCommandBasename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"/usr/bin/ls", "ls"},
		{"C:\\codex.cmd", "codex.cmd"},
		{"echo hello", "echo"},
		{"", ""},
		{"  ", ""},
	}
	for _, tc := range cases {
		got := commandBasename(tc.in)
		if got != tc.want {
			t.Fatalf("commandBasename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
