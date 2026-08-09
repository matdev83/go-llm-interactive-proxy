package codex

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/routingstub"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Engine struct {
	rt        *backendRuntime
	inventory modelinventory.Provider
	cfgErr    error
}

func (e *Engine) Open(ctx context.Context, call *lipapi.Call, cand routingstub.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	if e == nil {
		return nil, lipapi.ErrNilContext
	}
	if e.cfgErr != nil {
		return nil, e.cfgErr
	}
	if e.rt == nil || call == nil {
		return nil, lipapi.ErrNilContext
	}
	return e.rt.open(ctx, *call, cand)
}

func (e *Engine) Inventory() modelinventory.Provider {
	if e == nil {
		return nil
	}
	return e.inventory
}

func (e *Engine) Caps() lipapi.BackendCaps { return backendCaps }

func (e *Engine) Close() error {
	if e == nil || e.rt == nil {
		return nil
	}
	e.rt.mu.Lock()
	native := e.rt.native
	e.rt.native = nil
	e.rt.mu.Unlock()
	if native != nil {
		native.Close()
	}
	return nil
}
