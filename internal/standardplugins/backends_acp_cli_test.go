package standardplugins

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/codexappserver"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

// TestResolveWorkspace_priorityOrderMatchesACPHintPriority locks in that the
// YAML→DefaultWorkspace resolution order mirrors the ACP workspace hint
// priority (project_dir > workspace_path > …) for the fields exposed by
// acpCLIYAML. DefaultWorkspace is the policy fallback, not a hint, so it stays
// last. Trivial paths (".", "..") are treated as unset.
func TestResolveWorkspace_priorityOrderMatchesACPHintPriority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		y    acpCLIYAML
		want string
	}{
		{
			name: "project_dir wins over workspace_path and default",
			y: acpCLIYAML{
				ProjectDir:       "/proj",
				WorkspacePath:    "/ws",
				DefaultWorkspace: "/def",
			},
			want: "/proj",
		},
		{
			name: "workspace_path wins when project_dir unset",
			y: acpCLIYAML{
				WorkspacePath:    "/ws",
				DefaultWorkspace: "/def",
			},
			want: "/ws",
		},
		{
			name: "default_workspace is the fallback",
			y:    acpCLIYAML{DefaultWorkspace: "/def"},
			want: "/def",
		},
		{
			name: "trivial project_dir (.) skips to workspace_path",
			y: acpCLIYAML{
				ProjectDir:       ".",
				WorkspacePath:    "/ws",
				DefaultWorkspace: "/def",
			},
			want: "/ws",
		},
		{
			name: "trivial workspace_path (..) skips to default",
			y: acpCLIYAML{
				WorkspacePath:    "..",
				DefaultWorkspace: "/def",
			},
			want: "/def",
		},
		{
			name: "all unset returns empty",
			y:    acpCLIYAML{},
			want: "",
		},
		{
			name: "whitespace-only fields are treated as unset",
			y: acpCLIYAML{
				ProjectDir:       "   ",
				WorkspacePath:    "  ",
				DefaultWorkspace: "\t",
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveWorkspace(tc.y); got != tc.want {
				t.Fatalf("resolveWorkspace = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestACPCLIYAML_connectorConfig_preservesDecodingSemantics locks in that the
// shared acp.ConnectorConfig built from decoded YAML carries the common
// local-agent fields (executable, model, extra_args, workspace resolution,
// idle/stale timers) with the same semantics the four per-factory bridges
// had before centralization. Vendor-specific fields are intentionally not
// present on ConnectorConfig.
func TestACPCLIYAML_connectorConfig_preservesDecodingSemantics(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/agent
model: composer-2
extra_args:
  - --foo
  - bar
project_dir: /proj
idle_timeout_seconds: 30
stale_kill_delay_seconds: 7.5
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	var y acpCLIYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	cc := y.connectorConfig()
	if cc.Executable != "/usr/local/bin/agent" {
		t.Fatalf("Executable = %q", cc.Executable)
	}
	if cc.Model != "composer-2" {
		t.Fatalf("Model = %q", cc.Model)
	}
	if len(cc.ExtraArgs) != 2 || cc.ExtraArgs[0] != "--foo" || cc.ExtraArgs[1] != "bar" {
		t.Fatalf("ExtraArgs = %v", cc.ExtraArgs)
	}
	if cc.DefaultWorkspace != "/proj" {
		t.Fatalf("DefaultWorkspace = %q, want /proj (project_dir wins)", cc.DefaultWorkspace)
	}
	if cc.IdleTimeout != 30*time.Second {
		t.Fatalf("IdleTimeout = %v, want 30s", cc.IdleTimeout)
	}
	if cc.StaleKillDelay != 7500*time.Millisecond {
		t.Fatalf("StaleKillDelay = %v, want 7.5s", cc.StaleKillDelay)
	}
}

// TestACPCLIYAML_connectorConfig_zeroTimersAndEmptyWorkspace verifies the
// centralized helper preserves the prior "zero / unset → zero Duration" and
// "empty workspace → empty string" semantics relied on by every connector.
func TestACPCLIYAML_connectorConfig_zeroTimersAndEmptyWorkspace(t *testing.T) {
	t.Parallel()
	var y acpCLIYAML
	cc := y.connectorConfig()
	if cc.IdleTimeout != 0 || cc.StaleKillDelay != 0 {
		t.Fatalf("zero YAML timers should yield zero Durations: %+v", cc)
	}
	if cc.DefaultWorkspace != "" {
		t.Fatalf("empty workspace should yield empty string: %q", cc.DefaultWorkspace)
	}
}

// TestBackendCursorCLIACP_rejectsAGYField ensures a cursorcliacp block fails
// loudly when it carries an AGY-only key, rather than silently dropping it.
func TestBackendCursorCLIACP_rejectsAGYField(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/agent
agy_binary: /usr/bin/agy
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendCursorCLIACP(root, nil); err == nil {
		t.Fatal("expected error for AGY-only key agy_binary on cursorcliacp block")
	}
}

// TestBackendAGYCLIACP_rejectsCursorField ensures an agycliacp block fails
// loudly when it carries a cursor-only key.
func TestBackendAGYCLIACP_rejectsCursorField(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/wrapper
cursor_api_endpoint: https://example.com
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendAGYCLIACP(root, nil); err == nil {
		t.Fatal("expected error for cursor-only key cursor_api_endpoint on agycliacp block")
	}
}

// TestBackendGeminiCLIACP_rejectsCodexField ensures a geminicliacp block fails
// when it carries a codex-only key.
func TestBackendGeminiCLIACP_rejectsCodexField(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/gemini
config_overrides:
  - k=v
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendGeminiCLIACP(root, nil); err == nil {
		t.Fatal("expected error for codex-only key config_overrides on geminicliacp block")
	}
}

// TestBackendCodexAppServer_rejectsCursorField ensures a codexappserver block
// fails when it carries a cursor-only key.
func TestBackendCodexAppServer_rejectsCursorField(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/codex
trust_workspace: true
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendCodexAppServer(root, nil, ""); err == nil {
		t.Fatal("expected error for cursor-only key trust_workspace on codexappserver block")
	}
}

func TestBackendCodexAppServer_acceptsAndValidatesDefaultVerbosity(t *testing.T) {
	t.Parallel()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("default_verbosity: HIGH\n"), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendCodexAppServer(root, nil, ""); err != nil {
		t.Fatalf("valid default_verbosity should be accepted: %v", err)
	}

	if err := yaml.Unmarshal([]byte("default_verbosity: extreme\n"), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendCodexAppServer(root, nil, ""); err == nil {
		t.Fatal("invalid default_verbosity should be rejected")
	}
}

func TestBackendCodexAppServer_catalogSourceControlsInventory(t *testing.T) {
	t.Parallel()
	cat, err := codexcatalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.RoutableSlugs()) == 0 {
		t.Fatal("expected shipped catalog slugs")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("executable: /usr/local/bin/codex\n"), &root); err != nil {
		t.Fatal(err)
	}

	discovered, err := backendCodexAppServer(root, cat, codexcatalog.SourceDiscovered)
	if err != nil {
		t.Fatal(err)
	}
	dSnap, err := discovered.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dSnap.Models) < 2 {
		t.Fatalf("discovered inventory len = %d, want auto + catalog slugs", len(dSnap.Models))
	}

	fallback, err := backendCodexAppServer(root, cat, codexcatalog.SourceShippedFallback)
	if err != nil {
		t.Fatal(err)
	}
	fSnap, err := fallback.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fSnap.Models) != 1 || fSnap.Models[0].NativeID != "auto" {
		t.Fatalf("shipped fallback inventory = %+v, want auto-only", fSnap.Models)
	}
}

func TestBackendCodexAppServer_configuredModelsUsesSetInner(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/codex
models:
  source: inline
  items:
    - canonical_id: openai/auto
      native_id: auto
    - canonical_id: openai/gpt-5.4
      native_id: gpt-5.4
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	cat, err := codexcatalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	be, err := backendCodexAppServer(root, cat, codexcatalog.SourceDiscovered)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("configured inventory len = %d, want 2 (operator override)", len(snap.Models))
	}
	byNative := map[string]bool{}
	for _, m := range snap.Models {
		byNative[m.NativeID] = true
	}
	if !byNative["auto"] || !byNative["gpt-5.4"] {
		t.Fatalf("models = %+v, want auto + gpt-5.4", snap.Models)
	}
	if byNative["gpt-5.3-codex"] {
		t.Fatal("configured override must not advertise catalog-only slugs")
	}
	if _, ok := be.ModelInventory.(*acp.TrackingInventory); !ok {
		t.Fatalf("ModelInventory type = %T, want *acp.TrackingInventory", be.ModelInventory)
	}
}

func TestApplyConfiguredTrackingModelInventory_rejectsNonTracking(t *testing.T) {
	t.Parallel()

	be := execbackend.Backend{
		ModelInventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{CanonicalID: "openai/auto", NativeID: "auto"}},
		},
	}
	_, err := applyConfiguredTrackingModelInventory(be, modelInventoryYAML{
		Source: "inline",
		Items: []modelInventoryItemYAML{
			{CanonicalID: "openai/gpt-5.4", NativeID: "gpt-5.4"},
		},
	})
	if err == nil {
		t.Fatal("expected error for non-tracking inventory")
	}
	if !strings.Contains(err.Error(), "not tracking") {
		t.Fatalf("error = %v, want not tracking", err)
	}
}

