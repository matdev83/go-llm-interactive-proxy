package streampeek

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

type promptCacheManagedStream struct {
	*lipapi.FixedEventStream
	source *promptcache.ObservationBuffer
}

func (s *promptCacheManagedStream) DrainPromptCacheObservations() []promptcache.Observation {
	return s.source.DrainPromptCacheObservations()
}

func (s *promptCacheManagedStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func TestManagedPrependForwardsPromptCacheObservations(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	observation := promptcache.Observation{
		ALegID: "a", BLegID: "b", BackendInstanceID: "backend",
		TargetID: "target", GenerationID: "generation",
		Lifecycle: promptcache.LifecycleBestEffort,
		Timing:    promptcache.Timing{ObservedAt: now},
		Renewable: true, Handle: promptcache.Handle("handle"),
	}
	var buffer promptcache.ObservationBuffer
	if err := buffer.Add(observation); err != nil {
		t.Fatal(err)
	}
	buffer.Commit()
	inner := &promptCacheManagedStream{FixedEventStream: lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), source: &buffer}
	wrapped := NewManagedPrependFirst(lipapi.Event{Kind: lipapi.EventResponseStarted}, inner)
	if _, err := wrapped.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := wrapped.(promptcache.ObservationSource)
	if !ok {
		t.Fatal("managed prepend does not preserve observation source")
	}
	observations := got.DrainPromptCacheObservations()
	if len(observations) != 1 || observations[0].TargetID != observation.TargetID {
		t.Fatalf("observations=%+v", observations)
	}
	if second := got.DrainPromptCacheObservations(); second != nil {
		t.Fatalf("observation sideband drained more than once: %+v", second)
	}
}
