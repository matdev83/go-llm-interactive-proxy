package codexappserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the Codex App Server backend.
const ID = "openai-codex-app-server"

// vendorPrefix is the model prefix for Codex App Server models.
const vendorPrefix = "openai"

// handshakeClientName is the client name sent during initialize.
const handshakeClientName = "llm-interactive-proxy"

// defaultModels is the fallback model list matching Python's _DEFAULT_CODEX_MODEL_IDS.
var defaultModels = []string{
	"auto",
	"gpt-5.4",
	"gpt-5.3-codex",
	"gpt-5.2",
}

// autoAcceptMethods are server-initiated JSON-RPC request methods that this
// headless proxy auto-accepts. Every other method fails closed via decline.
var autoAcceptMethods = map[string]bool{
	"execCommandApproval":                   true,
	"applyPatchApproval":                    true,
	"item/commandExecution/requestApproval": true,
	"item/fileChange/requestApproval":       true,
	"item/permissions/requestApproval":      true,
}

// Config configures the Codex App Server backend. The shared
// acp.ConnectorConfig embeds the local-agent fields common to every ACP CLI
// connector; Codex-specific fields stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// ConfigOverrides are -c key=value overrides passed between "app-server" and "--stdio".
	ConfigOverrides  []string
	DefaultVerbosity lipapi.VerbosityLevel
}

