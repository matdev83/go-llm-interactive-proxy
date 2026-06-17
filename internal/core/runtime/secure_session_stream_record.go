package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// beforeEmitClientFacing records one post-hook canonical event before it is emitted to the client.
func (s *retryRecvStream) beforeEmitClientFacing(ctx context.Context, ev lipapi.Event) error {
	if s == nil || s.executor == nil || s.executor.SecureSessionRecorder == nil || !s.secureTurnOK {
		return nil
	}
	if ev.Kind == lipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return nil
	}
	in := buildStreamEventRecordInput(s, ev)
	err := s.executor.SecureSessionRecorder.RecordPostHookStreamEvent(ctx, in)
	if err != nil {
		committed := s.isCommitted() || lipapi.OutputCommitted(ev)
		if s.executor != nil && s.executor.SecureSessionMetrics != nil {
			s.executor.SecureSessionMetrics.ObserveRecorderStreamEventFailed(committed, s.executor.SecureSessionRecordingMandatory)
		}
		if committed && s.executor.SecureSessionRecordingMandatory {
			s.secureRecvRecordingHardStop = true
		}
	}
	return err
}

func buildStreamEventRecordInput(s *retryRecvStream, ev lipapi.Event) app.StreamEventRecordInput {
	st := s.secureTurn
	now := s.executor.now()
	in := app.StreamEventRecordInput{
		Now:       now,
		TraceID:   strings.TrimSpace(s.traceID),
		SessionID: st.SessionID,
		TurnID:    st.TurnID,
		BLegID:    strings.TrimSpace(s.bleg.BLegID),
		BackendID: strings.TrimSpace(s.cand.Primary.Backend),
		Policy:    st.Policy,
		EventKind: string(ev.Kind),
	}
	if b, err := json.Marshal(streamEventWire(ev)); err == nil {
		in.EventPayloadJSON = string(b)
	}
	if ev.Kind == lipapi.EventUsageDelta || ev.Kind == lipapi.EventResponseFinished {
		in.IsUsageEvent = true
	}
	if ev.Kind == lipapi.EventUsageDelta {
		in.InputTokens = int64(ev.InputTokens)
		in.OutputTokens = int64(ev.OutputTokens)
		in.CacheReadTokens = int64(ev.CacheReadTokens)
		in.CacheWriteTokens = int64(ev.CacheWriteTokens)
		in.NonCachedInputTokens = int64(max(ev.InputTokens-ev.CacheReadTokens-ev.CacheWriteTokens, 0))
		in.ReasoningTokens = int64(ev.ReasoningTokens)
		in.NonReasoningOutputTokens = int64(max(ev.OutputTokens-ev.ReasoningTokens, 0))
		in.TotalTokens = int64(ev.TotalTokens)
		in.CostNanoUnits = ev.CostNanoUnits
		in.Currency = strings.TrimSpace(ev.Currency)
		in.CostSource = strings.TrimSpace(ev.CostSource)
		in.RawUsageJSON = boundRawUsageJSON(strings.TrimSpace(ev.RawUsageJSON))
		if in.InputTokens == 0 && in.OutputTokens == 0 && in.TotalTokens == 0 {
			in.BillingUnavailable = true
		}
	}
	if ev.Kind == lipapi.EventResponseFinished {
		acct := s.accounting.snapshot()
		in.RequestStartedAt = acct.RequestStartedAt
		in.FirstRemoteEventAt = acct.FirstRemoteEventAt
		in.FirstMeaningfulTokenAt = acct.FirstMeaningfulTokenAt
		in.RemoteCompletedAt = acct.RemoteCompletedAt
		in.ProxyCompletedAt = acct.ProxyCompletedAt
		in.TTFTMillis = acct.TTFTMillis
		in.RemoteDurationMillis = acct.RemoteDurationMillis
		in.CompletionDurationMillis = acct.CompletionDurationMillis
		in.CompletionTPSMilli = acct.CompletionTPSMilli
	}
	if pc := providerCorrelationJSON(ev); pc != "" {
		in.ProviderCorrelationJSON = pc
	}
	return in
}

const maxRawUsageJSONBytes = 16 << 10

func boundRawUsageJSON(s string) string {
	if len(s) <= maxRawUsageJSONBytes {
		return s
	}
	return s[:maxRawUsageJSONBytes] + `...{"truncated":true}`
}

func streamEventWire(ev lipapi.Event) map[string]any {
	w := map[string]any{
		"kind":          ev.Kind,
		"message_index": ev.MessageIndex,
	}
	if ev.ToolCallID != "" {
		w["tool_call_id"] = ev.ToolCallID
	}
	if ev.ToolName != "" {
		w["tool_name"] = ev.ToolName
	}
	if ev.Delta != "" {
		// Redact large bodies at the wire snapshot layer (digest only when very long).
		if len(ev.Delta) > 4096 {
			w["delta_digest"] = map[string]any{"len": len(ev.Delta)}
		} else {
			w["delta"] = ev.Delta
		}
	}
	if ev.WarningCode != "" {
		w["warning_code"] = ev.WarningCode
	}
	if ev.ErrorCode != "" {
		w["error_code"] = ev.ErrorCode
	}
	if ev.FinishReason != "" {
		w["finish_reason"] = ev.FinishReason
	}
	return w
}

// providerCorrelationJSON extracts non-authoritative provider correlation hints from canonical events.
func providerCorrelationJSON(ev lipapi.Event) string {
	// Wire snapshot only; never treat as session authority (see secure-session design 1.10–1.11).
	if ev.Kind != lipapi.EventResponseStarted {
		return ""
	}
	if strings.TrimSpace(ev.Delta) == "" {
		return ""
	}
	b, err := json.Marshal(map[string]any{"response_started_hint": strings.TrimSpace(ev.Delta)})
	if err != nil {
		return ""
	}
	return string(b)
}
