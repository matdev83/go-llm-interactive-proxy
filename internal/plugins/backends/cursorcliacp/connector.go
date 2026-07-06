package cursorcliacp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the Cursor CLI ACP backend.
const ID = "cursorcliacp"

// vendorPrefix is the model prefix for Cursor CLI models.
const vendorPrefix = "cursor"

// defaultModels is the fallback model list when `agent models` cannot be parsed.
var defaultModels = []string{
	"composer-2",
	"composer-2-fast",
	"auto",
	"gpt-5.2",
	"gpt-5.3-codex",
	"claude-4.6-opus-high-thinking",
}

// ansiEscapeRe matches ANSI escape sequences for stripping from CLI output.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// Config configures the Cursor CLI ACP backend. The shared
// acp.ConnectorConfig embeds the local-agent fields common to every ACP CLI
// connector; Cursor-specific fields stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// AutoAccept controls permission responses (true = allow-always, false = reject-once).
	AutoAccept bool
	// TrustWorkspace adds --trust to the command.
	TrustWorkspace bool
	// CursorAPIEndpoint is an optional -e flag value.
	CursorAPIEndpoint string
	// MCPServers is the JSON array for session/new mcpServers.
	MCPServers json.RawMessage
}

// cursorSpec implements acp.SubprocessConnectorSpec for the Cursor CLI.
type cursorSpec struct {
	cfg Config
}

// VendorID returns the backend identifier.
func (s *cursorSpec) VendorID() string { return ID }

// VendorPrefix returns the model prefix.
func (s *cursorSpec) VendorPrefix() string { return vendorPrefix }

// BuildCommand returns the subprocess command for the Cursor CLI agent.
func (s *cursorSpec) BuildCommand(model string, workspace string) ([]string, string, []string, error) {
	exe, err := resolveExecutable(s.cfg.Executable)
	if err != nil {
		return nil, "", nil, err
	}
	cmd := []string{exe}
	if s.cfg.CursorAPIEndpoint != "" {
		cmd = append(cmd, "-e", s.cfg.CursorAPIEndpoint)
	}
	if model != "" {
		cmd = append(cmd, "--model", model)
	}
	if s.cfg.TrustWorkspace {
		cmd = append(cmd, "--trust")
	}
	if len(s.cfg.ExtraArgs) > 0 {
		cmd = append(cmd, s.cfg.ExtraArgs...)
	}
	cmd = append(cmd, "acp")
	return cmd, workspace, nil, nil
}

// HandshakeProfile returns the Cursor-specific handshake configuration.
func (s *cursorSpec) HandshakeProfile() acp.HandshakeProfile {
	mcp := json.RawMessage("[]")
	if len(s.cfg.MCPServers) > 0 {
		mcp = s.cfg.MCPServers
	}
	return acp.HandshakeProfile{
		ProtocolVersion:      1,
		SkipAuthenticate:     false,
		AuthenticateParams:   json.RawMessage(`{"methodId":"cursor_login"}`),
		SessionNewCwd:        s.cfg.DefaultWorkspace,
		SessionNewMCPServers: mcp,
		ClientCapabilities:   json.RawMessage(`{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`),
		ClientInfo:           json.RawMessage(`{"name":"llm-interactive-proxy","version":"1"}`),
	}
}

// CancelProfile returns the default ACP cancel methods.
func (s *cursorSpec) CancelProfile() acp.CancelProfile {
	return acp.CancelProfile{
		Methods:          []string{"session/cancel", "session/stop", "session/end"},
		IncludeRequestID: true,
		IncludeMessageID: true,
	}
}

// ServerRequestHandler returns the Cursor-specific handler for inbound JSON-RPC.
func (s *cursorSpec) ServerRequestHandler() acp.ServerRequestHandler {
	return &cursorServerRequestHandler{autoAccept: s.cfg.AutoAccept}
}

// RequiresExplicitWorkspace returns true — Cursor CLI requires an explicit workspace.
func (s *cursorSpec) RequiresExplicitWorkspace() bool { return true }

// ResolveModel strips the vendor prefix and applies the default model if empty/auto.
func (s *cursorSpec) ResolveModel(effectiveModel string) string {
	return acp.ResolveVendorModel(vendorPrefix, s.cfg.Model, "composer-2", effectiveModel)
}

// cursorServerRequestHandler handles inbound JSON-RPC requests from the Cursor CLI.
type cursorServerRequestHandler struct {
	autoAccept bool
}

