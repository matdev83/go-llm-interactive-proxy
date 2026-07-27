package product

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureHandler struct {
	mu       sync.Mutex
	records  []slog.Record
	attrs    [][]slog.Attr
	onHandle func()
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	if h.onHandle != nil {
		h.onHandle()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	h.records = append(h.records, r.Clone())
	h.attrs = append(h.attrs, attrs)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() (msgs []string, attrs []map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, r := range h.records {
		msgs = append(msgs, r.Message)
		m := map[string]string{}
		for _, a := range h.attrs[i] {
			m[a.Key] = a.Value.String()
		}
		attrs = append(attrs, m)
	}
	return msgs, attrs
}

func filterEvent(attrs []map[string]string, event string) []map[string]string {
	var out []map[string]string
	for _, m := range attrs {
		if m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

func TestDiag_StatusSnapshotStableFieldsOnly(t *testing.T) {
	t.Parallel()
	d := NewDiag(nil, "inst-1")
	snap := d.Status(StatusInput{
		Info: BridgeInfo{
			SchemaVersion: 1,
			ImplVersion:   "0.1.0",
			SDKVersion:    "1.0.23",
			NodeVersion:   "22.13.0",
		},
		RuntimeState:   "ready",
		DiscoveryState: "ok",
		DiscoveryCode:  "",
		AgentCount:     2,
		BusyRunCount:   1,
	})
	require.Equal(t, ID, snap.BackendKind)
	require.Equal(t, "inst-1", snap.BackendInstance)
	require.Equal(t, 1, snap.BridgeProtocolVersion)
	require.Equal(t, "0.1.0", snap.BridgePackageVersion)
	require.Equal(t, "1.0.23", snap.SDKVersion)
	require.Equal(t, "22.13.0", snap.NodeVersion)
	require.Equal(t, "ready", snap.RuntimeState)
	require.Equal(t, "ok", snap.DiscoveryState)
	require.Equal(t, 2, snap.AgentCount)
	require.Equal(t, 1, snap.BusyRunCount)

	raw := snap.String()
	for _, bad := range []string{"api_key", "prompt", "agent-", "run-", "/home/", "crsr_", "tool_result"} {
		assert.NotContains(t, strings.ToLower(raw), bad)
	}
}

func TestDiag_LogRedactsSecretsPathsSDKIDsAndPayloads(t *testing.T) {
	t.Parallel()
	h := &captureHandler{}
	log := slog.New(h)
	d := NewDiag(log, "i1")
	ctx := WithCallDiag(context.Background(), "tr-1", "a-1")

	d.LogPool(ctx, "create", "", 1, 0, DiagCorr{CallID: "c1", BLegID: "b1"})
	d.LogRun(ctx, "error", "pre_output", CodeBridgeExited, "provider", DiagCorr{CallID: "c1"})
	d.LogBridge(ctx, "ready", 3, "", DiagCorr{})
	d.LogDiscovery(ctx, "failed", string(CodeInventoryUnavailable), DiagCorr{})
	d.LogShutdown(ctx, 12*time.Millisecond, "ok", DiagCorr{})

	d.emit(
		ctx, slog.LevelInfo, "cursorsdk: smuggle", DiagCorr{},
		slog.String("api_key", "secret-key-value"),
		slog.String("prompt", "user said hello"),
		slog.String("agent_id", "agent-99"),
		slog.String("run_id", "run-99"),
		slog.String("path", "/home/user/secret-project"),
		slog.String("outcome", "reuse"),
	)

	msgs, attrMaps := h.snapshot()
	require.NotEmpty(t, msgs)
	var joinedBuilder strings.Builder
	joinedBuilder.WriteString(strings.Join(msgs, "\n"))
	for _, m := range attrMaps {
		for k, v := range m {
			joinedBuilder.WriteString("\n")
			joinedBuilder.WriteString(k)
			joinedBuilder.WriteString("=")
			joinedBuilder.WriteString(v)
		}
	}
	joined := joinedBuilder.String()
	for _, bad := range []string{
		"secret-key-value", "user said hello", "agent-99", "run-99",
		"/home/user/secret-project", "api_key=", "prompt=",
		"tr-1", "a-1", "c1", "b1", "i1",
	} {
		assert.NotContains(t, joined, bad, "leaked %q", bad)
	}
	require.Equal(t, "reuse", attrMaps[len(attrMaps)-1]["outcome"])
	require.Equal(t, fingerprintDiagID("tr-1"), attrMaps[0]["trace_id"])
	require.Equal(t, fingerprintDiagID("a-1"), attrMaps[0]["a_leg_id"])
	require.Equal(t, fingerprintDiagID("c1"), attrMaps[0]["call_id"])
	require.Equal(t, fingerprintDiagID("b1"), attrMaps[0]["b_leg_id"])
	require.Equal(t, fingerprintDiagID("i1"), attrMaps[0]["backend_instance"])
}

func TestDiag_NilLoggerNoPanic(t *testing.T) {
	t.Parallel()
	d := NewDiag(nil, "")
	require.NotPanics(t, func() {
		d.LogPool(context.Background(), "evict", InvalidateEvict, 0, 0, DiagCorr{})
		d.LogRun(context.Background(), "error", "post_output", CodeRunFailed, "transport", DiagCorr{})
		_ = d.Status(StatusInput{})
	})
}

func TestDiag_AllowedKeysOnly(t *testing.T) {
	t.Parallel()
	for _, k := range []string{
		"backend_kind", "backend_instance", "event", "outcome", "cause",
		"failure_code", "failure_phase", "cancel_mode", "runtime_state",
		"discovery_state", "discovery_code", "agent_count", "busy_run_count",
		"bridge_generation", "duration_ms", "trace_id", "a_leg_id", "b_leg_id", "call_id",
	} {
		require.True(t, diagAttrAllowed(k), k)
	}
	for _, k := range []string{"api_key", "prompt", "agent_id", "run_id", "path", "error", "stderr"} {
		require.False(t, diagAttrAllowed(k), k)
	}
}

func TestDiag_RuntimeEmitsPoolCreateWithoutSDKIDs(t *testing.T) {
	h := &captureHandler{}
	exe := buildFakeBridgeExe(t)
	cfg := openTestConfig(t, exe, t.TempDir())
	cfg.SandboxMode = SandboxOff
	rt := newBackendRuntime(cfg, runtimeOpts{
		HostEnv: openTestHostEnv(),
		Log:     slog.New(h),
	})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	stream, err := rt.Open(ctx, textCall("gpt-5.3-codex"), AttemptCandidate{
		Primary: Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_ = drainManaged(ctx, t, stream)

	status := rt.Status()
	require.Equal(t, ID, status.BackendKind)
	require.Equal(t, "ready", status.RuntimeState)
	require.NotEmpty(t, status.SDKVersion)

	_, attrMaps := h.snapshot()
	foundPool := false
	for _, m := range attrMaps {
		if m["event"] == "pool" && m["outcome"] == "create" {
			foundPool = true
		}
		for k, v := range m {
			assert.True(t, diagAttrAllowed(k), "disallowed key %q", k)
			assert.NotContains(t, v, "agent-")
			assert.NotContains(t, v, "run-")
			assert.NotContains(t, v, cfg.APIKey)
		}
	}
	require.True(t, foundPool, "expected pool create diagnostic")
}

func TestSecret_DiagNeverLogsAPIKeyFromConfig(t *testing.T) {
	t.Parallel()
	h := &captureHandler{}
	d := NewDiag(slog.New(h), "x")
	secret := "super-secret-cursor-key"
	pathLeak := "/home/user/secret-project"
	promptLeak := "USER_PROMPT_LEAK_XYZ"
	fault := NewBridgeFault(CodeAuthFailed, errors.New("unauthorized"), "stderr "+pathLeak+" key="+secret+" prompt="+promptLeak)
	cf := ClassifyFailure(fault, false, secret)
	require.NotNil(t, cf)
	d.LogBridge(context.Background(), "failed", 1, string(cf.Code), DiagCorr{})
	d.LogPoolClassified(context.Background(), "create_failed", InvalidateCreateFail, cf.Code, string(cf.Phase), 0, 0, DiagCorr{})
	d.LogRun(context.Background(), "error", string(cf.Phase), cf.Code, "", DiagCorr{})
	_, attrs := h.snapshot()
	var joinedBuilder strings.Builder
	for _, m := range attrs {
		for _, v := range m {
			joinedBuilder.WriteString(v)
			joinedBuilder.WriteByte('\n')
			assert.NotContains(t, v, secret)
			assert.NotContains(t, v, pathLeak)
			assert.NotContains(t, v, promptLeak)
		}
	}
	joined := joinedBuilder.String()
	assert.Contains(t, joined, string(CodeAuthFailed))
}

func TestDiag_RunOutcomeEmittedOnceTable(t *testing.T) {
	t.Parallel()
	type want struct {
		outcome    string
		phase      string
		code       string
		cancelMode string
	}
	cases := []struct {
		name string
		run  func(t *testing.T, h *captureHandler, d *Diag) want
	}{
		{
			name: "success",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(4)
				owner := &recordingLeaseOwner{}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-ok"}, owner, RunStreamOpts{Diag: d})
				defer func() { _ = s.Close() }()
				bridge.push(eventFrame("run-ok", 1, protocol.KindFinished, `{"status":"finished"}`))
				_ = drainStream(t, s)
				return want{outcome: "success"}
			},
		},
		{
			name: "sdk_error_frame",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(4)
				owner := &recordingLeaseOwner{}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-err"}, owner, RunStreamOpts{Diag: d, APIKey: "k"})
				defer func() { _ = s.Close() }()
				bridge.push(eventFrame("run-err", 1, protocol.KindError, `{"code":"cursor_sdk_auth_failed","message":"unauthorized"}`))
				_ = drainStream(t, s)
				return want{outcome: "error", phase: "post_output", code: string(CodeAuthFailed)}
			},
		},
		{
			name: "protocol_mapping_failure",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(4)
				owner := &recordingLeaseOwner{}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-oo"}, owner, RunStreamOpts{Diag: d})
				defer func() { _ = s.Close() }()
				bridge.push(eventFrame("run-oo", 2, protocol.KindTextDelta, `{"text":"x"}`))
				_, err := s.Recv(context.Background())
				require.Error(t, err)
				return want{outcome: "error", phase: "pre_output", code: string(CodeBridgeProtocol)}
			},
		},
		{
			name: "bridge_channel_death",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(2)
				owner := &recordingLeaseOwner{}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-dead"}, owner, RunStreamOpts{Diag: d})
				defer func() { _ = s.Close() }()
				bridge.closeFeed()
				_, err := s.Recv(context.Background())
				require.Error(t, err)
				return want{outcome: "error", phase: "pre_output", code: string(CodeBridgeExited)}
			},
		},
		{
			name: "provider_cancel_once",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(4)
				owner := &recordingLeaseOwner{}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-c"}, owner, RunStreamOpts{
					Diag: d, CancelTimeout: time.Second,
				})
				_ = s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
				bridge.push(eventFrame("run-c", 1, protocol.KindFinished, `{"status":"canceled"}`))
				_, _ = s.Recv(context.Background())
				_ = s.Close()
				return want{outcome: "cancel", cancelMode: string(lipapi.CancelModeProvider)}
			},
		},
		{
			name: "cancel_timeout",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(2)
				bridge.cancelBlock = 200 * time.Millisecond
				owner := &recordingLeaseOwner{}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-to"}, owner, RunStreamOpts{
					Diag: d, CancelTimeout: 20 * time.Millisecond,
				})
				_ = s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
				_ = s.Close()
				return want{outcome: "cancel", cancelMode: string(lipapi.CancelModeTransport), code: string(CodeCancelTimeout)}
			},
		},
		{
			name: "release_ready_failure_classified",
			run: func(t *testing.T, h *captureHandler, d *Diag) want {
				t.Helper()
				bridge := newScriptedRunBridge(4)
				owner := &recordingLeaseOwner{releaseEr: ErrCommitRequired}
				s := NewRunStream(context.Background(), bridge, &AgentLease{RunID: "run-commit"}, owner, RunStreamOpts{Diag: d})
				defer func() { _ = s.Close() }()
				bridge.push(eventFrame("run-commit", 1, protocol.KindFinished, `{"status":"finished"}`))
				_ = drainStream(t, s)
				cf := ClassifyFailure(ErrCommitRequired, false, "")
				require.NotNil(t, cf)
				return want{outcome: "error", phase: string(cf.Phase), code: string(cf.Code)}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &captureHandler{}
			d := NewDiag(slog.New(h), "inst")
			want := tc.run(t, h, d)
			_, attrs := h.snapshot()
			runs := filterEvent(attrs, DiagEventRun)
			require.Len(t, runs, 1, "exactly one run outcome: %#v", runs)
			assert.Equal(t, want.outcome, runs[0]["outcome"])
			if want.phase != "" {
				assert.Equal(t, want.phase, runs[0]["failure_phase"])
			}
			if want.code != "" {
				assert.Equal(t, want.code, runs[0]["failure_code"])
			}
			if want.cancelMode != "" {
				assert.Equal(t, want.cancelMode, runs[0]["cancel_mode"])
			}
		})
	}
}

