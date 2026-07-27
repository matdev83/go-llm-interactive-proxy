package product

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
)

func fingerprintDiagID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func sanitizeDiagAttr(a slog.Attr) (slog.Attr, bool) {
	if !diagAttrAllowed(a.Key) {
		return slog.Attr{}, false
	}
	switch a.Key {
	case "backend_kind":
		if a.Value.String() != ID {
			return slog.Attr{}, false
		}
		return a, true
	case "backend_instance", "trace_id", "a_leg_id", "b_leg_id", "call_id":
		fp := fingerprintDiagID(a.Value.String())
		if fp == "" {
			return slog.Attr{}, false
		}
		return slog.String(a.Key, fp), true
	case "event":
		switch a.Value.String() {
		case DiagEventDiscovery, DiagEventPool, DiagEventRun, DiagEventBridge, DiagEventShutdown:
			return a, true
		default:
			return slog.Attr{}, false
		}
	case "outcome":
		return sanitizeOutcomeValue(a.Value.String())
	case "cause":
		return sanitizeCauseValue(a.Value.String())
	case "failure_code", "discovery_code":
		return sanitizeCodeValue(a.Key, a.Value.String())
	case "failure_phase":
		switch a.Value.String() {
		case "", "pre_output", "post_output":
			return a, true
		default:
			return slog.Attr{}, false
		}
	case "cancel_mode":
		switch a.Value.String() {
		case "", "provider", "transport":
			return a, true
		default:
			return slog.Attr{}, false
		}
	case "runtime_state":
		switch a.Value.String() {
		case "idle", "ready", "failed", "closing", "closed", "unknown":
			return a, true
		default:
			return slog.Attr{}, false
		}
	case "discovery_state":
		switch a.Value.String() {
		case "", "unknown", "ok", "failed":
			return a, true
		default:
			return slog.Attr{}, false
		}
	case "agent_count", "busy_run_count", "bridge_generation", "duration_ms",
		"bridge_protocol_version":
		return a, true
	case "bridge_package_version", "sdk_version", "node_version":
		v := a.Value.String()
		if strings.ContainsAny(v, "\n\r\t") {
			return slog.Attr{}, false
		}
		if len(v) > 64 {
			v = v[:64]
		}
		return slog.String(a.Key, v), true
	default:
		return slog.Attr{}, false
	}
}

func sanitizeOutcomeValue(v string) (slog.Attr, bool) {
	switch v {
	case "create", "reuse", "create_failed", "send_failed", "invalidate", "evict",
		"success", "error", "cancel", "ok":
		return slog.String("outcome", v), true
	default:
		return slog.Attr{}, false
	}
}

func sanitizeCauseValue(v string) (slog.Attr, bool) {
	switch InvalidationCause(v) {
	case "", InvalidateTranscript, InvalidateIdentity, InvalidateCancel, InvalidateRunError,
		InvalidateBridge, InvalidateEvict, InvalidateShutdown, InvalidateGeneration,
		InvalidateCreateFail, InvalidateSendFail, InvalidateUncommitted:
		return slog.String("cause", v), true
	default:
		return slog.Attr{}, false
	}
}

func sanitizeCodeValue(key, v string) (slog.Attr, bool) {
	if v == "" {
		return slog.String(key, ""), true
	}
	switch FailureCode(v) {
	case CodeConfigInvalid, CodeKeyMissing, CodeAuthFailed, CodeBridgeMissing, CodeNodeMissing,
		CodeBridgeStartFailed, CodeBridgeIncompatible, CodeBridgeProtocol, CodeBridgeExited,
		CodeModelUnknown, CodeInventoryUnavailable, CodeCapabilityUnsupported, CodeAgentBusy,
		CodeAgentLimit, CodeAgentCreateFailed, CodeRunFailed, CodeCancelTimeout, CodeShutdownFailed:
		return slog.String(key, v), true
	default:
		return slog.Attr{}, false
	}
}
