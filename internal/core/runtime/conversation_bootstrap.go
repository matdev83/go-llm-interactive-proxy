package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// InitialModelIntent is logical route intent, before any candidate/native binding.
type InitialModelIntent struct {
	Model     string
	Ambiguous bool
}

// ConversationBootstrap runs before the normal snapshot. resolve must only be
// invoked inside the store's undecided callback; reused decisions need no routing.
type ConversationBootstrap func(context.Context, string, func() (InitialModelIntent, error)) error

func (e *Executor) initialModelIntent(raw string) (InitialModelIntent, error) {
	sel, err := routing.CompileSelector(raw, e.SelectorAliases, e.DefaultBackend)
	if err != nil {
		return InitialModelIntent{}, err
	}
	var out InitialModelIntent
	add := func(p routing.Primary) {
		model := strings.TrimSpace(p.Model)
		if out.Model == "" {
			out.Model = model
		} else if model != out.Model {
			out.Ambiguous = true
		}
	}
	parallel := func(p *routing.Parallel) {
		for _, branch := range p.Branches {
			add(branch.Target)
		}
	}
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			add(*alt.Primary)
		case alt.Parallel != nil:
			parallel(alt.Parallel)
		case alt.Weighted != nil:
			for _, branch := range alt.Weighted.Branches {
				if branch.Parallel != nil {
					parallel(branch.Parallel)
				} else {
					add(branch.Target)
				}
			}
		}
	}
	if out.Model == "" {
		return InitialModelIntent{}, fmt.Errorf("executor: empty initial model intent")
	}
	if out.Ambiguous {
		out.Model = ""
	}
	return out, nil
}

func (e *Executor) selectLocalAndBootstrap(ctx context.Context, ibt *identityBoundTurn, accepted lipapi.Call) error {
	ctx = ibt.projectContext(ctx)
	selection, err := e.selectLocalTurn(ctx, *ibt.ingressCall, ibt.aLeg.ALegID, ibt.traceID)
	if err != nil {
		return err
	}
	ibt.localSelection = selection
	if selection.handler != nil || e.ConversationBootstrap == nil {
		return nil
	}
	selector := accepted.Route.Selector
	if ibt.routeAuth.active() {
		selector = ibt.routeAuth.State.Selector
	}
	return e.ConversationBootstrap(ctx, ibt.aLeg.ALegID, func() (InitialModelIntent, error) {
		return e.initialModelIntent(selector)
	})
}
