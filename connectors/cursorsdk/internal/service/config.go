package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product"
	"gopkg.in/yaml.v3"
)

const (
	FactoryKind = "cursorsdk"
	PluginID    = "io.golip.backend.cursorsdk"
)

type Config struct {
	APIKey                    string    `yaml:"api_key"`
	BridgeExecutable          string    `yaml:"bridge_executable"`
	Model                     string    `yaml:"model"`
	DefaultWorkspace          string    `yaml:"default_workspace"`
	WorkspacePath             string    `yaml:"workspace_path"`
	ProjectDir                string    `yaml:"project_dir"`
	MCPServers                yaml.Node `yaml:"mcp_servers"`
	SettingSources            []string  `yaml:"setting_sources"`
	SandboxMode               string    `yaml:"sandbox_mode"`
	AutoReview                bool      `yaml:"auto_review"`
	BridgeEnvAllowlist        []string  `yaml:"bridge_env_allowlist"`
	MaxAgents                 *int      `yaml:"max_agents"`
	MaxConcurrentRuns         *int      `yaml:"max_concurrent_runs"`
	BridgeStartTimeoutSeconds *float64  `yaml:"bridge_start_timeout_seconds"`
	CancelTimeoutSeconds      *float64  `yaml:"cancel_timeout_seconds"`
	ShutdownTimeoutSeconds    *float64  `yaml:"shutdown_timeout_seconds"`
	AgentIdleTimeoutSeconds   *float64  `yaml:"agent_idle_timeout_seconds"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("cursorsdk: config yaml: %w", err)
		}
	}
	return cfg, nil
}

func (c Config) toProductInput(secretAPIKey string) (product.Input, error) {
	apiKey := strings.TrimSpace(c.APIKey)
	if s := strings.TrimSpace(secretAPIKey); s != "" {
		apiKey = s
	}
	mcpJSON, err := yamlNodeToJSON(c.MCPServers)
	if err != nil {
		return product.Input{}, fmt.Errorf("cursorsdk: mcp_servers: %w", err)
	}
	return product.Input{
		APIKey:                    apiKey,
		BridgeExecutable:          c.BridgeExecutable,
		Model:                     c.Model,
		DefaultWorkspace:          c.DefaultWorkspace,
		WorkspacePath:             c.WorkspacePath,
		ProjectDir:                c.ProjectDir,
		MCPServers:                mcpJSON,
		SettingSources:            c.SettingSources,
		SandboxMode:               c.SandboxMode,
		AutoReview:                c.AutoReview,
		BridgeEnvAllowlist:        c.BridgeEnvAllowlist,
		MaxAgents:                 c.MaxAgents,
		MaxConcurrentRuns:         c.MaxConcurrentRuns,
		BridgeStartTimeoutSeconds: c.BridgeStartTimeoutSeconds,
		CancelTimeoutSeconds:      c.CancelTimeoutSeconds,
		ShutdownTimeoutSeconds:    c.ShutdownTimeoutSeconds,
		AgentIdleTimeoutSeconds:   c.AgentIdleTimeoutSeconds,
	}, nil
}

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
