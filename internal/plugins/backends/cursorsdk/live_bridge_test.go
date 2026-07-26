package cursorsdk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildLiveBridgeCommand_DirectNodeAndBridgeBin(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "bridge", "bin", "lip-cursor-sdk-bridge.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(bin), 0o755))
	require.NoError(t, os.WriteFile(bin, []byte("#!/usr/bin/env node\n"), 0o644))

	argv, err := buildLiveBridgeCommand(liveBridgeCommandOpts{
		LookPath: func(name string) (string, error) {
			assert.Equal(t, "node", name)
			return filepath.Join(root, "node-stub"), nil
		},
		BridgeRoot: root,
	})
	require.NoError(t, err)
	require.Len(t, argv, 2)
	assert.Equal(t, filepath.Join(root, "node-stub"), argv[0])
	assert.Equal(t, bin, argv[1])
	for _, a := range argv {
		assert.NotContains(t, strings.ToLower(a), "npm")
		assert.NotContains(t, strings.ToLower(a), "npx")
		assert.NotContains(t, a, "CURSOR_API_KEY")
	}
}

func TestBuildLiveBridgeCommand_RejectsMissingBridgeBin(t *testing.T) {
	t.Parallel()
	_, err := buildLiveBridgeCommand(liveBridgeCommandOpts{
		LookPath:   func(string) (string, error) { return "/usr/bin/node", nil },
		BridgeRoot: t.TempDir(),
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "CURSOR_API_KEY")
}

func TestLiveBridgeHarnessLifecyclePrompts_TextOnlyDeny(t *testing.T) {
	t.Parallel()
	prompts := newLiveBridgeLifecyclePrompts("TOKEN")
	require.Len(t, prompts.all(), 7)
	for _, prompt := range prompts.all() {
		assert.True(t, liveBridgePromptHasTextOnlyDeny(prompt), "prompt missing text-only deny semantics")
		assert.False(t, liveBridgePromptIsVague(prompt), "prompt still vague")
		assert.Contains(t, prompt, "exactly the token")
	}
	assert.Contains(t, prompts.RebootstrapFirst, "TOKEN first")
	assert.Contains(t, prompts.RebootstrapRebuilt, "TOKEN rebuilt")

	call := liveBridgeTextCall("m", "c1", "s1", prompts.Stream)
	assert.Equal(t, prompts.Stream, liveBridgeCallUserText(call))
	assert.True(t, liveBridgePromptHasTextOnlyDeny(liveBridgeCallUserText(call)))
}

func TestLiveBridgeHostEnv_StripsCredentials(t *testing.T) {
	t.Parallel()
	env := liveBridgeHostEnv([]string{
		"PATH=/bin",
		"CURSOR_API_KEY=crsr_secret_value",
		"OPENAI_API_KEY=sk-other",
		"HOME=/tmp/home",
		"SECRET_TOKEN=nope",
	})
	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "PATH=")
	assert.NotContains(t, joined, "CURSOR_API_KEY")
	assert.NotContains(t, joined, "crsr_secret_value")
	assert.NotContains(t, joined, "OPENAI_API_KEY")
	assert.NotContains(t, joined, "SECRET_TOKEN")
}

func TestSanitizeLiveBridgeText_RedactsSecretsPathsIDs(t *testing.T) {
	t.Parallel()
	in := `fail agentId=bc-abcdef012345 runId=run-xyz apiKey=crsr_leak path=C:\Users\secret-user\ws sk-abcdef`
	out := sanitizeLiveBridgeText(in)
	assert.NotContains(t, out, "crsr_leak")
	assert.NotContains(t, out, "sk-abcdef")
	assert.NotContains(t, out, "secret-user")
	assert.NotContains(t, out, "bc-abcdef012345")
	assert.NotContains(t, out, "run-xyz")
	assert.NotContains(t, out, `C:\Users`)
}

func TestVerifyLiveBridgePinnedSDK_RequiresExactVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess")
	}
	exe := buildFakeBridgeExe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, verifyLiveBridgePinnedSDK(ctx, []string{exe}))
}

