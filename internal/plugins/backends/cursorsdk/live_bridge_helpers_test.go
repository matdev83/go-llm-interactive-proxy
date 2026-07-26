package cursorsdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type liveBridgePeerClass int

const (
	liveBridgePeerProof liveBridgePeerClass = iota
	liveBridgePeerNaturalEnd
	liveBridgePeerUnexpected
)

const (
	liveBridgeTextOnlyPrefix        = "LIP_LIVE_BRIDGE_TEXT_ONLY:"
	liveBridgeDefaultRebootToken    = "LIVE_BRIDGE_FULL_PROMPT_TOKEN"
	liveBridgeLifecycleTokenStream  = "STREAM_OK"
	liveBridgeLifecycleTokenCancel  = "CANCEL_OK"
	liveBridgeLifecycleTokenPeerA   = "PEER_A_OK"
	liveBridgeLifecycleTokenPeerB   = "PEER_B_OK"
	liveBridgeLifecycleTokenRestart = "RESTART_OK"
)

type liveBridgeLifecyclePrompts struct {
	Stream             string
	Cancel             string
	PeerA              string
	PeerB              string
	Restart            string
	RebootstrapFirst   string
	RebootstrapRebuilt string
}

func (p liveBridgeLifecyclePrompts) all() []string {
	return []string{
		p.Stream, p.Cancel, p.PeerA, p.PeerB, p.Restart,
		p.RebootstrapFirst, p.RebootstrapRebuilt,
	}
}

func liveBridgeTextOnlyPrompt(exactToken string) string {
	token := strings.TrimSpace(exactToken)
	if token == "" {
		token = "OK"
	}
	return liveBridgeTextOnlyPrefix +
		` Reply with exactly the token "` + token +
		`" as plain text only. Do not use tools, shell, PowerShell, filesystem, commands, or workspace actions.`
}

func newLiveBridgeLifecyclePrompts(rebootstrapToken string) liveBridgeLifecyclePrompts {
	tok := strings.TrimSpace(rebootstrapToken)
	if tok == "" {
		tok = liveBridgeDefaultRebootToken
	}
	return liveBridgeLifecyclePrompts{
		Stream:             liveBridgeTextOnlyPrompt(liveBridgeLifecycleTokenStream),
		Cancel:             liveBridgeTextOnlyPrompt(liveBridgeLifecycleTokenCancel),
		PeerA:              liveBridgeTextOnlyPrompt(liveBridgeLifecycleTokenPeerA),
		PeerB:              liveBridgeTextOnlyPrompt(liveBridgeLifecycleTokenPeerB),
		Restart:            liveBridgeTextOnlyPrompt(liveBridgeLifecycleTokenRestart),
		RebootstrapFirst:   liveBridgeTextOnlyPrompt(tok + " first"),
		RebootstrapRebuilt: liveBridgeTextOnlyPrompt(tok + " rebuilt"),
	}
}

func liveBridgeCallUserText(call lipapi.Call) string {
	if len(call.Messages) == 0 || len(call.Messages[0].Parts) == 0 {
		return ""
	}
	p := call.Messages[0].Parts[0]
	if p.Kind != lipapi.PartText {
		return ""
	}
	return p.Text
}

func liveBridgePromptHasTextOnlyDeny(prompt string) bool {
	lower := strings.ToLower(prompt)
	for _, frag := range []string{
		"do not use tools",
		"shell",
		"filesystem",
		"commands",
		"workspace",
		"plain text",
	} {
		if !strings.Contains(lower, frag) {
			return false
		}
	}
	return strings.HasPrefix(prompt, liveBridgeTextOnlyPrefix)
}

func liveBridgePromptIsVague(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	for _, vague := range []string{
		"hello stream",
		"hold for cancel",
		"peer a hold",
		"peer b hold",
		"after kill",
	} {
		if lower == vague || strings.Contains(lower, vague) {
			return true
		}
	}
	return false
}

func classifyLiveBridgePeerErr(err error) (liveBridgePeerClass, string) {
	if err == nil {
		return liveBridgePeerUnexpected, "missing peer failure"
	}
	if errors.Is(err, io.EOF) {
		return liveBridgePeerNaturalEnd, "peer completed before bridge kill"
	}
	if errors.Is(err, ErrBridgeExited) {
		return liveBridgePeerProof, err.Error()
	}
	var cf *ClassifiedFailure
	if errors.As(err, &cf) && cf != nil && cf.Code == CodeBridgeExited {
		return liveBridgePeerProof, err.Error()
	}
	return liveBridgePeerUnexpected, err.Error()
}

