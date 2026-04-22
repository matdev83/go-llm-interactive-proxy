package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	acpUpdateMethod      = "session/update"
	acpSessionUpdatePlan = "plan"
	acpSessionUpdateMode = "current_mode_update"
	acpAgentMessageChunk = "agent_message_chunk"
	acpAgentThoughtChunk = "agent_thought_chunk"
	acpToolCall          = "tool_call"
	acpToolCallUpdate    = "tool_call_update"
)

// SessionUpdateMapperOptions controls how ACP session/update notifications map to lipapi events.
type SessionUpdateMapperOptions struct {
	// DisableAgentThought suppresses agent_thought_chunk → EventReasoningDelta.
	DisableAgentThought bool
	// DisablePlanReasoning emits plan/mode progress as EventWarning instead of EventReasoningDelta.
	DisablePlanReasoning bool
	ToolSink             ToolUpdateSink
}

func defaultSessionUpdateMapperOptions() SessionUpdateMapperOptions {
	return SessionUpdateMapperOptions{}
}

func mergeMapperOptions(cfg Config) SessionUpdateMapperOptions {
	def := SessionUpdateMapperOptions{}
	u := cfg.SessionUpdate
	out := SessionUpdateMapperOptions{
		DisableAgentThought:  u.DisableAgentThought,
		DisablePlanReasoning: u.DisablePlanReasoning,
		ToolSink:             u.ToolSink,
	}
	if out.ToolSink == nil {
		out.ToolSink = def.ToolSink
	}
	return out
}

func (o SessionUpdateMapperOptions) emitThought() bool {
	return !o.DisableAgentThought
}

func (o SessionUpdateMapperOptions) planAsReasoning() bool {
	return !o.DisablePlanReasoning
}

func textFromACPContent(content map[string]any) string {
	if content == nil {
		return ""
	}
	if td, ok := content["textDelta"].(string); ok && strings.TrimSpace(td) != "" {
		return td
	}
	if typ, ok := content["type"].(string); ok && typ == "text" {
		if raw, ok := content["text"].(string); ok {
			return raw
		}
	}
	return ""
}

func mapSessionUpdateToEvents(ctx context.Context, o SessionUpdateMapperOptions, upd map[string]any) ([]lipapi.Event, error) {
	kind, ok := upd["sessionUpdate"].(string)
	if !ok || kind == "" {
		return nil, nil
	}
	var content map[string]any
	if c, ok := upd["content"].(map[string]any); ok {
		content = c
	}
	// When !ok (wrong JSON shape), textFromACPContent(nil) drops text — same as missing content.

	switch kind {
	case acpAgentMessageChunk:
		text := textFromACPContent(content)
		if text == "" {
			return nil, nil
		}
		return []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: text}}, nil

	case acpAgentThoughtChunk:
		if !o.emitThought() {
			return nil, nil
		}
		text := textFromACPContent(content)
		if text == "" {
			return nil, nil
		}
		return []lipapi.Event{{Kind: lipapi.EventReasoningDelta, Delta: text}}, nil

	case acpSessionUpdatePlan:
		line := planProgressLine(upd)
		if line == "" {
			return nil, nil
		}
		if o.planAsReasoning() {
			return []lipapi.Event{{Kind: lipapi.EventReasoningDelta, Delta: line}}, nil
		}
		return []lipapi.Event{{Kind: lipapi.EventWarning, WarningCode: "acp_plan", WarningMessage: strings.TrimSpace(line)}}, nil

	case acpSessionUpdateMode:
		line := modeProgressLine(upd)
		if line == "" {
			return nil, nil
		}
		if o.planAsReasoning() {
			return []lipapi.Event{{Kind: lipapi.EventReasoningDelta, Delta: line}}, nil
		}
		return []lipapi.Event{{Kind: lipapi.EventWarning, WarningCode: "acp_mode", WarningMessage: strings.TrimSpace(line)}}, nil

	case acpToolCall, acpToolCallUpdate:
		if o.ToolSink != nil {
			evs, err := o.ToolSink.HandleToolUpdate(ctx, kind, upd)
			if err != nil || len(evs) > 0 {
				return evs, err
			}
		}
		return []lipapi.Event{{
			Kind:           lipapi.EventWarning,
			WarningCode:    "acp_tool_unmapped",
			WarningMessage: "ACP tool update received; configure ToolUpdateSink for full handling",
		}}, nil
	default:
		return nil, nil
	}
}

