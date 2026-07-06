package geminicliacp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
)

func TestGeminiSpec_ResolveModel(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{cfg: Config{ConnectorConfig: acp.ConnectorConfig{Model: "gemini-2.5-pro"}}}
	cases := []struct {
		in, want string
	}{
		{"google:gemini-2.5-flash", "gemini-2.5-flash"},
		{"google/gemini-2.5-pro", "gemini-2.5-pro"},
		{"gemini-3-flash-preview", "gemini-3-flash-preview"},
		{"", "gemini-2.5-pro"},
		{"google", "gemini-2.5-pro"},
		{"google:auto", "gemini-2.5-pro"},
		{"auto", "gemini-2.5-pro"},
		{"  google:gemini-3.1-pro-preview  ", "gemini-3.1-pro-preview"},
	}
	for _, tc := range cases {
		got := spec.ResolveModel(tc.in)
		if got != tc.want {
			t.Fatalf("ResolveModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGeminiSpec_ResolveModel_emptyConfigDefault(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{cfg: Config{}}
	got := spec.ResolveModel("")
	if got != "gemini-2.5-flash" {
		t.Fatalf("empty config default = %q, want gemini-2.5-flash", got)
	}
}

func TestGeminiSpec_HandshakeProfile(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{cfg: Config{ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: "/project"}}}
	hp := spec.HandshakeProfile()

	if hp.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", hp.ProtocolVersion)
	}
	if !hp.SkipAuthenticate {
		t.Fatal("SkipAuthenticate should be true for Gemini (minimal handshake)")
	}
	if hp.SessionNewCwd != "/project" {
		t.Fatalf("SessionNewCwd = %q, want /project", hp.SessionNewCwd)
	}
	// Verify empty client capabilities.
	var caps map[string]any
	if err := json.Unmarshal(hp.ClientCapabilities, &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps) != 0 {
		t.Fatalf("ClientCapabilities = %v, want empty object", caps)
	}
	// Verify client info.
	var info map[string]any
	if err := json.Unmarshal(hp.ClientInfo, &info); err != nil {
		t.Fatal(err)
	}
	if info["name"] != "llm-interactive-proxy" {
		t.Fatalf("clientInfo.name = %v, want llm-interactive-proxy", info["name"])
	}
	if info["version"] != "dev" {
		t.Fatalf("clientInfo.version = %v, want dev", info["version"])
	}
	// Verify default MCP servers is empty array.
	if string(hp.SessionNewMCPServers) != "[]" {
		t.Fatalf("MCPServers = %s, want []", hp.SessionNewMCPServers)
	}
}

func TestGeminiSpec_CancelProfile(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{}
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

func TestGeminiSpec_ServerRequestHandler_nil(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{}
	if spec.ServerRequestHandler() != nil {
		t.Fatal("Gemini should return nil to use headless default handler")
	}
}

func TestGeminiSpec_RequiresExplicitWorkspace(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{}
	if !spec.RequiresExplicitWorkspace() {
		t.Fatal("Gemini requires explicit workspace")
	}
}

func TestGeminiSpec_VendorIDAndPrefix(t *testing.T) {
	t.Parallel()
	spec := &geminiSpec{}
	if spec.VendorID() != "geminicliacp" {
		t.Fatalf("VendorID = %q, want geminicliacp", spec.VendorID())
	}
	if spec.VendorPrefix() != "google" {
		t.Fatalf("VendorPrefix = %q, want google", spec.VendorPrefix())
	}
}

func TestGeminiDefaultInventoryModels(t *testing.T) {
	t.Parallel()
	models := defaultInventoryModels()
	if len(models) == 0 {
		t.Fatal("expected non-empty default models")
	}
	expected := map[string]bool{
		"gemini-2.5-flash": true,
		"gemini-2.5-pro":   true,
	}
	found := 0
	for _, m := range models {
		if !strings.HasPrefix(m.CanonicalID, "google/") {
			t.Fatalf("CanonicalID %q should have prefix google/", m.CanonicalID)
		}
		if expected[m.NativeID] {
			found++
		}
	}
	if found != len(expected) {
		t.Fatalf("expected %d known models, found %d", len(expected), found)
	}
}

func TestWrapBatchIfNeeded_nonWindows(t *testing.T) {
	t.Parallel()
	// On non-Windows, the command should pass through unchanged.
	cmd := []string{"/usr/bin/gemini", "--experimental-acp"}
	got := wrapBatchIfNeeded(cmd)
	if len(got) != len(cmd) {
		t.Fatalf("non-Windows: command should be unchanged, got %v", got)
	}
}

func TestCheckExecutable_nonexistentAbsolute(t *testing.T) {
	t.Parallel()
	_, ok := acp.CheckExecutable("/nonexistent/path/to/gemini")
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

func TestGeminiNew_defaultModelApplied(t *testing.T) {
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

// Compile-time assertion that geminiSpec satisfies the interface.
var _ acp.SubprocessConnectorSpec = (*geminiSpec)(nil)
