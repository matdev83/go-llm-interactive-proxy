package product

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type liveBridgeTrackStarter struct {
	inner  ProcessStarter
	mu     sync.Mutex
	last   *liveBridgeWaitProc
	starts atomic.Int32
}

type liveBridgeWaitProc struct {
	inner  Process
	waited atomic.Bool
	kills  atomic.Int32
}

func (p *liveBridgeWaitProc) PID() int              { return p.inner.PID() }
func (p *liveBridgeWaitProc) Stdin() io.WriteCloser { return p.inner.Stdin() }
func (p *liveBridgeWaitProc) Stdout() io.ReadCloser { return p.inner.Stdout() }
func (p *liveBridgeWaitProc) Stderr() io.ReadCloser { return p.inner.Stderr() }
func (p *liveBridgeWaitProc) Kill() error {
	p.kills.Add(1)
	return p.inner.Kill()
}

func (p *liveBridgeWaitProc) Wait() error {
	err := p.inner.Wait()
	p.waited.Store(true)
	return err
}

func (p *liveBridgeWaitProc) KillCount() int32 {
	if p == nil {
		return 0
	}
	return p.kills.Load()
}

func (p *liveBridgeWaitProc) Waited() bool {
	return p != nil && p.waited.Load()
}

func (s *liveBridgeTrackStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	inner := s.inner
	if inner == nil {
		inner = OSProcessStarter{}
	}
	p, err := inner.Start(cmd, cwd, env)
	if err != nil {
		return nil, err
	}
	wrapped := &liveBridgeWaitProc{inner: p}
	s.mu.Lock()
	s.last = wrapped
	s.mu.Unlock()
	s.starts.Add(1)
	return wrapped, nil
}

