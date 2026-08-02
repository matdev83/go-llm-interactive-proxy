package runtime

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func buildClientTurnRecordInput(now time.Time, traceID string, br app.BeginResult, call *lipapi.Call) app.ClientTurnRecordInput {
	if call == nil {
		return app.ClientTurnRecordInput{
			Now:       now,
			TraceID:   strings.TrimSpace(traceID),
			SessionID: br.Record.SessionID,
			TurnID:    br.TurnID,
			Policy:    br.EffectivePolicy,
		}
	}
	items := lipapi.NormalizedItems(*call)
	lines := make([]app.ClientInputLine, 0, len(items))
	for ord, item := range items {
		lines = append(lines, clientInputLineFromItem(item, ord))
	}
	return app.ClientTurnRecordInput{
		Now:       now,
		TraceID:   strings.TrimSpace(traceID),
		SessionID: br.Record.SessionID,
		TurnID:    br.TurnID,
		Policy:    br.EffectivePolicy,
		Lines:     lines,
	}
}

func clientInputLineFromItem(item lipapi.Item, ordinal int) app.ClientInputLine {
	if item.Kind == lipapi.ItemKindMessage {
		kinds := make([]string, 0, len(item.Content))
		for _, cp := range item.Content {
			kinds = append(kinds, string(cp.Kind))
		}
		return app.ClientInputLine{
			Role:    string(item.Role),
			Ordinal: ordinal,
			Parts:   kinds,
		}
	}
	return app.ClientInputLine{
		Role:    string(item.Kind),
		Ordinal: ordinal,
		Parts:   []string{string(item.Kind)},
	}
}
