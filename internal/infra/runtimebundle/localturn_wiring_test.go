package runtimebundle

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
)

type wiringHandler struct {
	id  string
	ord int
}

func (h wiringHandler) ID() string                        { return h.id }
func (h wiringHandler) Order() int                        { return h.ord }
func (h wiringHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h wiringHandler) Match(_ context.Context, _ lipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h wiringHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "ok"}, nil
}

func TestLocalTurn_ProductionWiring_SortedFrozen(t *testing.T) {
	t.Parallel()
	// Two handlers unordered, expect snapshot sorted by Order then ID
	hA := wiringHandler{id: "b", ord: 2}
	hB := wiringHandler{id: "a", ord: 1}
	hC := wiringHandler{id: "c", ord: 1}
	b1 := lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, LocalTurnHandlers: []localturn.Handler{hA}}
	b2 := lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, LocalTurnHandlers: []localturn.Handler{hB, hC}}
	merged := featurebundle.MergeBundles(b1, b2)
	if len(merged.LocalTurnHandlers) != 3 {
		t.Fatalf("merged %d want 3", len(merged.LocalTurnHandlers))
	}
	ext := extensionsFromMerged(merged, nil)
	if len(ext.LocalTurnHandlers) != 3 {
		t.Fatalf("extensions %d want 3", len(ext.LocalTurnHandlers))
	}
	// Build snapshot via production path NewRequestRuntimeSnapshot with LocalTurnHandlers
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{LocalTurnHandlers: ext.LocalTurnHandlers})
	got := snap.LocalTurnHandlers()
	if len(got) != 3 {
		t.Fatalf("snapshot %d want 3", len(got))
	}
	// Sorted frozen: a(1), c(1), b(2)
	if got[0].ID() != "a" || got[1].ID() != "c" || got[2].ID() != "b" {
		t.Fatalf("sorted %v", []string{got[0].ID(), got[1].ID(), got[2].ID()})
	}
	// Frozen: mutating source slice must not affect snapshot
	ext.LocalTurnHandlers[0] = wiringHandler{id: "mut", ord: 99}
	got2 := snap.LocalTurnHandlers()
	if got2[0].ID() != "a" {
		t.Fatalf("frozen violated after source mutate")
	}
	// Overlay preserves order
	var dst ExtensionsOptions
	overlayExtensions(&dst, ext)
	if len(dst.LocalTurnHandlers) != 3 {
		t.Fatalf("overlay %d", len(dst.LocalTurnHandlers))
	}
}

func TestLocalTurn_ProductionWiring_OverlayPreserves(t *testing.T) {
	t.Parallel()
	h := wiringHandler{id: "x", ord: 5}
	src := ExtensionsOptions{LocalTurnHandlers: []localturn.Handler{h}}
	var dst ExtensionsOptions
	// Use real types for overlay
	overlayExtensions(&dst, src)
	if len(dst.LocalTurnHandlers) != 1 || dst.LocalTurnHandlers[0].ID() != "x" {
		t.Fatalf("overlay failed")
	}
	// Second overlay appends
	src2 := ExtensionsOptions{LocalTurnHandlers: []localturn.Handler{wiringHandler{id: "y", ord: 1}}}
	overlayExtensions(&dst, src2)
	if len(dst.LocalTurnHandlers) != 2 {
		t.Fatalf("second overlay %d", len(dst.LocalTurnHandlers))
	}
}
