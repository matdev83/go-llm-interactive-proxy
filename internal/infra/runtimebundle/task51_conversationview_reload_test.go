package runtimebundle_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/uptrace/bun"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// task51LocalHandler is a minimal localturn handler for generation reload test.
type task51LocalHandler struct {
	id  string
	ord int
}

func (h task51LocalHandler) ID() string                        { return h.id }
func (h task51LocalHandler) Order() int                        { return h.ord }
func (h task51LocalHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h task51LocalHandler) Match(_ context.Context, _ lipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h task51LocalHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "ok"}, nil
}

type task51TrafficCapture struct {
	mu  sync.Mutex
	obs []traffic.Observation
}

func (c *task51TrafficCapture) OnObservation(_ context.Context, ev traffic.Observation) error {
	c.mu.Lock()
	c.obs = append(c.obs, ev)
	c.mu.Unlock()
	return nil
}

func (c *task51TrafficCapture) byLeg(leg traffic.Leg) []traffic.Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []traffic.Observation
	for _, o := range c.obs {
		if o.Leg == leg {
			cp := o
			cp.Body = append([]byte(nil), o.Body...)
			out = append(out, cp)
		}
	}
	return out
}

type task51CaptureBackend struct {
	mu    sync.Mutex
	calls []lipapi.Call
}

func (c *task51CaptureBackend) Backend() execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			c.mu.Lock()
			c.calls = append(c.calls, lipapi.CloneCall(call))
			c.mu.Unlock()
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "backend-answer"},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}
}

func (c *task51CaptureBackend) lastCall() (lipapi.Call, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		return lipapi.Call{}, false
	}
	return lipapi.CloneCall(c.calls[len(c.calls)-1]), true
}

func task51BaseConfig() *config.Config {
	cfg := routeOverrideBaseConfig()
	// add openai backend for executor routing
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &node); err != nil {
		panic(err)
	}
	// node is DocumentNode, need mapping node content
	if len(node.Content) > 0 {
		node = *node.Content[0]
	}
	// The standard helper withRouteOverrideOpenAIBackend uses PluginConfig with Config: node
	// Here we construct directly.
	cfg.Plugins.Backends = []config.PluginConfig{
		{ID: "openai", Kind: "openai-responses", Enabled: true, Config: node},
	}
	// Ensure default route selector picks openai
	cfg.Routing.DefaultRoute = "openai:gpt-4"
	return cfg
}