func (s *liveBridgeTrackStarter) lastProc() *liveBridgeWaitProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func runLiveBridgeHarness(ctx context.Context, in liveBridgeInput) (*liveBridgeSummary, error) {
	summary := &liveBridgeSummary{
		OK:     false,
		Status: "blocked",
		Lane:   liveBridgeLane,
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}
	getenv := in.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if ready, reason := LiveOptInReady(getenv); !ready {
		summary.Scenarios = []liveBridgeScenario{{
			Name: "opt_in", Status: "blocked", Detail: sanitizeLiveBridgeText(reason),
		}}
		return summary, nil
	}
	apiKey := strings.TrimSpace(in.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(getenv("CURSOR_API_KEY"))
	}
	if apiKey == "" {
		summary.Scenarios = []liveBridgeScenario{{
			Name: "opt_in", Status: "blocked", Detail: "CURSOR_API_KEY missing",
		}}
		return summary, nil
	}
	argv := append([]string(nil), in.BridgeArgv...)
	if len(argv) == 0 {
		summary.Status = "failed"
		return summary, liveBridgeErr(fmt.Errorf("cursorsdk: live bridge requires BridgeArgv"))
	}
	ws := strings.TrimSpace(in.Workspace)
	if ws == "" {
		summary.Status = "failed"
		return summary, liveBridgeErr(fmt.Errorf("cursorsdk: live bridge requires Workspace"))
	}

	timeout := in.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	outcomes := make([]liveBridgeScenario, 0, 10)
	record := func(name, status, detail string) {
		outcomes = append(outcomes, liveBridgeScenario{
			Name: name, Status: status, Detail: sanitizeLiveBridgeText(detail),
		})
	}
	finalize := func(err error) (*liveBridgeSummary, error) {
		summary.Scenarios = outcomes
		failed, blocked := 0, 0
		for _, sc := range outcomes {
			switch sc.Status {
			case "failed":
				failed++
			case "blocked":
				blocked++
			}
		}
		switch {
		case failed > 0:
			summary.Status = "failed"
			summary.OK = false
		case blocked > 0:
			summary.Status = "blocked"
			summary.OK = false
		default:
			summary.Status = "complete"
			summary.OK = true
		}
		return summary, liveBridgeErr(err)
	}

	if err := verifyLiveBridgePinnedSDK(runCtx, argv); err != nil {
		record("sdk_pin", "failed", err.Error())
		return finalize(err)
	}
	record("sdk_pin", "passed", "pinned sdk version verified")

	hostEnv := liveBridgeHostEnv(in.HostEnv)
	track := &liveBridgeTrackStarter{inner: fixedArgvStarter{argv: argv, inner: in.Starter}}

	cfg := Config{
		APIKey:             apiKey,
		BridgeExecutable:   argv[0],
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 20 * time.Second,
		CancelTimeout:      8 * time.Second,
		ShutdownTimeout:    10 * time.Second,
		MaxAgents:          4,
		MaxConcurrentRuns:  2,
		AgentIdleTimeout:   time.Minute,
		SandboxMode:        SandboxOff,
		DefaultWorkspace:   ws,
	}

	rt := newBackendRuntime(cfg, runtimeOpts{
		Starter: track,
		HostEnv: hostEnv,
	})
	defer func() { _ = rt.Close() }()

	snap, err := rt.tracking.LoadModels(runCtx)
	if err != nil || len(snap.Models) == 0 {
		detail := "model list empty"
		if err != nil {
			detail = err.Error()
		}
		record("discovery", "failed", detail)
		return finalize(fmt.Errorf("cursorsdk: live bridge discovery: %s", detail))
	}
	rt.tracking.AcceptInventory(snap.Models)
	modelID := strings.TrimSpace(snap.Models[0].NativeID)
	if modelID == "" {
		modelID = "gpt-5.3-codex"
	}
	record("discovery", "passed", fmt.Sprintf("models=%d", len(snap.Models)))

	rbToken := strings.TrimSpace(in.FullPromptToken)
	if rbToken == "" {
		rbToken = liveBridgeDefaultRebootToken
	}
	lc := newLiveBridgeLifecyclePrompts(rbToken)

	streamCall := liveBridgeTextCall(modelID, "stream-1", "live-bridge-stream", lc.Stream)
	stream, err := rt.Open(runCtx, streamCall, AttemptCandidate{Primary: Primary{Model: modelID}})
	if err != nil {
		record("stream", "failed", err.Error())
		return finalize(err)
	}
	if _, ok := stream.(*RunStream); !ok {
		_ = stream.Close()
		record("stream", "failed", "Open did not return *RunStream")
		return finalize(fmt.Errorf("cursorsdk: live bridge stream type"))
	}
	text, terminals, err := drainCanonicalStream(runCtx, stream)
	_ = stream.Close()
	if err != nil || terminals != 1 || strings.TrimSpace(text) == "" {
		record("stream", "failed", errString(err, "canonical stream incomplete"))
		return finalize(fmt.Errorf("cursorsdk: live bridge stream incomplete"))
	}
	record("stream", "passed", "canonical content and single terminal")

	cancelCall := liveBridgeTextCall(modelID, "cancel-1", "live-bridge-cancel", lc.Cancel)
	cancelStream, err := rt.Open(runCtx, cancelCall, AttemptCandidate{Primary: Primary{Model: modelID}})
	if err != nil {
		record("cancellation", "failed", err.Error())
		return finalize(err)
	}
	cancelOK, cancelDetail := observeCancellableCancel(runCtx, cancelStream)
	_ = cancelStream.Close()
	switch {
	case cancelOK:
		record("cancellation", "passed", "cancelled terminal; no later content")
	case strings.HasPrefix(cancelDetail, "blocked:"):
		record("cancellation", "blocked", strings.TrimPrefix(cancelDetail, "blocked:"))
	default:
		record("cancellation", "failed", cancelDetail)
		return finalize(fmt.Errorf("cursorsdk: live bridge cancel: %s", cancelDetail))
	}

	peerACall := liveBridgeTextCall(modelID, "peer-a", "live-bridge-peer-a", lc.PeerA)
	peerBCall := liveBridgeTextCall(modelID, "peer-b", "live-bridge-peer-b", lc.PeerB)
	peerA, err := rt.Open(runCtx, peerACall, AttemptCandidate{Primary: Primary{Model: modelID}})
	if err != nil {
		record("hard_bridge_restart", "failed", err.Error())
		return finalize(err)
	}
	peerB, err := rt.Open(runCtx, peerBCall, AttemptCandidate{Primary: Primary{Model: modelID}})
	if err != nil {
		_ = peerA.Close()
		record("hard_bridge_restart", "failed", err.Error())
		return finalize(err)
	}
	gen1 := rt.bp.Generation()
	if gen1 <= 0 {
		_ = peerA.Close()
		_ = peerB.Close()
		record("hard_bridge_restart", "failed", "missing bridge generation")
		return finalize(fmt.Errorf("cursorsdk: live bridge missing generation"))
	}
	rsA, okA := peerA.(*RunStream)
	rsB, okB := peerB.(*RunStream)
	if !okA || !okB || rsA.lease == nil || rsB.lease == nil ||
		rsA.lease.ProcessGeneration() != gen1 || rsB.lease.ProcessGeneration() != gen1 {
		_ = peerA.Close()
		_ = peerB.Close()
		record("hard_bridge_restart", "failed", "peer streams not on current generation")
		return finalize(fmt.Errorf("cursorsdk: live bridge peer generation mismatch"))
	}
	startsBefore := track.starts.Load()
	gen1Proc := track.lastProc()
	if gen1Proc == nil || gen1Proc.PID() <= 0 {
		_ = peerA.Close()
		_ = peerB.Close()
		record("hard_bridge_restart", "failed", "missing tracked bridge process for generation")
		return finalize(fmt.Errorf("cursorsdk: live bridge missing tracked process"))
	}
	watchA := watchLiveBridgePeer(runCtx, peerA)
	watchB := watchLiveBridgePeer(runCtx, peerB)
	closePeers := func() {
		_ = peerA.Close()
		_ = peerB.Close()
		joinLiveBridgePeerWatch(runCtx, watchA, watchB)
	}

	activeCtx := runCtx
	if in.ActiveProofTimeout > 0 {
		var cancelActive context.CancelFunc
		activeCtx, cancelActive = context.WithTimeout(runCtx, in.ActiveProofTimeout)
		defer cancelActive()
	}
	if blocked, detail := awaitLiveBridgePeersActive(activeCtx, watchA, watchB, in.PeerActiveNotify); blocked {
		closePeers()
		record("hard_bridge_restart", "blocked", detail)
	} else if watchA.sawTerminal() || watchB.sawTerminal() {
		closePeers()
		record("hard_bridge_restart", "blocked", "unable to hold active peer streams until KillGeneration")
	} else if ended, _ := liveBridgePeerEndedEarly(watchA.errCh); ended {
		closePeers()
		record("hard_bridge_restart", "blocked", "unable to hold active peer streams until KillGeneration")
	} else if ended, _ := liveBridgePeerEndedEarly(watchB.errCh); ended {
		closePeers()
		record("hard_bridge_restart", "blocked", "unable to hold active peer streams until KillGeneration")
	} else if gen1Proc.Waited() || !rt.bp.Ready() {
		closePeers()
		record("hard_bridge_restart", "blocked", "bridge generation died before KillGeneration")
	} else {
		killsBefore := gen1Proc.KillCount()
		if err := rt.bp.KillGeneration(runCtx, gen1); err != nil {
			closePeers()
			record("hard_bridge_restart", "failed", err.Error())
			return finalize(err)
		}
		killCtx, cancelKill := context.WithTimeout(runCtx, 8*time.Second)
		killErr := waitLiveBridgeCond(killCtx, 5*time.Millisecond, func() bool {
			return gen1Proc.KillCount() > killsBefore && gen1Proc.Waited() && !rt.bp.Ready()
		})
		cancelKill()
		killEvidenceOK := killErr == nil &&
			gen1Proc.KillCount() > killsBefore &&
			gen1Proc.Waited() &&
			!rt.bp.Ready() &&
			track.lastProc() == gen1Proc

		errPeerA := waitLiveBridgePeerErr(runCtx, watchA.errCh)
		errPeerB := waitLiveBridgePeerErr(runCtx, watchB.errCh)
		closePeers()

		poolCtx, cancelPool := context.WithTimeout(runCtx, 2*time.Second)
		poolErr := waitLiveBridgeCond(poolCtx, 5*time.Millisecond, func() bool {
			return rt.pool.LiveCount() == 0
		})
		cancelPool()
		if poolErr != nil || rt.pool.LiveCount() != 0 {
			record("hard_bridge_restart", "failed", "pool live count not cleared")
			return finalize(fmt.Errorf("cursorsdk: live bridge pool not cleared"))
		}

		var restartErr error
		var gen2 int64
		var gen2Waited bool
		restartCall := liveBridgeTextCall(modelID, "restart-1", "live-bridge-restart", lc.Restart)
		restartStream, openErr := rt.Open(runCtx, restartCall, AttemptCandidate{Primary: Primary{Model: modelID}})
		if openErr != nil {
			restartErr = openErr
			gen2 = rt.bp.Generation()
			if p := track.lastProc(); p != nil && p != gen1Proc {
				gen2Waited = p.Waited()
			}
		} else {
			gen2 = rt.bp.Generation()
			gen2Proc := track.lastProc()
			if gen2 <= gen1 || track.starts.Load() <= startsBefore || gen2Proc == nil || gen2Proc == gen1Proc {
				_ = restartStream.Close()
				restartErr = fmt.Errorf("generation or process not restarted")
			} else {
				_, _, drainErr := drainCanonicalStream(runCtx, restartStream)
				_ = restartStream.Close()
				restartErr = drainErr
				gen2Waited = gen2Proc.Waited()
			}
		}
		status, detail := liveBridgeHardRestartOutcome(errPeerA, errPeerB, restartErr, killEvidenceOK)
		if status == "failed" && restartErr != nil {
			detail = appendLiveBridgeRestartDiag(detail, liveBridgeRestartDiag{
				Ready:       rt.bp.Ready(),
				Gen:         gen2,
				Gen2Waited:  gen2Waited,
				StderrClass: liveBridgeStderrClass(rt.bp.RetainedStderr()),
			})
		}
		record("hard_bridge_restart", status, detail)
		if status == "failed" {
			return finalize(fmt.Errorf("cursorsdk: live bridge hard restart: %s", detail))
		}
	}

	rbSession := "live-bridge-rebootstrap"
	token := rbToken
	c1, ok1 := readIntFile(in.CreateCountPath)
	if !ok1 || strings.TrimSpace(in.PromptCapturePath) == "" {
		record("canonical_rebootstrap", "blocked", "full-prompt provider proof instrumentation unavailable")
	} else {
		if err := killBridgeGeneration(runCtx, rt, track); err != nil {
			record("canonical_rebootstrap", "failed", err.Error())
			return finalize(err)
		}
		rb1 := liveBridgeTextCall(modelID, "rb-1", rbSession, lc.RebootstrapFirst)
		s1, err := rt.Open(runCtx, rb1, AttemptCandidate{Primary: Primary{Model: modelID}})
		if err != nil {
			record("canonical_rebootstrap", "failed", err.Error())
			return finalize(err)
		}
		_, _, err = drainCanonicalStream(runCtx, s1)
		_ = s1.Close()
		if err != nil {
			record("canonical_rebootstrap", "failed", err.Error())
			return finalize(err)
		}
		cSeed, okSeed := readIntFile(in.CreateCountPath)
		if !okSeed || cSeed < c1 {
			record("canonical_rebootstrap", "failed", "create-count missing after seed open")
			return finalize(fmt.Errorf("cursorsdk: live bridge rebootstrap seed"))
		}
		if err := killBridgeGeneration(runCtx, rt, track); err != nil {
			record("canonical_rebootstrap", "failed", err.Error())
			return finalize(err)
		}
		rb2 := liveBridgeTextCall(modelID, "rb-2", rbSession, lc.RebootstrapRebuilt)
		s2, err := rt.Open(runCtx, rb2, AttemptCandidate{Primary: Primary{Model: modelID}})
		if err != nil {
			record("canonical_rebootstrap", "failed", err.Error())
			return finalize(err)
		}
		_, _, err = drainCanonicalStream(runCtx, s2)
		_ = s2.Close()
		if err != nil {
			record("canonical_rebootstrap", "failed", err.Error())
			return finalize(err)
		}
		c2, ok2 := readIntFile(in.CreateCountPath)
		prompt, okP := readStringFile(in.PromptCapturePath)
		if !ok2 || c2 <= cSeed || !okP || !strings.Contains(prompt, token) {
			record("canonical_rebootstrap", "failed", "create-count/full-prompt proof missing")
			return finalize(fmt.Errorf("cursorsdk: live bridge rebootstrap proof missing"))
		}
		record("canonical_rebootstrap", "passed", "create-count advanced; full prompt observed")
	}

	record("configured_mcp", "blocked", "harmless automated MCP tool exercise not available without provider side effects")
	if runtime.GOOS == "windows" {
		record("workspace_safety_required", "blocked", "sandbox required unavailable on this platform")
	} else {
		record("workspace_safety_required", "blocked", "sandbox required not exercised in live-bridge harness")
	}

	if err := rt.Close(); err != nil {
		record("shutdown", "failed", err.Error())
		return finalize(err)
	}
	last := track.lastProc()
	if last == nil || !last.Waited() || rt.bp.Ready() {
		record("shutdown", "failed", "process wait/reap or Ready after Close")
		return finalize(fmt.Errorf("cursorsdk: live bridge shutdown orphan"))
	}
	record("shutdown", "passed", "bridge closed and reaped")
	return finalize(nil)
}

