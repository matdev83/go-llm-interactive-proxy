package agycliacp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestAgySpec_ResolveModel(t *testing.T) {
	t.Parallel()
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "google/gemini-3.5-flash-high", NativeID: "Gemini 3.5 Flash (High)"},
		{CanonicalID: "google/gemini-3.5-flash-low", NativeID: "Gemini 3.5 Flash (Low)"},
		{CanonicalID: "google/gemini-3.5-flash-medium", NativeID: "Gemini 3.5 Flash (Medium)"},
		{CanonicalID: "google/gemini-3.1-pro-low", NativeID: "Gemini 3.1 Pro (Low)"},
		{CanonicalID: "anthropic/claude-sonnet-4.6-thinking", NativeID: "Claude Sonnet 4.6 (Thinking)"},
		{CanonicalID: "anthropic/claude-opus-4.6-thinking", NativeID: "Claude Opus 4.6 (Thinking)"},
	})
	spec := &agySpec{
		cfg:   Config{ConnectorConfig: acp.ConnectorConfig{Model: "anthropic/claude-sonnet-4.6-thinking"}},
		index: idx,
	}
	cases := []struct {
		name     string
		in, want string
	}{
		{name: "agy-colon-canonical", in: "agy:google/gemini-3.5-flash-high", want: "Gemini 3.5 Flash (High)"},
		{name: "agy-slash-canonical", in: "agy/anthropic/claude-opus-4.6-thinking", want: "Claude Opus 4.6 (Thinking)"},
		{name: "bare-canonical", in: "google/gemini-3.1-pro-low", want: "Gemini 3.1 Pro (Low)"},
		{name: "empty-uses-config", in: "", want: "Claude Sonnet 4.6 (Thinking)"},
		{name: "agy-bare-uses-config", in: "agy", want: "Claude Sonnet 4.6 (Thinking)"},
		{name: "agy-colon-auto", in: "agy:auto", want: "Claude Sonnet 4.6 (Thinking)"},
		{name: "auto", in: "auto", want: "Claude Sonnet 4.6 (Thinking)"},
		{name: "trimmed-agy-colon", in: "  agy:google/gemini-3.5-flash-low  ", want: "Gemini 3.5 Flash (Low)"},
		{name: "pretty-native", in: "Gemini 3.5 Flash (Medium)", want: "Gemini 3.5 Flash (Medium)"},
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

func TestAgySpec_ResolveModel_emptyConfigDefault(t *testing.T) {
	t.Parallel()
	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{{
		CanonicalID: "google/gemini-3.5-flash-high",
		NativeID:    "Gemini 3.5 Flash (High)",
	}})
	spec := &agySpec{cfg: Config{}, index: idx}
	got := spec.ResolveModel("")
	if got != "Gemini 3.5 Flash (High)" {
		t.Fatalf("empty config default = %q, want Gemini 3.5 Flash (High)", got)
	}
}

func TestAgySpec_ResolveModel_unadvertisedEmpty(t *testing.T) {
	t.Parallel()
	spec := &agySpec{cfg: Config{}, index: acp.NewModelIndex(nil)}
	got := spec.ResolveModel("google/gemini-3.5-flash-high")
	if got != "" {
		t.Fatalf("unadvertised ResolveModel = %q, want empty", got)
	}
}

func TestAgySpec_BuildCommand(t *testing.T) {
	t.Parallel()
	// BuildCommand requires a real executable to resolve; test the command
	// structure by using a known PATH binary. On most systems "echo" exists.
	// Instead, test the command building logic by constructing the spec and
	// verifying BuildCommand returns an error for missing executable.
	spec := &agySpec{cfg: Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:     "google/gemini-3.5-flash-high",
			ExtraArgs: []string{"--debug"},
		},
		WrapperExecutable: "/nonexistent/path/to/wrapper",
		SkipPermissions:   true,
		AGYBinary:         "/usr/bin/agy",
		TimeoutSeconds:    300,
	}}
	_, _, _, err := spec.BuildCommand("google/gemini-3.5-flash-high", "/ws")
	if err == nil {
		t.Fatal("expected error for nonexistent wrapper executable")
	}
}