func planProgressLine(upd map[string]any) string {
	if t, ok := upd["title"].(string); ok && strings.TrimSpace(t) != "" {
		return "[plan] " + strings.TrimSpace(t) + "\n"
	}
	return "[plan]\n"
}

func modeProgressLine(upd map[string]any) string {
	var mode string
	if m, ok := upd["modeId"].(string); ok {
		mode = m
	}
	if mode == "" {
		if m, ok := upd["mode"].(string); ok {
			mode = m
		}
	}
	if strings.TrimSpace(mode) != "" {
		return "[mode] " + strings.TrimSpace(mode) + "\n"
	}
	return "[mode]\n"
}

// decodeProbeLine unmarshals one JSON value into a map. UseNumber is set so JSON numbers stay
// [json.Number] and very large integer "id" values (beyond float53 precision) match [jsonRPCIDEqual]
// against int64 prompt ids.
func decodeProbeLine(line string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var probe map[string]any
	if err := dec.Decode(&probe); err != nil {
		return nil, fmt.Errorf("acp: decode probe line: %w", err)
	}
	return probe, nil
}

// jsonRPCIDEqual reports whether a JSON-RPC "id" (from encoding/json) matches want.
// It supports [json.Number] (preferred), string, and exact float64 integers; arbitrary float64
// fractional values are rejected.
func jsonRPCIDEqual(id any, want int64) bool {
	if id == nil {
		return false
	}
	wantStr := strconv.FormatInt(want, 10)
	switch v := id.(type) {
	case json.Number:
		if strings.TrimSpace(v.String()) == wantStr {
			return true
		}
		n, err := v.Int64()
		return err == nil && n == want
	case string:
		s := strings.TrimSpace(v)
		if s == wantStr {
			return true
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return err == nil && n == want
	case float64:
		// Only exact integers; large values that rounded in float64 will not match wrong want.
		n := int64(v)
		if float64(n) != v {
			return false
		}
		return n == want
	default:
		return false
	}
}

// parseNDJSONLine maps one NDJSON line to lipapi events (session/update, terminal prompt result, errors).
func parseNDJSONLine(ctx context.Context, o SessionUpdateMapperOptions, line string, promptRPCID int64) ([]lipapi.Event, error) {
	probe, err := decodeProbeLine(line)
	if err != nil {
		return nil, err
	}

	if errObj, ok := probe["error"].(map[string]any); ok && probe["method"] == nil {
		msg := "unknown error"
		if m, ok := errObj["message"].(string); ok && m != "" {
			msg = m
		}
		return []lipapi.Event{
			{Kind: lipapi.EventError, ErrorMessage: msg},
			{Kind: lipapi.EventResponseFinished},
		}, nil
	}

	if method, ok := probe["method"].(string); ok && method == acpUpdateMethod {
		params, ok := probe["params"].(map[string]any)
		if !ok || params == nil {
			return nil, nil
		}
		upd, ok := params["update"].(map[string]any)
		if !ok || upd == nil {
			return nil, nil
		}
		return mapSessionUpdateToEvents(ctx, o, upd)
	}

	idVal, hasID := probe["id"]
	if !hasID || idVal == nil {
		return nil, nil
	}
	if !jsonRPCIDEqual(idVal, promptRPCID) {
		return nil, nil
	}

	if errObj, ok := probe["error"].(map[string]any); ok {
		msg := "unknown error"
		if m, ok := errObj["message"].(string); ok && m != "" {
			msg = m
		}
		return []lipapi.Event{
			{Kind: lipapi.EventError, ErrorMessage: msg},
			{Kind: lipapi.EventResponseFinished},
		}, nil
	}

	if _, ok := probe["result"].(map[string]any); ok {
		return []lipapi.Event{{Kind: lipapi.EventResponseFinished}}, nil
	}

	return nil, nil
}
