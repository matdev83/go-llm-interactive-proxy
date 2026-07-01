package openaicodex

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func debugTurnsEnabled() bool {
	return diag.DebugTurnsEnabled()
}

func logPayloadShape(ctx context.Context, call *lipapi.Call, payload Payload) {
	if !debugTurnsEnabled() || call == nil {
		return
	}
	raw, _ := json.Marshal(payload)
	summary := summarizePayload(payload)
	slog.DebugContext(ctx, "openaicodex.debug.payload",
		"call_id", call.ID,
		"trace_id", diag.StableCallID(call),
		"a_leg_id", strings.TrimSpace(call.Session.ALegID),
		"model", payload.Model,
		"payload_bytes", len(raw),
		"instructions_bytes", len(payload.Instructions),
		"input_text_bytes", summary.inputTextBytes,
		"input_items", len(payload.Input),
		"input_types", strings.Join(summary.inputTypes, ","),
		"function_call_ids", strings.Join(summary.functionCallIDs, ","),
		"function_output_ids", strings.Join(summary.functionOutputIDs, ","),
		"tools", len(payload.Tools),
		"tool_names", strings.Join(summary.toolNames, ","),
		"reasoning_effort", reasoningEffort(payload),
		"parallel_tool_calls", boolPtrString(payload.ParallelToolCalls),
	)
}

func logFirstEventWait(ctx context.Context, call lipapi.Call, model string, start time.Time, ev lipapi.Event, err error) {
	if !debugTurnsEnabled() {
		return
	}
	attrs := []any{
		"call_id", call.ID,
		"trace_id", diag.StableCallID(&call),
		"a_leg_id", strings.TrimSpace(call.Session.ALegID),
		"model", model,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if err != nil {
		attrs = append(attrs, "status", "error", "error", err.Error())
	} else {
		attrs = append(attrs, "status", "ok", "event_kind", string(ev.Kind))
	}
	slog.DebugContext(ctx, "openaicodex.debug.first_event", attrs...)
}

func logWSContinuation(ctx context.Context, call lipapi.Call, model, mode string, inputBefore, inputAfter int, previousResponseID string) {
	if !debugTurnsEnabled() {
		return
	}
	slog.DebugContext(ctx, "openaicodex.debug.ws_continuation",
		"call_id", call.ID,
		"trace_id", diag.StableCallID(&call),
		"a_leg_id", strings.TrimSpace(call.Session.ALegID),
		"model", model,
		"mode", mode,
		"input_before", inputBefore,
		"input_after", inputAfter,
		"previous_response_id", previousResponseID,
	)
}

type payloadSummary struct {
	inputTypes        []string
	functionCallIDs   []string
	functionOutputIDs []string
	toolNames         []string
	inputTextBytes    int
}

func summarizePayload(payload Payload) payloadSummary {
	typeCounts := map[string]int{}
	var functionCallIDs []string
	var functionOutputIDs []string
	inputTextBytes := 0
	for _, item := range payload.Input {
		switch v := item.(type) {
		case textMessageItem:
			typeCounts[v.Type+":"+v.Role]++
			inputTextBytes += len(v.Content)
		case richMessageItem:
			typeCounts[v.Type+":"+v.Role]++
			inputTextBytes += richMessageTextBytes(v)
		case functionCallItem:
			typeCounts[v.Type]++
			inputTextBytes += len(v.Arguments)
			functionCallIDs = diag.AppendLimited(functionCallIDs, v.CallID, 12)
		case functionCallOutputItem:
			typeCounts[v.Type]++
			inputTextBytes += len(v.Output)
			functionOutputIDs = diag.AppendLimited(functionOutputIDs, v.CallID, 12)
		default:
			typeCounts["unknown"]++
		}
	}
	toolNames := make([]string, 0, min(len(payload.Tools), 12))
	for _, tool := range payload.Tools {
		toolNames = diag.AppendLimited(toolNames, tool.Name, 12)
	}
	return payloadSummary{
		inputTypes:        diag.StableCounts(typeCounts),
		functionCallIDs:   functionCallIDs,
		functionOutputIDs: functionOutputIDs,
		toolNames:         toolNames,
		inputTextBytes:    inputTextBytes,
	}
}

func richMessageTextBytes(item richMessageItem) int {
	total := 0
	for _, block := range item.Content {
		switch v := block.(type) {
		case inputTextPart:
			total += len(v.Text)
		case inputImagePart:
			total += len(v.ImageURL)
		case inputFilePart:
			total += len(v.FileData) + len(v.Filename)
		}
	}
	return total
}

func reasoningEffort(payload Payload) string {
	if payload.Reasoning == nil {
		return ""
	}
	return payload.Reasoning.Effort
}

func boolPtrString(v *bool) string {
	if v == nil {
		return ""
	}
	return strconv.FormatBool(*v)
}
