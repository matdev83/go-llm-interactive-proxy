package runtimebundle_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
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
	cvAny, ok := conversationview.AsStore(ps.Continuity)
	if !ok || cvAny == nil {
		t.Fatalf("ConversationViewStore nil - store does not implement capability")
	}
	cv := cvAny
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
	ex2.ConversationViewReader = &pinnedReaderForReload{store: ps.Continuity, pinned: pinnedALeg}

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
	store  b2bua.Store
	pinned string
}

func (r *pinnedReaderForReload) Snapshot(ctx context.Context, _ string) (conversationview.Snapshot, error) {
	// Use conversationview.AsReader to get reader from store, then Snapshot for pinned
	if rd, ok := conversationview.AsReader(r.store); ok {
		return rd.Snapshot(ctx, r.pinned)
	}
	return conversationview.Snapshot{}, conversationview.ErrALegNotFound
}

func execPrincipalWithID(ctx context.Context, id string) context.Context {
	return execview.WithPrincipal(ctx, execview.PrincipalView{ID: id})
}
