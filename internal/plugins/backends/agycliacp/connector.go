package agycliacp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the AGY CLI ACP backend.
const ID = "agycliacp"

// vendorPrefix is the model prefix for AGY models.
const vendorPrefix = "agy"

// defaultModelIDs are the fallback model IDs matching Python's _DEFAULT_AGY_MODEL_IDS.
// These are already fully qualified with vendor namespaces (e.g. "google/gemini-3.5-flash-high").
var defaultModelIDs = []string{
	"google/gemini-3.5-flash-high",
	"google/gemini-3.5-flash-medium",
	"google/gemini-3.5-flash-low",
	"google/gemini-3.1-pro",
	"anthropic/claude-sonnet-4.6-thinking",
	"anthropic/claude-opus-4.6-thinking",
}

// Config configures the AGY CLI ACP backend. The shared acp.ConnectorConfig
// embeds the local-agent fields common to every ACP CLI connector; AGY-specific
// fields (wrapper, binary, permissions, timeout, mcp) stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// WrapperExecutable is the path to the go-agy-acp-wrapper binary.
	// If empty, resolves from AGY_ACP_WRAPPER_BIN env or PATH.
	WrapperExecutable string
	// AGYBinary is an optional --agy-binary flag value pointing to the agy binary.
	AGYBinary string
	// SkipPermissions controls the --skip-permissions vs --no-skip-permissions flag.
	SkipPermissions bool
	// TimeoutSeconds is the optional --timeout-seconds flag value.
	TimeoutSeconds int
	// MCPServers is the JSON array for session/new mcpServers.
	MCPServers json.RawMessage
}

// agySpec implements acp.SubprocessConnectorSpec for the AGY CLI.
type agySpec struct {
	cfg Config
}

func (s *agySpec) VendorID() string                { return ID }
func (s *agySpec) VendorPrefix() string            { return vendorPrefix }
func (s *agySpec) RequiresExplicitWorkspace() bool { return true }

// BuildCommand returns the subprocess command for the go-agy-acp-wrapper.
func (s *agySpec) BuildCommand(model string, workspace string) ([]string, string, []string, error) {
	exe, err := resolveWrapper(s.cfg.WrapperExecutable)
	if err != nil {
		return nil, "", nil, err
	}
	cmd := []string{exe}
	if s.cfg.AGYBinary != "" {
		cmd = append(cmd, "--agy-binary", s.cfg.AGYBinary)
	}
	if model != "" && model != "auto" {
		cmd = append(cmd, "--model", model)
	}
	if s.cfg.TimeoutSeconds > 0 {
		cmd = append(cmd, "--timeout-seconds", fmt.Sprintf("%d", s.cfg.TimeoutSeconds))
	}
	if s.cfg.SkipPermissions {
		cmd = append(cmd, "--skip-permissions")
	} else {
		cmd = append(cmd, "--no-skip-permissions")
	}
	if len(s.cfg.ExtraArgs) > 0 {
		cmd = append(cmd, s.cfg.ExtraArgs...)
	}
	return cmd, workspace, nil, nil
}

// HandshakeProfile returns the AGY-specific handshake configuration.
// AGY uses a full handshake: initialize → authenticate (methodId: "agy") → session/new.
func (s *agySpec) HandshakeProfile() acp.HandshakeProfile {
	mcp := json.RawMessage("[]")
	if len(s.cfg.MCPServers) > 0 {
		mcp = s.cfg.MCPServers
	}
	return acp.HandshakeProfile{
		ProtocolVersion:      1,
		SkipAuthenticate:     false,
		AuthenticateParams:   json.RawMessage(`{"methodId":"agy"}`),
		SessionNewCwd:        s.cfg.DefaultWorkspace,
		SessionNewMCPServers: mcp,
		ClientCapabilities:   json.RawMessage(`{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`),
		ClientInfo:           json.RawMessage(`{"name":"llm-interactive-proxy","version":"1"}`),
	}
}

