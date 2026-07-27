package standardplugins

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"gopkg.in/yaml.v3"
)

// acpCLIYAML is the shared YAML config shape for local-agent fields used by
// remaining root ACP helper tests and inventory adapters. Concrete ACP CLI
// products are external optional connector artifacts.
type acpCLIYAML struct {
	Executable       string             `yaml:"executable"`
	Model            string             `yaml:"model"`
	ExtraArgs        []string           `yaml:"extra_args"`
	DefaultWorkspace string             `yaml:"default_workspace"`
	WorkspacePath    string             `yaml:"workspace_path"`
	ProjectDir       string             `yaml:"project_dir"`
	IdleTimeoutS     float64            `yaml:"idle_timeout_seconds"`
	StaleKillDelayS  float64            `yaml:"stale_kill_delay_seconds"`
	Models           modelInventoryYAML `yaml:"models"`
}

// yamlKeysOf returns the yaml key names for the exported fields of v's struct
// type, reading the `yaml` tag (name before the first comma) and falling back
// to the lowercased field name when the tag is absent. Fields tagged "-" are
// skipped. This drives rejectUnknownYAMLKeys without maintaining a separate
// key list that could drift from the struct definitions.
func yamlKeysOf(v any) []string {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var keys []string
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name == "-" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// rejectUnknownYAMLKeys fails when n is a mapping that contains a key not in
// known. Non-mapping or zero nodes pass (nothing to check). A DocumentNode
// (produced by yaml.Unmarshal into a yaml.Node) is unwrapped to its first
// child so the check sees the actual mapping. Each ACP CLI factory calls this
// with the union of acpCLIYAML keys and its own extras keys so a
// vendor-specific key on the wrong connector block is rejected loudly instead
// of being silently ignored.
func rejectUnknownYAMLKeys(n yaml.Node, known []string) error {
	node := &n
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node == nil || node.Kind == 0 || node.IsZero() || node.Kind != yaml.MappingNode {
		return nil
	}
	allowed := make(map[string]struct{}, len(known))
	for _, k := range known {
		allowed[k] = struct{}{}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		// Mapping keys are scalar string nodes; skip non-scalar keys (e.g. merge
		// anchors) rather than risk misreading them.
		if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
			continue
		}
		if _, ok := allowed[keyNode.Value]; !ok {
			return fmt.Errorf("unknown config key %q for this backend (valid keys: %s)", keyNode.Value, strings.Join(known, ", "))
		}
	}
	return nil
}

// acpCLIKnownKeys returns the yaml keys the given connector factory accepts:
// the shared acpCLIYAML keys plus the connector-specific extras keys. Called
// once per factory; cheap enough for startup, but the result could be cached if
// it ever shows up in profiles.
func acpCLIKnownKeys(extras any) []string {
	return slices.Concat(yamlKeysOf(acpCLIYAML{}), yamlKeysOf(extras))
}

// yamlNodeToJSON converts a yaml.Node to json.RawMessage, returning nil for empty nodes.
// This ensures YAML-configured mcp_servers (which may use YAML object syntax) are properly
// converted to JSON for the connector Config's json.RawMessage field.
func yamlNodeToJSON(n yaml.Node) (json.RawMessage, error) {
	if n.Kind == 0 || n.IsZero() {
		return nil, nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// applyConfiguredTrackingModelInventory applies a YAML models override via
// TrackingInventory.SetInner so the concurrent ModelIndex object is preserved.
func applyConfiguredTrackingModelInventory(be execbackend.Backend, y modelInventoryYAML) (execbackend.Backend, error) {
	provider, ok, err := configuredModelInventory(y)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if !ok {
		return be, nil
	}
	t, ok := be.ModelInventory.(*acp.TrackingInventory)
	if !ok {
		return execbackend.Backend{}, fmt.Errorf("standardplugins: model inventory is not tracking")
	}
	t.SetInner(provider)
	return be, nil
}

// resolveWorkspace picks the workspace directory from the YAML hints in the
// same priority order as acp.workspaceHintKeys (project_dir > workspace_path;
// cwd/project are not exposed by acpCLIYAML), then falls back to
// DefaultWorkspace. Trivial paths (".", "..") are treated as unset.
func resolveWorkspace(y acpCLIYAML) string {
	for _, s := range []string{y.ProjectDir, y.WorkspacePath, y.DefaultWorkspace} {
		if v := strings.TrimSpace(s); v != "" && v != "." && v != ".." {
			return v
		}
	}
	return ""
}

func acpIdleTimeout(s float64) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

func acpStaleKillDelay(s float64) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

// connectorConfig builds the shared acp.ConnectorConfig (executable, model,
// extra args, workspace, idle/stale timers) from the decoded YAML. Each ACP
// CLI factory embeds this into its concrete Config alongside vendor-specific
// extras, eliminating the per-factory field-by-field bridge copy for the
// common local-agent fields.
func (y acpCLIYAML) connectorConfig() acp.ConnectorConfig {
	return acp.ConnectorConfig{
		Executable:       y.Executable,
		Model:            y.Model,
		ExtraArgs:        y.ExtraArgs,
		DefaultWorkspace: resolveWorkspace(y),
		IdleTimeout:      acpIdleTimeout(y.IdleTimeoutS),
		StaleKillDelay:   acpStaleKillDelay(y.StaleKillDelayS),
	}
}
