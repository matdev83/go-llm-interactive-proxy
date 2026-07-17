package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func TestPhase5_realBundleStateErrorExcludesCandidate(t *testing.T) {
	t.Parallel()
	cfg := p5Config(t)
	inner, err := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144,
	})
	if err != nil {
		t.Fatal(err)
	}
	tel := reasoningpreservation.NewTelemetry()
	toggle := &toggleSnapshotStore{inner: inner}
	xform := reasoningpreservation.NewAttemptTransform(cfg, toggle, tel)
	obs := reasoningpreservation.NewStreamObserverFactory(cfg, toggle, tel)
	bundle := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		AttemptTransforms:       []request.AttemptTransform{xform},
		StreamObserverFactories: []response.StreamObserverFactory{obs},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, bundle)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.MaxAttempts, ex.Rand = st, bus, snap, 3, routing.NewSeededRng(11)
	var opens atomic.Int64
	ex.Backends = map[string]execbackend.Backend{
		"a": p5ReplayBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return lipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
		}),
		"b": p5ReplayBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return lipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
		}),
	}
	mint := p5ObserveCall()
	mint.Route.Selector = "a:m"
	p5Collect(t, ex, mint)
	mintOpens := opens.Load()
	toggle.failSnapshot.Store(true)
	restore := p5RestoreCall("a:m|b:m")
	restore.Session.ResumeToken = mint.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = mint.Session.AuthoritativeSessionID
	probeCall := lipapi.CloneCall(*restore)
	dec, derr := xform.HandleAttempt(t.Context(), &probeCall, request.AttemptMeta{
		BackendID: "a", Model: "m",
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: restore.Session.AuthoritativeSessionID},
	}, request.Services{})
	if derr != nil {
		t.Fatalf("HandleAttempt: %v", derr)
	}
	if dec.Kind != request.AttemptExcludeCandidate || dec.ReasonCode != "state_error" {
		t.Fatalf("transform must exclude with ReasonCode=state_error, got kind=%v reason=%q", dec.Kind, dec.ReasonCode)
	}
	_, err = ex.Execute(t.Context(), restore)
	if err == nil {
		t.Fatal("state_error reject must surface when all candidates hit store Snapshot failure")
	}
	if !errors.Is(err, lipapi.ErrAllCandidatesExcluded) {
		t.Fatalf("want ErrAllCandidatesExcluded, got %v", err)
	}
	if opens.Load() != mintOpens {
		t.Fatalf("restore candidates must not Open after state_error exclude, opens=%d mint=%d", opens.Load(), mintOpens)
	}
	if tel.Snapshot()[reasoningpreservation.OutcomeStateError] == 0 {
		t.Fatal("telemetry must record state_error")
	}
}

func TestPhase5_realBundleRestoredContextLimitExclusion(t *testing.T) {
	t.Parallel()
	cfg := p5ConfigWithExtraBackends(t, "smallctx", "bigctx")
	parts, bundle, err := reasoningpreservation.FeatureBundleWithParts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, bundle)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(2)
	ex.CatalogResolver = contextLimitCatalogResolver{}
	ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	ex.RequestTokenEstimator = reasoningAwareTokenEstimator{base: 5}
	var openedSmall, openedBig atomic.Int64
	var gotBig lipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"be": p5ReplayBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished},
			}), nil
		}),
		"smallctx": p5ReplayBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			openedSmall.Add(1)
			_ = call
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished},
			}), nil
		}),
		"bigctx": p5ReplayBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			openedBig.Add(1)
			gotBig = call
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished},
			}), nil
		}),
	}
	mint := p5ObserveCall()
	p5Collect(t, ex, mint)
	art := rpLargeArtifact
	art.CreatedAt = time.Time{}
	if _, err := parts.Store.Append(t.Context(), reasoningpreservation.NewSessionPartition(mint.Session.AuthoritativeSessionID), art); err != nil {
		t.Fatalf("Append: %v", err)
	}
	restore := p5RestoreCall("smallctx:m|bigctx:m")
	restore.Session.ResumeToken = mint.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = mint.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	if openedSmall.Load() != 0 {
		t.Fatal("restored reasoning must exclude smallctx before Open")
	}
	if openedBig.Load() == 0 || !rpHasReasoningText(gotBig, rpLargeThought) {
		t.Fatal("bigctx must open with restored large reasoning after recompute")
	}
}

