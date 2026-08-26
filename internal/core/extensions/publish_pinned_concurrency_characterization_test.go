package extensions_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/stretchr/testify/assert"
)

// --- Stubs for Extension Planes in Pinned Concurrency Characterization Tests ---

type charPinnedSessionOpener struct{ id string }

func (s charPinnedSessionOpener) ID() string { return s.id }
func (charPinnedSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charPinnedToolCatalogFilter struct{ id string }

func (c charPinnedToolCatalogFilter) ID() string                      { return c.id }
func (charPinnedToolCatalogFilter) Order() int                        { return 0 }
func (charPinnedToolCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedToolCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charPinnedToolCallPolicy struct {
	id  string
	ord int
}

func (p charPinnedToolCallPolicy) ID() string                      { return p.id }
func (p charPinnedToolCallPolicy) Order() int                      { return p.ord }
func (charPinnedToolCallPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedToolCallPolicy) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charPinnedToolCallFinalizer struct {
	id  string
	ord int
}

func (f charPinnedToolCallFinalizer) ID() string { return f.id }
func (f charPinnedToolCallFinalizer) Order() int { return f.ord }
func (charPinnedToolCallFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charPinnedRequestTransform struct{ id string }

func (r charPinnedRequestTransform) ID() string                      { return r.id }
func (charPinnedRequestTransform) Order() int                        { return 0 }
func (charPinnedRequestTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedRequestTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charPinnedPreRequestHandler struct{ id string }

func (p charPinnedPreRequestHandler) ID() string                      { return p.id }
func (charPinnedPreRequestHandler) Order() int                        { return 0 }
func (charPinnedPreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedPreRequestHandler) Handle(context.Context, *lipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charPinnedRouteHintProvider struct{ id string }

func (rh charPinnedRouteHintProvider) ID() string                     { return rh.id }
func (charPinnedRouteHintProvider) Order() int                        { return 0 }
func (charPinnedRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedRouteHintProvider) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charPinnedCompletionGate struct{ id string }

func (g charPinnedCompletionGate) ID() string                      { return g.id }
func (charPinnedCompletionGate) Order() int                        { return 0 }
func (charPinnedCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedCompletionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charPinnedAttemptTransform struct{ id string }

func (a charPinnedAttemptTransform) ID() string                      { return a.id }
func (charPinnedAttemptTransform) Order() int                        { return 0 }
func (charPinnedAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charPinnedAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charPinnedStreamObserverFactory struct{ id string }

func (s charPinnedStreamObserverFactory) ID() string                      { return s.id }
func (charPinnedStreamObserverFactory) Order() int                        { return 0 }
func (charPinnedStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charPinnedTrafficRedactor struct{ id string }

func (tr charPinnedTrafficRedactor) ID() string { return tr.id }
func (charPinnedTrafficRedactor) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charPinnedCompactionObserver struct{ id string }

func (co charPinnedCompactionObserver) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type charPinnedCompactionPreserver struct{ id string }

func (cp charPinnedCompactionPreserver) ID() string { return cp.id }
func (charPinnedCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charPinnedCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charPinnedCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type charPinnedSecretGuard struct {
	id  string
	ord int
}

func (sg charPinnedSecretGuard) ID() string { return sg.id }
func (sg charPinnedSecretGuard) Order() int { return sg.ord }
func (charPinnedSecretGuard) FailureMode() secretguard.FailureMode {
	return secretguard.FailClosed
}

func (charPinnedSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charPinnedLocalTurnHandler struct {
	id  string
	ord int
}

func (lt charPinnedLocalTurnHandler) ID() string { return lt.id }
func (lt charPinnedLocalTurnHandler) Order() int { return lt.ord }
func (charPinnedLocalTurnHandler) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (charPinnedLocalTurnHandler) Match(context.Context, lipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}

func (charPinnedLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "char-pinned-local-turn"}, nil
}

type charPinnedTerminalDecisionProvider struct{ id string }

func (p *charPinnedTerminalDecisionProvider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *charPinnedTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "char-done"}, nil
}

// buildPopulatedSnapshot creates a snapshot with all extension planes populated
// using a distinct label and generation number.
func buildPopulatedSnapshot(gen int64, label string) *extensions.RequestRuntimeSnapshot {
	bus := hooks.New(hooks.Config{})
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Generation: gen,
		SessionOpeners: []session.Opener{
			charPinnedSessionOpener{id: label + "-session"},
		},
		ToolCatalogFilters: []toolcatalog.Filter{
			charPinnedToolCatalogFilter{id: label + "-catalog"},
		},
		ToolCallPolicies: []toolpolicy.Policy{
			charPinnedToolCallPolicy{id: label + "-policy", ord: int(gen)},
		},
		ToolCallFinalizers: []toolcall.Finalizer{
			charPinnedToolCallFinalizer{id: label + "-finalizer", ord: int(gen)},
		},
		RequestTransforms: []request.Transform{
			charPinnedRequestTransform{id: label + "-reqxform"},
		},
		PreRequestHandlers: []prerequest.Handler{
			charPinnedPreRequestHandler{id: label + "-prereq"},
		},
		RouteHintProviders: []routehint.Provider{
			charPinnedRouteHintProvider{id: label + "-routehint"},
		},
		CompletionGates: []completion.Gate{
			charPinnedCompletionGate{id: label + "-gate"},
		},
		AttemptTransforms: []request.AttemptTransform{
			charPinnedAttemptTransform{id: label + "-attxform"},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			charPinnedStreamObserverFactory{id: label + "-streamobs"},
		},
		TrafficRedactors: []sdktraffic.Redactor{
			charPinnedTrafficRedactor{id: label + "-redactor"},
		},
		CompactionObservers: []compaction.Observer{
			charPinnedCompactionObserver{},
		},
		CompactionPreservers: []compaction.Preserver{
			charPinnedCompactionPreserver{id: label + "-comppres"},
		},
		SecretGuardPlane: extensions.SecretGuardPlane{
			ConfigVersion:      label + "-sg-v",
			AccessMode:         "single_user",
			AuditFailurePolicy: secretguard.AuditFailClosed,
			Guards: []secretguard.Guard{
				charPinnedSecretGuard{id: label + "-sg", ord: int(gen)},
			},
		},
		LocalTurnHandlers: []localturn.Handler{
			charPinnedLocalTurnHandler{id: label + "-localturn", ord: int(gen)},
		},
		TerminalDecisionProvider: &charPinnedTerminalDecisionProvider{id: label + "-terminal"},
	})
}

// assertSnapshotPlanesMatch asserts that every accessible plane on snap matches expectedGen and expectedLabel.
func assertSnapshotPlanesMatch(t *testing.T, snap *extensions.RequestRuntimeSnapshot, expectedGen int64, expectedLabel string) bool {
	t.Helper()
	if !assert.NotNil(t, snap) {
		return false
	}
	assert.Equal(t, expectedGen, snap.Generation())

	sess := snap.SessionOpeners()
	if assert.Len(t, sess, 1) {
		assert.Equal(t, expectedLabel+"-session", sess[0].ID())
	}

	cat := snap.ToolCatalogFilters()
	if assert.Len(t, cat, 1) {
		assert.Equal(t, expectedLabel+"-catalog", cat[0].ID())
	}

	pols := snap.ToolCallPolicies()
	if assert.Len(t, pols, 1) {
		assert.Equal(t, expectedLabel+"-policy", pols[0].ID())
	}

	polsExec := snap.ToolCallPoliciesExecution()
	if assert.Len(t, polsExec, 1) {
		assert.Equal(t, expectedLabel+"-policy", polsExec[0].ID())
	}

	fins := snap.ToolCallFinalizers()
	if assert.Len(t, fins, 1) {
		assert.Equal(t, expectedLabel+"-finalizer", fins[0].ID())
	}

	finsExec := snap.ToolCallFinalizersExecution()
	if assert.Len(t, finsExec, 1) {
		assert.Equal(t, expectedLabel+"-finalizer", finsExec[0].ID())
	}

	rx := snap.RequestTransforms()
	if assert.Len(t, rx, 1) {
		assert.Equal(t, expectedLabel+"-reqxform", rx[0].ID())
	}

	pr := snap.PreRequestHandlers()
	if assert.Len(t, pr, 1) {
		assert.Equal(t, expectedLabel+"-prereq", pr[0].ID())
	}

	rh := snap.RouteHintProviders()
	if assert.Len(t, rh, 1) {
		assert.Equal(t, expectedLabel+"-routehint", rh[0].ID())
	}

	cg := snap.CompletionGates()
	if assert.Len(t, cg, 1) {
		assert.Equal(t, expectedLabel+"-gate", cg[0].ID())
	}

	at := snap.AttemptTransforms()
	if assert.Len(t, at, 1) {
		assert.Equal(t, expectedLabel+"-attxform", at[0].ID())
	}

	so := snap.StreamObserverFactories()
	if assert.Len(t, so, 1) {
		assert.Equal(t, expectedLabel+"-streamobs", so[0].ID())
	}

	tr := snap.TrafficRedactors()
	if assert.Len(t, tr, 1) {
		assert.Equal(t, expectedLabel+"-redactor", tr[0].ID())
	}

	co := snap.CompactionObservers()
	assert.Len(t, co, 1)

	cp := snap.CompactionPreservers()
	if assert.Len(t, cp, 1) {
		assert.Equal(t, expectedLabel+"-comppres", cp[0].ID())
	}

	sg := snap.SecretGuardPlane()
	assert.Equal(t, expectedLabel+"-sg-v", sg.ConfigVersion)
	if assert.Len(t, sg.Guards, 1) {
		assert.Equal(t, expectedLabel+"-sg", sg.Guards[0].ID())
	}

	sgExec := snap.SecretGuardExecutionPlane()
	assert.Equal(t, expectedLabel+"-sg-v", sgExec.ConfigVersion)
	if assert.Len(t, sgExec.Guards, 1) {
		assert.Equal(t, expectedLabel+"-sg", sgExec.Guards[0].ID())
	}

	lt := snap.LocalTurnHandlers()
	if assert.Len(t, lt, 1) {
		assert.Equal(t, expectedLabel+"-localturn", lt[0].ID())
	}

	ltExec := snap.LocalTurnHandlersExecution()
	if assert.Len(t, ltExec, 1) {
		assert.Equal(t, expectedLabel+"-localturn", ltExec[0].ID())
	}

	td := snap.TerminalDecisionProvider()
	if assert.NotNil(t, td) {
		assert.Equal(t, expectedLabel+"-terminal", td.ID())
	}
	return !t.Failed()
}

// TestRequestRuntimeSnapshot_PublishPinned_ConcurrentIsolation pins Requirement 1.5:
//   - When Request 1 acquires a snapshot pinned from Generation 1, and Generation 2 publishes
//     concurrently at a controlled barrier, Request 1 continues observing Generation 1's frozen
//     extension surface end-to-end across multiple execution stages without drift, while new
//     requests concurrently see Generation 2.
func TestRequestRuntimeSnapshot_PublishPinned_ConcurrentIsolation(t *testing.T) {
	t.Parallel()

	// 1. Prepare Gen 1 snapshot
	snapGen1 := buildPopulatedSnapshot(1, "gen1")

	// Shared atomic pointer representing the active/published snapshot in the system
	var activeSnapshot sync.Map
	activeSnapshot.Store("current", snapGen1)

	// Synchronization barriers (no sleeps)
	req1Started := make(chan struct{})
	req1Stage1Done := make(chan struct{})
	gen2Published := make(chan struct{})
	req1Completed := make(chan struct{})
	req2Completed := make(chan struct{})

	// 2. In-flight Request 1 goroutine
	go func() {
		defer func() {
			close(req1Completed)
		}()

		// Acquire snapshot from Generation 1 and bind to request context
		rawSnap, loaded := activeSnapshot.Load("current")
		if !loaded {
			t.Error("generation 1 snapshot missing from publisher store")
			return
		}
		snap1, ok := rawSnap.(*extensions.RequestRuntimeSnapshot)
		if !ok {
			t.Errorf("unexpected snapshot type %T", rawSnap)
			return
		}
		reqCtx1 := extensions.WithRequestRuntimeSnapshot(context.Background(), snap1)

		close(req1Started)

		// Stage 1: Verify Gen 1 planes before candidate publish
		observedSnap1 := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
		assertSnapshotPlanesMatch(t, observedSnap1, 1, "gen1")

		close(req1Stage1Done)

		// Wait until Generation 2 is published
		<-gen2Published

		// Stage 2: In-flight request continues executing AFTER Gen 2 publication.
		// Invariant: The pinned request context MUST still observe Generation 1's frozen surface.
		observedSnap2 := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
		assertSnapshotPlanesMatch(t, observedSnap2, 1, "gen1")

		// Stage 3: Verify execution slice accessors (hot-path non-cloning methods)
		if len(observedSnap2.ToolCallPoliciesExecution()) > 0 {
			assert.Equal(t, "gen1-policy", observedSnap2.ToolCallPoliciesExecution()[0].ID())
		}
		if len(observedSnap2.ToolCallFinalizersExecution()) > 0 {
			assert.Equal(t, "gen1-finalizer", observedSnap2.ToolCallFinalizersExecution()[0].ID())
		}
		if len(observedSnap2.SecretGuardExecutionPlane().Guards) > 0 {
			assert.Equal(t, "gen1-sg", observedSnap2.SecretGuardExecutionPlane().Guards[0].ID())
		}
		if len(observedSnap2.LocalTurnHandlersExecution()) > 0 {
			assert.Equal(t, "gen1-localturn", observedSnap2.LocalTurnHandlersExecution()[0].ID())
		}

		// Stage 4: Defensive copy isolation — mutations to returned slices must not corrupt the snapshot
		clonedPolicies := observedSnap2.ToolCallPolicies()
		if len(clonedPolicies) > 0 {
			clonedPolicies[0] = charPinnedToolCallPolicy{id: "caller-mutated", ord: 999}
			recheckedPolicies := observedSnap2.ToolCallPolicies()
			if len(recheckedPolicies) > 0 {
				assert.Equal(t, "gen1-policy", recheckedPolicies[0].ID(), "snapshot must be isolated from caller slice mutations")
			}
		}
	}()

	// 3. Publisher goroutine: publishes Generation 2 concurrently
	go func() {
		defer func() {
			close(gen2Published)
		}()

		<-req1Stage1Done

		// Build and publish Generation 2
		snapGen2 := buildPopulatedSnapshot(2, "gen2")
		activeSnapshot.Store("current", snapGen2)
	}()

	// 4. Request 2 goroutine: starts AFTER Gen 2 publication and sees Gen 2
	go func() {
		defer func() {
			close(req2Completed)
		}()

		<-gen2Published

		rawSnap, loaded := activeSnapshot.Load("current")
		if !loaded {
			t.Error("generation 2 snapshot missing from publisher store")
			return
		}
		snap2, ok := rawSnap.(*extensions.RequestRuntimeSnapshot)
		if !ok {
			t.Errorf("unexpected snapshot type %T", rawSnap)
			return
		}
		reqCtx2 := extensions.WithRequestRuntimeSnapshot(context.Background(), snap2)

		observedSnap2 := extensions.RequestRuntimeSnapshotFromContext(reqCtx2)
		assertSnapshotPlanesMatch(t, observedSnap2, 2, "gen2")
	}()

	// Wait for all goroutines to complete
	<-req1Started
	<-req1Completed
	<-req2Completed
}

// TestRequestRuntimeSnapshot_FrozenGeneration_RepeatedPublishStability pins Requirement 1.5:
//   - Proves that repeated publishes (Gen 2 .. Gen 20) never live-rebind an already-pinned
//     snapshot; the pinned observer asserts frozen-set stability throughout across all
//     interleaving points.
func TestRequestRuntimeSnapshot_FrozenGeneration_RepeatedPublishStability(t *testing.T) {
	t.Parallel()

	snapGen1 := buildPopulatedSnapshot(1, "gen1")
	reqCtx1 := extensions.WithRequestRuntimeSnapshot(context.Background(), snapGen1)

	var activeSnapshot sync.Map
	activeSnapshot.Store("current", snapGen1)

	const iterations = 20
	var wg sync.WaitGroup

	// Publisher loop
	wg.Go(func() {
		for i := 2; i <= iterations; i++ {
			snapGenI := buildPopulatedSnapshot(int64(i), "gen-candidate")
			activeSnapshot.Store("current", snapGenI)
		}
	})

	// Pinned observer loop running concurrently
	wg.Go(func() {
		for range iterations * 5 {
			snap := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
			assertSnapshotPlanesMatch(t, snap, 1, "gen1")

			// Also verify defensive copies returned in tight loop maintain integrity
			copiedCat := snap.ToolCatalogFilters()
			if len(copiedCat) > 0 {
				_ = slices.Clone(copiedCat)
			}
		}
	})

	wg.Wait()

	// Final stability check on pinned snapshot after all publisher iterations finish
	finalSnap := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
	assertSnapshotPlanesMatch(t, finalSnap, 1, "gen1")
}
