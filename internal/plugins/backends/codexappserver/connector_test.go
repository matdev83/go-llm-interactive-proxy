package codexappserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestStripOpenAIModelPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in, want string
	}{
		{name: "openai-slash", in: "openai/gpt-5.4", want: "gpt-5.4"},
		{name: "bare-slug", in: "gpt-5.3-codex", want: "gpt-5.3-codex"},
		{name: "empty", in: "", want: ""},
		{name: "auto", in: "auto", want: "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stripOpenAIModelPrefix(tc.in)
			if got != tc.want {
				t.Fatalf("stripOpenAIModelPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsAutoModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "auto", in: "auto", want: true},
		{name: "AUTO", in: "AUTO", want: true},
		{name: "padded-auto", in: "  auto  ", want: true},
		{name: "empty", in: "", want: true},
		{name: "gpt-5.4", in: "gpt-5.4", want: false},
		{name: "openai-auto", in: "openai/auto", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isAutoModel(tc.in)
			if got != tc.want {
				t.Fatalf("isAutoModel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
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
		name     string
		in, want string
	}{
		{name: "high", in: "high", want: "high"},
		{name: "HIGH", in: "HIGH", want: "high"},
		{name: "empty", in: "", want: ""},
		{name: "bogus", in: "bogus", want: ""},
		{name: "padded-medium", in: "  medium  ", want: "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{DefaultVerbosity: lipapi.VerbosityLevel(tc.in)}
			clearInvalidDefaultVerbosity(&cfg)
			if got := string(cfg.DefaultVerbosity); got != tc.want {
				t.Fatalf("clearInvalidDefaultVerbosity(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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

func TestDefaultInventoryModels_sourceMatrix(t *testing.T) {
	t.Parallel()

	cat, err := codexcatalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.RoutableSlugs()) == 0 {
		t.Fatal("expected shipped catalog slugs for discovered matrix case")
	}

	tests := []struct {
		name         string
		cat          *codexcatalog.Catalog
		src          codexcatalog.Source
		wantOnlyAuto bool
	}{
		{name: "discovered advertises catalog slugs", cat: cat, src: codexcatalog.SourceDiscovered, wantOnlyAuto: false},
		{name: "shipped fallback auto-only", cat: cat, src: codexcatalog.SourceShippedFallback, wantOnlyAuto: true},
		{name: "override fallback auto-only", cat: cat, src: codexcatalog.SourceOverrideFallback, wantOnlyAuto: true},
		{name: "nil catalog unknown auto-only", cat: nil, src: codexcatalog.SourceUnknown, wantOnlyAuto: true},
		{name: "nil catalog discovered auto-only", cat: nil, src: codexcatalog.SourceDiscovered, wantOnlyAuto: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			models := defaultInventoryModels(tt.cat, tt.src)
			if len(models) == 0 {
				t.Fatal("inventory empty")
			}
			if models[0].NativeID != autoModelSentinel || models[0].CanonicalID != "openai/"+autoModelSentinel {
				t.Fatalf("first model = %+v, want openai/auto", models[0])
			}
			if tt.wantOnlyAuto {
				if len(models) != 1 {
					t.Fatalf("models len = %d, want auto-only", len(models))
				}
				return
			}
			if len(models) < 2 {
				t.Fatalf("models len = %d, want auto + discovered slugs", len(models))
			}
		})
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
		name     string
		in, want string
	}{
		{name: "unix-path", in: "/usr/bin/ls", want: "ls"},
		{name: "windows-path", in: "C:\\codex.cmd", want: "codex.cmd"},
		{name: "with-args", in: "echo hello", want: "echo"},
		{name: "empty", in: "", want: ""},
		{name: "whitespace", in: "  ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := commandBasename(tc.in)
			if got != tc.want {
				t.Fatalf("commandBasename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
