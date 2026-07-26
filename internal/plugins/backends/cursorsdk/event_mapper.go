package cursorsdk

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type mapResult struct {
	events   []lipapi.Event
	terminal bool
	success  bool
	err      error
}

func mapBridgeEvent(f *protocol.Frame, runID string, expectSeq int64, apiKey string) (mapResult, int64) {
	if f == nil {
		return mapResult{err: fmt.Errorf("cursorsdk: nil event frame")}, expectSeq
	}
	if f.Type != protocol.TypeEvent {
		return mapResult{err: fmt.Errorf("cursorsdk: expected event frame")}, expectSeq
	}
	if f.RunID != runID {
		return mapResult{err: fmt.Errorf("cursorsdk: runId mismatch")}, expectSeq
	}
	if f.Seq == nil || *f.Seq != expectSeq {
		return mapResult{err: fmt.Errorf("cursorsdk: %s", protocol.ErrorSequenceRegression)}, expectSeq
	}
	next := expectSeq + 1

	switch f.Kind {
	case protocol.KindActivity:
		return mapResult{}, next
	case protocol.KindTextDelta:
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return mapResult{err: fmt.Errorf("cursorsdk: text_delta payload: %w", err)}, expectSeq
		}
		ev := lipapi.Event{Kind: lipapi.EventTextDelta, MessageIndex: 0, Delta: p.Text}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return mapResult{err: err}, expectSeq
		}
		return mapResult{events: []lipapi.Event{ev}}, next
	case protocol.KindReasoningDelta:
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return mapResult{err: fmt.Errorf("cursorsdk: reasoning_delta payload: %w", err)}, expectSeq
		}
		ev := lipapi.Event{Kind: lipapi.EventReasoningDelta, MessageIndex: 0, Delta: p.Text}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return mapResult{err: err}, expectSeq
		}
		return mapResult{events: []lipapi.Event{ev}}, next
	case protocol.KindUsage:
		ev, err := mapUsageEvent(f.Payload)
		if err != nil {
			return mapResult{err: err}, expectSeq
		}
		if ev == nil {
			return mapResult{}, next
		}
		if err := lipapi.ValidateEventEnvelope(ev); err != nil {
			return mapResult{err: err}, expectSeq
		}
		return mapResult{events: []lipapi.Event{*ev}}, next
	case protocol.KindWarning:
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(f.Payload, &p)
		msg := sanitizeWarningMessage(p.Message, apiKey)
		ev := lipapi.Event{Kind: lipapi.EventWarning, WarningCode: "cursor_sdk_warning", WarningMessage: msg}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return mapResult{err: err}, expectSeq
		}
		return mapResult{events: []lipapi.Event{ev}}, next
	case protocol.KindFinished:
		var p struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(f.Payload, &p)
		status := strings.TrimSpace(p.Status)
		success := status == "" || status == "finished"
		ev := lipapi.Event{Kind: lipapi.EventResponseFinished}
		if !success {
			ev.FinishReason = status
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return mapResult{err: err}, expectSeq
		}
		return mapResult{events: []lipapi.Event{ev}, terminal: true, success: success}, next
	case protocol.KindError:
		var p struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		_ = json.Unmarshal(f.Payload, &p)
		msg := sanitizeWarningMessage(p.Message, apiKey)
		code := strings.TrimSpace(p.Code)
		if code == "" {
			code = "cursor_sdk_run_error"
		}
		if len(code) > lipapi.MaxEventCodeFieldBytes {
			code = code[:lipapi.MaxEventCodeFieldBytes]
		}
		ev := lipapi.Event{Kind: lipapi.EventError, ErrorCode: code, ErrorMessage: msg}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return mapResult{err: err}, expectSeq
		}
		return mapResult{events: []lipapi.Event{ev}, terminal: true, success: false}, next
	default:
		return mapResult{err: fmt.Errorf("cursorsdk: %s", protocol.ErrorUnknownEventKind)}, expectSeq
	}
}

func mapUsageEvent(raw json.RawMessage) (*lipapi.Event, error) {
	var p struct {
		InputTokens      *int `json:"inputTokens"`
		OutputTokens     *int `json:"outputTokens"`
		TotalTokens      *int `json:"totalTokens"`
		CacheReadTokens  *int `json:"cacheReadTokens"`
		CacheWriteTokens *int `json:"cacheWriteTokens"`
		ReasoningTokens  *int `json:"reasoningTokens"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("cursorsdk: usage payload: %w", err)
	}
	if p.InputTokens == nil || p.OutputTokens == nil || p.TotalTokens == nil {
		return nil, nil
	}
	if *p.InputTokens < 0 || *p.OutputTokens < 0 || *p.TotalTokens < 0 {
		return nil, nil
	}
	ev := &lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  *p.InputTokens,
		OutputTokens: *p.OutputTokens,
		TotalTokens:  *p.TotalTokens,
		UsagePresence: lipapi.UsagePresence{
			InputTokens:  true,
			OutputTokens: true,
			TotalTokens:  true,
		},
	}
	if p.CacheReadTokens != nil && *p.CacheReadTokens >= 0 {
		ev.CacheReadTokens = *p.CacheReadTokens
		ev.UsagePresence.CacheReadTokens = true
	}
	if p.CacheWriteTokens != nil && *p.CacheWriteTokens >= 0 {
		ev.CacheWriteTokens = *p.CacheWriteTokens
		ev.UsagePresence.CacheWriteTokens = true
	}
	if p.ReasoningTokens != nil && *p.ReasoningTokens >= 0 {
		ev.ReasoningTokens = *p.ReasoningTokens
		ev.UsagePresence.ReasoningTokens = true
	}
	return ev, nil
}
