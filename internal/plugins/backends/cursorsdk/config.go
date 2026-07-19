package cursorsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
)

const ID = "cursorsdk"

const DefaultBridgeExecutable = "lip-cursor-sdk-bridge"

const (
	MaxFrameBytes        = protocol.MaxFrameBytes
	MaxPromptBytes       = 8 << 20
	MaxMCPConfigBytes    = 256 << 10
	MaxStderrRetainBytes = 8 << 10
)

const (
	defaultMaxAgents          = 32
	defaultMaxConcurrentRuns  = 8
	defaultBridgeStartTimeout = 30 * time.Second
	defaultCancelTimeout      = 5 * time.Second
	defaultShutdownTimeout    = 10 * time.Second
	defaultAgentIdleTimeout   = 15 * time.Minute
	minMaxAgents              = 1
	maxMaxAgents              = 32
	minMaxConcurrentRuns      = 1
	maxMaxConcurrentRuns      = 8
	minBridgeStartTimeout     = 1 * time.Second
	maxBridgeStartTimeout     = 120 * time.Second
	minCancelTimeout          = 100 * time.Millisecond
	maxCancelTimeout          = 30 * time.Second
	minShutdownTimeout        = 1 * time.Second
	maxShutdownTimeout        = 120 * time.Second
	minAgentIdleTimeout       = 1 * time.Second
	maxAgentIdleTimeout       = 24 * time.Hour
)

type SandboxMode string

const (
	SandboxRequired SandboxMode = "required"
	SandboxOff      SandboxMode = "off"
)

// EffectiveSandboxMode treats any non-explicit-off value as required so empty
// zero-value Config never silently disables sandboxing.
func EffectiveSandboxMode(mode SandboxMode) SandboxMode {
	if mode == SandboxOff {
		return SandboxOff
	}
	return SandboxRequired
}

type SettingSource string

const (
	SettingSourceProject SettingSource = "project"
	SettingSourceUser    SettingSource = "user"
	SettingSourceTeam    SettingSource = "team"
	SettingSourceMDM     SettingSource = "mdm"
	SettingSourcePlugins SettingSource = "plugins"
	SettingSourceAll     SettingSource = "all"
)

type Config struct {
	APIKey             string
	BridgeExecutable   string
	Model              string
	DefaultWorkspace   string
	MCPServers         json.RawMessage
	SettingSources     []SettingSource
	SandboxMode        SandboxMode
	AutoReview         bool
	BridgeEnvAllowlist []string
	MaxAgents          int
	MaxConcurrentRuns  int
	BridgeStartTimeout time.Duration
	CancelTimeout      time.Duration
	ShutdownTimeout    time.Duration
	AgentIdleTimeout   time.Duration
}

type Input struct {
	APIKey                    string
	BridgeExecutable          string
	Model                     string
	DefaultWorkspace          string
	WorkspacePath             string
	ProjectDir                string
	MCPServers                json.RawMessage
	SettingSources            []string
	SandboxMode               string
	AutoReview                bool
	BridgeEnvAllowlist        []string
	MaxAgents                 *int
	MaxConcurrentRuns         *int
	BridgeStartTimeoutSeconds *float64
	CancelTimeoutSeconds      *float64
	ShutdownTimeoutSeconds    *float64
	AgentIdleTimeoutSeconds   *float64
}