func TestDiag_CreateAndSendFailuresEmitClassified(t *testing.T) {
	t.Parallel()
	secret := "super-secret-api-key-value"
	promptLeak := "USER_PROMPT_LEAK_XYZ"
	pathLeak := "/home/user/secret-project"
	stderrLeak := "stderr poison " + pathLeak + " key=" + secret

	t.Run("create_failed", func(t *testing.T) {
		h := &captureHandler{}
		d := NewDiag(slog.New(h), "pool-inst")
		bridge := newFakeAgentBridge(1)
		bridge.createErr = NewBridgeFault(CodeAuthFailed, errors.New("unauthorized"), stderrLeak)
		pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{Diag: d})
		defer func() { _ = pool.Close(context.Background()) }()
		key := testAgentKey("sess-create-fail")
		params := createParamsFor(key)
		params.APIKey = secret
		_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
			Key: key, Create: params, View: view(1, "h1", "", "t1"),
			FullPrompt: promptLeak, SuffixPrompt: "S",
		})
		require.Error(t, err)

		_, attrs := h.snapshot()
		pools := filterEvent(attrs, DiagEventPool)
		var createFail map[string]string
		for _, m := range pools {
			if m["outcome"] == "create_failed" {
				createFail = m
				break
			}
		}
		require.NotEmpty(t, createFail)
		assert.Equal(t, string(InvalidateCreateFail), createFail["cause"])
		assert.Equal(t, string(CodeAuthFailed), createFail["failure_code"])
		assert.Equal(t, "pre_output", createFail["failure_phase"])

		runs := filterEvent(attrs, DiagEventRun)
		require.NotEmpty(t, runs)
		assert.Equal(t, "error", runs[0]["outcome"])
		assert.Equal(t, string(CodeAuthFailed), runs[0]["failure_code"])
		assert.Equal(t, "pre_output", runs[0]["failure_phase"])

		var joinedBuilder strings.Builder
		for _, m := range attrs {
			for k, v := range m {
				joinedBuilder.WriteString(k)
				joinedBuilder.WriteByte('=')
				joinedBuilder.WriteString(v)
				joinedBuilder.WriteByte('\n')
			}
		}
		joined := joinedBuilder.String()
		for _, bad := range []string{secret, promptLeak, pathLeak, stderrLeak, "agent-", "run-"} {
			assert.NotContains(t, joined, bad)
		}
	})

	t.Run("send_failed", func(t *testing.T) {
		h := &captureHandler{}
		d := NewDiag(slog.New(h), "pool-inst")
		bridge := newFakeAgentBridge(1)
		bridge.sendErr = NewBridgeFault(CodeBridgeExited, ErrBridgeExited, stderrLeak+"; prompt="+promptLeak)
		pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{Diag: d})
		defer func() { _ = pool.Close(context.Background()) }()
		key := testAgentKey("sess-send-fail")
		params := createParamsFor(key)
		params.APIKey = secret
		_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
			Key: key, Create: params, View: view(1, "h1", "", "t1"),
			FullPrompt: promptLeak, SuffixPrompt: "S",
		})
		require.Error(t, err)

		_, attrs := h.snapshot()
		var sendFail map[string]string
		for _, m := range filterEvent(attrs, DiagEventPool) {
			if m["outcome"] == "send_failed" {
				sendFail = m
				break
			}
		}
		require.NotEmpty(t, sendFail)
		assert.Equal(t, string(InvalidateSendFail), sendFail["cause"])
		assert.Equal(t, string(CodeBridgeExited), sendFail["failure_code"])
		assert.Equal(t, "pre_output", sendFail["failure_phase"])

		runs := filterEvent(attrs, DiagEventRun)
		require.NotEmpty(t, runs)
		assert.Equal(t, "error", runs[0]["outcome"])
		assert.Equal(t, string(CodeBridgeExited), runs[0]["failure_code"])

		var joinedBuilder strings.Builder
		for _, m := range attrs {
			for _, v := range m {
				joinedBuilder.WriteString(v)
				joinedBuilder.WriteByte('\n')
			}
		}
		joined := joinedBuilder.String()
		for _, bad := range []string{secret, promptLeak, pathLeak, "agent-", "run-"} {
			assert.NotContains(t, joined, bad)
		}
	})
}

