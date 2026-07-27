package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type runtimeOpts struct {
	Starter         ProcessStarter
	HostEnv         []string
	ModelListSource ModelListSource
	Log             *slog.Logger
	InstanceID      string
}

type backendRuntime struct {
	cfg      Config
	catalog  *Catalog
	index    *acp.ModelIndex
	tracking *acp.TrackingInventory
	bp       *bridgeProcess
	agent    AgentBridge
	pool     *SessionPool
	coord    *FailureCoordinator
	diag     *Diag

	discoveryState atomic.Value // string
	discoveryCode  atomic.Value // string
	closed         atomic.Bool
}

func newBackendRuntime(cfg Config, opts runtimeOpts) *backendRuntime {
	cfg.SandboxMode = EffectiveSandboxMode(cfg.SandboxMode)
	catalog := NewCatalog()
	index := acp.NewModelIndex(canonicalIDForNative)
	diag := NewDiag(opts.Log, opts.InstanceID)

	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: opts.Starter,
		HostEnv: opts.HostEnv,
		Log:     opts.Log,
		Diag:    diag,
	})

	source := opts.ModelListSource
	if source == nil {
		source = newBridgeModelListSource(bp, cfg.APIKey)
	}
	tracking := acp.NewTrackingInventory(newInventoryProvider(source, catalog), index, ID)

	agent := NewBridgeAgentClient(bp)
	pool := NewSessionPool(cfg, agent, SessionPoolOpts{Diag: diag})
	coord := NewFailureCoordinator(pool, FailureCoordinatorOpts{})
	bp.onBridgeGenerationDead = func(gen int64) {
		diag.LogBridge(context.Background(), "failed", gen, string(CodeBridgeExited), DiagCorr{})
		coord.InvalidateOnBridgeDeath(gen)
	}

	rt := &backendRuntime{
		cfg:      cfg,
		catalog:  catalog,
		index:    index,
		tracking: tracking,
		bp:       bp,
		agent:    agent,
		pool:     pool,
		coord:    coord,
		diag:     diag,
	}
	rt.discoveryState.Store("unknown")
	rt.discoveryCode.Store("")
	return rt
}

func (rt *backendRuntime) asBackend() Backend {
	return Backend{
		BackendPrefixes:         []string{ID},
		EnforcesMaxOutputTokens: false,
		ModelInventory:          rt.tracking,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand AttemptCandidate) lipapi.BackendCaps {
			return resolveCaps(rt.catalog, rt.index, call, cand)
		},
		Open:  rt.Open,
		Close: rt.Close,
	}
}

func (rt *backendRuntime) Close() error {
	if rt == nil {
		return nil
	}
	if !rt.closed.CompareAndSwap(false, true) {
		return nil
	}
	start := time.Now()
	var closePool func() error
	if rt.pool != nil {
		pool := rt.pool
		closePool = func() error { return pool.Close(context.Background()) }
	}
	var closeBridge func() error
	if rt.bp != nil {
		bp := rt.bp
		closeBridge = func() error { return bp.Close() }
	}
	err := closePoolThenBridge(closePool, closeBridge)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	if rt.diag != nil {
		rt.diag.LogShutdown(context.Background(), time.Since(start), outcome, DiagCorr{})
	}
	return err
}

func (rt *backendRuntime) Status() StatusSnapshot {
	if rt == nil {
		return StatusSnapshot{BackendKind: ID}
	}
	var info BridgeInfo
	state := "closed"
	if rt.bp != nil {
		info, state = rt.bp.statusSnapshot()
	}
	discState, _ := rt.discoveryState.Load().(string)
	discCode, _ := rt.discoveryCode.Load().(string)
	agents, busy := 0, 0
	if rt.pool != nil {
		agents = rt.pool.LiveCount()
		busy = rt.pool.BusyCount()
	}
	return rt.diag.Status(StatusInput{
		Info:           info,
		RuntimeState:   state,
		DiscoveryState: discState,
		DiscoveryCode:  discCode,
		AgentCount:     agents,
		BusyRunCount:   busy,
	})
}