func TestBackendCodexAppServer_sourceMatrixOpenAllowlist(t *testing.T) {
	t.Parallel()
	cat, err := codexcatalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	slugs := cat.RoutableSlugs()
	if len(slugs) == 0 {
		t.Fatal("expected shipped catalog slugs")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("executable: /usr/local/bin/codex\ndefault_workspace: /tmp/ws\n"), &root); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		src      codexcatalog.Source
		wantSlug bool
	}{
		{name: "discovered allows catalog slug after LoadModels", src: codexcatalog.SourceDiscovered, wantSlug: true},
		{name: "shipped fallback rejects catalog slug", src: codexcatalog.SourceShippedFallback, wantSlug: false},
		{name: "override fallback rejects catalog slug", src: codexcatalog.SourceOverrideFallback, wantSlug: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			be, err := backendCodexAppServer(root, cat, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := be.ModelInventory.LoadModels(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// Mirror core publish: LoadModels alone does not update the Open allowlist.
			if a, ok := be.ModelInventory.(modelinventory.AcceptedInventory); ok {
				a.AcceptInventory(snap.Models)
			}
			// Open will fail on missing exe after allowlist; distinguish via ErrUnknownModel.
			call := lipapi.Call{
				Route: lipapi.RouteIntent{Selector: "openai-codex-app-server:" + slugs[0]},
				Messages: []lipapi.Message{{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
				}},
			}
			_, openErr := be.Open(context.Background(), call, routing.AttemptCandidate{})
			if tt.wantSlug {
				if errors.Is(openErr, codexappserver.ErrUnknownModel) {
					t.Fatalf("discovered Open must not reject catalog slug: %v", openErr)
				}
				return
			}
			if !errors.Is(openErr, codexappserver.ErrUnknownModel) {
				t.Fatalf("fallback Open error = %v, want ErrUnknownModel", openErr)
			}
		})
	}
}