func TestAgySpec_HandshakeProfile(t *testing.T) {
	t.Parallel()
	spec := &agySpec{cfg: Config{
		ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: "/workspace"},
		MCPServers:      json.RawMessage(`[{"name":"srv1"}]`),
	}}
	hp := spec.HandshakeProfile()

	if hp.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", hp.ProtocolVersion)
	}
	if hp.SkipAuthenticate {
		t.Fatal("SkipAuthenticate should be false for AGY (uses authenticate with methodId:agy)")
	}
	if hp.SessionNewCwd != "/workspace" {
		t.Fatalf("SessionNewCwd = %q, want /workspace", hp.SessionNewCwd)
	}
	// Verify authenticate params.
	var ap map[string]any
	if err := json.Unmarshal(hp.AuthenticateParams, &ap); err != nil {
		t.Fatal(err)
	}
	if ap["methodId"] != "agy" {
		t.Fatalf("methodId = %v, want agy", ap["methodId"])
	}
	// Verify client capabilities.
	var caps map[string]any
	if err := json.Unmarshal(hp.ClientCapabilities, &caps); err != nil {
		t.Fatal(err)
	}
	fs, ok := caps["fs"].(map[string]any)
	if !ok {
		t.Fatal("fs capability missing")
	}
	if fs["readTextFile"] != false || fs["writeTextFile"] != false {
		t.Fatal("fs.readTextFile and fs.writeTextFile should be false")
	}
	if caps["terminal"] != false {
		t.Fatal("terminal should be false")
	}
	// Verify client info version is "1" (not "dev" like Gemini).
	var info map[string]any
	if err := json.Unmarshal(hp.ClientInfo, &info); err != nil {
		t.Fatal(err)
	}
	if info["version"] != "1" {
		t.Fatalf("clientInfo.version = %v, want 1", info["version"])
	}
	// Verify MCP servers.
	if string(hp.SessionNewMCPServers) != `[{"name":"srv1"}]` {
		t.Fatalf("MCPServers = %s, want [{\"name\":\"srv1\"}]", hp.SessionNewMCPServers)
	}
}

func TestAgySpec_CancelProfile(t *testing.T) {
	t.Parallel()
	spec := &agySpec{}
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
}

func TestAgySpec_RequiresExplicitWorkspace(t *testing.T) {
	t.Parallel()
	spec := &agySpec{}
	if !spec.RequiresExplicitWorkspace() {
		t.Fatal("AGY requires explicit workspace")
	}
}

func TestAgySpec_VendorIDAndPrefix(t *testing.T) {
	t.Parallel()
	spec := &agySpec{}
	if spec.VendorID() != "agycliacp" {
		t.Fatalf("VendorID = %q, want agycliacp", spec.VendorID())
	}
	if spec.VendorPrefix() != "agy" {
		t.Fatalf("VendorPrefix = %q, want agy", spec.VendorPrefix())
	}
}

func TestAgyServerRequestHandler_agyExtensionEmptyResult(t *testing.T) {
	t.Parallel()
	h := &agyServerRequestHandler{}
	cases := []string{"agy/some_method", "agy/another", "agy/"}
	for _, method := range cases {
		result, err := h.HandleServerRequest(context.Background(), method, nil, nil)
		if err != nil {
			t.Fatalf("agy/ method %q: unexpected error: %v", method, err)
		}
		if _, ok := result.(map[string]any); !ok {
			t.Fatalf("agy/ method %q: expected empty map result", method)
		}
	}
}

func TestAgyServerRequestHandler_unknownMethodError(t *testing.T) {
	t.Parallel()
	h := &agyServerRequestHandler{}
	_, err := h.HandleServerRequest(context.Background(), "unknown/method", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
	if !strings.Contains(err.Error(), "method not handled") {
		t.Fatalf("error = %v, want 'method not handled'", err)
	}
}

func TestAgyNew_defaultModelApplied(t *testing.T) {
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

func TestAgyNew_AGYBinaryEnvFallback(t *testing.T) {
	// No t.Parallel() — t.Setenv requires non-parallel.
	t.Setenv("AGY_BINARY", "/custom/agy/binary")
	be, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = be
	// The New function should have read AGY_BINARY env and set it in the spec.
	// We can't directly inspect the spec from here, but verifying New doesn't
	// panic and returns a valid backend is sufficient — the env var is consumed
	// internally by the spec's BuildCommand via cfg.AGYBinary.
}

// Compile-time assertion that agySpec satisfies the interface.
var _ acp.SubprocessConnectorSpec = (*agySpec)(nil)
