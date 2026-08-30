package featurebundle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
)

type ltHandler struct {
	id  string
	ord int
}

func (h ltHandler) ID() string                     { return h.id }
func (h ltHandler) Order() int                     { return h.ord }
func (h ltHandler) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (h ltHandler) Match(_ context.Context, _ lipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h ltHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "ok"}, nil
}

type ptrLTHandler struct {
	id  string
	ord int
}

func (h *ptrLTHandler) ID() string                     { return h.id }
func (h *ptrLTHandler) Order() int                     { return h.ord }
func (h *ptrLTHandler) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (h *ptrLTHandler) Match(_ context.Context, _ lipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h *ptrLTHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "ok"}, nil
}

func ltBundle(t *testing.T, id string, handlers ...localturn.Handler) lipfeature.FeatureBundle {
	t.Helper()
	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneLocalTurnHandlers, id, handlers))
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil)
}

func TestMergeBundlesGenerated_LocalTurnHandlersConcatenates(t *testing.T) {
	t.Parallel()
	b1 := ltBundle(t, "feat-1", ltHandler{id: "a", ord: 2})
	b2 := ltBundle(t, "feat-2", ltHandler{id: "b", ord: 1})
	gen, err := MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)
	handlers := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	require.Len(t, handlers, 2)
	require.Equal(t, "a", handlers[0].ID())
	require.Equal(t, "b", handlers[1].ID())
}

func TestMergeBundlesGenerated_LocalTurnHandlersOrderViaMaterializeSorted(t *testing.T) {
	t.Parallel()
	b1 := ltBundle(t, "feat-1", ltHandler{id: "b", ord: 2})
	b2 := ltBundle(t, "feat-2", ltHandler{id: "a", ord: 1}, ltHandler{id: "c", ord: 1})
	gen, err := MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)
	handlers := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	sorted := localturn.MaterializeSorted(handlers)
	require.Len(t, sorted, 3)
	require.Equal(t, "a", sorted[0].ID())
	require.Equal(t, "c", sorted[1].ID())
	require.Equal(t, "b", sorted[2].ID())
}

func TestMergeBundlesGenerated_LocalTurnHandlersImmutableSnapshot(t *testing.T) {
	t.Parallel()
	b := ltBundle(t, "feat", ltHandler{id: "a", ord: 1})
	gen, err := MergeBundlesGenerated(b)
	require.NoError(t, err)
	handlers := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	require.Equal(t, "a", handlers[0].ID())
	// Mutate merged slice copy must not affect original sort input isolation
	sorted := localturn.MaterializeSorted(handlers)
	sorted[0] = ltHandler{id: "mut2", ord: 99}
	require.Equal(t, "a", lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)[0].ID())
}

func TestFeatureBundle_Validate_LocalTurnHandlersNilRejected(t *testing.T) {
	t.Parallel()
	cs := lipfeature.NewContributionSet()
	err1 := lipfeature.Contribute(cs, lipfeature.PlaneLocalTurnHandlers, "bad", []localturn.Handler{nil})
	require.Error(t, err1)
	var typedNil *ptrLTHandler
	cs2 := lipfeature.NewContributionSet()
	err2 := lipfeature.Contribute(cs2, lipfeature.PlaneLocalTurnHandlers, "bad2", []localturn.Handler{typedNil})
	require.Error(t, err2)
}

func TestMergeBundlesGenerated_LocalTurnHandlersNilListPreserved(t *testing.T) {
	t.Parallel()
	gen, err := MergeBundlesGenerated()
	require.NoError(t, err)
	require.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers))
	gen2, err := MergeBundlesGenerated(lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1})
	require.NoError(t, err)
	require.Nil(t, lipfeature.Get(gen2.Frozen, lipfeature.PlaneLocalTurnHandlers))
}

func TestMergeBundlesGenerated_LocalTurnHandlersDuplicateIDsAllowed(t *testing.T) {
	t.Parallel()
	b1 := ltBundle(t, "feat-1", ltHandler{id: "dup", ord: 1})
	b2 := ltBundle(t, "feat-2", ltHandler{id: "dup", ord: 2})
	gen, err := MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)
	handlers := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	require.Len(t, handlers, 2)
	require.Equal(t, "dup", handlers[0].ID())
	require.Equal(t, "dup", handlers[1].ID())
	sorted := localturn.MaterializeSorted(handlers)
	require.Len(t, sorted, 2)
}