func TestRunLiveBridgeHarness_BlockedWithoutOptIn(t *testing.T) {
	t.Parallel()
	summary, err := runLiveBridgeHarness(context.Background(), liveBridgeInput{
		Getenv: func(string) string { return "" },
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, "blocked", summary.Status)
	assert.False(t, summary.OK)
	assert.Equal(t, "live-bridge", summary.Lane)
	raw, err := json.Marshal(summary)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "CURSOR_API_KEY")
}

func TestRunLiveBridgeHarness_FakeOpenRunStreamLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess lifecycle")
	}
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	createCount := filepath.Join(ws, "creates.txt")
	promptCapture := filepath.Join(ws, "prompt.txt")
	peerActiveA := filepath.Join(ws, "peer-a-active")
	peerActiveB := filepath.Join(ws, "peer-b-active")
	token := "LIVE_BRIDGE_FULL_PROMPT_TOKEN"

	autoRespond := json.RawMessage(`{"runId":"$auto"}`)
	finish := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"ok"}`)},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	holdCancel := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID},
	}
	holdPeerA := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: peerActiveA},
	}
	holdPeerB := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: peerActiveB},
	}

	script := fakebridge.DefaultScript()
	script.CreateCountFile = createCount
	script.PromptCaptureFile = promptCapture
	script.OnAgentSend = [][]fakebridge.Action{
		finish,
		holdCancel,
		holdPeerA,
		holdPeerB,
	}
	rawScript, err := json.Marshal(script)
	require.NoError(t, err)

	starter := &platformStarter{scriptJSON: string(rawScript)}
	var recordedCmd []string
	var recordedEnv []string
	wrapping := processStarterFunc(func(cmd []string, cwd string, env []string) (Process, error) {
		recordedCmd = append([]string(nil), cmd...)
		recordedEnv = append([]string(nil), env...)
		return starter.Start(cmd, cwd, env)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	secret := "crsr_harness_unit_secret"
	summary, err := runLiveBridgeHarness(ctx, liveBridgeInput{
		Getenv: func(k string) string {
			switch k {
			case "CURSOR_SDK_LIVE":
				return "1"
			case "CURSOR_API_KEY":
				return secret
			default:
				return ""
			}
		},
		BridgeArgv:         []string{exe},
		Workspace:          ws,
		HostEnv:            []string{"PATH=" + os.Getenv("PATH"), "CURSOR_API_KEY=" + secret, "HOME=" + ws},
		Starter:            wrapping,
		APIKey:             secret,
		Timeout:            55 * time.Second,
		CreateCountPath:    createCount,
		PromptCapturePath:  promptCapture,
		FullPromptToken:    token,
		PeerActiveNotify:   []string{peerActiveA, peerActiveB},
		ActiveProofTimeout: 15 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, "live-bridge", summary.Lane)
	assert.Equal(t, runtime.GOOS, summary.GOOS)

	byName := map[string]liveBridgeScenario{}
	for _, sc := range summary.Scenarios {
		byName[sc.Name] = sc
	}
	for _, name := range []string{
		"sdk_pin", "discovery", "stream", "cancellation", "hard_bridge_restart",
		"canonical_rebootstrap", "configured_mcp", "workspace_safety_required", "shutdown",
	} {
		require.Contains(t, byName, name, "missing scenario %s", name)
	}
	assert.Equal(t, "passed", byName["sdk_pin"].Status)
	assert.Equal(t, "passed", byName["discovery"].Status)
	assert.Equal(t, "passed", byName["stream"].Status)
	assert.Equal(t, "passed", byName["cancellation"].Status)
	assert.Equal(t, "passed", byName["hard_bridge_restart"].Status)
	assert.Equal(t, "passed", byName["canonical_rebootstrap"].Status)
	assert.Equal(t, "passed", byName["shutdown"].Status)
	assert.Equal(t, "blocked", byName["configured_mcp"].Status)
	assert.Equal(t, "blocked", byName["workspace_safety_required"].Status)
	assert.Equal(t, "blocked", summary.Status)
	assert.False(t, summary.OK)

	assert.Equal(t, []string{exe}, recordedCmd)
	joinedEnv := strings.Join(recordedEnv, "\n")
	assert.NotContains(t, joinedEnv, secret)
	assert.NotContains(t, joinedEnv, "CURSOR_API_KEY")

	out, err := json.Marshal(summary)
	require.NoError(t, err)
	text := string(out)
	assert.NotContains(t, text, secret)
	assert.NotContains(t, text, exe)
	assert.NotContains(t, text, ws)
	assert.NotContains(t, text, "apiKey")
	assert.NotContains(t, text, token)
	assert.NotContains(t, text, liveBridgeTextOnlyPrefix)
	assert.NotContains(t, text, "Do not use tools")
}

type quietDeadlineSpyStream struct {
	parent context.Context
	short  bool
}

func (s *quietDeadlineSpyStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if dl, ok := ctx.Deadline(); ok {
		pdl, pok := s.parent.Deadline()
		if !pok || dl.Before(pdl.Add(-500*time.Millisecond)) {
			s.short = true
		}
	}
	return lipapi.Event{}, io.EOF
}
func (s *quietDeadlineSpyStream) Close() error { return nil }
func (s *quietDeadlineSpyStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func TestQuietCancelWindow_DoesNotUseShortRecvDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	spy := &quietDeadlineSpyStream{parent: parent}
	ok, detail := quietCancelWindow(parent, spy)
	require.True(t, ok, detail)
	assert.False(t, spy.short, "quietCancelWindow must not Recv with a short child deadline (RunStream.Recv closes on timeout)")
}

func TestRunLiveBridgeHarness_RebootstrapBlockedWithoutInstrumentation(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess lifecycle")
	}
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	peerActiveA := filepath.Join(ws, "peer-a-active")
	peerActiveB := filepath.Join(ws, "peer-b-active")
	autoRespond := json.RawMessage(`{"runId":"$auto"}`)
	finish := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"ok"}`)},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	holdCancel := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID},
	}
	holdPeerA := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: peerActiveA},
	}
	holdPeerB := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: peerActiveB},
	}
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{finish, holdCancel, holdPeerA, holdPeerB}
	rawScript, err := json.Marshal(script)
	require.NoError(t, err)
	starter := &platformStarter{scriptJSON: string(rawScript)}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	summary, err := runLiveBridgeHarness(ctx, liveBridgeInput{
		Getenv: func(k string) string {
			if k == "CURSOR_SDK_LIVE" {
				return "1"
			}
			if k == "CURSOR_API_KEY" {
				return "crsr_block_rb_key"
			}
			return ""
		},
		BridgeArgv:         []string{exe},
		Workspace:          ws,
		HostEnv:            platformSmokeHostEnv(),
		Starter:            starter,
		APIKey:             "crsr_block_rb_key",
		Timeout:            55 * time.Second,
		PeerActiveNotify:   []string{peerActiveA, peerActiveB},
		ActiveProofTimeout: 15 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	byName := map[string]liveBridgeScenario{}
	for _, sc := range summary.Scenarios {
		byName[sc.Name] = sc
	}
	require.Equal(t, "blocked", byName["canonical_rebootstrap"].Status)
	assert.Equal(t, "blocked", summary.Status)
	raw, _ := json.Marshal(summary)
	assert.NotContains(t, string(raw), "crsr_block_rb_key")
}

func TestRunLiveBridgeHarness_TimeoutHonorsContext(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess lifecycle")
	}
	exe := buildFakeBridgeExe(t)
	script := fakebridge.DefaultScript()
	script.OnStartup = []fakebridge.Action{{Type: fakebridge.ActionSleep, Ms: 5000}}
	rawScript, err := json.Marshal(script)
	require.NoError(t, err)
	starter := &platformStarter{scriptJSON: string(rawScript)}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	summary, err := runLiveBridgeHarness(ctx, liveBridgeInput{
		Getenv: func(k string) string {
			if k == "CURSOR_SDK_LIVE" {
				return "1"
			}
			if k == "CURSOR_API_KEY" {
				return "crsr_timeout_key"
			}
			return ""
		},
		BridgeArgv: []string{exe},
		Workspace:  t.TempDir(),
		HostEnv:    platformSmokeHostEnv(),
		Starter:    starter,
		APIKey:     "crsr_timeout_key",
		Timeout:    150 * time.Millisecond,
	})
	require.Error(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, "failed", summary.Status)
	assert.False(t, summary.OK)
	raw, _ := json.Marshal(summary)
	assert.NotContains(t, string(raw), "crsr_timeout_key")
	assert.NotContains(t, err.Error(), "crsr_timeout_key")
}