func TestDiag_NoSlogUnderMutexReentrantHandler(t *testing.T) {
	h := &captureHandler{}
	d := NewDiag(slog.New(h), "deadlock-inst")
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{Diag: d})
	defer func() { _ = pool.Close(context.Background()) }()

	h.onHandle = func() {
		_ = pool.LiveCount()
		_ = pool.BusyCount()
		_ = d.Status(StatusInput{
			RuntimeState: "ready",
			AgentCount:   pool.LiveCount(),
			BusyRunCount: pool.BusyCount(),
		})
	}

	key := testAgentKey("sess-deadlock")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "FULL", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	pool.CommitSend(lease)

	runBridge := newScriptedRunBridge(4)
	lease.RunID = "run-deadlock"
	s := NewRunStream(context.Background(), runBridge, lease, pool, RunStreamOpts{Diag: d, CancelTimeout: time.Second})

	done := make(chan struct{})
	go func() {
		defer close(done)
		runBridge.push(eventFrame("run-deadlock", 1, protocol.KindFinished, `{"status":"finished"}`))
		_ = drainStream(t, s)
		_ = s.Close()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: slog handler re-entered Status/LiveCount while mutex held")
	}

	bridge2 := newFakeAgentBridge(1)
	bridge2.createErr = NewBridgeFault(CodeAuthFailed, errors.New("unauthorized"), "")
	pool2 := NewSessionPool(poolTestConfig(), bridge2, SessionPoolOpts{Diag: d})
	defer func() { _ = pool2.Close(context.Background()) }()
	h.onHandle = func() {
		_ = pool2.LiveCount()
		_ = pool2.BusyCount()
	}
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		key2 := testAgentKey("sess-deadlock-create")
		_, _ = pool2.PrepareSend(context.Background(), PrepareSendInput{
			Key: key2, Create: createParamsFor(key2), View: view(1, "h1", "", "t1"),
			FullPrompt: "FULL", SuffixPrompt: "S",
		})
	}()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock on create_failed log path")
	}
}

