package service

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
		resolved, err := i.resolveModel(ctx, inv)
		if err != nil {
			return nil, err
		}
		return i.router.Open(ctx, call, resolved)
	})
}
