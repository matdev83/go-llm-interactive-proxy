package product

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

// PlatformSmokeInput configures a fake-bridge platform smoke run.
type PlatformSmokeInput struct {
	BridgeExecutable string
	HostEnv          []string
}

// PlatformSmokeSummary is a sanitized aggregate result for release evidence.
type PlatformSmokeSummary struct {
	OK          bool   `json:"ok"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	Lane        string `json:"lane"`
	Start       bool   `json:"start"`
	Stream      bool   `json:"stream"`
	Cancel      bool   `json:"cancel"`
	Crash       bool   `json:"crash"`
	Restart     bool   `json:"restart"`
	Rebootstrap bool   `json:"rebootstrap"`
	Shutdown    bool   `json:"shutdown"`
}

type platformStarter struct {
	scriptJSON string
	mu         sync.Mutex
	last       Process
}

func (s *platformStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	if s.scriptJSON != "" {
		env = append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+s.scriptJSON)
	}
	p, err := (OSProcessStarter{}).Start(cmd, cwd, env)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.last = p
	s.mu.Unlock()
	return p, nil
}

func (s *platformStarter) lastProcess() Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func poolSmokeConfig(exe string) Config {
	return Config{
		APIKey:             "smoke-key",
		BridgeExecutable:   exe,
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 5 * time.Second,
		CancelTimeout:      time.Second,
		ShutdownTimeout:    2 * time.Second,
		MaxAgents:          4,
		MaxConcurrentRuns:  2,
		AgentIdleTimeout:   time.Minute,
		SandboxMode:        SandboxOff,
	}
}

func smokeAgentKey(session string) AgentKey {
	return AgentKey{
		SessionID:           session,
		Workspace:           "/smoke-workspace",
		ModelID:             "gpt-5.3-codex",
		KeyFingerprint:      FingerprintSecret("smoke-key"),
		SettingsFingerprint: FingerprintSettingSources(nil),
		MCPFingerprint:      FingerprintJSON([]byte(`{}`)),
		Sandbox:             SandboxOff,
	}
}

func smokeCreateParams(key AgentKey) protocol.AgentCreateParams {
	return protocol.AgentCreateParams{
		APIKey:             "smoke-key",
		Model:              protocol.ModelSelection{ID: key.ModelID},
		Local:              protocol.AgentCreateLocal{Cwd: key.Workspace},
		SettingSources:     nil,
		SandboxOptions:     &protocol.SandboxOptions{Enabled: false},
		AutoReview:         false,
		EnableAgentRetries: false,
	}
}

// RunPlatformSmoke exercises start/stream/cancel/crash/restart/rebootstrap/shutdown on the current OS.
func RunPlatformSmoke(ctx context.Context, in PlatformSmokeInput) (*PlatformSmokeSummary, error) {
	if strings.TrimSpace(in.BridgeExecutable) == "" {
		return nil, fmt.Errorf("cursorsdk: platform smoke requires bridge executable")
	}

	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{
		{
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-smoke"}`)},
			{Type: fakebridge.ActionEvent, RunID: "run-smoke", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"hello"}`)},
			{Type: fakebridge.ActionEvent, RunID: "run-smoke", Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
		},
		{
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-cancel"}`)},
			{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-cancel"},
		},
	}
	raw, err := json.Marshal(script)
	if err != nil {
		return nil, err
	}

	starter := &platformStarter{scriptJSON: string(raw)}
	cfg := poolSmokeConfig(in.BridgeExecutable)
	hostEnv := in.HostEnv
	if len(hostEnv) == 0 {
		hostEnv = platformSmokeDefaultHostEnv()
	}

	var pool *SessionPool
	var coord *FailureCoordinator
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: starter,
		HostEnv: hostEnv,
		OnBridgeGenerationDead: func(gen int64) {
			if coord != nil {
				coord.InvalidateOnBridgeDeath(gen)
			}
		},
	})
	client := NewBridgeAgentClient(bp)
	pool = NewSessionPool(cfg, client, SessionPoolOpts{})
	coord = NewFailureCoordinator(pool, FailureCoordinatorOpts{})

	summary := &PlatformSmokeSummary{
		OK:     true,
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
		Lane:   "fake",
	}

	defer func() {
		_ = pool.Close(ctx)
		_ = bp.Close()
	}()

	info, err := bp.EnsureReady(ctx)
	summary.Start = err == nil && info.SchemaVersion == protocol.SchemaVersion
	if !summary.Start {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke start: %w", err)
	}

	agentID, err := client.CreateAgent(ctx, smokeCreateParams(smokeAgentKey("stream")))
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke create: %w", err)
	}
	ch, unsub, _ := client.SubscribeRun("run-smoke")
	defer unsub()
	_, err = client.SendAgent(ctx, agentID, "platform smoke stream")
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke send: %w", err)
	}
	summary.Stream, err = waitRunTerminal(ctx, ch, 1)
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke stream: %w", err)
	}

	runID2, err := client.SendAgent(ctx, agentID, "platform smoke cancel")
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke send for cancel: %w", err)
	}
	chCancel, unsubCancel, _ := client.SubscribeRun(runID2)
	defer unsubCancel()
	if err := client.CancelRun(ctx, runID2); err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke cancel: %w", err)
	}
	summary.Cancel, err = waitCancelledTerminal(ctx, chCancel)
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke cancel terminal: %w", err)
	}

	key := smokeAgentKey("rebootstrap")
	lease, err := pool.PrepareSend(ctx, PrepareSendInput{
		Key: key, Create: smokeCreateParams(key), View: TranscriptView{MessageCount: 1, PrefixHash: "h1", LastTurnID: "t1"},
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke bootstrap prepare: %w", err)
	}
	pool.CommitSend(lease)

	proc := starter.lastProcess()
	if proc == nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke missing bridge process for crash")
	}
	if err := proc.Kill(); err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke crash kill: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !bp.Ready() {
			summary.Crash = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !summary.Crash {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke crash not observed")
	}

	info2, err := bp.EnsureReady(ctx)
	summary.Restart = err == nil && info2.Generation > info.Generation
	if !summary.Restart {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke restart: %w", err)
	}

	lease2, err := pool.PrepareSend(ctx, PrepareSendInput{
		Key: key, Create: smokeCreateParams(key), View: TranscriptView{MessageCount: 2, PrefixHash: "h2", HeadPrefixHash: "h1", LastTurnID: "t2"},
		FullPrompt: "FULL2", SuffixPrompt: "SUF2",
	})
	if err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke rebootstrap prepare: %w", err)
	}
	pool.CommitSend(lease2)
	marker := pool.Marker(key)
	summary.Rebootstrap = marker.ProcessGeneration == bp.Generation() && marker.MessageCount == 2

	if err := bp.Close(); err != nil {
		summary.OK = false
		return summary, fmt.Errorf("cursorsdk: platform smoke shutdown: %w", err)
	}
	summary.Shutdown = true

	if !summary.Stream || !summary.Cancel || !summary.Crash || !summary.Restart || !summary.Rebootstrap || !summary.Shutdown {
		summary.OK = false
		return summary, fmt.Errorf(
			"cursorsdk: platform smoke incomplete (stream=%v cancel=%v crash=%v restart=%v rebootstrap=%v shutdown=%v)",
			summary.Stream, summary.Cancel, summary.Crash, summary.Restart, summary.Rebootstrap, summary.Shutdown,
		)
	}
	return summary, nil
}

