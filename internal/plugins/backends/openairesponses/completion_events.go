package openairesponses

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/responses"
)

// CompletionEvents converts a non-streaming Responses API payload into canonical events.
func CompletionEvents(resp responses.Response) ([]lipapi.Event, error) {
	pending := stream.NewPendingEventQueue(0)
	mapper := openairesponsestream.New(&pending)
	cur := responses.ResponseStreamEventUnion{Type: "response.completed", Response: resp}
	if err := handleCompletedUnion(mapper, cur); err != nil {
		return nil, fmt.Errorf("openai-responses: completion events: %w", err)
	}
	return stream.DrainPending(&pending), nil
}

func handleCompletedUnion(m *openairesponsestream.Mapper, cur responses.ResponseStreamEventUnion) error {
	resp := cur.Response
	if err := m.BeginCompleted(); err != nil {
		return err
	}
	if err := m.EmitCompletedOutputByIndex(resp); err != nil {
		return err
	}
	if resp.JSON.Usage.Valid() {
		ev := openaiusage.ResponsesUsageEvent(resp.Usage)
		if err := m.PushUsage(&ev); err != nil {
			return err
		}
	}
	return m.ResponseFinished()
}
