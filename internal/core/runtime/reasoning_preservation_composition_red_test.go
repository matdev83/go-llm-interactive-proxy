package runtime_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

const rpStoredThought = "stored-thought"

var rpLargeThought = strings.Repeat("R", 512)

var rpReplaySupport = lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}}

func rpArtifact(text string) reasoningpreservation.TurnArtifact {
	anchor, err := reasoningpreservation.ComputeAnchor(lipapi.Message{
		Role:  lipapi.RoleAssistant,
		Parts: []lipapi.Part{lipapi.TextPart("visible answer")},
	})
	if err != nil {
		panic("rpArtifact: ComputeAnchor: " + err.Error())
	}
	return reasoningpreservation.TurnArtifact{
		ID: "art-1", Anchor: anchor, SourceBackend: "src-be", SourceModel: "src-m",
		Reasoning: []reasoningpreservation.PlacedReasoning{{BeforeNonReasoningPart: 0, Part: lipapi.Part{
			Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: text},
		}}},
		ReasoningBytes: len(text),
	}
}

var (
	rpStoredArtifact = rpArtifact(rpStoredThought)
	rpLargeArtifact  = rpArtifact(rpLargeThought)
)

type reasoningPreservationTransform struct {
	mode      string
	artifacts []reasoningpreservation.TurnArtifact
	replay    lipapi.ReasoningReplaySupport
	calls     *atomic.Int64
	onUnrep   string
	eligible  *bool
}

func (r *reasoningPreservationTransform) ID() string { return "reasoning-preservation-xform" }
func (r *reasoningPreservationTransform) Order() int { return 0 }
func (r *reasoningPreservationTransform) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (r *reasoningPreservationTransform) HandleAttempt(_ context.Context, call *lipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if r.calls != nil {
		r.calls.Add(1)
	}
	eligible := true
	if r.eligible != nil {
		eligible = *r.eligible
	}
	replay := r.replay
	onUnrep := r.onUnrep
	if onUnrep == "" {
		onUnrep = "reject"
	}

	switch r.mode {
	case "restore_missing", "restore_per_candidate", "restore_large", "preserve_or_conflict", "exclude_all_unrepresentable":
		if r.mode == "exclude_all_unrepresentable" {
			replay = lipapi.ReasoningReplaySupport{}
		}
		res, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
			Action:            reasoningpreservation.ActionRestore,
			OnUnrepresentable: onUnrep,
			Call:              call,
			Artifacts:         r.artifacts,
			Eligible:          eligible,
			ReplaySupport:     replay,
		})
		if err != nil {
			return request.AttemptDecision{}, err
		}
		if res.Exclude {
			return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "unrepresentable_replay"}, nil
		}
		if r.mode == "preserve_or_conflict" && res.Mutated {
			return request.AttemptDecision{}, errors.New("preserve/conflict path must not mutate")
		}
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	default:
		return request.AttemptDecision{}, errors.New("unknown reasoning preservation transform mode")
	}
}

func rpReasoningPart(text string) lipapi.Part {
	return lipapi.Part{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{
		Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: text,
	}}
}

func rpMissingReasoningCall(selector string) *lipapi.Call {
	return &lipapi.Call{
		ID: "rp-baseline", Route: lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("visible answer")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("follow-up")}},
		},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeNonStreaming},
	}
}

func rpClientReasoningCall(selector, clientThought string) *lipapi.Call {
	call := rpMissingReasoningCall(selector)
	call.Messages[0].Parts = append([]lipapi.Part{rpReasoningPart(clientThought)}, call.Messages[0].Parts...)
	return call
}

func rpReasoningTexts(call *lipapi.Call) []string {
	if call == nil {
		return nil
	}
	var out []string
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if p.Kind == lipapi.PartReasoning && p.Reasoning != nil {
				out = append(out, p.Reasoning.Text)
			}
		}
	}
	return out
}

func rpHasReasoningText(call lipapi.Call, want string) bool {
	return slices.Contains(rpReasoningTexts(&call), want)
}

func rpWire(t *testing.T, bundle lipfeature.FeatureBundle) (*hooks.Bus, *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	bus := hooks.New(hooks.Config{ResponsePartHooks: lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneResponsePartHooks)})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: bundle.PlaneSet,
	})
	if want, got := len(lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneAttemptTransforms)), len(snap.AttemptTransforms()); want != got {
		t.Fatalf("precondition: snapshot AttemptTransforms len=%d want %d", got, want)
	}
	if want, got := len(lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneStreamObserverFactories)), len(snap.StreamObserverFactories()); want != got {
		t.Fatalf("precondition: snapshot StreamObserverFactories len=%d want %d", got, want)
	}
	return bus, snap
}

