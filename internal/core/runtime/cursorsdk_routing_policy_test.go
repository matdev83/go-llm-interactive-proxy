package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type scriptEnvStarter struct {
	scriptJSON string
}

func (s scriptEnvStarter) Start(cmd []string, cwd string, env []string) (cursorsdk.Process, error) {
	return (cursorsdk.OSProcessStarter{}).Start(cmd, cwd, append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+s.scriptJSON))
}

type cancelCountingStream struct {
	inner   lipapi.ManagedEventStream
	cancels *atomic.Int32
}

func (s *cancelCountingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	return s.inner.Recv(ctx)
}

func (s *cancelCountingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.cancels.Add(1)
	return s.inner.Cancel(ctx, cause)
}
func (s *cancelCountingStream) Close() error { return s.inner.Close() }

func buildRuntimeCursorSDK(t *testing.T, script fakebridge.Script, ws string) execbackend.Backend {
	t.Helper()
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	raw, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	cancelSec := 0.15
	cfg, err := cursorsdk.Normalize(cursorsdk.Input{
		APIKey:               "runtime-policy-key",
		BridgeExecutable:     exe,
		DefaultWorkspace:     ws,
		SandboxMode:          string(cursorsdk.SandboxOff),
		CancelTimeoutSeconds: &cancelSec,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	be := cursorsdk.NewScaffold(cfg).
		WithProcessStarter(scriptEnvStarter{scriptJSON: string(raw)}).
		Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acceptedInv, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	if !ok {
		t.Fatal("expected AcceptedInventory")
	}
	acceptedInv.AcceptInventory(snap.Models)
	return be
}

func policyCall(selector, id string) *lipapi.Call {
	return &lipapi.Call{
		ID:      id,
		Session: lipapi.SessionRef{ContinuityKey: "cursorsdk-policy"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("policy probe")},
		}},
	}
}

func preOutputExitScript() fakebridge.Script {
	script := fakebridge.DefaultScript()
	script.OnMethod = map[string][]fakebridge.Action{
		protocol.MethodAgentSend: {
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-pre"}`)},
			{Type: fakebridge.ActionExit, Code: 9},
		},
	}
	return script
}

func assertBridgeExitedRecoverable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("want IsRecoverablePreOutput, got %v", err)
	}
	if !errors.Is(err, cursorsdk.ErrBridgeExited) {
		t.Fatalf("want ErrBridgeExited root, got %v", err)
	}
	var cf *cursorsdk.ClassifiedFailure
	if !errors.As(err, &cf) || cf == nil {
		t.Fatalf("want ClassifiedFailure, got %T %v", err, err)
	}
	if cf.Code != cursorsdk.CodeBridgeExited {
		t.Fatalf("code=%q want %q", cf.Code, cursorsdk.CodeBridgeExited)
	}
	if cf.Phase != lipapi.PhasePreOutput {
		t.Fatalf("phase=%q want pre_output", cf.Phase)
	}
}

type classifyingStream struct {
	inner   lipapi.ManagedEventStream
	onError func(error)
}

func (s *classifyingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err != nil && !errors.Is(err, io.EOF) && s.onError != nil {
		s.onError(err)
	}
	return ev, err
}

func (s *classifyingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}
func (s *classifyingStream) Close() error { return s.inner.Close() }

