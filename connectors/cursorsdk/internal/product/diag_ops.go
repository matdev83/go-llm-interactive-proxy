package product

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	corediag "github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

const (
	DiagEventDiscovery = "discovery"
	DiagEventPool      = "pool"
	DiagEventRun       = "run"
	DiagEventBridge    = "bridge"
	DiagEventShutdown  = "shutdown"
)

type DiagCorr struct {
	CallID string
	BLegID string
}

type StatusInput struct {
	Info           BridgeInfo
	RuntimeState   string
	DiscoveryState string
	DiscoveryCode  string
	AgentCount     int
	BusyRunCount   int
}

type StatusSnapshot struct {
	BackendKind           string
	BackendInstance       string
	BridgeProtocolVersion int
	BridgePackageVersion  string
	SDKVersion            string
	NodeVersion           string
	RuntimeState          string
	DiscoveryState        string
	DiscoveryCode         string
	AgentCount            int
	BusyRunCount          int
}

func (s StatusSnapshot) String() string {
	return fmt.Sprintf(
		"kind=%s instance=%s proto=%d bridge=%s sdk=%s node=%s state=%s discovery=%s/%s agents=%d busy=%d",
		s.BackendKind, s.BackendInstance, s.BridgeProtocolVersion, s.BridgePackageVersion,
		s.SDKVersion, s.NodeVersion, s.RuntimeState, s.DiscoveryState, s.DiscoveryCode,
		s.AgentCount, s.BusyRunCount,
	)
}

type Diag struct {
	log      *slog.Logger
	kind     string
	instance string
}

func NewDiag(log *slog.Logger, instance string) *Diag {
	return &Diag{
		log:      log,
		kind:     ID,
		instance: strings.TrimSpace(instance),
	}
}

// Status returns a best-effort diagnostics snapshot. Fields are gathered without a
// single global lock, so concurrent pool/bridge mutation may produce a torn view
// of counts versus runtime/discovery state. The snapshot never includes secrets,
// prompts, paths, or raw SDK agent/run identifiers.
func (d *Diag) Status(in StatusInput) StatusSnapshot {
	if d == nil {
		d = NewDiag(nil, "")
	}
	return StatusSnapshot{
		BackendKind:           d.kind,
		BackendInstance:       d.instance,
		BridgeProtocolVersion: in.Info.SchemaVersion,
		BridgePackageVersion:  in.Info.ImplVersion,
		SDKVersion:            in.Info.SDKVersion,
		NodeVersion:           in.Info.NodeVersion,
		RuntimeState:          in.RuntimeState,
		DiscoveryState:        in.DiscoveryState,
		DiscoveryCode:         in.DiscoveryCode,
		AgentCount:            in.AgentCount,
		BusyRunCount:          in.BusyRunCount,
	}
}

func (d *Diag) LogDiscovery(ctx context.Context, state, code string, corr DiagCorr) {
	d.emit(
		ctx, slog.LevelInfo, "cursorsdk: discovery", corr,
		slog.String("event", DiagEventDiscovery),
		slog.String("discovery_state", state),
		slog.String("discovery_code", code),
	)
}

func (d *Diag) LogPool(ctx context.Context, outcome string, cause InvalidationCause, agentCount, busy int, corr DiagCorr) {
	d.LogPoolClassified(ctx, outcome, cause, "", "", agentCount, busy, corr)
}

func (d *Diag) LogPoolClassified(ctx context.Context, outcome string, cause InvalidationCause, code FailureCode, phase string, agentCount, busy int, corr DiagCorr) {
	attrs := []slog.Attr{
		slog.String("event", DiagEventPool),
		slog.String("outcome", outcome),
		slog.Int("agent_count", agentCount),
		slog.Int("busy_run_count", busy),
	}
	if cause != "" {
		attrs = append(attrs, slog.String("cause", string(cause)))
	}
	if code != "" {
		attrs = append(attrs, slog.String("failure_code", string(code)))
	}
	if phase != "" {
		attrs = append(attrs, slog.String("failure_phase", phase))
	}
	d.emit(ctx, slog.LevelInfo, "cursorsdk: pool", corr, attrs...)
}

func (d *Diag) LogRun(ctx context.Context, outcome, phase string, code FailureCode, cancelMode string, corr DiagCorr) {
	d.emit(
		ctx, slog.LevelInfo, "cursorsdk: run", corr,
		slog.String("event", DiagEventRun),
		slog.String("outcome", outcome),
		slog.String("failure_phase", phase),
		slog.String("failure_code", string(code)),
		slog.String("cancel_mode", cancelMode),
	)
}

func (d *Diag) LogBridge(ctx context.Context, state string, generation int64, code string, corr DiagCorr) {
	d.emit(
		ctx, slog.LevelInfo, "cursorsdk: bridge", corr,
		slog.String("event", DiagEventBridge),
		slog.String("runtime_state", state),
		slog.Int64("bridge_generation", generation),
		slog.String("failure_code", code),
	)
}

func (d *Diag) LogShutdown(ctx context.Context, durr time.Duration, outcome string, corr DiagCorr) {
	d.emit(
		ctx, slog.LevelInfo, "cursorsdk: shutdown", corr,
		slog.String("event", DiagEventShutdown),
		slog.String("outcome", outcome),
		slog.Int64("duration_ms", durr.Milliseconds()),
	)
}

func (d *Diag) emit(ctx context.Context, level slog.Level, msg string, corr DiagCorr, attrs ...slog.Attr) {
	if d == nil || d.log == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base := []slog.Attr{
		slog.String("backend_kind", d.kind),
		slog.String("backend_instance", d.instance),
	}
	if tid := corediag.TraceID(ctx); tid != "" {
		base = append(base, slog.String("trace_id", tid))
	}
	if aid := corediag.ALegID(ctx); aid != "" {
		base = append(base, slog.String("a_leg_id", aid))
	}
	if corr.BLegID != "" {
		base = append(base, slog.String("b_leg_id", corr.BLegID))
	}
	if corr.CallID != "" {
		base = append(base, slog.String("call_id", corr.CallID))
	}
	out := make([]slog.Attr, 0, len(base)+len(attrs))
	out = append(out, base...)
	out = append(out, attrs...)
	out = filterDiagAttrs(out)
	if len(out) == 0 {
		return
	}
	d.log.LogAttrs(ctx, level, msg, out...)
}

func filterDiagAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if v, ok := sanitizeDiagAttr(a); ok {
			out = append(out, v)
		}
	}
	return out
}

func diagAttrAllowed(key string) bool {
	switch key {
	case "backend_kind", "backend_instance", "event", "outcome", "cause",
		"failure_code", "failure_phase", "cancel_mode", "runtime_state",
		"discovery_state", "discovery_code", "agent_count", "busy_run_count",
		"bridge_generation", "duration_ms",
		"bridge_protocol_version", "bridge_package_version", "sdk_version", "node_version",
		"trace_id", "a_leg_id", "b_leg_id", "call_id":
		return true
	default:
		return false
	}
}

func bridgeStateName(s bridgeState) string {
	switch s {
	case bridgeIdle:
		return "idle"
	case bridgeReady:
		return "ready"
	case bridgeFailed:
		return "failed"
	case bridgeClosing:
		return "closing"
	case bridgeClosed:
		return "closed"
	default:
		return "unknown"
	}
}
