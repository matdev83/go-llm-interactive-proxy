package extensions_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

type gateID string

func (g gateID) ID() string                      { return string(g) }
func (gateID) Order() int                        { return 0 }
func (gateID) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateID) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestCompletionGatesFromContext_nilCtxUsesFallback(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{gateID("fb")},
	})
	got := extensions.CompletionGatesFromContext(nil, fallback)
	if len(got) != 1 || got[0].ID() != "fb" {
		t.Fatalf("got %+v", got)
	}
}

func TestCompletionGatesFromContext_prefersContextOverFallback(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	ctxSnap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{gateID("ctx")},
	})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{gateID("fb")},
	})
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), ctxSnap)
	got := extensions.CompletionGatesFromContext(ctx, fallback)
	if len(got) != 1 || got[0].ID() != "ctx" {
		t.Fatalf("want ctx gate, got %+v", got)
	}
}

func TestCompletionGatesFromContext_missingContextUsesFallback(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{gateID("fb")},
	})
	got := extensions.CompletionGatesFromContext(context.Background(), fallback)
	if len(got) != 1 || got[0].ID() != "fb" {
		t.Fatalf("got %+v", got)
	}
}

func TestCompletionGatesFromContext_nilFallback(t *testing.T) {
	t.Parallel()
	got := extensions.CompletionGatesFromContext(context.Background(), nil)
	if len(got) != 0 {
		t.Fatalf("want empty, got len=%d", len(got))
	}
	if got == nil {
		t.Fatal("want non-nil empty slice")
	}
}

type otherGatesView struct {
	gates []completion.Gate
}

func (o otherGatesView) CompletionGates() []completion.Gate {
	return o.gates
}

func TestCompletionGatesFromContext_fallbackIsInterface(t *testing.T) {
	t.Parallel()
	fallback := otherGatesView{gates: []completion.Gate{gateID("alt")}}
	got := extensions.CompletionGatesFromContext(context.Background(), fallback)
	if len(got) != 1 || got[0].ID() != "alt" {
		t.Fatalf("got %+v", got)
	}
}

func TestRequestRuntimeSnapshot_TrafficPortBundle_nil(t *testing.T) {
	t.Parallel()
	var snap *extensions.RequestRuntimeSnapshot
	b := snap.TrafficPortBundle()
	if !b.EmitIsNoop() {
		t.Fatal("nil snapshot traffic bundle should be noop")
	}
}

func TestRequestRuntimeSnapshot_TrafficPortBundle_matchesPortBundleFromSnapshot(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver:  sdktraffic.NoopObserver{},
		TrafficRedactors: []sdktraffic.Redactor{stubTrafficRed{}},
	})
	got := snap.TrafficPortBundle()
	want := coretraffic.PortBundleFromSnapshot(snap)
	if got.Raw != want.Raw || got.Obs != want.Obs || len(got.Red) != len(want.Red) {
		t.Fatalf("got vs want Raw=%v Obs=%v lens red %d/%d", got.Raw, got.Obs, len(got.Red), len(want.Red))
	}
	for i := range got.Red {
		if got.Red[i] != want.Red[i] {
			t.Fatalf("redactor %d ptr mismatch", i)
		}
	}
}