func TestCursorSDK_preOutputRecoverable_singleCandidateNeverOpensACP(t *testing.T) {
	ws := t.TempDir()
	sdk := buildRuntimeCursorSDK(t, preOutputExitScript(), ws)
	t.Cleanup(func() { _ = sdk.Close() })

	var acpOpens atomic.Int32
	var sdkOpens atomic.Int32
	var classified atomic.Bool
	wrappedSDK := sdk
	origOpen := wrappedSDK.Open
	wrappedSDK.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		sdkOpens.Add(1)
		stream, err := origOpen(ctx, call, cand)
		if err != nil {
			assertBridgeExitedRecoverable(t, err)
			classified.Store(true)
			return nil, err
		}
		return &classifyingStream{inner: stream, onError: func(rerr error) {
			assertBridgeExitedRecoverable(t, rerr)
			classified.Store(true)
		}}, nil
	}
	acpStub := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			acpOpens.Add(1)
			return lipapi.NewFixedEventStream(completionEvents("acp-should-not-run")), nil
		},
	}

	ex := runtime.TestExecutor()
	ex.Store = parallelStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"cursor-sdk": wrappedSDK,
		"cursor-acp": acpStub,
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	stream, err := ex.Execute(context.Background(), policyCall("cursor-sdk:gpt-5.3-codex", "pre-1"))
	if err == nil && stream != nil {
		_, _ = lipapi.Collect(context.Background(), stream)
		_ = stream.Close()
	}
	// Exact path: stream Recv returns recoverable bridge_exited; core swallows and
	// surfaces no-eligible when the operator plan has only the SDK candidate.
	if !classified.Load() {
		t.Fatal("expected bridge_exited recoverable classification on SDK stream path")
	}
	if acpOpens.Load() != 0 {
		t.Fatalf("hidden ACP opens=%d want 0 (single SDK candidate)", acpOpens.Load())
	}
	if sdkOpens.Load() < 1 {
		t.Fatal("expected SDK Open")
	}
}

