package product

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the Gemini CLI ACP backend.
const ID = "geminicliacp"

// vendorPrefix is the model prefix for Gemini CLI models.
const vendorPrefix = "google"

// defaultModels is the fallback model list matching Python's
// get_shared_gemini_fallback_models() (deduplicated DEFAULT_AVAILABLE_MODELS
// + GEMINI_OAUTH_STANDARD_MODELS).
var defaultModels = []string{
	"gemini-3.1-flash-lite-preview",
	"gemini-3.1-pro-preview",
	"gemini-3-pro-preview",
	"gemini-3-flash-preview",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-embedding-001",
}

// Config configures the Gemini CLI ACP backend. The shared
// acp.ConnectorConfig embeds the local-agent fields common to every ACP CLI
// connector; Gemini-specific fields stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// AutoAccept adds the -y flag to the command.
	AutoAccept bool
	// ExeCache is instance-owned LookPath cache. When nil, New allocates one.
	ExeCache *acp.ExecutableCache
}

// geminiSpec implements acp.SubprocessConnectorSpec for the Gemini CLI.
type geminiSpec struct {
	cfg Config
}

func (s *geminiSpec) VendorID() string                { return ID }
func (s *geminiSpec) VendorPrefix() string            { return vendorPrefix }
func (s *geminiSpec) RequiresExplicitWorkspace() bool { return true }

// BuildCommand returns the subprocess command for the Gemini CLI agent.
func (s *geminiSpec) BuildCommand(model string, workspace string) ([]string, string, []string, error) {
	exe, err := resolveExecutable(s.cfg.ExeCache, s.cfg.Executable)
	if err != nil {
		return nil, "", nil, err
	}
	cmd := []string{exe, "--experimental-acp", "--model", model}
	if s.cfg.AutoAccept {
		cmd = append(cmd, "-y")
	}
	if len(s.cfg.ExtraArgs) > 0 {
		cmd = append(cmd, s.cfg.ExtraArgs...)
	}
	// On Windows, .bat/.cmd files must be invoked via cmd.exe.
	cmd = wrapBatchIfNeeded(cmd)
	return cmd, workspace, nil, nil
}

// HandshakeProfile returns the Gemini-specific handshake configuration.
// Gemini uses a minimal handshake: initialize → session/new (no authenticate).
func (s *geminiSpec) HandshakeProfile() acp.HandshakeProfile {
	return acp.HandshakeProfile{
		ProtocolVersion:      1,
		SkipAuthenticate:     true,
		SessionNewCwd:        s.cfg.DefaultWorkspace,
		SessionNewMCPServers: json.RawMessage("[]"),
		ClientCapabilities:   json.RawMessage(`{}`),
		ClientInfo:           json.RawMessage(`{"name":"llm-interactive-proxy","version":"dev"}`),
	}
}

func (s *geminiSpec) CancelProfile() acp.CancelProfile {
	return acp.CancelProfile{
		Methods:          []string{"session/cancel", "session/stop", "session/end"},
		IncludeRequestID: true,
		IncludeMessageID: true,
	}
}

// ServerRequestHandler returns nil to use the default headless handler.
// Gemini CLI historically does not handle server-originated requests.
func (s *geminiSpec) ServerRequestHandler() acp.ServerRequestHandler {
	return nil
}

// ResolveModel strips the vendor prefix and applies the default model if empty/auto.
func (s *geminiSpec) ResolveModel(effectiveModel string) string {
	return acp.ResolveVendorModel(vendorPrefix, s.cfg.Model, "gemini-2.5-flash", effectiveModel)
}