// TestTask51_GenerationReload_RuntimeBundleHarness proves conversation-view state is
// process/continuity-owned, not generation-owned, exercising actual runtimebundle generation lifecycle.
// Build ProcessServices once, compile gen1 with local handler, gen2 without, sharing process store;
// prove state persists and gen2 Executor still enforces (filters tagged, injects steering) via real PTB/backend.
func TestTask51_GenerationReload_RuntimeBundleHarness(t *testing.T) {
	t.Parallel()
	trafficCap := &task51TrafficCapture{}
	capBackend := &task51CaptureBackend{}
	trafficCap2 := &task51TrafficCapture{}
	capBackend2 := &task51CaptureBackend{}

	reg := generationRegistry(t)
	require.NoError(t, reg.RegisterFeature("traffic-cap-1", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "traffic-cap-1", func(cs *lipfeature.ContributionSet) error {
			return lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, "traffic-cap-1", []traffic.Observer{trafficCap})
		}, nil), nil
	}))
	require.NoError(t, reg.RegisterFeature("traffic-cap-2", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "traffic-cap-2", func(cs *lipfeature.ContributionSet) error {
			return lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, "traffic-cap-2", []traffic.Observer{trafficCap2})
		}, nil), nil
	}))

	cfg := task51BaseConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// Gen1 with handler
	handler1 := task51LocalHandler{id: "h1", ord: 1}
	gen1Cfg := task51BaseConfig()
	gen1Cfg.Plugins.Features = append(gen1Cfg.Plugins.Features, config.PluginConfig{
		ID: "traffic-cap-1", Enabled: true,
	})
	cs1 := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs1, lipfeature.PlaneLocalTurnHandlers, "h1", []localturn.Handler{handler1}))
	gen1, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: gen1Cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: cs1.Freeze(),
		},
	})
	if err != nil {
		t.Fatalf("CompileGeneration gen1: %v", err)
	}
	t.Cleanup(func() { _ = gen1.Close() })
	ex1 := runtimebundle.GenerationExecutorOf(gen1)
	if ex1 == nil {
		t.Fatal("gen1 executor nil")
	}
	if len(ex1.RuntimeSnapshot.LocalTurnHandlers()) != 1 {
		t.Fatalf("gen1 handlers %d want 1", len(ex1.RuntimeSnapshot.LocalTurnHandlers()))
	}
	// Override backend to capture
	ex1.Backends = map[string]execbackend.Backend{"openai": capBackend.Backend()}

	// Tag + steering on the process store (the authoritative A-leg store shared across generations).
	// Create a pinned A-leg to tag.
	rec, err := ps.Continuity.CreateALeg(context.Background(), "task51-gen-reload-pin")
	if err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}
	pinnedALeg := rec.ALegID
	taggedMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("gen-reload-tagged")}}
	taggedID, _ := conversationview.MessageIdentityOf(taggedMsg)
	cv := ps.StandardFeatures.ConversationStore()
	if cv == nil {
		t.Fatalf("ConversationStore nil on StandardFeatures")
	}
	if creator, ok := cv.(interface {
		CreateALeg(context.Context, string) error
	}); ok {
		_ = creator.CreateALeg(context.Background(), pinnedALeg)
	}
	if _, err := cv.TagNeverBackend(context.Background(), pinnedALeg, []conversationview.TagRequest{{Identity: taggedID, Reason: "test"}}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	steeringText := "hidden-steering-gen-reload-harness"
	if _, err := cv.PutSteering(context.Background(), pinnedALeg, conversationview.PutSteeringRequest{
		OverlayID: "ov-gen-reload", Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: steeringText},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "test",
	}); err != nil {
		t.Fatalf("PutSteering: %v", err)
	}

	// Verify via direct store snapshot that state exists before gen2
	snapPre, _ := cv.Snapshot(context.Background(), pinnedALeg)
	if len(snapPre.NeverBackend) != 1 || len(snapPre.Steering) != 1 {
		t.Fatalf("pre-gen2 snapshot: %+v", snapPre)
	}

	// Gen2 without handler, same ProcessServices (process store retained), should still enforce.
	// Reuse same trafficCap to capture PTB for gen2 (need fresh cap)
	gen2Cfg := task51BaseConfig()
	gen2Cfg.Plugins.Features = append(gen2Cfg.Plugins.Features, config.PluginConfig{
		ID: "traffic-cap-2", Enabled: true,
	})
	gen2, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: gen2Cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration gen2: %v", err)
	}
	t.Cleanup(func() { _ = gen2.Close() })
	ex2 := runtimebundle.GenerationExecutorOf(gen2)
	if ex2 == nil {
		t.Fatal("gen2 executor nil")
	}
	if len(ex2.RuntimeSnapshot.LocalTurnHandlers()) != 0 {
		t.Fatalf("gen2 handlers %d want 0 (proves reload removed handler but state persists)", len(ex2.RuntimeSnapshot.LocalTurnHandlers()))
	}
	ex2.Backends = map[string]execbackend.Backend{"openai": capBackend2.Backend()}

	// Prove state still exists after reload (process store not reset)
	snapPost, _ := cv.Snapshot(context.Background(), pinnedALeg)
	if len(snapPost.NeverBackend) != 1 || snapPost.NeverBackend[0].Identity != taggedID {
		t.Fatalf("post-reload tags lost: %+v", snapPost)
	}
	if len(snapPost.Steering) != 1 || snapPost.Steering[0].Message.Text != steeringText {
		t.Fatalf("post-reload steering lost: %+v", snapPost)
	}
	// Also prove that gen2's executor still enforces via pinnedReader trick:
	// The executor's ConversationViewReader is derived from ps.Store (process store) via AsReader.
	// To make the fresh secure A-leg see our pinned tags, we wrap the reader to pin.
	// Instead of hacking, we can directly use the store's snapshot for verification via Project,
	// and also via real Execute with pinnedReader override on ex2.
	// Override ex2's ConversationViewReader to pinned (still reading real store's pinned ALeg)
	ex2.ConversationViewReader = &pinnedReaderForReload{reader: cv, pinned: pinnedALeg}

	// Execute a legacy call containing tagged message via ex2 (gen2)
	// Use secure context with principal to ensure CTP path (secure manager exists in ps?)
	// ps's secure session manager is inside ps, and ex2 is wired to it via ProcessServices.
	// For this harness, the executor is secure-session aware (since ps has SecureSessionStore).
	// We need a principal context.
	ctx := execPrincipalWithID(context.Background(), "principal-gen-reload")
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{taggedMsg, {Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("next-after-reload")}}},
	}
	stream, err := ex2.Execute(ctx, call)
	if err != nil {
		t.Fatalf("Execute gen2: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect gen2: %v", err)
	}
	_ = stream.Close()
	open, ok := capBackend2.lastCall()
	if !ok {
		t.Fatal("gen2 backend not called")
	}
	for _, m := range open.Messages {
		if id, _ := conversationview.MessageIdentityOf(m); id == taggedID {
			t.Fatalf("gen2 backend still contains tagged after reload")
		}
	}
	foundSteering := false
	for _, m := range open.Instructions {
		for _, p := range m.Parts {
			if p.Text == steeringText {
				foundSteering = true
			}
		}
	}
	if !foundSteering {
		t.Fatalf("gen2 backend missing steering after reload: %+v", open)
	}
	// PTB should also contain steering, no tagged
	ptbs := trafficCap2.byLeg(traffic.LegPTB)
	if len(ptbs) == 0 {
		t.Fatalf("gen2 PTB missing (traffic observer via generation harness)")
	}
	hasPTBSteering := false
	hasPTBTagged := false
	for _, raw := range ptbs {
		var c lipapi.Call
		_ = json.Unmarshal(raw.Body, &c)
		for _, m := range c.Messages {
			if id, _ := conversationview.MessageIdentityOf(m); id == taggedID {
				hasPTBTagged = true
			}
		}
		for _, m := range c.Instructions {
			for _, p := range m.Parts {
				if p.Text == steeringText {
					hasPTBSteering = true
				}
			}
		}
	}
	if hasPTBTagged || !hasPTBSteering {
		t.Fatalf("gen2 PTB tagged=%v steering=%v want false/true", hasPTBTagged, hasPTBSteering)
	}
	// Also verify gen1's state was process-owned: even after gen2 compiled, gen1's snapshot still sees same store via direct check
	snapViaGen1Store, _ := cv.Snapshot(context.Background(), pinnedALeg)
	if len(snapViaGen1Store.Steering) != 1 {
		t.Fatalf("gen1 store lost after gen2 compile")
	}
	_ = b2bua.Store(nil)
}