func TestDiag_RejectsNonAllowlistedValues(t *testing.T) {
	t.Parallel()
	h := &captureHandler{}
	d := NewDiag(slog.New(h), "v")
	d.LogPool(context.Background(), "create_failed\n/home/evil", InvalidateCreateFail, 1, 0, DiagCorr{})
	d.LogRun(context.Background(), "error;prompt=hi", "pre_output", FailureCode("not_a_real_code"), "evil-mode", DiagCorr{})
	_, attrs := h.snapshot()
	for _, m := range attrs {
		if m["event"] == DiagEventPool {
			assert.NotEqual(t, "create_failed\n/home/evil", m["outcome"])
			assert.NotContains(t, m["outcome"], "/home/")
		}
		if m["event"] == DiagEventRun {
			assert.NotContains(t, m["outcome"], "prompt=")
			assert.NotEqual(t, "not_a_real_code", m["failure_code"])
			assert.NotEqual(t, "evil-mode", m["cancel_mode"])
		}
	}
}

func TestDiag_StatusBestEffortTornSnapshotDocumented(t *testing.T) {
	t.Parallel()
	// Compiles/runs Status under concurrent mutation; godoc documents best-effort torn reads.
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	d := NewDiag(nil, "s")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 50 {
			key := testAgentKey("sess-status")
			lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
				Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
				FullPrompt: "F", SuffixPrompt: "S",
			})
			if err != nil {
				continue
			}
			pool.CommitSend(lease)
			_ = pool.ReleaseReady(lease)
			pool.InvalidateKey(key, InvalidateCancel)
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			snap := d.Status(StatusInput{
				RuntimeState: "ready",
				AgentCount:   pool.LiveCount(),
				BusyRunCount: pool.BusyCount(),
			})
			assert.Equal(t, ID, snap.BackendKind)
			assert.NotContains(t, snap.String(), "api_key")
		}
	}()
	wg.Wait()
}