// resolveExecutable finds the Codex CLI binary, with cross-platform fallbacks.
func resolveExecutable(configured string) (string, error) {
	// 1. Check configured path.
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := acp.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	// 2. Check CODEX_BIN env.
	if env := strings.TrimSpace(os.Getenv("CODEX_BIN")); env != "" {
		if resolved, ok := acp.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	// 3. Check PATH for codex variants.
	for _, name := range []string{"codex", "codex.cmd", "codex.exe"} {
		if resolved, err := acp.LookPathCached(name); err == nil {
			return resolved, nil
		}
	}
	// 4. Check npm-global locations.
	if isWindows() {
		for _, envVar := range []string{"APPDATA", "LOCALAPPDATA"} {
			if dir := strings.TrimSpace(os.Getenv(envVar)); dir != "" {
				candidate := filepath.Join(dir, "npm", "codex.cmd")
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			for _, rel := range []string{".local/bin/codex", ".npm-global/bin/codex"} {
				candidate := filepath.Join(home, rel)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	}
	return "", fmt.Errorf("codex CLI executable not found; install Codex CLI and ensure `codex` is on PATH, or set CODEX_BIN")
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}

// buildCodexCommand builds the "codex app-server --stdio" launch command.
// Global flags precede "app-server"; -c overrides sit between "app-server" and
// "--stdio"; extra args are appended last. The model is passed via thread/start.
func buildCodexCommand(exe string, cfgOverrides, extraArgs []string) []string {
	cmd := []string{
		exe,
		"--dangerously-bypass-approvals-and-sandbox",
		"--search",
		"app-server",
	}
	for _, override := range cfgOverrides {
		cmd = append(cmd, "-c", override)
	}
	cmd = append(cmd, "--stdio")
	cmd = append(cmd, extraArgs...)
	return cmd
}

func buildCodexCommandWithVerbosity(exe string, cfgOverrides []string, verbosity lipapi.VerbosityLevel, extraArgs []string) []string {
	overrides := make([]string, 0, len(cfgOverrides)+1)
	if verbosity == "" {
		overrides = append(overrides, cfgOverrides...)
	} else {
		for _, override := range cfgOverrides {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(override)), "model_verbosity=") {
				continue
			}
			overrides = append(overrides, override)
		}
		overrides = append(overrides, "model_verbosity="+string(verbosity))
	}
	return buildCodexCommand(exe, overrides, extraArgs)
}

// codexServerRequestHandler handles inbound JSON-RPC approval requests from the
// Codex app-server. Auto-accepts known approval methods; declines everything else.
type codexServerRequestHandler struct{}

func (h *codexServerRequestHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, params json.RawMessage) (any, error) {
	if autoAcceptMethods[method] {
		if method == "item/permissions/requestApproval" {
			// Echo back the requested permissions as granted.
			var p struct {
				Permissions map[string]any `json:"permissions"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Permissions == nil {
				p.Permissions = map[string]any{}
			}
			return map[string]any{"permissions": p.Permissions}, nil
		}
		return map[string]any{"decision": "accept"}, nil
	}
	// Unknown method — fail closed with decline (not an error, to avoid terminating the stream).
	return map[string]any{"decision": "decline"}, nil
}

// stripOpenAIModelPrefix strips a leading "openai/" vendor prefix.
func stripOpenAIModelPrefix(model string) string {
	if model == "" {
		return ""
	}
	if strings.HasPrefix(model, "openai/") {
		return model[len("openai/"):]
	}
	return model
}

// isAutoModel returns true when the model is empty or "auto".
func isAutoModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return m == "" || m == "auto"
}

func defaultInventoryModels() []modelinventory.Model {
	return acp.DefaultInventoryModels(vendorPrefix, defaultModels)
}

// New returns a runtime backend that invokes the Codex CLI app-server via stdio.
func New(cfg Config) (execbackend.Backend, error) {
	verbosity, err := lipapi.ParseVerbosityLevel(string(cfg.DefaultVerbosity))
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("%s: default_verbosity: %w", ID, err)
	}
	cfg.DefaultVerbosity = verbosity
	cfg.applyDefaults()
	return newBackend(cfg, true, nil), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
// Production code should use New; tests use this to inject a fakeProcess that
// simulates the Codex app-server subprocess without spawning a real binary.
// The executable is set to a placeholder so BuildSpawnCommand does not require
// the real codex binary to be on PATH.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) execbackend.Backend {
	clearInvalidDefaultVerbosity(&cfg)
	cfg.applyDefaults()
	return newBackend(cfg, false, starter)
}

// clearInvalidDefaultVerbosity normalizes DefaultVerbosity for test constructors.
// Unlike New, it cannot fail the call, so invalid values are cleared instead of
// being forwarded into -c model_verbosity=<raw>.
func clearInvalidDefaultVerbosity(cfg *Config) {
	verbosity, err := lipapi.ParseVerbosityLevel(string(cfg.DefaultVerbosity))
	if err != nil {
		cfg.DefaultVerbosity = ""
		return
	}
	cfg.DefaultVerbosity = verbosity
}

// applyDefaults normalizes the config model field.
func (cfg *Config) applyDefaults() {
	if cfg.Model == "" {
		cfg.Model = "auto"
	}
	cfg.Model = stripOpenAIModelPrefix(cfg.Model)
}

// newBackend builds the execbackend.Backend struct from a normalized Config.
// requireExplicitWorkspace controls whether the WorkspacePolicy requires an
// explicit workspace (production) or allows fallback to the default (tests).
// starter, if non-nil, overrides the default OS process starter (used by tests)
// and selects test-mode executable resolution: the spec.exe is set to a
// placeholder so BuildSpawnCommand does not require a real codex binary.
func newBackend(cfg Config, requireExplicitWorkspace bool, starter acp.ProcessStarter) execbackend.Backend {
	spec := &codexSpec{cfg: cfg}
	var exeErr error
	if starter != nil {
		// Test mode: the fake starter ignores the command, so a placeholder exe
		// is sufficient and avoids requiring codex on PATH (CI runners lack it).
		spec.exe = "codex"
	} else {
		// Production: resolve once here so BuildSpawnCommand never re-resolves.
		// exeErr is surfaced from the Open closure so operators see startup
		// errors early instead of on first prompt.
		spec.exe, exeErr = resolveExecutable(cfg.Executable)
	}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: requireExplicitWorkspace,
	}
	backendCfg := acp.SubprocessBackendConfig{
		Protocol:  &codexProtocol{spec: spec},
		Workspace: workspace,
		Pool: acp.RuntimePoolConfig{
			IdleTimeout:    cfg.IdleTimeout,
			StaleKillDelay: cfg.StaleKillDelay,
		},
	}
	if starter != nil {
		backendCfg.ProcessStarter = starter
	}
	backend := acp.NewSubprocessBackend(backendCfg)
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{ID},
		ModelInventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticBuiltin,
			Models: defaultInventoryModels(),
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			_ = cand
			if exeErr != nil {
				return nil, exeErr
			}
			return backend.Open(ctx, &call)
		},
	}
}

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityTools,
		lipapi.CapabilityVision,
		lipapi.CapabilityReasoning,
	)
}