func TestClassifyLiveBridgePeerErr_BridgeRunEndedIsProof(t *testing.T) {
	t.Parallel()
	pre := ClassifyAndMap(errBridgeRunEnded, false, "")
	require.Error(t, pre)
	assert.Contains(t, pre.Error(), "bridge run ended before terminal")
	class, detail := classifyLiveBridgePeerErr(pre)
	assert.Equal(t, liveBridgePeerProof, class, detail)
	assert.True(t, errors.Is(pre, ErrBridgeExited))

	post := ClassifyAndMap(errBridgeRunEnded, true, "")
	require.Error(t, post)
	assert.Equal(t, "cursor_sdk_bridge_exited: bridge run ended before terminal", post.Error())
	class, detail = classifyLiveBridgePeerErr(post)
	assert.Equal(t, liveBridgePeerProof, class, detail)
}

func TestClassifyLiveBridgePeerErr_RejectsLooseStringMatch(t *testing.T) {
	t.Parallel()
	loose := errors.New("note: cursor_sdk_bridge_exited bridge run ended before terminal")
	class, detail := classifyLiveBridgePeerErr(loose)
	assert.Equal(t, liveBridgePeerUnexpected, class, detail)
}

func TestClassifyLiveBridgePeerErr_NaturalEOFBlocked(t *testing.T) {
	t.Parallel()
	class, detail := classifyLiveBridgePeerErr(io.EOF)
	assert.Equal(t, liveBridgePeerNaturalEnd, class, detail)
}

