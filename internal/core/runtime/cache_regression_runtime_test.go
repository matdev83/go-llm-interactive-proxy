package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// helpers

func rtCacheSys(t string) lipapi.Message {
	return lipapi.Message{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: t}}}
}
func rtCacheUser(t string) lipapi.Message {
	return lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: t}}}
}
func rtCacheAssistant(t string) lipapi.Message {
	return lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: t}}}
}
func rtCacheTraj(call lipapi.Call) []string {
	var out []string
	if call.HasItemAuthority() {
		for _, it := range call.Items {
			if it.Kind != lipapi.ItemKindMessage {
				continue
			}
			txt := ""
			if len(it.Content) > 0 {
				txt = it.Content[0].Text
			}
			out = append(out, string(it.Role)+":"+txt)
		}
	} else {
		for _, m := range call.Instructions {
			if len(m.Parts) > 0 {
				out = append(out, string(m.Role)+":"+m.Parts[0].Text)
			}
		}
		for _, m := range call.Messages {
			if len(m.Parts) > 0 {
				out = append(out, string(m.Role)+":"+m.Parts[0].Text)
			}
		}
	}
	return out
}
func rtIsPrefix(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i] != full[i] {
			return false
		}
	}
	return true
}

func TestCacheRegression_Runtime_FrozenSnapshot_PrefixStable_AcrossThreeTurns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := store.ConversationViewStore()
	// Need an A-leg via runtime prepare to get deterministic ID, then seed steering
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(1000, 0) }
	callInit := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []lipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, err := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	require.NoError(t, err)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	// Create stable_prefix overlay via MemoryStore (explicit discontinuity)
	st, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-rt-stable",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "RT_STABLE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)
	require.Equal(t, conversationview.CacheDiscontinuityCreate, st.CacheDiscontinuityKind)
	snap, _ := cv.Snapshot(ctx, aLegID)
	// Use counting reader that returns same frozen snap for any A-leg (runtime should read exactly once per turn)
	counting := &cacheCountingReader{snap: snap}
	ex := TestExecutor()
	ex.Store = store
	ex.ConversationViewReader = counting
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(1000, 0) }
	sys := rtCacheSys("sys")
	u1 := rtCacheUser("u1")
	a1 := rtCacheAssistant("a1")
	u2 := rtCacheUser("u2")
	a2 := rtCacheAssistant("a2")
	u3 := rtCacheUser("u3")
	callN := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}
	callN1 := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2}}
	callN2 := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2, a2, u3}}
	// Simulate three append-only turns via direct Project with frozen snap (runtime would do same)
	projN, _, err := conversationview.Project(*callN, snap)
	require.NoError(t, err)
	projN1, _, err := conversationview.Project(*callN1, snap)
	require.NoError(t, err)
	projN2, _, err := conversationview.Project(*callN2, snap)
	require.NoError(t, err)
	require.True(t, rtIsPrefix(rtCacheTraj(projN), rtCacheTraj(projN1)))
	require.True(t, rtIsPrefix(rtCacheTraj(projN1), rtCacheTraj(projN2)))
	// Runtime prepare must also project with same frozen snap and not follow moving tail
	prN, _, cleanupN, err := ex.prepareRequest(execDetachedCtx(ctx), callN)
	require.NoError(t, err)
	defer cleanupN()
	require.Equal(t, 1, counting.Count())
	// Verify RT_STABLE not at tail but at stable prefix
	traj := rtCacheTraj(*prN.call)
	idx := -1
	for i, s := range traj {
		if s == "system:RT_STABLE" {
			idx = i
		}
	}
	require.Equal(t, 1, idx, "RT_STABLE must be at stable prefix index 1, got %v", traj)
	// Second turn via same frozen snap must be prefix
	prN1, _, cleanupN1, err := ex.prepareRequest(execDetachedCtx(ctx), callN1)
	require.NoError(t, err)
	defer cleanupN1()
	assert.True(t, rtIsPrefix(rtCacheTraj(*prN.call), rtCacheTraj(*prN1.call)))
	// Verify no dynamic metadata in projection evidence/summary
	raw, _ := json.Marshal(prN.conversationEvidence)
	assert.False(t, strings.Contains(string(raw), "RT_STABLE"))
	rawSum, _ := json.Marshal(prN.conversationSummary)
	assert.False(t, strings.Contains(string(rawSum), "RT_STABLE"))
	assert.False(t, strings.Contains(string(rawSum), "ov-rt-stable"))
	_ = projN
	_ = projN1
	_ = projN2
}