func waitRunTerminal(ctx context.Context, ch <-chan *protocol.Frame, _ int) (bool, error) {
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				return false, fmt.Errorf("subscription closed before terminal event")
			}
			if f != nil && f.Type == protocol.TypeEvent && f.Kind == protocol.KindFinished {
				return true, nil
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func waitCancelledTerminal(ctx context.Context, ch <-chan *protocol.Frame) (bool, error) {
	var terminals int
	var sawCancelled bool
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				if sawCancelled && terminals == 1 {
					return true, nil
				}
				return false, fmt.Errorf("subscription closed; cancelled=%v terminals=%d", sawCancelled, terminals)
			}
			if f == nil || f.Type != protocol.TypeEvent {
				continue
			}
			switch f.Kind {
			case protocol.KindTextDelta, protocol.KindReasoningDelta:
				if sawCancelled {
					return false, fmt.Errorf("content after cancelled terminal")
				}
			case protocol.KindFinished:
				terminals++
				var payload struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(f.Payload, &payload)
				if payload.Status != "cancelled" {
					return false, fmt.Errorf("unexpected finished status %q", payload.Status)
				}
				if terminals > 1 {
					return false, fmt.Errorf("multiple terminals after cancel")
				}
				sawCancelled = true
				quiet, quietCancel := context.WithTimeout(ctx, 75*time.Millisecond)
				for {
					select {
					case late, lateOK := <-ch:
						if !lateOK {
							quietCancel()
							return true, nil
						}
						if late == nil || late.Type != protocol.TypeEvent {
							continue
						}
						switch late.Kind {
						case protocol.KindFinished:
							quietCancel()
							return false, fmt.Errorf("second terminal after cancelled")
						case protocol.KindTextDelta, protocol.KindReasoningDelta:
							quietCancel()
							return false, fmt.Errorf("content after cancelled terminal")
						}
					case <-quiet.Done():
						quietCancel()
						return true, nil
					}
				}
			case protocol.KindError:
				return false, fmt.Errorf("error terminal during cancel wait")
			}
		case <-ctx.Done():
			if sawCancelled && terminals == 1 {
				return true, nil
			}
			return false, ctx.Err()
		}
	}
}

func platformSmokeDefaultHostEnv() []string {
	out := []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"SYSTEMROOT", "SYSTEMDRIVE", "HOME", "USERPROFILE", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
