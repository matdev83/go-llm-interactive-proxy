package standardplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorcliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

type routeClassifyingStream struct {
	inner   lipapi.ManagedEventStream
	onError func(error)
}

func (s *routeClassifyingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err != nil && !errors.Is(err, io.EOF) && s.onError != nil {
		s.onError(err)
	}
	return ev, err
}

func (s *routeClassifyingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}
func (s *routeClassifyingStream) Close() error { return s.inner.Close() }

type scriptEnvStarter struct {
	scriptJSON string
}

func (s scriptEnvStarter) Start(cmd []string, cwd string, env []string) (cursorsdk.Process, error) {
	return (cursorsdk.OSProcessStarter{}).Start(cmd, cwd, append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+s.scriptJSON))
}

func buildScriptedCursorSDK(t *testing.T, script fakebridge.Script, ws string) execbackend.Backend {
	t.Helper()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	raw, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	yamlText := fmt.Sprintf(`api_key: route-test-key
bridge_executable: %s
default_workspace: %s
sandbox_mode: off
`, yamlQuote(exe), yamlQuote(ws))
	sc, models, err := parseCursorSDKScaffold(decodeCursorSDKNode(t, yamlText), UpstreamAPIKeys{})
	if err != nil {
		t.Fatal(err)
	}
	be, err := applyConfiguredTrackingModelInventory(
		sc.WithProcessStarter(scriptEnvStarter{scriptJSON: string(raw)}).Backend(),
		models,
	)
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func acceptInventory(t *testing.T, be execbackend.Backend) {
	t.Helper()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ai, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	if !ok {
		t.Fatalf("inventory %T missing AcceptedInventory", be.ModelInventory)
	}
	ai.AcceptInventory(snap.Models)
}

func acpStubBackend(opens *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		BackendPrefixes: []string{cursorcliacp.ID},
		ModelInventory: modelinventory.StaticProvider{
			Models: []modelinventory.Model{{
				CanonicalID: "cursor/gpt-5.3-codex",
				NativeID:    "gpt-5.3-codex",
			}},
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "acp-stub"},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}
}