func Normalize(in Input, fallbackAPIKey string) (Config, error) {
	apiKey := strings.TrimSpace(in.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(fallbackAPIKey)
	}
	if apiKey == "" {
		return Config{}, errors.New("cursorsdk: api_key is required (set api_key or CURSOR_API_KEY)")
	}

	sandbox := SandboxMode(strings.TrimSpace(in.SandboxMode))
	if sandbox == "" {
		sandbox = SandboxRequired
	}
	switch sandbox {
	case SandboxRequired, SandboxOff:
	default:
		return Config{}, fmt.Errorf("cursorsdk: sandbox_mode must be %q or %q", SandboxRequired, SandboxOff)
	}

	sources := make([]SettingSource, 0, len(in.SettingSources))
	for _, raw := range in.SettingSources {
		s := SettingSource(strings.TrimSpace(raw))
		if !validSettingSource(s) {
			if s == "" {
				return Config{}, errors.New("cursorsdk: setting_sources entries must be non-empty")
			}
			return Config{}, fmt.Errorf("cursorsdk: unknown setting_sources value %q (allowed: project, user, team, mdm, plugins, all per @cursor/sdk@1.0.23)", raw)
		}
		sources = append(sources, s)
	}

	mcpServers, err := NormalizeMCPServers(in.MCPServers)
	if err != nil {
		return Config{}, err
	}

	maxAgents := defaultMaxAgents
	if in.MaxAgents != nil {
		maxAgents = *in.MaxAgents
	}
	if maxAgents < minMaxAgents || maxAgents > maxMaxAgents {
		return Config{}, fmt.Errorf("cursorsdk: max_agents must be between %d and %d", minMaxAgents, maxMaxAgents)
	}

	maxRuns := defaultMaxConcurrentRuns
	if in.MaxConcurrentRuns != nil {
		maxRuns = *in.MaxConcurrentRuns
	}
	if maxRuns < minMaxConcurrentRuns || maxRuns > maxMaxConcurrentRuns {
		return Config{}, fmt.Errorf("cursorsdk: max_concurrent_runs must be between %d and %d", minMaxConcurrentRuns, maxMaxConcurrentRuns)
	}
	if maxRuns > maxAgents {
		return Config{}, errors.New("cursorsdk: max_concurrent_runs must not exceed max_agents")
	}

	startTO, err := durationFromSeconds(in.BridgeStartTimeoutSeconds, defaultBridgeStartTimeout, minBridgeStartTimeout, maxBridgeStartTimeout, "bridge_start_timeout_seconds")
	if err != nil {
		return Config{}, err
	}
	cancelTO, err := durationFromSeconds(in.CancelTimeoutSeconds, defaultCancelTimeout, minCancelTimeout, maxCancelTimeout, "cancel_timeout_seconds")
	if err != nil {
		return Config{}, err
	}
	shutdownTO, err := durationFromSeconds(in.ShutdownTimeoutSeconds, defaultShutdownTimeout, minShutdownTimeout, maxShutdownTimeout, "shutdown_timeout_seconds")
	if err != nil {
		return Config{}, err
	}
	idleTO, err := idleTimeoutFromSeconds(in.AgentIdleTimeoutSeconds)
	if err != nil {
		return Config{}, err
	}

	exeName := strings.TrimSpace(in.BridgeExecutable)
	if exeName == "" {
		exeName = DefaultBridgeExecutable
	}
	if err := rejectShellOrNPMExecutable(exeName); err != nil {
		return Config{}, err
	}
	resolved, ok := acp.CheckExecutable(exeName)
	if !ok {
		return Config{}, fmt.Errorf("cursorsdk: bridge executable %q not found (direct PATH/absolute lookup only; Go-LIP never runs npm install)", exeName)
	}

	workspace := ""
	for _, s := range []string{in.ProjectDir, in.WorkspacePath, in.DefaultWorkspace} {
		if v := strings.TrimSpace(s); v != "" && v != "." && v != ".." {
			workspace = v
			break
		}
	}

	extraAllow := make([]string, 0, len(in.BridgeEnvAllowlist))
	for _, name := range in.BridgeEnvAllowlist {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if isCredentialEnvName(name) {
			return Config{}, fmt.Errorf("cursorsdk: bridge_env_allowlist must not include credential names (%q)", name)
		}
		extraAllow = append(extraAllow, name)
	}

	return Config{
		APIKey:             apiKey,
		BridgeExecutable:   resolved,
		Model:              strings.TrimSpace(in.Model),
		DefaultWorkspace:   workspace,
		MCPServers:         mcpServers,
		SettingSources:     sources,
		SandboxMode:        sandbox,
		AutoReview:         in.AutoReview,
		BridgeEnvAllowlist: mergeEnvAllowlist(PlatformMinimumEnvNames(), extraAllow),
		MaxAgents:          maxAgents,
		MaxConcurrentRuns:  maxRuns,
		BridgeStartTimeout: startTO,
		CancelTimeout:      cancelTO,
		ShutdownTimeout:    shutdownTO,
		AgentIdleTimeout:   idleTO,
	}, nil
}

