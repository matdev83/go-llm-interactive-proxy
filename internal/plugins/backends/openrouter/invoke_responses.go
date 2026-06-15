package openrouter

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	responsesbackend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
)

func openResponsesStream(ctx context.Context, cli openai.Client, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	p, err := responsesbackend.ParamsForCall(&call, cand)
	if err != nil {
		return nil, err
	}
	raw := cli.Responses.NewStreaming(ctx, p)
	return newResponsesStream(raw, call.MaxPendingWireEvents), nil
}