func TestLiveBridgeHardRestartOutcome_PeerProofPassRestartBridgeExitedFail(t *testing.T) {
	t.Parallel()
	peer := ClassifyAndMap(errBridgeRunEnded, true, "")
	require.Error(t, peer)

	status, detail := liveBridgeHardRestartOutcome(peer, peer, nil, true)
	assert.Equal(t, "passed", status, detail)
	assert.NotContains(t, detail, "restart:")

	restartErr := ClassifyAndMap(errBridgeRunEnded, false, "")
	status, detail = liveBridgeHardRestartOutcome(peer, peer, restartErr, true)
	assert.Equal(t, "failed", status, detail)
	assert.Contains(t, detail, "restart:")
	assert.Contains(t, detail, "bridge run ended before terminal")
	detail = appendLiveBridgeRestartDiag(detail, liveBridgeRestartDiag{
		Ready: false, Gen: 2, Gen2Waited: true, StderrClass: liveBridgeStderrClass("x"),
	})
	assert.Contains(t, detail, "ready=false")
	assert.Contains(t, detail, "gen=2")
	assert.Contains(t, detail, "gen2_waited=true")
	assert.Contains(t, detail, "stderr_class=present")
	assert.NotContains(t, detail, "secret")
	assert.NotContains(t, detail, `C:\`)
}

func TestLiveBridgeWaitProc_WaitedIsPerProcess(t *testing.T) {
	t.Parallel()
	a := &liveBridgeWaitProc{}
	b := &liveBridgeWaitProc{}
	assert.False(t, a.Waited())
	assert.False(t, b.Waited())
	a.waited.Store(true)
	assert.True(t, a.Waited())
	assert.False(t, b.Waited())
}

func TestLiveBridgeStderrClass_EmptyVsPresent(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "empty", liveBridgeStderrClass(""))
	assert.Equal(t, "empty", liveBridgeStderrClass("   "))
	assert.Equal(t, "present", liveBridgeStderrClass("x"))
}

func TestLiveBridgeHardRestartOutcome_UnexpectedPeerFails(t *testing.T) {
	t.Parallel()
	peer := ClassifyAndMap(errBridgeRunEnded, true, "")
	require.Error(t, peer)
	status, detail := liveBridgeHardRestartOutcome(peer, errors.New("unexpected peer fault"), nil, true)
	assert.Equal(t, "failed", status, detail)
	assert.Contains(t, detail, "peer B:")
}

func TestLiveBridgeHardRestartOutcome_NaturalPeerEndBlocked(t *testing.T) {
	t.Parallel()
	status, detail := liveBridgeHardRestartOutcome(io.EOF, io.EOF, nil, true)
	assert.Equal(t, "blocked", status, detail)
}

func TestLiveBridgeHardRestartOutcome_MissingKillEvidenceFails(t *testing.T) {
	t.Parallel()
	peer := ClassifyAndMap(errBridgeRunEnded, true, "")
	require.Error(t, peer)
	status, detail := liveBridgeHardRestartOutcome(peer, peer, nil, false)
	assert.Equal(t, "failed", status, detail)
	assert.Contains(t, detail, "kill")
}

func TestRunLiveBridgeHarness_HardRestartNaturalPeerEndBlocked(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess lifecycle")
	}
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	autoRespond := json.RawMessage(`{"runId":"$auto"}`)
	finish := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"ok"}`)},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	hold := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID},
	}
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{finish, hold, finish, finish}
	rawScript, err := json.Marshal(script)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	summary, err := runLiveBridgeHarness(ctx, liveBridgeInput{
		Getenv: func(k string) string {
			switch k {
			case "CURSOR_SDK_LIVE":
				return "1"
			case "CURSOR_API_KEY":
				return "crsr_peer_natural_key"
			default:
				return ""
			}
		},
		BridgeArgv:         []string{exe},
		Workspace:          ws,
		HostEnv:            platformSmokeHostEnv(),
		Starter:            &platformStarter{scriptJSON: string(rawScript)},
		APIKey:             "crsr_peer_natural_key",
		Timeout:            55 * time.Second,
		ActiveProofTimeout: 15 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	byName := map[string]liveBridgeScenario{}
	for _, sc := range summary.Scenarios {
		byName[sc.Name] = sc
	}
	require.Equal(t, "blocked", byName["hard_bridge_restart"].Status, byName["hard_bridge_restart"].Detail)
	assert.NotEqual(t, "passed", byName["hard_bridge_restart"].Status)
	assert.NotEqual(t, "failed", byName["hard_bridge_restart"].Status)
	raw, _ := json.Marshal(summary)
	assert.NotContains(t, string(raw), "crsr_peer_natural_key")
}

func TestRunLiveBridgeHarness_HardRestartPeerContentThenKillPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess lifecycle")
	}
	exe := buildFakeBridgeExe(t)
	ws := t.TempDir()
	peerActiveA := filepath.Join(ws, "peer-a-active")
	peerActiveB := filepath.Join(ws, "peer-b-active")
	autoRespond := json.RawMessage(`{"runId":"$auto"}`)
	finish := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"ok"}`)},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	holdCancel := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID},
	}
	peerWithContent := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"peer"}`)},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: peerActiveA},
	}
	holdPeerB := []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: peerActiveB},
	}
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{finish, holdCancel, peerWithContent, holdPeerB}
	rawScript, err := json.Marshal(script)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	summary, err := runLiveBridgeHarness(ctx, liveBridgeInput{
		Getenv: func(k string) string {
			switch k {
			case "CURSOR_SDK_LIVE":
				return "1"
			case "CURSOR_API_KEY":
				return "crsr_peer_content_key"
			default:
				return ""
			}
		},
		BridgeArgv:         []string{exe},
		Workspace:          ws,
		HostEnv:            platformSmokeHostEnv(),
		Starter:            &platformStarter{scriptJSON: string(rawScript)},
		APIKey:             "crsr_peer_content_key",
		Timeout:            55 * time.Second,
		PeerActiveNotify:   []string{peerActiveA, peerActiveB},
		ActiveProofTimeout: 15 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	byName := map[string]liveBridgeScenario{}
	for _, sc := range summary.Scenarios {
		byName[sc.Name] = sc
	}
	require.Equal(t, "passed", byName["hard_bridge_restart"].Status, byName["hard_bridge_restart"].Detail)
	assert.NotContains(t, byName["hard_bridge_restart"].Detail, "restart:")
	raw, _ := json.Marshal(summary)
	assert.NotContains(t, string(raw), "crsr_peer_content_key")
}
