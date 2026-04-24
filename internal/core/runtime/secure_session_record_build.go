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
	lines := make([]app.ClientInputLine, 0, len(call.Instructions)+len(call.Messages))
	ord := 0
	for _, m := range call.Instructions {
		lines = append(lines, clientInputLineFromMessage(m, ord))
		ord++
	}
	for _, m := range call.Messages {
		lines = append(lines, clientInputLineFromMessage(m, ord))
		ord++
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

func clientInputLineFromMessage(m lipapi.Message, ordinal int) app.ClientInputLine {
	kinds := make([]string, 0, len(m.Parts))
	for _, p := range m.Parts {
		kinds = append(kinds, string(p.Kind))
	}
	return app.ClientInputLine{
		Role:    string(m.Role),
		Ordinal: ordinal,
		Parts:   kinds,
	}
}
