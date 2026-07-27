package standardplugins

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
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