func (rt *backendRuntime) Open(ctx context.Context, call lipapi.Call, cand AttemptCandidate) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, errors.New("cursorsdk: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rt.closed.Load() {
		return nil, errors.New("cursorsdk: backend closed")
	}

	corr := DiagCorr{CallID: strings.TrimSpace(call.ID)}
	native, ok := resolveNativeFromCandidate(rt.index, call, cand)
	if !ok {
		rt.noteDiscovery("failed", string(CodeModelUnknown))
		rt.diag.LogDiscovery(ctx, "failed", string(CodeModelUnknown), corr)
		return nil, errors.New("cursorsdk: unknown or unaccepted model")
	}

	workspace, err := rt.resolveWorkspace(&call)
	if err != nil {
		return nil, err
	}

	if err := validateOpenCall(&call, rt.catalog, native); err != nil {
		return nil, err
	}
	if err := rt.enforceSecurityPolicy(ctx); err != nil {
		return nil, err
	}
	rt.noteDiscovery("ok", "")
	rt.diag.LogDiscovery(ctx, "ok", "", corr)

	modelParams, err := buildModelParams(rt.catalog, native, call.Options.ReasoningEffort)
	if err != nil {
		return nil, err
	}

	key := buildAgentKey(rt.cfg, &call, native, workspace, modelParams)

	headCount := rt.pool.Marker(key).MessageCount
	encoded, err := EncodePrompt(&call, headCount)
	if err != nil {
		return nil, err
	}

	create := protocol.AgentCreateParams{
		APIKey: rt.cfg.APIKey,
		Model: protocol.ModelSelection{
			ID:     native,
			Params: modelParams,
		},
		Local:              protocol.AgentCreateLocal{Cwd: workspace},
		SettingSources:     settingSourcesAsStrings(rt.cfg.SettingSources),
		SandboxOptions:     sandboxOptionsFor(rt.cfg.SandboxMode),
		AutoReview:         rt.cfg.AutoReview,
		EnableAgentRetries: false,
		MCPServers:         append(json.RawMessage(nil), rt.cfg.MCPServers...),
	}

	lease, err := rt.pool.PrepareSend(ctx, PrepareSendInput{
		Key:          key,
		Create:       create,
		View:         encoded.View,
		FullPrompt:   encoded.FullPrompt,
		SuffixPrompt: encoded.SuffixPrompt,
	})
	if err != nil {
		return nil, err
	}

	rt.pool.CommitSend(lease)

	return NewRunStream(ctx, rt.agent, lease, rt.pool, RunStreamOpts{
		CancelTimeout:    rt.cfg.CancelTimeout,
		GenerationKiller: rt.bp,
		APIKey:           rt.cfg.APIKey,
		Diag:             rt.diag,
		Corr:             corr,
	}), nil
}

func (rt *backendRuntime) noteDiscovery(state, code string) {
	if rt == nil {
		return
	}
	rt.discoveryState.Store(state)
	rt.discoveryCode.Store(code)
}

func (rt *backendRuntime) resolveWorkspace(call *lipapi.Call) (string, error) {
	hints := acp.WorkspaceHintsFromCall(call)
	if def := strings.TrimSpace(rt.cfg.DefaultWorkspace); def != "" {
		hasHint := false
		for _, key := range []string{"project_dir", "workspace_path", "cwd", "project"} {
			if strings.TrimSpace(hints[key]) != "" {
				hasHint = true
				break
			}
		}
		if !hasHint {
			hints["project_dir"] = def
		}
	}
	wp := acp.WorkspacePolicy{
		DefaultDir:      rt.cfg.DefaultWorkspace,
		RequireExplicit: true,
	}
	workspace, err := wp.ResolveWorkspace(hints)
	if err != nil {
		return "", fmt.Errorf("cursorsdk: workspace: %w", err)
	}
	return workspace, nil
}

func validateOpenCall(call *lipapi.Call, catalog *Catalog, native string) error {
	if call.Options.MaxOutputTokens != nil {
		return fmt.Errorf("%w: max_output_tokens", ErrUnsupportedPrompt)
	}
	if err := validatePromptCall(call); err != nil {
		return err
	}
	if _, err := buildModelParams(catalog, native, call.Options.ReasoningEffort); err != nil {
		return err
	}
	return nil
}

func buildModelParams(catalog *Catalog, native, effort string) (json.RawMessage, error) {
	effort = strings.TrimSpace(effort)
	profile := catalog.reasoningProfile(native)
	if effort == "" {
		return nil, nil
	}
	switch profile.Mode {
	case reasoningModeReasoning:
		if !profile.acceptsExact(effort) {
			return nil, fmt.Errorf("%w: reasoning effort %q", ErrUnsupportedPrompt, effort)
		}
		return mustJSON([]map[string]string{{"id": "reasoning", "value": effort}}), nil
	case reasoningModeEffort:
		if !profile.acceptsExact(effort) {
			return nil, fmt.Errorf("%w: reasoning effort %q", ErrUnsupportedPrompt, effort)
		}
		return mustJSON([]map[string]string{
			{"id": "effort", "value": effort},
			{"id": "thinking", "value": "true"},
		}), nil
	default:
		return nil, fmt.Errorf("%w: reasoning effort %q", ErrUnsupportedPrompt, effort)
	}
}

func settingSourcesAsStrings(sources []SettingSource) []string {
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = string(s)
	}
	return out
}

func sandboxOptionsFor(mode SandboxMode) *protocol.SandboxOptions {
	return &protocol.SandboxOptions{Enabled: EffectiveSandboxMode(mode) == SandboxRequired}
}
