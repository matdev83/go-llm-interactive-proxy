package product

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestHostEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"SYSTEMROOT", "SYSTEMDRIVE", "TEMP", "TMP", "COMSPEC"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func openTestConfig(t *testing.T, exe, workspace string) Config {
	t.Helper()
	cfg := testConfig(exe)
	cfg.DefaultWorkspace = workspace
	cfg.BridgeStartTimeout = 30 * time.Second
	cfg.ShutdownTimeout = 10 * time.Second
	cfg.CancelTimeout = 5 * time.Second
	cfg.MaxAgents = 4
	cfg.MaxConcurrentRuns = 2
	return cfg
}

func textCall(model string) lipapi.Call {
	return lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello from open test")},
		}},
		Route: lipapi.RouteIntent{Selector: "cursorsdk:" + model},
	}
}

func acceptNatives(t *testing.T, inv modelinventory.Provider, natives ...string) {
	t.Helper()
	ai, ok := inv.(modelinventory.AcceptedInventory)
	require.True(t, ok)
	models := make([]modelinventory.Model, 0, len(natives))
	for _, n := range natives {
		models = append(models, modelinventory.Model{
			CanonicalID: "cursor/" + n,
			NativeID:    n,
			DisplayName: n,
		})
	}
	ai.AcceptInventory(models)
}

func drainManaged(ctx context.Context, t *testing.T, stream lipapi.ManagedEventStream) []lipapi.EventKind {
	t.Helper()
	var kinds []lipapi.EventKind
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return kinds
		}
		require.NoError(t, err)
		require.NoError(t, lipapi.ValidateEventEnvelope(&ev))
		kinds = append(kinds, ev.Kind)
	}
}

func TestBackend_FactoryOperationalSurface(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	be := NewScaffold(cfg).WithHostEnv(openTestHostEnv()).Backend()
	require.NotNil(t, be.ModelInventory)
	require.NotNil(t, be.ResolveCaps)
	require.Equal(t, []string{ID}, be.BackendPrefixes)
	require.False(t, be.EnforcesMaxOutputTokens)
	require.NotNil(t, be.Close)
	require.NotNil(t, be.Open)
	require.NoError(t, be.Close())
	require.NoError(t, be.Close())
}

func TestOpen_FakeBridgeHappyPath(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	call := textCall("gpt-5.3-codex")
	call.ID = "open-happy-1"
	stream, err := rt.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	require.NotNil(t, stream)
	defer func() { _ = stream.Close() }()

	kinds := drainManaged(ctx, t, stream)
	require.Contains(t, kinds, lipapi.EventResponseStarted)
	require.Contains(t, kinds, lipapi.EventResponseFinished)

	key := openAgentKey(t, rt, call, "gpt-5.3-codex", ws)
	require.Equal(t, "gpt-5.3-codex", key.ModelID)
	require.Equal(t, "attempt:open-happy-1", key.SessionID)
	marker := rt.pool.Marker(key)
	require.Greater(t, marker.MessageCount, 0)
}

func TestOpen_NoCommitOnPrepareFailure(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })

	ctx := context.Background()
	call := textCall("gpt-5.3-codex")
	_, err := rt.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "unknown-model-xyz"},
	})
	require.Error(t, err)

	cfgNoWS := openTestConfig(t, exe, "")
	rt2 := newBackendRuntime(cfgNoWS, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt2.Close() })
	rows := mustLoadSanitizedRows(t)
	_, entries, err := normalizeModelRows(rows)
	require.NoError(t, err)
	rt2.catalog.Replace(entries)
	acceptNatives(t, rt2.tracking, "gpt-5.3-codex")
	_, err = rt2.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	require.Equal(t, 0, rt2.pool.LiveCount())
}

func TestOpen_MaxOutputTokensFailClosed(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })
	rows := mustLoadSanitizedRows(t)
	_, entries, err := normalizeModelRows(rows)
	require.NoError(t, err)
	rt.catalog.Replace(entries)
	acceptNatives(t, rt.tracking, "gpt-5.3-codex")

	maxTok := 128
	call := textCall("gpt-5.3-codex")
	call.Options.MaxOutputTokens = &maxTok
	_, err = rt.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_output_tokens")
}

func TestOpen_UnsupportedToolsOrStructuredFailClosed(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })
	rows := mustLoadSanitizedRows(t)
	_, entries, err := normalizeModelRows(rows)
	require.NoError(t, err)
	rt.catalog.Replace(entries)
	acceptNatives(t, rt.tracking, "gpt-5.3-codex")

	call := textCall("gpt-5.3-codex")
	call.Tools = []lipapi.ToolDef{{Name: "x", Description: "d", Parameters: json.RawMessage(`{}`)}}
	_, err = rt.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedPrompt)

	call2 := textCall("gpt-5.3-codex")
	call2.Options.ResponseMIMEType = "application/json"
	_, err = rt.Open(context.Background(), call2, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedPrompt)
}

