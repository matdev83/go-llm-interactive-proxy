package usage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

type seqObs struct {
	id    string
	calls *[]string
	err   error
}

func (o seqObs) OnUsage(_ context.Context, _ usage.Event) error {
	if o.calls != nil {
		*o.calls = append(*o.calls, o.id)
	}
	return o.err
}

func TestChainObservers_dropsNilObservers(t *testing.T) {
	t.Parallel()
	var calls []string
	ch := usage.ChainObservers(nil, seqObs{id: "a", calls: &calls}, nil, seqObs{id: "b", calls: &calls})
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestChainObservers_invokesInRegistrationOrder(t *testing.T) {
	t.Parallel()
	var calls []string
	ch := usage.ChainObservers(seqObs{id: "first", calls: &calls}, seqObs{id: "second", calls: &calls})
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "first" || calls[1] != "second" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestChainObservers_firstObserverErrorShortCircuits(t *testing.T) {
	t.Parallel()
	var calls []string
	wantErr := errors.New("stop")
	ch := usage.ChainObservers(seqObs{id: "fail", calls: &calls, err: wantErr}, seqObs{id: "skipped", calls: &calls})
	err := ch.OnUsage(context.Background(), usage.Event{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got %v", err)
	}
	if len(calls) != 1 || calls[0] != "fail" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestChainObservers_emptyChainIsNoOp(t *testing.T) {
	t.Parallel()
	ch := usage.ChainObservers()
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestChainObservers_allNilBehavesAsNoOp(t *testing.T) {
	t.Parallel()
	ch := usage.ChainObservers(nil, nil)
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
}