// TestBackendCursorCLIACP_acceptsSharedAndOwnFields ensures a cursorcliacp
// block with all shared + cursor-specific fields still decodes successfully
// after strict decoding is wired in.
func TestBackendCursorCLIACP_acceptsSharedAndOwnFields(t *testing.T) {
	t.Parallel()
	raw := `executable: /usr/local/bin/agent
model: composer-2
auto_accept: true
trust_workspace: true
cursor_api_endpoint: https://example.com
project_dir: /proj
idle_timeout_seconds: 30
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if _, err := backendCursorCLIACP(root, nil); err != nil {
		t.Fatalf("expected success for shared+cursor fields, got: %v", err)
	}
}

func TestYAMLKeysOf_acpCLIYAML(t *testing.T) {
	t.Parallel()
	keys := yamlKeysOf(acpCLIYAML{})
	want := []string{
		"executable", "model", "extra_args", "default_workspace",
		"workspace_path", "project_dir", "idle_timeout_seconds",
		"stale_kill_delay_seconds", "models",
	}
	if len(keys) != len(want) {
		t.Fatalf("yamlKeysOf(acpCLIYAML{}) = %v, want %v", keys, want)
	}
	for _, w := range want {
		if !slices.Contains(keys, w) {
			t.Fatalf("yamlKeysOf(acpCLIYAML{}) missing %q; got %v", w, keys)
		}
	}
}

func TestRejectUnknownYAMLKeys_detectsDocumentNodeMapping(t *testing.T) {
	t.Parallel()
	raw := `executable: /x
agy_binary: /y
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	if err := rejectUnknownYAMLKeys(root, []string{"executable"}); err == nil {
		t.Fatal("expected error for agy_binary not in known list")
	}
	// Known key only → no error.
	if err := rejectUnknownYAMLKeys(root, []string{"executable", "agy_binary"}); err != nil {
		t.Fatalf("expected no error when all keys known: %v", err)
	}
}

func TestRejectUnknownYAMLKeys_zeroNodePasses(t *testing.T) {
	t.Parallel()
	if err := rejectUnknownYAMLKeys(yaml.Node{}, []string{"executable"}); err != nil {
		t.Fatalf("zero node should pass: %v", err)
	}
}
