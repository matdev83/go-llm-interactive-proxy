package codex

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func debugTurnsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("LIP_DEBUG_TURNS")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("LIP_DEBUG_TURNS")), "true")
}

func stableCallID(call *lipapi.Call) string {
	if call == nil {
		return ""
	}
	if id := strings.TrimSpace(call.ID); id != "" {
		return id
	}
	return strings.TrimSpace(call.Session.ALegID)
}

func appendLimited(items []string, value string, max int) []string {
	value = strings.TrimSpace(value)
	if value == "" || max <= 0 {
		return items
	}
	for _, existing := range items {
		if existing == value {
			return items
		}
	}
	if len(items) >= max {
		return items
	}
	return append(items, value)
}

func stableCounts(counts map[string]int) []string {
	if len(counts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k)
	}
	return out
}

func logPayloadShape(ctx context.Context, call *lipapi.Call, payload Payload) {
	if !debugTurnsEnabled() || call == nil {
		return
	}
	raw, _ := json.Marshal(payload)
	summary := summarizePayload(payload)
	slog.DebugContext(ctx, "codex.debug.payload",
		"call_id", call.ID,
		"trace_id", stableCallID(call),
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
		"verbosity", payloadVerbosity(payload),
		"parallel_tool_calls", boolPtrString(payload.ParallelToolCalls),
	)
}

func logFirstEventWait(ctx context.Context, call lipapi.Call, model string, start time.Time, ev lipapi.Event, err error) {
	if !debugTurnsEnabled() {
		return
	}
	attrs := []any{
		"call_id", call.ID,
		"trace_id", stableCallID(&call),
		"a_leg_id", strings.TrimSpace(call.Session.ALegID),
		"model", model,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if err != nil {
		attrs = append(attrs, "status", "error", "error", err.Error())
	} else {
		attrs = append(attrs, "status", "ok", "event_kind", string(ev.Kind))
	}
	slog.DebugContext(ctx, "codex.debug.first_event", attrs...)
}

func logWSContinuation(ctx context.Context, call lipapi.Call, model, mode string, inputBefore, inputAfter int, previousResponseID string) {
	if !debugTurnsEnabled() {
		return
	}
	slog.DebugContext(ctx, "codex.debug.ws_continuation",
		"call_id", call.ID,
		"trace_id", stableCallID(&call),
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
			functionCallIDs = appendLimited(functionCallIDs, v.CallID, 12)
		case functionCallOutputItem:
			typeCounts[v.Type]++
			inputTextBytes += len(v.Output)
			functionOutputIDs = appendLimited(functionOutputIDs, v.CallID, 12)
		default:
			typeCounts["unknown"]++
		}
	}
	toolNames := make([]string, 0, min(len(payload.Tools), 12))
	for _, tool := range payload.Tools {
		toolNames = appendLimited(toolNames, tool.Name, 12)
	}
	return payloadSummary{
		inputTypes:        stableCounts(typeCounts),
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

func payloadVerbosity(payload Payload) string {
	if payload.Text == nil {
		return ""
	}
	return string(payload.Text.Verbosity)
}

func boolPtrString(v *bool) string {
	if v == nil {
		return ""
	}
	return strconv.FormatBool(*v)
}
