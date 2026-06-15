package openrouter

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	legacybackend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
)

func openChatStream(ctx context.Context, cli openai.Client, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	p, err := legacybackend.ParamsForCall(&call, cand)
	if err != nil {
		return nil, err
	}
	raw := cli.Chat.Completions.NewStreaming(ctx, p)
	return newChatStream(raw, call.MaxPendingWireEvents), nil
}
