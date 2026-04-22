package acp

import (
	"context"
	"encoding/json"
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
	content, _ := upd["content"].(map[string]any)

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

// parseNDJSONLine maps one NDJSON line to lipapi events (session/update, terminal prompt result, errors).
func parseNDJSONLine(ctx context.Context, o SessionUpdateMapperOptions, line string, promptRPCID int64) ([]lipapi.Event, error) {
	var probe map[string]any
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
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
	var idFloat float64
	switch v := idVal.(type) {
	case float64:
		idFloat = v
	default:
		return nil, nil
	}
	if int64(idFloat) != promptRPCID {
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