func TestCursorSDK_explicitRouteSelectsSDKNotACP_noConnectorFallback(t *testing.T) {
	ws := t.TempDir()
	script := fakebridge.DefaultScript()
	script.OnMethod = map[string][]fakebridge.Action{
		protocol.MethodAgentSend: {
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-pre"}`)},
			{Type: fakebridge.ActionExit, Code: 3},
		},
	}
	sdkBE := buildScriptedCursorSDK(t, script, ws)
	t.Cleanup(func() { _ = sdkBE.Close() })
	acceptInventory(t, sdkBE)

	var acpOpens atomic.Int32
	var sdkOpens atomic.Int32
	var classified atomic.Bool
	wrapped := sdkBE
	orig := wrapped.Open
	wrapped.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		sdkOpens.Add(1)
		stream, err := orig(ctx, call, cand)
		if err != nil {
			if lipapi.IsRecoverablePreOutput(err) && errors.Is(err, cursorsdk.ErrBridgeExited) {
				classified.Store(true)
			}
			return nil, err
		}
		return &routeClassifyingStream{inner: stream, onError: func(rerr error) {
			if !lipapi.IsRecoverablePreOutput(rerr) {
				t.Errorf("Recv want IsRecoverablePreOutput, got %v", rerr)
				return
			}
			if !errors.Is(rerr, cursorsdk.ErrBridgeExited) {
				t.Errorf("Recv want ErrBridgeExited, got %v", rerr)
				return
			}
			var cf *cursorsdk.ClassifiedFailure
			if !errors.As(rerr, &cf) || cf.Code != cursorsdk.CodeBridgeExited {
				t.Errorf("Recv want CodeBridgeExited, got %#v", cf)
				return
			}
			classified.Store(true)
		}}, nil
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(7)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"cursor-sdk": wrapped,
		"cursor-acp": acpStubBackend(&acpOpens),
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	call := &lipapi.Call{
		ID:      "no-fallback-1",
		Session: lipapi.SessionRef{ContinuityKey: "no-fallback"},
		Route:   lipapi.RouteIntent{Selector: "cursor-sdk:gpt-5.3-codex"},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err == nil && stream != nil {
		_, _ = lipapi.Collect(context.Background(), stream)
		_ = stream.Close()
	}
	if !classified.Load() {
		t.Fatal("expected bridge_exited recoverable on SDK stream path")
	}
	if sdkOpens.Load() < 1 {
		t.Fatal("expected SDK Open")
	}
	if acpOpens.Load() != 0 {
		t.Fatalf("connector-local ACP opens=%d want 0", acpOpens.Load())
	}
}

func TestCursorSDK_explicitACPRouteOpensOnlyACPStub(t *testing.T) {
	ws := t.TempDir()
	script := fakebridge.DefaultScript()
	sdkBE := buildScriptedCursorSDK(t, script, ws)
	t.Cleanup(func() { _ = sdkBE.Close() })
	acceptInventory(t, sdkBE)

	var acpOpens, sdkOpens atomic.Int32
	wrapped := sdkBE
	orig := wrapped.Open
	wrapped.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		sdkOpens.Add(1)
		return orig(ctx, call, cand)
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(8)
	ex.Backends = map[string]execbackend.Backend{
		"cursor-sdk": wrapped,
		"cursor-acp": acpStubBackend(&acpOpens),
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	call := &lipapi.Call{
		ID:      "acp-route-1",
		Session: lipapi.SessionRef{ContinuityKey: "acp-route"},
		Route:   lipapi.RouteIntent{Selector: "cursor-acp:gpt-5.3-codex"},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	col, err := lipapi.Collect(context.Background(), stream)
	_ = stream.Close()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if col.Text.String() != "acp-stub" {
		t.Fatalf("text=%q want acp-stub", col.Text.String())
	}
	if acpOpens.Load() != 1 {
		t.Fatalf("ACP opens=%d want 1", acpOpens.Load())
	}
	if sdkOpens.Load() != 0 {
		t.Fatalf("SDK opens=%d want 0", sdkOpens.Load())
	}
}

func TestCursorSDK_modelOnlyAmbiguousRejected_noConnectorFallback(t *testing.T) {
	ws := t.TempDir()
	sdkBE := buildScriptedCursorSDK(t, fakebridge.DefaultScript(), ws)
	t.Cleanup(func() { _ = sdkBE.Close() })
	acceptInventory(t, sdkBE)

	var acpOpens, sdkOpens atomic.Int32
	wrapped := sdkBE
	orig := wrapped.Open
	wrapped.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		sdkOpens.Add(1)
		return orig(ctx, call, cand)
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(9)
	ex.DefaultBackend = ""
	ex.Backends = map[string]execbackend.Backend{
		"cursor-sdk": wrapped,
		"cursor-acp": acpStubBackend(&acpOpens),
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	call := &lipapi.Call{
		ID:      "model-only-1",
		Session: lipapi.SessionRef{ContinuityKey: "model-only"},
		Route:   lipapi.RouteIntent{Selector: "gpt-5.3-codex"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil || !errors.Is(err, lipapi.ErrUnresolvedModelOnlySelector) {
		t.Fatalf("want ErrUnresolvedModelOnlySelector, got %v", err)
	}
	if sdkOpens.Load() != 0 || acpOpens.Load() != 0 {
		t.Fatalf("fallback opens sdk=%d acp=%d want 0/0", sdkOpens.Load(), acpOpens.Load())
	}
}

func TestCursorSDK_standardRegistrationBuildsOperationalBackend(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	ws := t.TempDir()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`api_key: std-reg-key
bridge_executable: %s
default_workspace: %s
`, yamlQuote(exe), yamlQuote(ws))
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(cursorsdk.ID, node, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = be.Close() })
	if be.Open == nil || be.Close == nil {
		t.Fatal("expected operational Open/Close")
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != cursorsdk.ID {
		t.Fatalf("prefixes=%v", be.BackendPrefixes)
	}
}