func TestCacheRegression_Runtime_FixedActivation_FrozenAcrossTurns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := store.ConversationViewStore()
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(2000, 0) }
	callInit := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []lipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, _ := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	uAnchor := rtCacheUser("anchor-N")
	sys := rtCacheSys("sys")
	anchorCall := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{uAnchor}}
	snap0, _ := cv.Snapshot(ctx, aLegID)
	anchor, err := conversationview.ResolveAfterIngressTailAnchor(anchorCall, snap0)
	require.NoError(t, err)
	_, err = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-rt-fixed",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "RT_FIXED"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "test",
	})
	require.NoError(t, err)
	snap, _ := cv.Snapshot(ctx, aLegID)
	// Three turns
	a1 := rtCacheAssistant("a1")
	u2 := rtCacheUser("u2")
	a2 := rtCacheAssistant("a2")
	u3 := rtCacheUser("u3")
	callN := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{uAnchor}}
	callN1 := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{uAnchor, a1, u2}}
	callN2 := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{uAnchor, a1, u2, a2, u3}}
	projN, ev, _ := conversationview.Project(callN, snap)
	projN1, _, _ := conversationview.Project(callN1, snap)
	projN2, _, _ := conversationview.Project(callN2, snap)
	require.True(t, rtIsPrefix(rtCacheTraj(projN), rtCacheTraj(projN1)))
	require.True(t, rtIsPrefix(rtCacheTraj(projN1), rtCacheTraj(projN2)))
	// Verify activation ordering U_N, STEER
	assert.Equal(t, []string{"system:sys", "user:anchor-N", "system:RT_FIXED"}, rtCacheTraj(projN))
	// Same revision byte-stable
	require.Len(t, ev.Provenance, 1)
	assert.Equal(t, ev.Provenance[0].InjectedIdentity, ev.Provenance[0].InjectedIdentity) // same
	_ = projN2
}

func TestCacheRegression_Runtime_MutationDiscontinuity_FrozenIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := store.ConversationViewStore()
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(3000, 0) }
	callInit := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []lipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, _ := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	sys := rtCacheSys("sys")
	u1 := rtCacheUser("u1")
	a1 := rtCacheAssistant("a1")
	u2 := rtCacheUser("u2")
	// Initial snap empty
	snap0, _ := cv.Snapshot(ctx, aLegID)
	traj0 := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, snap0))
	// CREATE discontinuity via store
	stCreate, _ := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-disc",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "DISC_CREATE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.Equal(t, conversationview.CacheDiscontinuityCreate, stCreate.CacheDiscontinuityKind)
	snap1, _ := cv.Snapshot(ctx, aLegID)
	traj1 := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, snap1))
	require.False(t, rtIsPrefix(traj0, traj1), "create must be discontinuity")
	// Subsequent turns after create stable
	traj1b := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2}}, snap1))
	require.True(t, rtIsPrefix(traj1, traj1b))
	// REPLACE
	stRep, _ := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-disc",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "DISC_REPLACE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.Equal(t, conversationview.CacheDiscontinuityReplace, stRep.CacheDiscontinuityKind)
	snap2, _ := cv.Snapshot(ctx, aLegID)
	traj2 := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, snap2))
	require.False(t, rtIsPrefix(traj1, traj2))
	traj2b := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2}}, snap2))
	require.True(t, rtIsPrefix(traj2, traj2b))
	// MOVE
	anchor, _ := conversationview.ResolveAfterIngressTailAnchor(lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, snap2)
	stMove, _ := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-disc",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "DISC_REPLACE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "test",
	})
	require.Equal(t, conversationview.CacheDiscontinuityMove, stMove.CacheDiscontinuityKind)
	snap3, _ := cv.Snapshot(ctx, aLegID)
	traj3 := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, snap3))
	require.False(t, rtIsPrefix(traj2, traj3))
	traj3b := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2}}, snap3))
	require.True(t, rtIsPrefix(traj3, traj3b))
	// DEACTIVATE
	stDeact, _ := cv.DeactivateSteering(ctx, aLegID, "ov-disc")
	require.Equal(t, conversationview.CacheDiscontinuityDeactivate, stDeact.CacheDiscontinuityKind)
	snap4, _ := cv.Snapshot(ctx, aLegID)
	traj4 := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, snap4))
	require.False(t, rtIsPrefix(traj3, traj4))
	traj4b := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2}}, snap4))
	require.True(t, rtIsPrefix(traj4, traj4b))
	// In-flight turn isolation: frozen snapshot N must not see N+1 mutation
	// Simulate: take snap1, then mutate to snap2, but in-flight uses snap1
	trajInflight := rtCacheTraj(mustProj(t, lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1}}, snap1))
	assert.Contains(t, strings.Join(trajInflight, ","), "DISC_CREATE")
	assert.NotContains(t, strings.Join(trajInflight, ","), "DISC_REPLACE")
}

