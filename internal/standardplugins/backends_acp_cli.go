package standardplugins

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/agycliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/codexappserver"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorcliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/geminicliacp"
	"gopkg.in/yaml.v3"
)

// acpCLIYAML is the shared YAML config shape for the local-agent fields common
// to every ACP CLI subprocess connector (cursorcliacp, geminicliacp,
// agycliacp, codexappserver). Vendor-specific fields live on per-connector
// extras structs (cursorCLIYAMLExtras, etc.) and are decoded separately by each
// factory, so a key that belongs to one vendor (e.g. agy_binary) is rejected
// loudly when it appears on another vendor's block rather than silently
// dropped. See rejectUnknownYAMLKeys.
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

// cursorCLIYAMLExtras carries cursorcliacp-only config fields.
type cursorCLIYAMLExtras struct {
	AutoAccept        bool      `yaml:"auto_accept"`
	TrustWorkspace    bool      `yaml:"trust_workspace"`
	CursorAPIEndpoint string    `yaml:"cursor_api_endpoint"`
	MCPServers        yaml.Node `yaml:"mcp_servers"`
}

// geminiCLIYAMLExtras carries geminicliacp-only config fields.
type geminiCLIYAMLExtras struct {
	AutoAccept bool `yaml:"auto_accept"`
}

// agyCLIYAMLExtras carries agycliacp-only config fields.
type agyCLIYAMLExtras struct {
	WrapperExecutable string    `yaml:"wrapper_executable"`
	AGYBinary         string    `yaml:"agy_binary"`
	SkipPermissions   *bool     `yaml:"skip_permissions"`
	TimeoutSeconds    int       `yaml:"timeout_seconds"`
	MCPServers        yaml.Node `yaml:"mcp_servers"`
}

// codexAppServerYAMLExtras carries codexappserver-only config fields.
type codexAppServerYAMLExtras struct {
	ConfigOverrides []string `yaml:"config_overrides"`
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

func backendCursorCLIACP(n yaml.Node, _ *http.Client) (execbackend.Backend, error) {
	var y acpCLIYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("cursorcliacp backend config: %w", err)
	}
	var xs cursorCLIYAMLExtras
	if err := config.DecodeYAMLNode(n, &xs); err != nil {
		return execbackend.Backend{}, fmt.Errorf("cursorcliacp backend config: %w", err)
	}
	if err := rejectUnknownYAMLKeys(n, acpCLIKnownKeys(cursorCLIYAMLExtras{})); err != nil {
		return execbackend.Backend{}, fmt.Errorf("cursorcliacp backend config: %w", err)
	}
	mcpJSON, err := yamlNodeToJSON(xs.MCPServers)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("cursorcliacp: mcp_servers: %w", err)
	}
	cfg := cursorcliacp.Config{
		ConnectorConfig:   y.connectorConfig(),
		AutoAccept:        xs.AutoAccept,
		TrustWorkspace:    xs.TrustWorkspace,
		CursorAPIEndpoint: xs.CursorAPIEndpoint,
		MCPServers:        mcpJSON,
	}
	be, err := cursorcliacp.New(cfg)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("cursorcliacp: %w", err)
	}
	return applyConfiguredModelInventory(be, y.Models)
}

func backendGeminiCLIACP(n yaml.Node, _ *http.Client) (execbackend.Backend, error) {
	var y acpCLIYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("geminicliacp backend config: %w", err)
	}
	var xs geminiCLIYAMLExtras
	if err := config.DecodeYAMLNode(n, &xs); err != nil {
		return execbackend.Backend{}, fmt.Errorf("geminicliacp backend config: %w", err)
	}
	if err := rejectUnknownYAMLKeys(n, acpCLIKnownKeys(geminiCLIYAMLExtras{})); err != nil {
		return execbackend.Backend{}, fmt.Errorf("geminicliacp backend config: %w", err)
	}
	cfg := geminicliacp.Config{
		ConnectorConfig: y.connectorConfig(),
		AutoAccept:      xs.AutoAccept,
	}
	be, err := geminicliacp.New(cfg)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("geminicliacp: %w", err)
	}
	return applyConfiguredModelInventory(be, y.Models)
}

func backendCodexAppServer(n yaml.Node, catalog *codexcatalog.Catalog) (execbackend.Backend, error) {
	var y acpCLIYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("codexappserver backend config: %w", err)
	}
	var xs codexAppServerYAMLExtras
	if err := config.DecodeYAMLNode(n, &xs); err != nil {
		return execbackend.Backend{}, fmt.Errorf("codexappserver backend config: %w", err)
	}
	if err := rejectUnknownYAMLKeys(n, acpCLIKnownKeys(codexAppServerYAMLExtras{})); err != nil {
		return execbackend.Backend{}, fmt.Errorf("codexappserver backend config: %w", err)
	}
	cfg := codexappserver.Config{
		ConnectorConfig: y.connectorConfig(),
		ConfigOverrides: xs.ConfigOverrides,
		ModelCatalog:    catalog,
	}
	be, err := codexappserver.New(cfg)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("codexappserver: %w", err)
	}
	return applyConfiguredModelInventory(be, y.Models)
}

func backendAGYCLIACP(n yaml.Node, _ *http.Client) (execbackend.Backend, error) {
	var y acpCLIYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("agycliacp backend config: %w", err)
	}
	var xs agyCLIYAMLExtras
	if err := config.DecodeYAMLNode(n, &xs); err != nil {
		return execbackend.Backend{}, fmt.Errorf("agycliacp backend config: %w", err)
	}
	if err := rejectUnknownYAMLKeys(n, acpCLIKnownKeys(agyCLIYAMLExtras{})); err != nil {
		return execbackend.Backend{}, fmt.Errorf("agycliacp backend config: %w", err)
	}
	skipPerm := true // default: skip permissions (matching Python's _skip_permissions = True)
	if xs.SkipPermissions != nil {
		skipPerm = *xs.SkipPermissions
	}
	mcpJSON, err := yamlNodeToJSON(xs.MCPServers)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("agycliacp: mcp_servers: %w", err)
	}
	cfg := agycliacp.Config{
		ConnectorConfig:   y.connectorConfig(),
		WrapperExecutable: xs.WrapperExecutable,
		AGYBinary:         xs.AGYBinary,
		SkipPermissions:   skipPerm,
		TimeoutSeconds:    xs.TimeoutSeconds,
		MCPServers:        mcpJSON,
	}
	be, err := agycliacp.New(cfg)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("agycliacp: %w", err)
	}
	return applyConfiguredModelInventory(be, y.Models)
}