func rpExecutor(t *testing.T, xform *reasoningPreservationTransform, mutate func(*runtime.Executor)) (*runtime.Executor, *reasoningPreservationTransform) {
	t.Helper()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cs := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "xform", []request.AttemptTransform{xform})
	b := lipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	bus, snap := rpWire(t, b)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	if mutate != nil {
		mutate(ex)
	}
	return ex, xform
}

func rpCollectCall(t *testing.T, ex *runtime.Executor, call *lipapi.Call) {
	t.Helper()
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		if strings.Contains(err.Error(), `unknown part kind "reasoning"`) {
			t.Fatal("RED: Call.Validate must accept PartReasoning so client-preserved/conflicting history can reach AttemptTransform (Phase 2.1)")
		}
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = lipapi.Collect(t.Context(), stream)
	}
}

func rpCollect(t *testing.T, ex *runtime.Executor, selector string) {
	t.Helper()
	rpCollectCall(t, ex, rpMissingReasoningCall(selector))
}

func rpStreamingBackend(openFn func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps:          lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityReasoningReplay),
		ReplaySupport: rpReplaySupport,
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		}),
		Open: openFn,
	}
}

type reasoningAwareTokenEstimator struct{ base int64 }

func (r reasoningAwareTokenEstimator) EstimateRequestTokens(_ context.Context, call lipapi.Call) modelcatalog.SizeEstimate {
	tokens := r.base
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if p.Kind == lipapi.PartReasoning && p.Reasoning != nil {
				tokens += int64(len(p.Reasoning.Text))
			}
		}
	}
	return modelcatalog.SizeEstimate{Available: true, Units: "tokens", Input: tokens, Basis: "test-reasoning-restore"}
}

func TestReasoningPreservationComposition_disabledFeatureNonInterference_characterization(t *testing.T) {
	t.Parallel()
	var opened atomic.Int64
	ex := runtime.TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store, ex.Bus, ex.Rand = st, hooks.New(hooks.Config{}), routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		opened.Add(1)
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
	})}
	call := rpMissingReasoningCall("be:m")
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(t.Context(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opened.Load() != 1 || call.ID != "rp-baseline" {
		t.Fatalf("baseline unchanged without feature: opens=%d id=%q", opened.Load(), call.ID)
	}
}

func TestReasoningPreservationComposition_exactRestoreBeforeOpen(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_missing", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var gotCall lipapi.Call
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(2)
		ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			gotCall = call
			return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
		})}
	})
	rpCollect(t, ex, "be:m")
	if xform.calls.Load() == 0 {
		t.Fatal("RED: reasoning preservation AttemptTransform must run before backend Open (runner absent)")
	}
	if !rpHasReasoningText(gotCall, rpStoredThought) {
		t.Fatal("RED: restored PartReasoning must reach backend Open after exact restoration path")
	}
}

func TestReasoningPreservationComposition_clientReasoningPreservedNoOverwrite(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"preserved", "conflicting"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			xform := &reasoningPreservationTransform{
				mode: "preserve_or_conflict", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
				replay: rpReplaySupport, calls: &atomic.Int64{},
			}
			var gotCall lipapi.Call
			ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
				ex.Rand = routing.NewSeededRng(2)
				ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					gotCall = call
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				})}
			})
			rpCollectCall(t, ex, rpClientReasoningCall("be:m", "client-thought"))
			if xform.calls.Load() == 0 {
				t.Fatal("RED: reasoning preservation AttemptTransform must run to enforce preserved/conflicting no-overwrite policy")
			}
			if !rpHasReasoningText(gotCall, "client-thought") || rpHasReasoningText(gotCall, rpStoredThought) {
				t.Fatal("RED: preserved/conflicting client PartReasoning must not be overwritten by restoration")
			}
		})
	}
}

