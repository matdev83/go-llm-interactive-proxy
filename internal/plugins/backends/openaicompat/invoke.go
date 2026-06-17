package openaicompat

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	legacybackend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	responsesbackend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type InvokeRequest struct {
	ProviderID string
	Call       lipapi.Call
	Candidate  routing.AttemptCandidate
	SDKOptions []option.RequestOption
}

func OpenChat(ctx context.Context, cli openai.Client, req InvokeRequest) (lipapi.ManagedEventStream, error) {
	p, err := legacybackend.ParamsForCall(&req.Call, req.Candidate)
	if err != nil {
		return nil, err
	}
	if req.Call.Invocation.TransportMode == lipapi.TransportModeNonStreaming {
		opts := append([]option.RequestOption{}, req.SDKOptions...)
		opts = append(opts, option.WithJSONDel("stream_options"))
		comp, err := cli.Chat.Completions.New(ctx, p, opts...)
		if err != nil {
			return nil, err
		}
		return lipapi.NewFixedEventStream(ChatCompletionEvents(*comp)), nil
	}
	// Empty TransportMode is the legacy default and maps to streaming.
	raw := cli.Chat.Completions.NewStreaming(ctx, p, req.SDKOptions...)
	return NewChatStream(req.ProviderID, raw, req.Call.MaxPendingWireEvents), nil
}

func OpenResponses(ctx context.Context, cli openai.Client, req InvokeRequest) (lipapi.ManagedEventStream, error) {
	p, err := responsesbackend.ParamsForCall(&req.Call, req.Candidate)
	if err != nil {
		return nil, err
	}
	if req.Call.Invocation.TransportMode == lipapi.TransportModeNonStreaming {
		resp, err := cli.Responses.New(ctx, p, req.SDKOptions...)
		if err != nil {
			return nil, err
		}
		events, err := ResponseEvents(*resp)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.ProviderID, err)
		}
		return lipapi.NewFixedEventStream(events), nil
	}
	// Empty TransportMode is the legacy default and maps to streaming.
	raw := cli.Responses.NewStreaming(ctx, p, req.SDKOptions...)
	return NewResponsesStream(req.ProviderID, raw, req.Call.MaxPendingWireEvents), nil
}