// resolveExecutable finds the Gemini CLI binary, with Windows-specific fallbacks.
func resolveExecutable(cache *acp.ExecutableCache, configured string) (string, error) {
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := cache.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("GEMINI_CLI_BIN")); env != "" {
		if resolved, ok := cache.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	if isWindows() {
		for _, name := range []string{"gemini.cmd", "gemini.exe", "gemini.bat", "gemini"} {
			if resolved, err := cache.LookPath(name); err == nil {
				return resolved, nil
			}
		}
	} else {
		if resolved, err := cache.LookPath("gemini"); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("gemini CLI executable not found; install with: npm install -g @google/gemini-cli, or set GEMINI_CLI_BIN")
}

// wrapBatchIfNeeded wraps .bat/.cmd commands with cmd.exe /d /s /c on Windows,
// matching Python's build_gemini_cli_command behavior.
func wrapBatchIfNeeded(cmd []string) []string {
	if !isWindows() || len(cmd) == 0 {
		return cmd
	}
	ext := strings.ToLower(filepath.Ext(cmd[0]))
	if ext == ".bat" || ext == ".cmd" {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		return append([]string{comspec, "/d", "/s", "/c"}, cmd...)
	}
	return cmd
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}

func defaultInventoryModels() []modelinventory.Model {
	return acp.DefaultInventoryModels(vendorPrefix, defaultModels)
}

// New returns a runtime backend that invokes the Gemini CLI via ACP stdio.
func New(cfg Config) (*Engine, error) {
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-flash"
	}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	cfg.ExeCache = cache
	spec := &geminiSpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	exe, exeErr := resolveExecutable(cache, cfg.Executable)
	return newGeminiBackend(spec, workspace, cfg, nil, exeErr, defaultInventoryModels(), cache, exe), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
// Production code should use New; tests use this to inject a fakeProcess that
// simulates the Gemini CLI agent without spawning a real binary. It skips
// executable resolution, using the default inventory.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) *Engine {
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-flash"
	}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	cfg.ExeCache = cache
	exe := strings.TrimSpace(cfg.Executable)
	if exe == "" {
		exe = "gemini"
	}
	spec := &geminiSpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	return newGeminiBackend(spec, workspace, cfg, starter, nil, defaultInventoryModels(), cache, exe)
}

// newGeminiBackend assembles the execbackend.Backend from a normalized spec,
// workspace, and config. starter, if non-nil, overrides the default OS process
// starter (used by tests). exeErr is surfaced from the Open closure so callers
// see startup errors early. models is the static model inventory.
func newGeminiBackend(spec *geminiSpec, workspace acp.WorkspacePolicy, cfg Config, starter acp.ProcessStarter, exeErr error, models []modelinventory.Model, cache *acp.ExecutableCache, exePath string) *Engine {
	backendCfg := acp.SubprocessBackendConfig{
		Protocol:  acp.NewACPProtocol(spec, nil),
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
	return &Engine{
		Inventory: modelinventory.StaticProvider{Source: modelinventory.SourceStaticBuiltin, Models: models},
		Caps:      defaultBackendCaps(),
		exeCache:  cache,
		exePath:   exePath,
		closeFn:   backend.Close,
		open: func(ctx context.Context, call *lipapi.Call) (lipapi.ManagedEventStream, error) {
			if exeErr != nil {
				return nil, exeErr
			}
			call = withDefaultWorkspace(call, cfg.DefaultWorkspace)
			return backend.Open(ctx, call)
		},
	}
}

func withDefaultWorkspace(call *lipapi.Call, workspace string) *lipapi.Call {
	workspace = strings.TrimSpace(workspace)
	if call == nil || workspace == "" {
		return call
	}
	cp := *call
	if cp.Extensions == nil {
		cp.Extensions = map[string]json.RawMessage{}
	} else {
		ext := make(map[string]json.RawMessage, len(cp.Extensions)+1)
		maps.Copy(ext, cp.Extensions)
		cp.Extensions = ext
	}
	if _, ok := cp.Extensions["acp.cwd"]; ok {
		return &cp
	}
	raw, _ := json.Marshal(workspace)
	cp.Extensions["acp.cwd"] = raw
	return &cp
}

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityVision,
		lipapi.CapabilityReasoning,
	)
}