func TestReasoningPreservationComposition_failoverRestorationIsolation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, redCalls string
		backendA       func(*sync.Mutex, *[]lipapi.Call) execbackend.Backend
	}{
		{"sequential", "RED: AttemptTransform must run independently per sequential candidate", func(mu *sync.Mutex, calls *[]lipapi.Call) execbackend.Backend {
			return rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				mu.Lock()
				*calls = append(*calls, call)
				mu.Unlock()
				return nil, lipapi.RecoverablePreOutputError(errors.New("temp-a"))
			})
		}},
		{"recv", "RED: AttemptTransform must run for each recv-replaced candidate", func(mu *sync.Mutex, calls *[]lipapi.Call) execbackend.Backend {
			return rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				mu.Lock()
				*calls = append(*calls, call)
				mu.Unlock()
				return &observerFailAfterStream{
					events: []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}},
					fail:   lipapi.RecoverablePreOutputError(errors.New("recv-pre-output")),
				}, nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			xform := &reasoningPreservationTransform{
				mode: "restore_per_candidate", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
				replay: rpReplaySupport, calls: &atomic.Int64{},
			}
			var mu sync.Mutex
			var openCalls []lipapi.Call
			ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
				ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(11)
				ex.Backends = map[string]execbackend.Backend{
					"a": tc.backendA(&mu, &openCalls),
					"b": rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						mu.Lock()
						openCalls = append(openCalls, call)
						mu.Unlock()
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventMessageStarted},
							{Kind: lipapi.EventTextDelta, Delta: "replacement"},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					}),
				}
			})
			rpCollect(t, ex, "a:m|b:m")
			mu.Lock()
			got := append([]lipapi.Call(nil), openCalls...)
			mu.Unlock()
			if len(got) < 2 {
				t.Fatalf("want failover opens>=2 got %d", len(got))
			}
			if xform.calls.Load() < 2 {
				t.Fatal(tc.redCalls)
			}
			if !rpHasReasoningText(got[0], rpStoredThought) || !rpHasReasoningText(got[1], rpStoredThought) {
				t.Fatal("RED: restored PartReasoning must reach each isolated failover attempt clone")
			}
		})
	}
}

func TestReasoningPreservationComposition_restoredContextLimitExclusionRecompute(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_large", artifacts: []reasoningpreservation.TurnArtifact{rpLargeArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var openedSmall, openedBig atomic.Int64
	var gotBig lipapi.Call
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(2)
		ex.CatalogResolver = contextLimitCatalogResolver{}
		ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
		ex.RequestTokenEstimator = reasoningAwareTokenEstimator{base: 5}
		ex.Backends = map[string]execbackend.Backend{
			"smallctx": rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openedSmall.Add(1)
				_ = call
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			}),
			"bigctx": rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openedBig.Add(1)
				gotBig = call
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			}),
		}
	})
	rpCollect(t, ex, "smallctx:m|bigctx:m")
	if xform.calls.Load() == 0 {
		t.Fatal("RED: AttemptTransform must run before context-limit eligibility recompute")
	}
	if openedSmall.Load() != 0 {
		t.Fatal("RED: restored reasoning size must exclude smallctx before Open")
	}
	if openedBig.Load() == 0 {
		t.Fatal("RED: failover must open bigctx after restored reasoning pushes request over smallctx limit")
	}
	if !rpHasReasoningText(gotBig, rpLargeThought) {
		t.Fatal("RED: bigctx Open must observe restored PartReasoning after context-limit failover")
	}
}

func TestReasoningPreservationComposition_parallelRestorationIndependentClones(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_per_candidate", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var mu sync.Mutex
	var openCalls []lipapi.Call
	var openArrived atomic.Int32
	gate := make(chan struct{})
	track := func(name string) execbackend.Backend {
		return rpStreamingBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			mu.Lock()
			openCalls = append(openCalls, call)
			mu.Unlock()
			if openArrived.Add(1) == 2 {
				close(gate)
			}
			select {
			case <-gate:
			case <-time.After(3 * time.Second):
				return nil, errors.New("parallel open barrier timed out waiting for peer arm")
			}
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: name + "-response"},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		})
	}
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(1)
		ex.Backends = map[string]execbackend.Backend{"slow": track("slow"), "fast": track("fast")}
	})
	rpCollect(t, ex, "slow:model!fast:model")
	mu.Lock()
	got := append([]lipapi.Call(nil), openCalls...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("parallel arms must each Open, got %d", len(got))
	}
	if xform.calls.Load() < 2 {
		t.Fatal("RED: AttemptTransform must run independently on each parallel arm clone")
	}
	restored := 0
	for _, call := range got {
		if rpHasReasoningText(call, rpStoredThought) {
			restored++
		}
	}
	if restored < 2 {
		t.Fatal("RED: each parallel arm Open must observe restored PartReasoning on its independent clone")
	}
}

