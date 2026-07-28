package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	resp, err := buildWireResponse(ctx, call, es, opts)
	if err != nil {
		return err
	}
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

func buildWireResponse(ctx context.Context, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) (wireResponse, error) {
	order := &nonstreamOutputOrder{}
	col, err := lipapi.Collect(ctx, &orderTeeStream{inner: es, order: order})
	if err != nil {
		return wireResponse{}, err
	}
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	rid := opts.ResponseID
	mid := opts.MessageID
	ts := opts.CreatedAt
	text := col.Text.String()
	msgOut := map[string]any{
		"type":    "message",
		"id":      mid,
		"status":  "completed",
		"role":    "assistant",
		"content": wireMessageContentParts(text, col.AssistantMedia),
	}
	responsesExact := make([]lipapi.ReasoningPart, 0, len(col.ReasoningParts))
	for i := range col.ReasoningParts {
		if lipapi.NormalizeReasoningDialect(col.ReasoningParts[i].Dialect) == lipapi.ReasoningDialectOpenAIResponsesItemV1 {
			responsesExact = append(responsesExact, col.ReasoningParts[i])
		}
	}
	toolsByID := make(map[string]lipapi.ToolCallSummary, len(col.ToolCallOrder))
	for _, tc := range col.OrderedToolCalls() {
		toolsByID[tc.ID] = tc
	}
	out := make([]any, 0, len(order.markers)+1)
	if order.exactCount > 0 {
		for _, m := range order.markers {
			switch m.kind {
			case nonstreamOrderMessage:
				if text != "" || len(col.AssistantMedia) > 0 {
					out = append(out, msgOut)
				}
			case nonstreamOrderExactReasoning:
				if m.exactOrdinal < 0 || m.exactOrdinal >= len(responsesExact) {
					return wireResponse{}, fmt.Errorf("openairesponses: invalid reasoning item")
				}
				item, err := exactReasoningWireObject(&responsesExact[m.exactOrdinal])
				if err != nil {
					return wireResponse{}, err
				}
				out = append(out, item)
			case nonstreamOrderTool:
				tc, ok := toolsByID[m.toolCallID]
				if !ok {
					continue
				}
				out = append(out, map[string]any{
					"type":      "function_call",
					"id":        fcItemID(tc.ID),
					"call_id":   tc.ID,
					"name":      tc.Name,
					"arguments": tc.Arguments,
					"status":    "completed",
				})
			}
		}
	} else {
		if reasoning := col.Reasoning.String(); reasoning != "" {
			out = append(out, map[string]any{
				"type":    "reasoning",
				"id":      "rs_" + rid,
				"status":  "completed",
				"summary": []any{map[string]any{"type": "summary_text", "text": reasoning}},
			})
		}
		if text != "" || len(col.AssistantMedia) > 0 {
			out = append(out, msgOut)
		}
		for _, tc := range col.OrderedToolCalls() {
			out = append(out, map[string]any{
				"type":      "function_call",
				"id":        fcItemID(tc.ID),
				"call_id":   tc.ID,
				"name":      tc.Name,
				"arguments": tc.Arguments,
				"status":    "completed",
			})
		}
	}
	resp := wireResponse{
		ID:        rid,
		Object:    "response",
		CreatedAt: ts,
		Status:    "completed",
		Model:     model,
		Output:    out,
	}
	resp.Usage = wireResponsesUsage(col, opts.ExposeLipUsageExtensions)
	return resp, nil
}