func (h *cursorServerRequestHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, _ json.RawMessage) (any, error) {
	switch method {
	case "session/request_permission":
		optionID := "reject-once"
		if h.autoAccept {
			optionID = "allow-always"
		}
		return map[string]any{
			"outcome": map[string]any{
				"outcome":  "selected",
				"optionId": optionID,
			},
		}, nil

	case "cursor/ask_question":
		return map[string]any{
			"outcome": map[string]any{
				"outcome": "skipped",
				"reason":  "proxy_auto_skip",
			},
		}, nil

	case "cursor/create_plan":
		return map[string]any{
			"outcome": map[string]any{
				"outcome": "rejected",
				"reason":  "proxy_auto_reject",
			},
		}, nil

	default:
		if strings.HasPrefix(method, "cursor/") {
			// Unhandled Cursor extension — reply with empty result (headless proxy).
			return map[string]any{}, nil
		}
		// Unknown method — return JSON-RPC method-not-found error.
		return nil, fmt.Errorf("method not handled: %s", method)
	}
}

// resolveExecutable finds the Cursor CLI agent binary.
func resolveExecutable(configured string) (string, error) {
	// 1. Check configured path.
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := acp.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	// 2. Check CURSOR_AGENT_BIN env.
	if env := strings.TrimSpace(os.Getenv("CURSOR_AGENT_BIN")); env != "" {
		if resolved, ok := acp.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	// 3. Check PATH for "agent".
	if resolved, err := acp.LookPathCached("agent"); err == nil {
		return resolved, nil
	}
	// 4. On Windows, check for "agent.cmd".
	if isWindows() {
		if resolved, err := acp.LookPathCached("agent.cmd"); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("cursor CLI (agent) executable not found; install Cursor CLI and ensure `agent` is on PATH or set CURSOR_AGENT_BIN")
}

// parseAgentModelsListing parses `agent models` human-readable output into model IDs.
func parseAgentModelsListing(stdout string) []string {
	var models []string
	seen := make(map[string]struct{})
	for rawLine := range strings.SplitSeq(stdout, "\n") {
		line := strings.TrimSpace(ansiEscapeRe.ReplaceAllString(rawLine, ""))
		if line == "" || strings.HasPrefix(strings.ToLower(line), "loading") {
			continue
		}
		before, _, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		modelID := strings.TrimSpace(before)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		if strings.Contains(modelID, " ") {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
	}
	return models
}

// discoverModels runs `agent models` to discover available models.
// Returns the default model list prefixed with the vendor prefix on error.
func discoverModels(ctx context.Context, executable string) []modelinventory.Model {
	cmd := exec.CommandContext(ctx, executable, "models")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return defaultInventoryModels()
	}
	raw := parseAgentModelsListing(string(output))
	if len(raw) == 0 {
		return defaultInventoryModels()
	}
	var models []modelinventory.Model
	for _, m := range raw {
		models = append(models, modelinventory.Model{
			CanonicalID: vendorPrefix + "/" + m,
			NativeID:    m,
			DisplayName: m,
		})
	}
	return models
}

func defaultInventoryModels() []modelinventory.Model {
	return acp.DefaultInventoryModels(vendorPrefix, defaultModels)
}

// isWindows returns true on Windows.
func isWindows() bool {
	return runtime.GOOS == "windows"
}

// New returns a runtime backend that invokes the Cursor CLI via ACP stdio.
func New(cfg Config) (execbackend.Backend, error) {
	if cfg.Model == "" {
		cfg.Model = "composer-2"
	}
	spec := &cursorSpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	exe, exeErr := resolveExecutable(cfg.Executable)
	models := defaultInventoryModels()
	if exeErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		models = discoverModels(ctx, exe)
	}
	return newCursorBackend(spec, workspace, cfg, nil, exeErr, models), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
// Production code should use New; tests use this to inject a fakeProcess that
// simulates the Cursor CLI agent without spawning a real binary. It skips
// executable resolution and model discovery, using the default inventory.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) execbackend.Backend {
	if cfg.Model == "" {
		cfg.Model = "composer-2"
	}
	spec := &cursorSpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	return newCursorBackend(spec, workspace, cfg, starter, nil, defaultInventoryModels())
}

// newCursorBackend assembles the execbackend.Backend from a normalized spec,
// workspace, and config. starter, if non-nil, overrides the default OS process
// starter (used by tests). exeErr is surfaced from the Open closure so callers
// see startup errors early. models is the static model inventory.
func newCursorBackend(spec *cursorSpec, workspace acp.WorkspacePolicy, cfg Config, starter acp.ProcessStarter, exeErr error, models []modelinventory.Model) execbackend.Backend {
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
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{ID},
		ModelInventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticBuiltin,
			Models: models,
		},
		ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps {
			return defaultBackendCaps()
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
		lipapi.CapabilityVision,
		lipapi.CapabilityReasoning,
	)
}
