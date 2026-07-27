package product

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Engine struct {
	open      func(context.Context, *lipapi.Call) (lipapi.ManagedEventStream, error)
	closeFn   func() error
	exeCache  *acp.ExecutableCache
	exePath   string
	Inventory modelinventory.Provider
	Caps      lipapi.BackendCaps
}

func (e *Engine) ResolvedExecutable() string {
	if e == nil {
		return ""
	}
	return e.exePath
}

func (e *Engine) ExecutableCache() *acp.ExecutableCache {
	if e == nil {
		return nil
	}
	return e.exeCache
}

func (e *Engine) Open(ctx context.Context, call *lipapi.Call) (lipapi.ManagedEventStream, error) {
	return e.open(ctx, call)
}
func (e *Engine) Close() error {
	if e.closeFn == nil {
		return nil
	}
	return e.closeFn()
}