func TestReasoningPreservationComposition_weightedRestorationIndependentClones(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_per_candidate", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var mu sync.Mutex
	var openCalls []lipapi.Call
	var backends []string
	track := func(name string) execbackend.Backend {
		return rpStreamingBackend(func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			mu.Lock()
			openCalls = append(openCalls, call)
			backends = append(backends, cand.Primary.Backend)
			mu.Unlock()
			_ = name
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "ok"},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		})
	}
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(7)
		ex.Backends = map[string]execbackend.Backend{"a": track("a"), "b": track("b")}
	})
	ex.Backends["a"] = rpStreamingBackend(func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		mu.Lock()
		openCalls = append(openCalls, call)
		backends = append(backends, cand.Primary.Backend)
		mu.Unlock()
		return nil, lipapi.RecoverablePreOutputError(errors.New("temp-a"))
	})
	rpCollect(t, ex, "[weight=1]a:m^[weight=1]b:m")
	mu.Lock()
	got := append([]lipapi.Call(nil), openCalls...)
	gotBE := append([]string(nil), backends...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("weighted failover must open >=2 candidates, got %d backends=%v", len(got), gotBE)
	}
	if xform.calls.Load() < 2 {
		t.Fatal("RED: AttemptTransform must run independently for each weighted candidate attempt")
	}
	if !rpHasReasoningText(got[0], rpStoredThought) || !rpHasReasoningText(got[1], rpStoredThought) {
		t.Fatalf("RED: weighted candidates must each observe isolated restored reasoning; opens=%v texts0=%v texts1=%v", gotBE, rpReasoningTexts(&got[0]), rpReasoningTexts(&got[1]))
	}
}

func TestReasoningPreservationComposition_responseHookAndGateCapture_RED(t *testing.T) {
	t.Parallel()
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	cs := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{factory})
	_ = lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "hook", []sdkhooks.ResponsePartHook{mutateTextResponseHook{}})
	b := lipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, b)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventReasoningDelta, Delta: "thought"},
			{Kind: lipapi.EventTextDelta, Delta: "orig"},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	})}
	stream, err := ex.Execute(t.Context(), rpMissingReasoningCall("be:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	col, err := lipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Text.String() != observerHookMarker {
		t.Fatalf("response hook must mutate client text; got %q", col.Text.String())
	}
	if factory.opens.Load() == 0 {
		t.Fatal("RED: stream observer must Open so response-hook mutation can be captured for reasoning preservation")
	}
	obs := factory.snapshotObservers()
	if len(obs) == 0 {
		t.Fatal("RED: observer must exist for hook-mutated capture")
	}
	obs[0].mu.Lock()
	events := append([]lipapi.Event(nil), obs[0].events...)
	obs[0].mu.Unlock()
	sawHook, sawReasoning := false, false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && ev.Delta == observerHookMarker {
			sawHook = true
		}
		if ev.Kind == lipapi.EventReasoningDelta && ev.Delta == "thought" {
			sawReasoning = true
		}
	}
	if !sawHook || !sawReasoning {
		t.Fatal("RED: observer must capture post-hook mutated text and reasoning deltas")
	}

	factory2 := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	cs2 := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs2, lipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{factory2})
	_ = lipfeature.Contribute(cs2, lipfeature.PlaneCompletionGates, "gate", []completion.Gate{replaceAllGate{}})
	b2 := lipfeature.BundleFromPlanes(cs2.Freeze(), nil)
	if err := b2.Validate(); err != nil {
		t.Fatal(err)
	}
	st2, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus2, snap2 := rpWire(t, b2)
	ex2 := runtime.TestExecutor()
	ex2.Store, ex2.Bus, ex2.RuntimeSnapshot, ex2.Rand = st2, bus2, snap2, routing.NewSeededRng(2)
	ex2.Backends = map[string]execbackend.Backend{"be": fixedSuccessBackend("orig")}
	stream2, err := ex2.Execute(t.Context(), rpMissingReasoningCall("be:m"))
	if err != nil {
		t.Fatalf("execute gate: %v", err)
	}
	col2, err := lipapi.Collect(t.Context(), stream2)
	if err != nil {
		t.Fatalf("collect gate: %v", err)
	}
	if col2.Text.String() != "gate-replaced-text" {
		t.Fatalf("gate must replace client text; got %q", col2.Text.String())
	}
	observers := factory2.snapshotObservers()
	if len(observers) == 0 {
		t.Fatal("RED: gate replacement must open observer lifecycle for capture")
	}
	var sawGateReplaced, sawReplacement, sawOrig bool
	for _, o := range observers {
		o.mu.Lock()
		for _, outcome := range o.outcomes {
			if outcome == response.OutcomeGateReplaced {
				sawGateReplaced = true
			}
		}
		for _, ev := range o.events {
			if ev.Kind == lipapi.EventTextDelta && ev.Delta == "gate-replaced-text" {
				sawReplacement = true
			}
			if ev.Kind == lipapi.EventTextDelta && ev.Delta == "orig" {
				sawOrig = true
			}
		}
		o.mu.Unlock()
	}
	if !sawGateReplaced || !sawReplacement || sawOrig {
		t.Fatal("RED: gate_replaced must discard original capture and observe replacement only")
	}
}

