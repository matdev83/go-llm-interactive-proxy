package appserver

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Engine struct {
	open      func(context.Context, *lipapi.Call) (lipapi.ManagedEventStream, error)
	closeFn   func() error
	inventory modelinventory.Provider
	exeCache  *acp.ExecutableCache
	exePath   string
	Caps      lipapi.BackendCaps
}

func (e *Engine) Open(ctx context.Context, call *lipapi.Call) (lipapi.ManagedEventStream, error) {
	if e == nil || e.open == nil {
		return nil, lipapi.ErrNilContext
	}
	return e.open(ctx, call)
}

func (e *Engine) Close() error {
	if e == nil || e.closeFn == nil {
		return nil
	}
	return e.closeFn()
}

func (e *Engine) Inventory() modelinventory.Provider {
	if e == nil {
		return nil
	}
	return e.inventory
}

func (e *Engine) ExecutableCache() *acp.ExecutableCache { return e.exeCache }
func (e *Engine) ResolvedExecutable() string            { return e.exePath }