func TestOpen_ReasoningExactNoAlias(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rows := mustLoadSanitizedRows(t)
	rt := newBackendRuntime(cfg, runtimeOpts{
		HostEnv:         openTestHostEnv(),
		ModelListSource: StaticModelListSource{Rows: rows},
	})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	openDrain := func(model, effort string) error {
		call := textCall(model)
		call.Options.ReasoningEffort = effort
		stream, err := rt.Open(ctx, call, routing.AttemptCandidate{
			Primary: routing.Primary{Model: model},
		})
		if err != nil {
			return err
		}
		_ = drainManaged(ctx, t, stream)
		_ = stream.Close()
		return nil
	}

	require.NoError(t, openDrain("gpt-5.3-codex", "xhigh"))
	require.Error(t, openDrain("gpt-5.3-codex", "extra-high"))
	require.Error(t, openDrain("claude-4.6-sonnet-thinking", "xhigh"))
	require.Error(t, openDrain("composer-2-fast", "high"))
}

func TestClose_IdempotentPoolThenBridge(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	require.NoError(t, rt.Close())
	require.NoError(t, rt.Close())

	_, err := rt.Open(context.Background(), textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.Error(t, err)
}

func TestBridgeModelListSource_ListModelsViaCall(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	cfg := openTestConfig(t, exe, t.TempDir())
	bp := newBridgeProcess(cfg, bridgeOpts{Starter: OSProcessStarter{}, HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = bp.Close() })

	rec := &recordingBridgeCaller{inner: bp}
	src := &bridgeModelListSource{call: rec, apiKey: cfg.APIKey}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rows, err := src.ListModels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	require.Equal(t, "gpt-5.3-codex", rows[0].ID)

	require.NotEmpty(t, rec.calls)
	require.Equal(t, protocol.MethodModelsList, rec.calls[0].method)
	var params protocol.ModelsListParams
	require.NoError(t, json.Unmarshal(rec.calls[0].params, &params))
	require.Equal(t, cfg.APIKey, params.APIKey)
}

func TestOpen_DoesNotReturnConstructionStub(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	be := NewScaffold(cfg).WithHostEnv(openTestHostEnv()).Backend()
	t.Cleanup(func() { _ = be.Close() })

	_, err := be.Open(context.Background(), textCall("missing"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "missing"},
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), cfg.APIKey)
	require.NotContains(t, err.Error(), "backend runtime construction is not implemented")
}

func TestScaffold_WithModelListSourceStillStatic(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	cfg := openTestConfig(t, exe, t.TempDir())
	rows := []protocol.ModelRow{{ID: "gpt-5.3-codex", DisplayName: "GPT"}}
	be := NewScaffold(cfg).
		WithHostEnv(openTestHostEnv()).
		WithModelListSource(StaticModelListSource{Rows: rows}).
		Backend()
	t.Cleanup(func() { _ = be.Close() })
	snap, err := be.ModelInventory.LoadModels(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Models, 1)
	require.Equal(t, "gpt-5.3-codex", snap.Models[0].NativeID)
}

func TestResolveAgentSessionID_AuthoritativeAndAttemptScoped(t *testing.T) {
	t.Parallel()
	require.Equal(t, "proxy-auth", resolveAgentSessionID(&lipapi.Call{
		ID: "call-1",
		Session: lipapi.SessionRef{
			ClientSessionID:        "client-hint",
			AuthoritativeSessionID: "proxy-auth",
		},
	}))
	require.Equal(t, "attempt:call-2", resolveAgentSessionID(&lipapi.Call{
		ID:      "call-2",
		Session: lipapi.SessionRef{ClientSessionID: "client-hint"},
	}))
	a := resolveAgentSessionID(&lipapi.Call{})
	b := resolveAgentSessionID(&lipapi.Call{})
	require.True(t, strings.HasPrefix(a, "attempt:"))
	require.True(t, strings.HasPrefix(b, "attempt:"))
	require.NotEqual(t, a, b)
	require.NotEqual(t, "default", a)
	require.NotEqual(t, "default", b)
}

func TestOpen_AuthoritativeSessionBootstrapThenIncrementalReuse(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	call1 := textCall("gpt-5.3-codex")
	call1.ID = "turn-1"
	call1.Session.AuthoritativeSessionID = "auth-reuse"
	stream1, err := rt.Open(ctx, call1, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	_ = drainManaged(ctx, t, stream1)
	_ = stream1.Close()
	key1 := openAgentKey(t, rt, call1, "gpt-5.3-codex", ws)
	require.Equal(t, "auth-reuse", key1.SessionID)
	count1 := rt.pool.Marker(key1).MessageCount
	require.Greater(t, count1, 0)
	require.Equal(t, 1, rt.pool.LiveCount())

	call2 := textCall("gpt-5.3-codex")
	call2.ID = "turn-2"
	call2.Session.AuthoritativeSessionID = "auth-reuse"
	call2.Messages = []lipapi.Message{
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello from open test")}},
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("prior reply")}},
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("second turn")}},
	}
	stream2, err := rt.Open(ctx, call2, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	_ = drainManaged(ctx, t, stream2)
	_ = stream2.Close()
	key2 := openAgentKey(t, rt, call2, "gpt-5.3-codex", ws)
	require.Equal(t, key1.IdentityHash(), key2.IdentityHash())
	require.Equal(t, 1, rt.pool.LiveCount())
	require.Greater(t, rt.pool.Marker(key2).MessageCount, count1)
}