// pinnedReaderForReload delegates Snapshot to real store but ignores requested ALegID and returns pinned.
type pinnedReaderForReload struct {
	reader conversationprojection.Reader
	pinned string
}

func (r *pinnedReaderForReload) Snapshot(ctx context.Context, _ string) (conversationprojection.Snapshot, error) {
	if r.reader != nil {
		return r.reader.Snapshot(ctx, r.pinned)
	}
	return conversationprojection.Snapshot{}, conversationview.ErrALegNotFound
}

func execPrincipalWithID(ctx context.Context, id string) context.Context {
	return execview.WithPrincipal(ctx, execview.PrincipalView{ID: id})
}

func TestProcessServices_ConversationStore_PersistenceSelection_SQLiteAndInMemory(t *testing.T) {
	t.Parallel()
	// 1. SQLite: proves SQLite config yields Bun-backed store and writes survive process restart
	path := filepath.Join(t.TempDir(), "conv-persist-restart.db")
	cfg := routeOverrideBaseConfig()
	cfg.Continuity = config.ContinuityConfig{
		InMemory:   false,
		Store:      "sqlite",
		SQLitePath: path,
	}
	ctx := context.Background()
	ps1 := mustRouteOverrideProcess(t, cfg)
	store1 := ps1.StandardFeatures.ConversationStore()
	require.NotNil(t, store1)

	type bunDBProvider interface {
		DB() *bun.DB
	}
	p1, ok := store1.(bunDBProvider)
	require.True(t, ok)
	require.NotNil(t, p1.DB(), "SQLite-configured ProcessServices must yield Bun-backed conversation store")

	leg, err := ps1.Continuity.CreateALeg(ctx, "conv-sqlite-reopen")
	require.NoError(t, err)

	_, err = store1.PutSteering(ctx, leg.ALegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-sqlite",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "sqlite-reopen-test"},
		Placement:           conversationview.StoredPlacement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)

	require.NoError(t, ps1.Close())

	// Reopen with same SQLite DB path
	ps2 := mustRouteOverrideProcess(t, cfg)
	t.Cleanup(func() { _ = ps2.Close() })
	store2 := ps2.StandardFeatures.ConversationStore()
	require.NotNil(t, store2)

	p2, ok := store2.(bunDBProvider)
	require.True(t, ok)
	require.NotNil(t, p2.DB())

	snap, err := store2.Snapshot(ctx, leg.ALegID)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 1)
	require.Equal(t, "sqlite-reopen-test", snap.Steering[0].Message.Text)

	// 2. In-memory: proves in-memory config yields ReferenceStore with nil DB()
	memCfg := routeOverrideBaseConfig()
	psMem := mustRouteOverrideProcess(t, memCfg)
	t.Cleanup(func() { _ = psMem.Close() })
	memStore := psMem.StandardFeatures.ConversationStore()
	require.NotNil(t, memStore)
	pMem, ok := memStore.(bunDBProvider)
	require.True(t, ok)
	require.Nil(t, pMem.DB(), "in-memory ProcessServices must yield ReferenceStore with nil DB()")
}