func TestReasoningPreservationComposition_streamObserverContributionDropped(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{mode: "restore_missing", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact}, replay: rpReplaySupport, calls: &atomic.Int64{}}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	cs := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "xform", []request.AttemptTransform{xform})
	_ = lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{factory})
	b := lipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, b)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventTextDelta, Delta: "ok"},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	})}
	rpCollect(t, ex, "be:m")
	if factory.opens.Load() == 0 {
		t.Fatal("RED: reasoning FeatureBundle StreamObserverFactories must Open on winning B-leg (runner absent; see Phase 1.3)")
	}
}

func TestReasoningPreservationTransform_unrepresentableRejectExcludesCandidate(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "exclude_all_unrepresentable", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{}, onUnrep: "reject",
	}
	dec, err := xform.HandleAttempt(t.Context(), rpMissingReasoningCall("a:m"), request.AttemptMeta{BackendID: "a", CandidateKey: "a:m"}, request.Services{})
	if err != nil {
		t.Fatalf("RestoreMissingReasoning: %v", err)
	}
	if dec.Kind != request.AttemptExcludeCandidate || dec.ReasonCode != "unrepresentable_replay" {
		t.Fatalf("expected exclude_candidate/unrepresentable_replay, got dec=%+v", dec)
	}
	if xform.calls.Load() != 1 {
		t.Fatalf("transform must be invoked once, calls=%d", xform.calls.Load())
	}
}

func TestReasoningPreservationComposition_unrepresentableReplayAllExcluded_RED(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "exclude_all_unrepresentable", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{}, onUnrep: "reject",
	}
	var opens atomic.Int64
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(11)
		ex.Backends = map[string]execbackend.Backend{
			"a": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			}),
			"b": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			}),
		}
	})
	_, err := ex.Execute(t.Context(), rpMissingReasoningCall("a:m|b:m"))
	if err == nil {
		t.Fatal("RED: all candidates excluded for unrepresentable_replay must surface stable error (contribution/transform wiring absent or exclude path incomplete)")
	}
	if errors.Is(err, reasoningpreservation.ErrNotImplemented) {
		t.Fatal("RED: ErrNotImplemented fail-closed must not satisfy unrepresentable_replay acceptance; RestoreMissingReasoning must emit stable exclude classification")
	}
	safe := strings.ToLower(err.Error())
	if !strings.Contains(safe, "unrepresentable_replay") {
		t.Fatalf("RED: expected stable unrepresentable_replay classification in error, got %v", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("RED: exclude_candidate for all backends must prevent Open, opens=%d", opens.Load())
	}
	if xform.calls.Load() == 0 {
		t.Fatal("RED: reasoning preservation AttemptTransform must run per considered candidate to emit exclude_candidate decisions")
	}
}

func TestReasoningPreservationComposition_noRetryAfterFirstOutput_characterization(t *testing.T) {
	t.Parallel()
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store, ex.Bus, ex.MaxAttempts, ex.Rand = st, hooks.New(hooks.Config{}), 3, routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"one": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return &deltaThenErrStream{n: 0}, nil
		}),
		"two": rpStreamingBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
		}),
	}
	stream, err := ex.Execute(t.Context(), rpMissingReasoningCall("one:m|two:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for range 3 {
		if _, err := stream.Recv(t.Context()); err != nil {
			t.Fatalf("unexpected recv error: %v", err)
		}
	}
	_, recvErr := stream.Recv(t.Context())
	if recvErr == nil {
		t.Fatal("expected error after committed output")
	}
	if lipapi.IsRecoverablePreOutput(recvErr) {
		t.Fatal("post-output failure must not classify as recoverable pre-output for retry")
	}
	if opens.Load() != 1 {
		t.Fatalf("expected no failover backend open after text delta committed, opens=%d", opens.Load())
	}
	_ = stream.Close()
}