func liveBridgeHardRestartOutcome(peerA, peerB, restartErr error, killEvidenceOK bool) (status, detail string) {
	classA, detailA := classifyLiveBridgePeerErr(peerA)
	classB, detailB := classifyLiveBridgePeerErr(peerB)
	if classA == liveBridgePeerNaturalEnd || classB == liveBridgePeerNaturalEnd {
		return "blocked", "unable to hold active peer streams until KillGeneration"
	}
	if classA != liveBridgePeerProof {
		return "failed", "peer A: " + detailA
	}
	if classB != liveBridgePeerProof {
		return "failed", "peer B: " + detailB
	}
	if !killEvidenceOK {
		return "failed", "kill-side evidence missing for matching generation process"
	}
	if restartErr != nil {
		return "failed", "restart: " + sanitizeLiveBridgeText(restartErr.Error())
	}
	return "passed", "peers failed with bridge-exited; generation restarted"
}

type liveBridgeRestartDiag struct {
	Ready       bool
	Gen         int64
	Gen2Waited  bool
	StderrClass string
}

func liveBridgeStderrClass(sanitized string) string {
	if strings.TrimSpace(sanitized) == "" {
		return "empty"
	}
	return "present"
}

func appendLiveBridgeRestartDiag(detail string, d liveBridgeRestartDiag) string {
	class := strings.TrimSpace(d.StderrClass)
	if class == "" {
		class = "empty"
	}
	class = sanitizeLiveBridgeText(class)
	suffix := fmt.Sprintf("ready=%t; gen=%d; gen2_waited=%t; stderr_class=%s",
		d.Ready, d.Gen, d.Gen2Waited, class)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return suffix
	}
	return detail + "; " + suffix
}

type liveBridgePeerWatch struct {
	active   <-chan struct{}
	errCh    <-chan error
	done     <-chan struct{}
	terminal *atomic.Bool
}

func watchLiveBridgePeer(ctx context.Context, stream lipapi.ManagedEventStream) *liveBridgePeerWatch {
	activeCh := make(chan struct{})
	errCh := make(chan error, 1)
	done := make(chan struct{})
	terminal := &atomic.Bool{}
	var activeOnce sync.Once
	go func() {
		defer close(done)
		if stream == nil {
			errCh <- fmt.Errorf("missing peer stream")
			return
		}
		for {
			ev, err := stream.Recv(ctx)
			if err != nil {
				errCh <- err
				return
			}
			switch ev.Kind {
			case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
				activeOnce.Do(func() { close(activeCh) })
			case lipapi.EventResponseFinished, lipapi.EventError:
				terminal.Store(true)
			}
		}
	}()
	return &liveBridgePeerWatch{active: activeCh, errCh: errCh, done: done, terminal: terminal}
}

func (w *liveBridgePeerWatch) sawTerminal() bool {
	return w != nil && w.terminal != nil && w.terminal.Load()
}

func waitLiveBridgeCond(ctx context.Context, tick time.Duration, cond func() bool) error {
	if tick <= 0 {
		tick = 5 * time.Millisecond
	}
	if cond() {
		return nil
	}
	t := time.NewTimer(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if cond() {
				return nil
			}
			t.Reset(tick)
		}
	}
}

func waitLiveBridgeFilesExist(ctx context.Context, paths ...string) error {
	return waitLiveBridgeCond(ctx, 5*time.Millisecond, func() bool {
		for _, p := range paths {
			if strings.TrimSpace(p) == "" {
				return false
			}
			if _, err := os.Stat(p); err != nil {
				return false
			}
		}
		return true
	})
}

func awaitLiveBridgePeersActive(ctx context.Context, a, b *liveBridgePeerWatch, notifyPaths []string) (blocked bool, detail string) {
	if a == nil || b == nil {
		return true, "unable to hold active peer streams until KillGeneration"
	}
	if len(notifyPaths) >= 2 &&
		strings.TrimSpace(notifyPaths[0]) != "" &&
		strings.TrimSpace(notifyPaths[1]) != "" {
		errCh := make(chan error, 1)
		go func() { errCh <- waitLiveBridgeFilesExist(ctx, notifyPaths[0], notifyPaths[1]) }()
		select {
		case err := <-errCh:
			if err != nil {
				return true, "unable to hold active peer streams until KillGeneration"
			}
		case err := <-a.errCh:
			_ = err
			return true, "unable to hold active peer streams until KillGeneration"
		case err := <-b.errCh:
			_ = err
			return true, "unable to hold active peer streams until KillGeneration"
		case <-ctx.Done():
			return true, "unable to hold active peer streams until KillGeneration"
		}
		return false, ""
	}
	for _, w := range []*liveBridgePeerWatch{a, b} {
		select {
		case <-w.active:
		case err := <-w.errCh:
			_ = err
			return true, "unable to hold active peer streams until KillGeneration"
		case <-ctx.Done():
			return true, "unable to hold active peer streams until KillGeneration"
		}
	}
	return false, ""
}

func liveBridgePeerEndedEarly(ch <-chan error) (ended bool, err error) {
	select {
	case err := <-ch:
		return true, err
	default:
		return false, nil
	}
}

