package extensions_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

type auxCollectOK struct {
	calls int
}

func (a *auxCollectOK) Collect(context.Context, auxiliary.Request) (lipapi.Collected, error) {
	a.calls++
	return lipapi.Collected{}, nil
}

func (a *auxCollectOK) Stream(context.Context, auxiliary.Request) (lipapi.EventStream, error) {
	return nil, fmt.Errorf("unused")
}

type gateUsesAux struct{}

func (gateUsesAux) ID() string                        { return "aux-gate" }
func (gateUsesAux) Order() int                        { return 0 }
func (gateUsesAux) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateUsesAux) Handle(ctx context.Context, _ completion.Meta, _ completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	_, err := svc.Aux.Collect(ctx, auxiliary.Request{
		Role:          "verifier",
		ParentTraceID: "trace-1",
		Call:          &lipapi.Call{},
	})
	if err != nil {
		return completion.PassOriginalOutcome(), nil
	}
	return completion.ReplaceOutcome([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "aux-steered"},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

func TestApplyCompletionGateChain_auxInfluencedReplace(t *testing.T) {
	t.Parallel()
	aux := &auxCollectOK{}
	orig := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "upstream"},
		{Kind: lipapi.EventResponseFinished},
	}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateUsesAux{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   aux,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if aux.calls != 1 {
		t.Fatalf("aux calls=%d", aux.calls)
	}
	if len(out) != 4 || out[2].Delta != "aux-steered" {
		t.Fatalf("got %#v", out)
	}
}

func TestApplyCompletionGateChain_auxDisabledPassThrough(t *testing.T) {
	t.Parallel()
	orig := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "upstream"},
		{Kind: lipapi.EventResponseFinished},
	}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateUsesAux{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Delta != "upstream" {
		t.Fatalf("got %#v", out)
	}
}