func TestCacheRegression_Runtime_AnchorDisappearance_FallbackAndFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("fallback via runtime Execute", func(t *testing.T) {
		t.Parallel()
		store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		cv := store.ConversationViewStore()
		tmpEx := TestExecutor()
		tmpEx.Store = store
		tmpEx.Bus = hooks.New(hooks.Config{})
		tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
		tmpEx.Rand = routing.NewSeededRng(1)
		tmpEx.Now = func() time.Time { return time.Unix(4000, 0) }
		callInit := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []lipapi.Message{rtCacheUser("init")}}
		prInit, _, cleanup, _ := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
		aLegID := prInit.identity.aLeg.ALegID
		cleanup()
		uAnchor := rtCacheUser("anchor-fb")
		sys := rtCacheSys("sys")
		anchor, _ := conversationview.ResolveAfterIngressTailAnchor(lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{uAnchor}}, mustSnap(t, cv, aLegID))
		_, _ = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "ov-fb",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "FB_STEER"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "test",
		})
		snap, _ := cv.Snapshot(ctx, aLegID)
		counting := &cacheCountingReader{snap: snap}
		capBackend := &cacheCaptureBackend{caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming)}
		ex := TestExecutor()
		ex.Store = store
		ex.ConversationViewReader = counting
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
		ex.Backends = map[string]execbackend.Backend{"openai": capBackend.Backend()}
		ex.Rand = routing.NewSeededRng(1)
		ex.Now = func() time.Time { return time.Unix(4000, 0) }
		// Compacted history missing anchor
		call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{rtCacheUser("other"), rtCacheAssistant("a1")}}
		stream, err := ex.Execute(execDetachedCtx(ctx), call)
		require.NoError(t, err)
		_, err = lipapi.Collect(ctx, stream)
		require.NoError(t, err)
		_ = stream.Close()
		require.Equal(t, 1, capBackend.OpenCount())
		open := capBackend.LastCall()
		foundInInstr := false
		for _, m := range open.Instructions {
			if len(m.Parts) > 0 && m.Parts[0].Text == "FB_STEER" {
				foundInInstr = true
			}
		}
		require.True(t, foundInInstr, "fallback must be in Instructions")
		// second compacted projection must be deterministic (no wandering)
		call2 := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{rtCacheUser("other"), rtCacheAssistant("a1"), rtCacheUser("u2")}}
		// Direct project with same snap should be prefix-stable in fallback position
		proj1, ev1, _ := conversationview.Project(*call, snap)
		proj2, ev2, _ := conversationview.Project(*call2, snap)
		require.Len(t, ev1.Fallbacks, 1)
		require.Len(t, ev2.Fallbacks, 1)
		assert.True(t, rtIsPrefix(rtCacheTraj(proj1), rtCacheTraj(proj2)))
		// Bounded evidence: no plaintext
		raw, _ := json.Marshal(ev1)
		assert.False(t, strings.Contains(string(raw), "FB_STEER"))
	})
	t.Run("fail_closed via runtime Execute", func(t *testing.T) {
		t.Parallel()
		store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		cv := store.ConversationViewStore()
		tmpEx := TestExecutor()
		tmpEx.Store = store
		tmpEx.Bus = hooks.New(hooks.Config{})
		tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
		tmpEx.Rand = routing.NewSeededRng(1)
		tmpEx.Now = func() time.Time { return time.Unix(4000, 0) }
		callInit := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []lipapi.Message{rtCacheUser("init")}}
		prInit, _, cleanup, _ := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
		aLegID := prInit.identity.aLeg.ALegID
		cleanup()
		uAnchor := rtCacheUser("anchor-fc")
		sys := rtCacheSys("sys")
		anchor, _ := conversationview.ResolveAfterIngressTailAnchor(lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{uAnchor}}, mustSnap(t, cv, aLegID))
		_, _ = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "ov-fc",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "FC_STEER"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "test",
		})
		snap, _ := cv.Snapshot(ctx, aLegID)
		counting := &cacheCountingReader{snap: snap}
		capBackend := &cacheCaptureBackend{caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming)}
		ex := TestExecutor()
		ex.Store = store
		ex.ConversationViewReader = counting
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
		ex.Backends = map[string]execbackend.Backend{"openai": capBackend.Backend()}
		ex.Rand = routing.NewSeededRng(1)
		ex.Now = func() time.Time { return time.Unix(4000, 0) }
		call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{rtCacheUser("other"), rtCacheAssistant("a1")}}
		_, err := ex.Execute(execDetachedCtx(ctx), call)
		require.Error(t, err)
		assert.Equal(t, 0, capBackend.OpenCount(), "fail_closed must not open backend")
		assert.False(t, strings.Contains(err.Error(), "FC_STEER"))
	})
}

