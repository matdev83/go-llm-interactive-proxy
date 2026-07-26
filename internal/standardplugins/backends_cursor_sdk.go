package standardplugins

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"gopkg.in/yaml.v3"
)

type cursorSDKYAML struct {
	APIKey                    string             `yaml:"api_key"`
	BridgeExecutable          string             `yaml:"bridge_executable"`
	Model                     string             `yaml:"model"`
	DefaultWorkspace          string             `yaml:"default_workspace"`
	WorkspacePath             string             `yaml:"workspace_path"`
	ProjectDir                string             `yaml:"project_dir"`
	MCPServers                yaml.Node          `yaml:"mcp_servers"`
	SettingSources            []string           `yaml:"setting_sources"`
	SandboxMode               string             `yaml:"sandbox_mode"`
	AutoReview                bool               `yaml:"auto_review"`
	BridgeEnvAllowlist        []string           `yaml:"bridge_env_allowlist"`
	MaxAgents                 *int               `yaml:"max_agents"`
	MaxConcurrentRuns         *int               `yaml:"max_concurrent_runs"`
	BridgeStartTimeoutSeconds *float64           `yaml:"bridge_start_timeout_seconds"`
	CancelTimeoutSeconds      *float64           `yaml:"cancel_timeout_seconds"`
	ShutdownTimeoutSeconds    *float64           `yaml:"shutdown_timeout_seconds"`
	AgentIdleTimeoutSeconds   *float64           `yaml:"agent_idle_timeout_seconds"`
	Models                    modelInventoryYAML `yaml:"models"`
}

func backendCursorSDK(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	sc, models, err := parseCursorSDKScaffold(n, keys)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredTrackingModelInventory(sc.Backend(), models)
}

// ExperimentalCursorSDKRegistration returns an opt-in factory registration for
// the in-tree cursorsdk adapter. It is intentionally absent from
// EssentialBackendBundle / StandardBackendBundle; production optional delivery
// is the external executable connector path. Tests and experimental composition
// roots may InstallBundleOn with this registration.
func ExperimentalCursorSDKRegistration(keys UpstreamAPIKeys) BackendRegistration {
	return BackendRegistration{
		ID: cursorsdk.ID,
		Factory: func(n yaml.Node, upstream *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendCursorSDK(n, upstream, keys)
		},
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialStatic,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
	}
}

func parseCursorSDKScaffold(n yaml.Node, keys UpstreamAPIKeys) (cursorsdk.Scaffold, modelInventoryYAML, error) {
	var y cursorSDKYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return cursorsdk.Scaffold{}, modelInventoryYAML{}, fmt.Errorf("cursorsdk backend config: %w", err)
	}
	if err := rejectUnknownYAMLKeys(n, yamlKeysOf(cursorSDKYAML{})); err != nil {
		return cursorsdk.Scaffold{}, modelInventoryYAML{}, fmt.Errorf("cursorsdk backend config: %w", err)
	}
	mcpJSON, err := yamlNodeToJSON(y.MCPServers)
	if err != nil {
		return cursorsdk.Scaffold{}, modelInventoryYAML{}, fmt.Errorf("cursorsdk: mcp_servers: %w", err)
	}
	cfg, err := cursorsdk.Normalize(cursorsdk.Input{
		APIKey:                    y.APIKey,
		BridgeExecutable:          y.BridgeExecutable,
		Model:                     y.Model,
		DefaultWorkspace:          y.DefaultWorkspace,
		WorkspacePath:             y.WorkspacePath,
		ProjectDir:                y.ProjectDir,
		MCPServers:                mcpJSON,
		SettingSources:            y.SettingSources,
		SandboxMode:               y.SandboxMode,
		AutoReview:                y.AutoReview,
		BridgeEnvAllowlist:        y.BridgeEnvAllowlist,
		MaxAgents:                 y.MaxAgents,
		MaxConcurrentRuns:         y.MaxConcurrentRuns,
		BridgeStartTimeoutSeconds: y.BridgeStartTimeoutSeconds,
		CancelTimeoutSeconds:      y.CancelTimeoutSeconds,
		ShutdownTimeoutSeconds:    y.ShutdownTimeoutSeconds,
		AgentIdleTimeoutSeconds:   y.AgentIdleTimeoutSeconds,
	}, keys.Cursor)
	if err != nil {
		return cursorsdk.Scaffold{}, modelInventoryYAML{}, secretSafeCursorSDKErr(err, y.APIKey, keys.Cursor)
	}
	return cursorsdk.NewScaffold(cfg), y.Models, nil
}

func secretSafeCursorSDKErr(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	if msg == err.Error() {
		return err
	}
	return fmt.Errorf("%s", msg)
}
