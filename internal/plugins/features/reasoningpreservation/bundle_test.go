package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func TestFeatureBundle_contributesPorts(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validRestoreYAML)
	b, err := reasoningpreservation.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	if len(b.AttemptTransforms) != 1 {
		t.Fatalf("AttemptTransforms=%d", len(b.AttemptTransforms))
	}
	if len(b.StreamObserverFactories) != 1 {
		t.Fatalf("StreamObserverFactories=%d", len(b.StreamObserverFactories))
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAttemptTransform_observeDoesNotMutate(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validObserveYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 1024,
		MaxSessionBytes:          4096,
		Now:                      time.Now,
	})
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	call, artifacts := missingRestoreFixture(t)
	partition := reasoningpreservation.NewSessionPartition("sess-1")
	if _, err := store.Append(context.Background(), partition, artifacts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before := cloneCall(t, call)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "kimi-k2",
		ReplaySupport:   lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:         session.SessionView{AuthoritativeSessionID: "sess-1"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptContinue {
		t.Fatalf("dec=%+v", dec)
	}
	if !reflectDeepEqualMessages(before.Messages, call.Messages) {
		t.Fatal("observe must not mutate")
	}
}

func TestStreamObserver_appendsOnlyOnSuccessReleased(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validObserveYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 1024,
		MaxSessionBytes:          4096,
		Now:                      time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-obs"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "answer"})
	if err := obs.Finish(context.Background(), response.OutcomeFailed); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-obs"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("failed outcome must not persist, got %d", len(snap))
	}

	obs2, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-obs"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open2: %v", err)
	}
	_ = obs2.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs2.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "answer"})
	if err := obs2.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish success: %v", err)
	}
	snap, err = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-obs"))
	if err != nil {
		t.Fatalf("Snapshot2: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("success_released must persist one artifact, got %d", len(snap))
	}
}

func reflectDeepEqualMessages(a, b []lipapi.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || len(a[i].Parts) != len(b[i].Parts) {
			return false
		}
		for j := range a[i].Parts {
			if a[i].Parts[j].Kind != b[i].Parts[j].Kind || a[i].Parts[j].Text != b[i].Parts[j].Text {
				return false
			}
		}
	}
	return true
}