func (s *agySpec) CancelProfile() acp.CancelProfile {
	return acp.CancelProfile{
		Methods:          []string{"session/cancel", "session/stop", "session/end"},
		IncludeRequestID: true,
		IncludeMessageID: true,
	}
}

// ServerRequestHandler returns the AGY-specific handler.
// agy/* methods get empty result replies; unknown methods get a method-not-found error.
func (s *agySpec) ServerRequestHandler() acp.ServerRequestHandler {
	return &agyServerRequestHandler{}
}

// ResolveModel strips the "agy:" vendor prefix and applies the default model if empty/auto.
// Note: AGY model IDs are already fully qualified (e.g. "google/gemini-3.5-flash-high"),
// so we only strip the route-level "agy:" prefix, not internal vendor namespaces.
func (s *agySpec) ResolveModel(effectiveModel string) string {
	return acp.ResolveVendorModel(vendorPrefix, s.cfg.Model, "google/gemini-3.5-flash-high", effectiveModel)
}

// agyServerRequestHandler handles inbound JSON-RPC requests from the AGY wrapper.
type agyServerRequestHandler struct{}

func (h *agyServerRequestHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, _ json.RawMessage) (any, error) {
	if strings.HasPrefix(method, "agy/") {
		// Unhandled AGY extension — reply with empty result (headless proxy).
		return map[string]any{}, nil
	}
	// Unknown method — return method-not-found error.
	return nil, fmt.Errorf("method not handled: %s", method)
}

// resolveWrapper finds the go-agy-acp-wrapper binary.
func resolveWrapper(configured string) (string, error) {
	// 1. Check configured path.
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := acp.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	// 2. Check AGY_ACP_WRAPPER_BIN env.
	if env := strings.TrimSpace(os.Getenv("AGY_ACP_WRAPPER_BIN")); env != "" {
		if resolved, ok := acp.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	// 3. Check PATH for wrapper variants.
	for _, name := range []string{"go-agy-acp-wrapper", "go-agy-acp-wrapper.exe"} {
		if resolved, err := acp.LookPathCached(name); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("go-agy-acp-wrapper executable not found; build go-agy-acp-wrapper and put it on PATH, or set AGY_ACP_WRAPPER_BIN / wrapper_executable")
}

func defaultInventoryModels() []modelinventory.Model {
	return acp.DefaultInventoryModels(vendorPrefix, defaultModelIDs)
}

// New returns a runtime backend that invokes the AGY CLI via ACP stdio.
func New(cfg Config) (execbackend.Backend, error) {
	if cfg.Model == "" {
		cfg.Model = "google/gemini-3.5-flash-high"
	}
	// Fall back to AGY_BINARY env var, matching Python's kwargs.get("agy_binary") or os.environ.get("AGY_BINARY").
	if cfg.AGYBinary == "" {
		cfg.AGYBinary = strings.TrimSpace(os.Getenv("AGY_BINARY"))
	}
	spec := &agySpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	_, exeErr := resolveWrapper(cfg.WrapperExecutable)
	return newAGYBackend(spec, workspace, cfg, nil, exeErr, defaultInventoryModels()), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
// Production code should use New; tests use this to inject a fakeProcess that
// simulates the go-agy-acp-wrapper without spawning a real binary. It skips
// wrapper executable resolution, using the default inventory.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) execbackend.Backend {
	if cfg.Model == "" {
		cfg.Model = "google/gemini-3.5-flash-high"
	}
	if cfg.AGYBinary == "" {
		cfg.AGYBinary = strings.TrimSpace(os.Getenv("AGY_BINARY"))
	}
	spec := &agySpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	return newAGYBackend(spec, workspace, cfg, starter, nil, defaultInventoryModels())
}

// newAGYBackend assembles the execbackend.Backend from a normalized spec,
// workspace, and config. starter, if non-nil, overrides the default OS process
// starter (used by tests). exeErr is surfaced from the Open closure so callers
// see startup errors early. models is the static model inventory.
func newAGYBackend(spec *agySpec, workspace acp.WorkspacePolicy, cfg Config, starter acp.ProcessStarter, exeErr error, models []modelinventory.Model) execbackend.Backend {
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