func waitLiveBridgePeerErr(ctx context.Context, ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func joinLiveBridgePeerWatch(ctx context.Context, watches ...*liveBridgePeerWatch) {
	for _, w := range watches {
		if w == nil {
			continue
		}
		select {
		case <-w.done:
		case <-ctx.Done():
			return
		}
	}
}

const liveBridgeLane = "live-bridge"

type liveBridgeCommandOpts struct {
	LookPath   func(name string) (string, error)
	BridgeRoot string
	BridgeBin  string
}

type liveBridgeInput struct {
	Getenv             func(string) string
	BridgeArgv         []string
	Workspace          string
	HostEnv            []string
	Starter            ProcessStarter
	APIKey             string
	Timeout            time.Duration
	CreateCountPath    string
	PromptCapturePath  string
	FullPromptToken    string
	PeerActiveNotify   []string
	ActiveProofTimeout time.Duration
}

type liveBridgeScenario struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type liveBridgeSummary struct {
	OK        bool                 `json:"ok"`
	Status    string               `json:"status"`
	Lane      string               `json:"lane"`
	GOOS      string               `json:"goos"`
	GOARCH    string               `json:"goarch"`
	Scenarios []liveBridgeScenario `json:"scenarios"`
}

type fixedArgvStarter struct {
	argv  []string
	inner ProcessStarter
}

func (s fixedArgvStarter) Start(_ []string, cwd string, env []string) (Process, error) {
	inner := s.inner
	if inner == nil {
		inner = OSProcessStarter{}
	}
	return inner.Start(s.argv, cwd, env)
}

type processStarterFunc func(cmd []string, cwd string, env []string) (Process, error)

func (f processStarterFunc) Start(cmd []string, cwd string, env []string) (Process, error) {
	return f(cmd, cwd, env)
}

func buildLiveBridgeCommand(opts liveBridgeCommandOpts) ([]string, error) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	nodeExe, err := lookPath("node")
	if err != nil {
		return nil, fmt.Errorf("cursorsdk: live bridge: node not found on PATH")
	}
	bin := strings.TrimSpace(opts.BridgeBin)
	if bin == "" {
		bin = defaultBridgeBinPath(opts.BridgeRoot)
	}
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() {
		return nil, fmt.Errorf("cursorsdk: live bridge: bridge bin missing")
	}
	if err := rejectShellOrNPMExecutable(nodeExe); err != nil {
		return nil, err
	}
	base := strings.ToLower(filepath.Base(bin))
	if base != "lip-cursor-sdk-bridge.js" {
		return nil, fmt.Errorf("cursorsdk: live bridge: unexpected bridge bin name")
	}
	return []string{nodeExe, bin}, nil
}

func liveBridgeHostEnv(hostEnv []string) []string {
	if len(hostEnv) == 0 {
		hostEnv = os.Environ()
	}
	return SelectHostEnv(hostEnv, PlatformMinimumEnvNames())
}

var (
	liveBridgePathRe = regexp.MustCompile(`[A-Za-z]:\\[^\s"']+|/(?:home|Users|tmp|var|private)/[^\s"']+`)
	liveBridgeIDRe   = regexp.MustCompile(`(?i)\b(agentId|runId|agent_id|run_id)\s*[:=]\s*\S+`)
	liveBridgeSDKRe  = regexp.MustCompile(`(?i)\b(bc-|agent-|run-)[A-Za-z0-9_-]{6,}\b`)
)

func sanitizeLiveBridgeText(in string) string {
	out := in
	out = regexp.MustCompile(`(?i)crsr_[A-Za-z0-9_-]+`).ReplaceAllString(out, "[REDACTED]")
	out = regexp.MustCompile(`(?i)sk-[A-Za-z0-9_-]+`).ReplaceAllString(out, "[REDACTED]")
	out = regexp.MustCompile(`(?i)apiKey["']?\s*[:=]\s*["']?[^"',\s}]+`).ReplaceAllString(out, "apiKey=[REDACTED]")
	out = liveBridgePathRe.ReplaceAllString(out, "[path]")
	out = liveBridgeIDRe.ReplaceAllString(out, "$1=[ID]")
	out = liveBridgeSDKRe.ReplaceAllString(out, "[ID]")
	out = strings.ReplaceAll(out, `\`, "/")
	if len(out) > 240 {
		out = out[:240]
	}
	return out
}

func liveBridgeErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", sanitizeLiveBridgeText(err.Error()))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func verifyLiveBridgePinnedSDK(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("cursorsdk: live bridge: empty argv for version check")
	}
	args := append(append([]string{}, argv[1:]...), "--version")
	cmd := exec.CommandContext(ctx, argv[0], args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"SYSTEMROOT", "SYSTEMDRIVE", "TEMP", "TMP", "COMSPEC"} {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cursorsdk: live bridge version check failed: %s", sanitizeLiveBridgeText(err.Error()))
	}
	out := stdout.String() + stderr.String()
	if strings.Contains(strings.ToLower(out), "cursor_api_key") || strings.Contains(out, "crsr_") {
		return fmt.Errorf("cursorsdk: live bridge version output leaked credentials")
	}
	if !strings.Contains(out, protocol.PinnedSDKVersion) {
		return fmt.Errorf("cursorsdk: live bridge sdk pin missing (want %s)", protocol.PinnedSDKVersion)
	}
	return nil
}