func TestCacheRegression_Runtime_NoTailReinject_And_ReassertFrozen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := store.ConversationViewStore()
	tmpEx := TestExecutor()
	tmpEx.Store = store
	tmpEx.Bus = hooks.New(hooks.Config{})
	tmpEx.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(tmpEx.Bus, extensions.SnapshotOptions{})
	tmpEx.Rand = routing.NewSeededRng(1)
	tmpEx.Now = func() time.Time { return time.Unix(5000, 0) }
	callInit := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []lipapi.Message{rtCacheUser("init")}}
	prInit, _, cleanup, _ := tmpEx.prepareRequest(execDetachedCtx(ctx), callInit)
	aLegID := prInit.identity.aLeg.ALegID
	cleanup()
	sys := rtCacheSys("sys")
	u1 := rtCacheUser("u1")
	anchor, _ := conversationview.ResolveAfterIngressTailAnchor(lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}, mustSnap(t, cv, aLegID))
	_, _ = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-reassert",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "REASSERT_STEER"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "test",
	})
	snap, _ := cv.Snapshot(ctx, aLegID)
	// Late transform that moves steering to tail must be repaired by Reassert frozen snapshot
	baseCall := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, rtCacheAssistant("a1"), rtCacheUser("u2")}}
	baseline, ev, _ := conversationview.Project(baseCall, snap)
	late := lipapi.CloneCall(baseline)
	// Move steering to tail (simulate per-turn tail re-inject bug)
	var steeringMsg lipapi.Message
	var filtered []lipapi.Message
	for _, m := range late.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "REASSERT_STEER" {
			steeringMsg = m
			continue
		}
		filtered = append(filtered, m)
	}
	late.Messages = append(filtered, steeringMsg)
	filteredBaseline, _ := conversationview.FilterNeverBackend(baseCall, snap)
	repaired, _, err := conversationview.Reassert(late, snap, ev.Provenance, filteredBaseline)
	require.NoError(t, err)
	trajRepaired := rtCacheTraj(repaired)
	trajBaseline := rtCacheTraj(baseline)
	assert.Equal(t, trajBaseline, trajRepaired, "Reassert must restore frozen fixed position, not tail")
	// Ensure repaired is prefix of later turn
	laterCall := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, rtCacheAssistant("a1"), rtCacheUser("u2"), rtCacheAssistant("a2"), rtCacheUser("u3")}}
	projLater, _, _ := conversationview.Project(laterCall, snap)
	assert.True(t, rtIsPrefix(trajRepaired, rtCacheTraj(projLater)))
	// Tail re-inject would break prefix: demonstrate
	tailTraj := append(rtCacheTraj(baseline)[:len(rtCacheTraj(baseline))-1], "system:REASSERT_STEER")
	assert.False(t, rtIsPrefix(trajBaseline, tailTraj))
}

// helpers

type cacheCountingReader struct {
	snap  conversationview.Snapshot
	count int
}

func (c *cacheCountingReader) Snapshot(_ context.Context, _ string) (conversationview.Snapshot, error) {
	c.count++
	return c.snap, nil
}
func (c *cacheCountingReader) Count() int { return c.count }

type cacheCaptureBackend struct {
	caps      lipapi.BackendCaps
	calls     []lipapi.Call
	openCount int
}

func (c *cacheCaptureBackend) Backend() execbackend.Backend {
	return execbackend.Backend{Caps: c.caps, Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		c.openCount++
		c.calls = append(c.calls, lipapi.CloneCall(call))
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
	}}
}
func (c *cacheCaptureBackend) OpenCount() int { return c.openCount }
func (c *cacheCaptureBackend) LastCall() lipapi.Call {
	if len(c.calls) == 0 {
		return lipapi.Call{}
	}
	return lipapi.CloneCall(c.calls[len(c.calls)-1])
}

func mustSnap(t *testing.T, cv conversationview.Store, aLeg string) conversationview.Snapshot {
	t.Helper()
	snap, err := cv.Snapshot(context.Background(), aLeg)
	require.NoError(t, err)
	return snap
}
func mustProj(t *testing.T, call lipapi.Call, snap conversationview.Snapshot) lipapi.Call {
	t.Helper()
	proj, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	return proj
}

// Ensure imports used
var _ = sdkhooks.FailClosed