func validSettingSource(s SettingSource) bool {
	switch s {
	case SettingSourceProject, SettingSourceUser, SettingSourceTeam, SettingSourceMDM, SettingSourcePlugins, SettingSourceAll:
		return true
	default:
		return false
	}
}

func PlatformMinimumEnvNames() []string {
	if runtime.GOOS == "windows" {
		return []string{
			"PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR",
			"TEMP", "TMP", "COMSPEC", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
			"APPDATA", "LOCALAPPDATA", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
		}
	}
	return []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TERM"}
}

func (c Config) BridgeArgv() []string {
	if c.BridgeExecutable == "" {
		return nil
	}
	return []string{c.BridgeExecutable}
}

func SelectHostEnv(hostEnv []string, allowlist []string) []string {
	return SelectHostEnvFold(hostEnv, allowlist, runtime.GOOS == "windows")
}

func SelectHostEnvFold(hostEnv []string, allowlist []string, foldCase bool) []string {
	want := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		if foldCase {
			want[strings.ToUpper(name)] = struct{}{}
		} else {
			want[name] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(allowlist))
	out := make([]string, 0, len(allowlist))
	for _, entry := range hostEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if isCredentialEnvName(key) {
			continue
		}
		lookup := key
		if foldCase {
			lookup = strings.ToUpper(key)
		}
		if _, allowed := want[lookup]; !allowed {
			continue
		}
		if _, dup := seen[lookup]; dup {
			continue
		}
		seen[lookup] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func mergeEnvAllowlist(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, name := range slices.Concat(base, extra) {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func isCredentialEnvName(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if u == "CURSOR_API_KEY" {
		return true
	}
	return strings.Contains(u, "API_KEY") || strings.Contains(u, "ACCESS_TOKEN") || strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD")
}

func durationFromSeconds(raw *float64, def, min, max time.Duration, field string) (time.Duration, error) {
	if raw == nil {
		return def, nil
	}
	if *raw <= 0 {
		return 0, fmt.Errorf("cursorsdk: %s must be positive", field)
	}
	d := time.Duration(*raw * float64(time.Second))
	if d < min || d > max {
		return 0, fmt.Errorf("cursorsdk: %s must be between %s and %s", field, min, max)
	}
	return d, nil
}

func idleTimeoutFromSeconds(raw *float64) (time.Duration, error) {
	if raw == nil {
		return defaultAgentIdleTimeout, nil
	}
	if *raw == 0 {
		return 0, nil
	}
	if *raw < 0 {
		return 0, errors.New("cursorsdk: agent_idle_timeout_seconds must be zero or positive")
	}
	d := time.Duration(*raw * float64(time.Second))
	if d < minAgentIdleTimeout || d > maxAgentIdleTimeout {
		return 0, fmt.Errorf("cursorsdk: agent_idle_timeout_seconds must be 0 or between %s and %s", minAgentIdleTimeout, maxAgentIdleTimeout)
	}
	return d, nil
}

func rejectShellOrNPMExecutable(name string) error {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	base = strings.TrimSuffix(base, ".bat")
	switch base {
	case "sh", "bash", "zsh", "fish", "cmd", "powershell", "pwsh", "nu", "npm", "npx", "yarn", "pnpm", "bun":
		return fmt.Errorf("cursorsdk: bridge_executable must be a direct bridge binary, not shell or npm launcher %q", name)
	}
	if strings.ContainsAny(name, "|&;<>`$") || strings.Contains(name, "&&") || strings.Contains(name, "||") {
		return fmt.Errorf("cursorsdk: bridge_executable must not contain shell metacharacters")
	}
	return nil
}