func killBridgeGeneration(ctx context.Context, rt *backendRuntime, track *liveBridgeTrackStarter) error {
	if rt == nil || rt.bp == nil {
		return fmt.Errorf("cursorsdk: live bridge missing runtime")
	}
	gen := rt.bp.Generation()
	target := track.lastProc()
	if err := rt.bp.KillGeneration(ctx, gen); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := waitLiveBridgeCond(waitCtx, 5*time.Millisecond, func() bool {
		return target != nil && target.Waited() && !rt.bp.Ready() && rt.pool.LiveCount() == 0
	}); err != nil {
		return fmt.Errorf("cursorsdk: live bridge generation kill not reaped")
	}
	return nil
}

func liveBridgeTextCall(model, callID, sessionID, text string) lipapi.Call {
	return lipapi.Call{
		ID: callID,
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Session: lipapi.SessionRef{AuthoritativeSessionID: sessionID},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(text)},
		}},
		Route: lipapi.RouteIntent{Selector: "cursorsdk:" + model},
	}
}

func drainCanonicalStream(ctx context.Context, stream lipapi.ManagedEventStream) (text string, terminals int, err error) {
	var b strings.Builder
	for {
		ev, recvErr := stream.Recv(ctx)
		if errors.Is(recvErr, io.EOF) {
			if terminals != 1 || strings.TrimSpace(b.String()) == "" {
				return b.String(), terminals, fmt.Errorf("expected content and single terminal")
			}
			return b.String(), terminals, nil
		}
		if recvErr != nil {
			return b.String(), terminals, recvErr
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return b.String(), terminals, err
		}
		switch ev.Kind {
		case lipapi.EventTextDelta:
			if terminals > 0 {
				return b.String(), terminals, fmt.Errorf("content after terminal")
			}
			b.WriteString(ev.Delta)
		case lipapi.EventResponseFinished, lipapi.EventError:
			terminals++
			if terminals > 1 {
				return b.String(), terminals, fmt.Errorf("multiple terminals")
			}
		}
	}
}