func TestCursorSDK_preOutputRecoverable_failoverWhenOperatorPlanIncludesNext(t *testing.T) {
	ws := t.TempDir()
	sdk := buildRuntimeCursorSDK(t, preOutputExitScript(), ws)
	t.Cleanup(func() { _ = sdk.Close() })

	var acpOpens atomic.Int32
	var sdkOpens atomic.Int32
	var failoverOpens atomic.Int32
	wrappedSDK := sdk
	origOpen := wrappedSDK.Open
	wrappedSDK.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		sdkOpens.Add(1)
		return origOpen(ctx, call, cand)
	}
	acpStub := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			acpOpens.Add(1)
			return nil, errors.New("acp must not open")
		},
	}
	failover := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			failoverOpens.Add(1)
			return lipapi.NewFixedEventStream(completionEvents("failover-ok")), nil
		},
	}

	ex := runtime.TestExecutor()
	ex.Store = parallelStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(4)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"cursor-sdk": wrappedSDK,
		"cursor-acp": acpStub,
		"failover":   failover,
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	stream, err := ex.Execute(context.Background(), policyCall("cursor-sdk:gpt-5.3-codex|failover:model", "pre-fail-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	col, err := lipapi.Collect(context.Background(), stream)
	_ = stream.Close()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if col.Text.String() != "failover-ok" {
		t.Fatalf("text=%q want failover-ok", col.Text.String())
	}
	if sdkOpens.Load() < 1 {
		t.Fatal("expected SDK Open")
	}
	if failoverOpens.Load() != 1 {
		t.Fatalf("failover opens=%d want 1", failoverOpens.Load())
	}
	if acpOpens.Load() != 0 {
		t.Fatalf("ACP opens=%d want 0 (not in operator plan)", acpOpens.Load())
	}
}

func TestCursorSDK_postOutputCrashSurfacesCommittedBLegNoRetry(t *testing.T) {
	ws := t.TempDir()
	// Gate Exit until the runtime Recv loop has demonstrably consumed the TextDelta.
	// Without this, process exit can race ahead of commit and look like a valid pre-output failure.
	gate := filepath.Join(ws, "post-output-gate")
	script := fakebridge.DefaultScript()
	script.OnMethod = map[string][]fakebridge.Action{
		protocol.MethodAgentSend: {
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-post"}`)},
			{Type: fakebridge.ActionEvent, RunID: "run-post", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"partial"}`)},
			{Type: fakebridge.ActionWaitForFile, Path: gate, Ms: 60000},
			{Type: fakebridge.ActionExit, Code: 11},
		},
	}
	sdk := buildRuntimeCursorSDK(t, script, ws)
	t.Cleanup(func() {
		// Release the gate even if an earlier assertion fails so WaitForFile cannot hang the bridge.
		_ = os.WriteFile(gate, []byte("release"), 0o644)
		_ = sdk.Close()
	})

	var opens atomic.Int32
	wrapped := sdk
	orig := wrapped.Open
	wrapped.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		opens.Add(1)
		return orig(ctx, call, cand)
	}
	failover := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			t.Fatal("post-output failure must not failover")
			return nil, errors.New("unreachable")
		},
	}

	ex := runtime.TestExecutor()
	ex.Store = parallelStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(5)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"cursor-sdk": wrapped,
		"failover":   failover,
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	stream, err := ex.Execute(context.Background(), policyCall("cursor-sdk:gpt-5.3-codex|failover:model", "post-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var sawText, sawErr bool
	var gateWritten bool
	for {
		ev, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = true
			if lipapi.IsRecoverablePreOutput(rerr) {
				t.Fatalf("post-output crash must not be recoverable pre-output: %v", rerr)
			}
			break
		}
		if ev.Kind == lipapi.EventTextDelta && ev.Delta != "" {
			sawText = true
			if !gateWritten {
				if werr := os.WriteFile(gate, []byte("release"), 0o644); werr != nil {
					t.Fatalf("write post-output gate: %v", werr)
				}
				gateWritten = true
			}
		}
	}
	_ = stream.Close()
	if !sawText {
		t.Fatal("expected committed text before crash")
	}
	if !sawErr {
		t.Fatal("expected post-output stream error on committed B-leg")
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1 (no retry after output)", opens.Load())
	}
}

func TestCursorSDK_parallelRaceLoserCancel_holdUntilCancel(t *testing.T) {
	ws := t.TempDir()
	active := filepath.Join(ws, "slow-active")
	slowScript := fakebridge.DefaultScript()
	slowScript.OnMethod = map[string][]fakebridge.Action{
		protocol.MethodAgentSend: {
			// Notify before Respond so Open cannot race ahead of the hold barrier.
			{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-slow", Path: active},
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-slow"}`)},
		},
	}
	slow := buildRuntimeCursorSDK(t, slowScript, ws)
	t.Cleanup(func() { _ = slow.Close() })

	// Gate fast-lane eligibility until the slow SDK Open returns. HoldUntilCancel
	// writes active before Respond, so Open completion means the hold barrier is up.
	// Without this, an immediately-ready fast backend can win and cancel slow before
	// AgentSend reaches the hold — waitForPath after Execute cannot enforce that order.
	slowReady := make(chan struct{})
	var signalSlowReady sync.Once
	var cancels atomic.Int32
	wrappedSlow := slow
	orig := wrappedSlow.Open
	wrappedSlow.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		stream, err := orig(ctx, call, cand)
		if err != nil {
			return nil, err
		}
		signalSlowReady.Do(func() { close(slowReady) })
		return &cancelCountingStream{inner: stream, cancels: &cancels}, nil
	}

	ex := runtime.TestExecutor()
	ex.Store = parallelStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"slow-sdk": wrappedSlow,
		"fast": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return &parallelRaceCleanupStream{
					waitReady: slowReady,
					events:    completionEvents("fast-wins"),
				}, nil
			},
		},
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := ex.Execute(ctx, parallelCall("slow-sdk:gpt-5.3-codex!fast:model"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	select {
	case <-slowReady:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for slow hold barrier: %v", ctx.Err())
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("slow-active missing after hold barrier: %v", err)
	}
	col, err := lipapi.Collect(ctx, stream)
	_ = stream.Close()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if col.Text.String() != "fast-wins" {
		t.Fatalf("winner=%q want fast-wins", col.Text.String())
	}
	if cancels.Load() < 1 {
		t.Fatalf("loser cancels=%d want >=1", cancels.Load())
	}
	// Direct HistoryMarker proof lives in cursorsdk.TestParallelRace_LoserCancelClearsHistoryMarker_NoCommitRetained.
}
