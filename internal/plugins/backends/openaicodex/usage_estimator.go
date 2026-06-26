package openaicodex

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tokenizers/imageestimator"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tokenizers/tiktoken"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type usageEstimator struct {
	counter *tiktoken.Counter
}

func newUsageEstimator() (*usageEstimator, error) {
	counter, err := tiktoken.NewCounter(tiktoken.Config{
		DefaultEncoding: "o200k_base",
		Image: tiktoken.ImageConfig{
			UseDefaultTokens: true,
			DefaultTokens:    imageestimator.DefaultBaseTokens,
		},
	})
	if err != nil {
		return nil, err
	}
	return &usageEstimator{counter: counter}, nil
}

func (e *usageEstimator) estimateUsage(ctx context.Context, call lipapi.Call, model, outputText string) (lipapi.Event, error) {
	callResult, err := e.counter.CountCall(ctx, app.CountCallInput{Call: call, Model: model})
	if err != nil {
		return lipapi.Event{}, err
	}
	outputResult, err := e.counter.CountOutput(ctx, app.CountOutputInput{Model: model, Text: outputText})
	if err != nil {
		return lipapi.Event{}, err
	}
	inputTokens := callResult.InputTokens
	outputTokens := outputResult.OutputTokens
	accounting := callResult.Accounting
	accounting.Plane = lipapi.UsagePlaneProviderBillable
	accounting.Tokenizer.ModelUsed = model
	return lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		Accounting:   accounting,
	}, nil
}