func observeCancellableCancel(ctx context.Context, stream lipapi.ManagedEventStream) (ok bool, detail string) {
	// Do not Recv with a short deadline: RunStream.Recv cancels+closes on ctx timeout.
	if err := sleepCtx(ctx, 20*time.Millisecond); err != nil {
		return false, err.Error()
	}
	_ = stream.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelExplicit})

	var sawCancelTerminal bool
	var terminals int
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		ev, err := stream.Recv(deadline)
		if herr := handleCancelRecv(ev, err, &sawCancelTerminal, &terminals); herr != nil {
			return false, herr.Error()
		}
		if errors.Is(err, io.EOF) && sawCancelTerminal && terminals == 1 {
			return true, ""
		}
		if sawCancelTerminal {
			return quietCancelWindow(deadline, stream)
		}
		if deadline.Err() != nil {
			if sawCancelTerminal && terminals == 1 {
				return true, ""
			}
			return false, "cancel terminal timeout"
		}
	}
}

func handleCancelRecv(ev lipapi.Event, err error, saw *bool, terminals *int) error {
	if errors.Is(err, io.EOF) {
		if *saw && *terminals == 1 {
			return nil
		}
		return fmt.Errorf("eof without cancelled terminal")
	}
	if err != nil {
		return err
	}
	switch ev.Kind {
	case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
		if *saw {
			return fmt.Errorf("content after cancelled terminal")
		}
	case lipapi.EventResponseFinished:
		*terminals++
		if *terminals > 1 {
			return fmt.Errorf("multiple terminals")
		}
		if strings.TrimSpace(ev.FinishReason) != "cancelled" {
			if strings.TrimSpace(ev.FinishReason) == "" || ev.FinishReason == "finished" {
				return fmt.Errorf("blocked:provider finished before cancellable in-flight state")
			}
			return fmt.Errorf("unexpected finish %q", ev.FinishReason)
		}
		*saw = true
	case lipapi.EventError:
		return fmt.Errorf("error terminal during cancel")
	}
	return nil
}

func quietCancelWindow(ctx context.Context, stream lipapi.ManagedEventStream) (bool, string) {
	// Never Recv with a short child deadline: RunStream.Recv cancels+closes on ctx timeout.
	if err := sleepCtx(ctx, 75*time.Millisecond); err != nil {
		return true, ""
	}
	ev, err := stream.Recv(ctx)
	if errors.Is(err, io.EOF) {
		return true, ""
	}
	if err != nil {
		return false, err.Error()
	}
	switch ev.Kind {
	case lipapi.EventTextDelta, lipapi.EventReasoningDelta, lipapi.EventResponseFinished, lipapi.EventError:
		return false, "content or terminal after cancelled"
	}
	return true, ""
}

func readIntFile(path string) (int, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}
	return n, true
}

func readStringFile(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func errString(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}