func TestOpen_AbsentSessionDoesNotMergeUnrelatedCalls(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	openOne := func(callID string) AgentKey {
		call := textCall("gpt-5.3-codex")
		call.ID = callID
		call.Session.ClientSessionID = "same-client-hint"
		stream, err := rt.Open(ctx, call, routing.AttemptCandidate{
			Primary: routing.Primary{Model: "gpt-5.3-codex"},
		})
		require.NoError(t, err)
		_ = drainManaged(ctx, t, stream)
		_ = stream.Close()
		return openAgentKey(t, rt, call, "gpt-5.3-codex", ws)
	}

	k1 := openOne("call-a")
	k2 := openOne("call-b")
	require.Equal(t, "attempt:call-a", k1.SessionID)
	require.Equal(t, "attempt:call-b", k2.SessionID)
	require.NotEqual(t, k1.IdentityHash(), k2.IdentityHash())
	require.NotEqual(t, "default", k1.SessionID)
	require.NotEqual(t, "same-client-hint", k1.SessionID)
}

func TestOpen_CancelStream(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	cfg := openTestConfig(t, exe, ws)
	rt := newBackendRuntime(cfg, runtimeOpts{HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	stream, err := rt.Open(ctx, textCall("gpt-5.3-codex"), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	res := stream.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	require.Equal(t, lipapi.CancelModeProvider, res.Mode)
	require.NoError(t, res.Err)
	require.NoError(t, stream.Close())
}

func TestBridgeModelListSource_APIKeyNotInArgvOrEnv(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	cfg := openTestConfig(t, exe, t.TempDir())
	secret := cfg.APIKey
	require.NotEmpty(t, secret)

	rec := &recordingProcessStarter{inner: OSProcessStarter{}}
	bp := newBridgeProcess(cfg, bridgeOpts{Starter: rec, HostEnv: openTestHostEnv()})
	t.Cleanup(func() { _ = bp.Close() })
	src := newBridgeModelListSource(bp, secret)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rows, err := src.ListModels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	require.NotEmpty(t, rec.starts)
	for _, s := range rec.starts {
		for _, arg := range s.cmd {
			require.NotContains(t, arg, secret)
		}
		for _, env := range s.env {
			require.NotContains(t, env, secret)
			require.False(t, strings.HasPrefix(env, "CURSOR_API_KEY="))
		}
	}
	diag := rtDiagnosticSafe(bp)
	require.NotContains(t, diag, secret)
}

func rtDiagnosticSafe(bp *bridgeProcess) string {
	if bp == nil {
		return ""
	}
	info, err := bp.EnsureReady(context.Background())
	if err != nil {
		return err.Error()
	}
	return info.ImplVersion + info.SDKVersion
}

type recordingProcessStarter struct {
	inner  ProcessStarter
	starts []struct {
		cmd []string
		env []string
	}
}

func (r *recordingProcessStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	r.starts = append(r.starts, struct {
		cmd []string
		env []string
	}{
		cmd: append([]string(nil), cmd...),
		env: append([]string(nil), env...),
	})
	return r.inner.Start(cmd, cwd, env)
}

func openAgentKey(t *testing.T, rt *backendRuntime, call lipapi.Call, native, _ string) AgentKey {
	t.Helper()
	workspace, err := rt.resolveWorkspace(&call)
	require.NoError(t, err)
	params, err := buildModelParams(rt.catalog, native, call.Options.ReasoningEffort)
	require.NoError(t, err)
	return buildAgentKey(rt.cfg, &call, native, workspace, params)
}

type recordingBridgeCaller struct {
	inner *bridgeProcess
	calls []struct {
		method string
		params json.RawMessage
	}
}

func (r *recordingBridgeCaller) EnsureReady(ctx context.Context) (BridgeInfo, error) {
	return r.inner.EnsureReady(ctx)
}

func (r *recordingBridgeCaller) Call(ctx context.Context, method string, params json.RawMessage) (*protocol.Frame, error) {
	r.calls = append(r.calls, struct {
		method string
		params json.RawMessage
	}{method: method, params: append(json.RawMessage(nil), params...)})
	return r.inner.Call(ctx, method, params)
}