func TestPhase5_parallelCancellationDiscardsPending(t *testing.T) {
	t.Parallel()
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(1)
		track := func(name string) execbackend.Backend {
			return p5ReplayBackend(func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				_ = call
				return &cancelAwareReasoningStream{name: name}, nil
			})
		}
		ex.Backends = map[string]execbackend.Backend{"slow": track("slow"), "fast": track("fast")}
	})
	ctx, cancel := context.WithCancel(t.Context())
	call := p5ObserveCall()
	call.Route.Selector = "slow:m!fast:m"
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	cancel()
	for {
		_, rerr := stream.Recv(context.Background())
		if rerr != nil {
			break
		}
	}
	_ = stream.Close()
	if sid := call.Session.AuthoritativeSessionID; sid != "" {
		if snap := mustSnapshot(t, parts, sid); len(snap) != 0 {
			t.Fatalf("cancelled parallel arms must not persist artifacts, got %d", len(snap))
		}
	}
}

func TestPhase5_winnerRestoredReasoningPrepended(t *testing.T) {
	t.Parallel()
	var got lipapi.Call
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					got = call
				}
				return lipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	observe := p5ObserveCall()
	p5Collect(t, ex, observe)
	if len(mustSnapshot(t, parts, observe.Session.AuthoritativeSessionID)) != 1 {
		t.Fatal("observe must persist")
	}
	restore := p5RestoreCall("be:m")
	restore.Session.ResumeToken = observe.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	if len(got.Messages) == 0 || len(got.Messages[0].Parts) < 2 {
		t.Fatalf("restored parts=%v", got.Messages)
	}
	if got.Messages[0].Parts[0].Kind != lipapi.PartReasoning || got.Messages[0].Parts[0].Reasoning.Text != p5Thought {
		t.Fatal("winner restore must prepend reasoning before non-reasoning parts")
	}
	if got.Messages[0].Parts[1].Kind != lipapi.PartText || got.Messages[0].Parts[1].Text != p5Visible {
		t.Fatal("visible non-reasoning content must follow prepended reasoning")
	}
}

type toggleSnapshotStore struct {
	inner        reasoningpreservation.TurnStore
	failSnapshot atomic.Bool
}

func (s *toggleSnapshotStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return s.inner.Append(ctx, p, a)
}
func (s *toggleSnapshotStore) Snapshot(ctx context.Context, p reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	if s.failSnapshot.Load() {
		return nil, errors.New("snapshot boom")
	}
	return s.inner.Snapshot(ctx, p)
}
func (s *toggleSnapshotStore) Delete(ctx context.Context, p reasoningpreservation.SessionPartition, ids ...string) error {
	return s.inner.Delete(ctx, p, ids...)
}

type cancelAwareReasoningStream struct {
	name string
	i    int
}

func (s *cancelAwareReasoningStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: p5Thought},
	}
	if s.i >= len(events) {
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return lipapi.Event{}, context.Canceled
		}
	}
	ev := events[s.i]
	s.i++
	return ev, nil
}
func (s *cancelAwareReasoningStream) Close() error { return nil }
func (s *cancelAwareReasoningStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func p5ConfigWithExtraBackends(t *testing.T, extras ...string) reasoningpreservation.Config {
	t.Helper()
	cfg := p5Config(t)
	for _, be := range extras {
		enabled := true
		cfg.Rules = append(cfg.Rules, reasoningpreservation.RuleConfig{
			ID: "test-" + be, Backend: be, Enabled: &enabled,
		})
	}
	return cfg
}
