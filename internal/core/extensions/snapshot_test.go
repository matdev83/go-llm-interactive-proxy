package extensions_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

type stubSessionOpener string

func (s stubSessionOpener) ID() string { return string(s) }

func (stubSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

func TestNewRequestRuntimeSnapshot_sessionOpenersIsolatedFromCallerSliceMutation(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	openers := []session.Opener{stubSessionOpener("stable-id")}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{SessionOpeners: openers})
	openers[0] = stubSessionOpener("caller-mutated")
	got := snap.SessionOpeners()
	if len(got) != 1 || got[0].ID() != "stable-id" {
		t.Fatalf("want snapshot to keep original opener id; got %#v", got[0])
	}
}

func TestRequestRuntimeSnapshotFromContext_missing(t *testing.T) {
	t.Parallel()
	if extensions.RequestRuntimeSnapshotFromContext(context.Background()) != nil {
		t.Fatal("want nil without WithRequestRuntimeSnapshot")
	}
}

func TestWithRequestRuntimeSnapshot_roundTrip(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{Generation: 7})
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), snap)
	got := extensions.RequestRuntimeSnapshotFromContext(ctx)
	if got != snap {
		t.Fatal("pointer mismatch")
	}
	if got.HookBus() != bus {
		t.Fatal("bus mismatch")
	}
	if got.Generation() != 7 {
		t.Fatalf("gen %d", got.Generation())
	}
	// Facades are non-nil defaults
	var _ state.Store = got.State() //nolint:staticcheck // QF1011: explicit interface satisfaction check
}

func TestWithRequestRuntimeSnapshot_nilSnap(t *testing.T) {
	t.Parallel()
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), nil)
	if ctx == nil {
		t.Fatal("ctx")
	}
	if extensions.RequestRuntimeSnapshotFromContext(ctx) != nil {
		t.Fatal("want nil snapshot when snap nil")
	}
}

func TestRequestRuntimeSnapshot_SessionOpeners_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		SessionOpeners: []session.Opener{stubSessionOpener("a")},
	})
	got := snap.SessionOpeners()
	if len(got) != 1 || got[0].ID() != "a" {
		t.Fatalf("first SessionOpeners: %+v", got)
	}
	got[0] = stubSessionOpener("mutated")
	again := snap.SessionOpeners()
	if len(again) != 1 || again[0].ID() != "a" {
		t.Fatalf("snapshot openers mutated via returned slice; got %q", again[0].ID())
	}
}

type stubCat struct{}

func (stubCat) ID() string                        { return "c1" }
func (stubCat) Order() int                        { return 0 }
func (stubCat) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubCat) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type stubRtx struct{}

func (stubRtx) ID() string                        { return "r1" }
func (stubRtx) Order() int                        { return 0 }
func (stubRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubRtx) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

func TestRequestRuntimeSnapshot_ToolCatalogFilters_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		ToolCatalogFilters: []toolcatalog.Filter{stubCat{}},
	})
	got := snap.ToolCatalogFilters()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.ToolCatalogFilters()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

func TestRequestRuntimeSnapshot_RequestTransforms_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		RequestTransforms: []request.Transform{stubRtx{}},
	})
	got := snap.RequestTransforms()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.RequestTransforms()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

type stubGate struct{}

func (stubGate) ID() string                        { return "g" }
func (stubGate) Order() int                        { return 0 }
func (stubGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestRequestRuntimeSnapshot_CompletionGates_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{stubGate{}},
	})
	got := snap.CompletionGates()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.CompletionGates()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

type stubTrafficRed struct{}

func (stubTrafficRed) ID() string { return "tr-red" }

func (stubTrafficRed) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

func TestRequestRuntimeSnapshot_TrafficRedactors_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficRedactors: []sdktraffic.Redactor{stubTrafficRed{}},
	})
	got := snap.TrafficRedactors()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.TrafficRedactors()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}
