package refverifier_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refverifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

type collectOK struct{ calls int }

func (a *collectOK) Collect(_ context.Context, r auxiliary.Request) (lipapi.Collected, error) {
	a.calls++
	if r.Role != "verifier" {
		return lipapi.Collected{}, fmt.Errorf("role")
	}
	return lipapi.Collected{}, nil
}

func (a *collectOK) Stream(_ context.Context, _ auxiliary.Request) (lipapi.EventStream, error) {
	return nil, fmt.Errorf("unused")
}

func TestCompletionGate_replaceWhenAuxWired(t *testing.T) {
	t.Parallel()
	g := refverifier.NewCompletionGate(refverifier.Config{SteerText: "pivot"})
	aux := &collectOK{}
	orig := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "upstream"},
		{Kind: lipapi.EventResponseFinished},
	}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{g},
		completion.Meta{TraceID: "tr1"}, orig, false, completion.Services{
			State: state.DisabledStore{},
			Aux:   aux,
		})
	if err != nil {
		t.Fatal(err)
	}
	if aux.calls != 1 {
		t.Fatalf("aux calls %d", aux.calls)
	}
	if len(out) < 1 || out[len(out)-2].Delta != "pivot" {
		t.Fatalf("out %#v", out)
	}
}

func TestCompletionGate_passesWhenAuxDisabled(t *testing.T) {
	t.Parallel()
	g := refverifier.NewCompletionGate(refverifier.Config{})
	orig := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "u"},
		{Kind: lipapi.EventResponseFinished},
	}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{g},
		completion.Meta{TraceID: "tr1"}, orig, false, completion.Services{
			State: state.DisabledStore{},
			Aux:   auxiliary.DisabledClient{},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Delta != "u" {
		t.Fatalf("out %#v", out)
	}
}
