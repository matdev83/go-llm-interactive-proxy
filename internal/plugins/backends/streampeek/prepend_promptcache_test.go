package streampeek

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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

type economicStateManagedStream struct {
	*lipapi.FixedEventStream
	enabled bool
}

type usageSidebandManagedStream struct {
	*lipapi.FixedEventStream
	evidence []lipapi.Event
}

func (s *usageSidebandManagedStream) DrainUsageEvidence() []lipapi.Event {
	out := append([]lipapi.Event(nil), s.evidence...)
	s.evidence = nil
	return out
}

func (s *usageSidebandManagedStream) AccountingEvidenceEnabled() bool { return true }

func (s *usageSidebandManagedStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *economicStateManagedStream) DrainEconomicObservations() []metering.Observation {
	return nil
}

func (s *economicStateManagedStream) AccountingEvidenceEnabled() bool {
	return s.enabled
}

func (s *economicStateManagedStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
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

func TestManagedPrependForwardsEconomicNegotiationState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		enabled := enabled
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			inner := &economicStateManagedStream{FixedEventStream: lipapi.NewFixedEventStream(nil), enabled: enabled}
			wrapped := NewManagedPrependFirst(lipapi.Event{Kind: lipapi.EventResponseStarted}, inner)
			state, ok := wrapped.(interface{ AccountingEvidenceEnabled() bool })
			if !ok {
				t.Fatal("managed prepend does not preserve economic negotiation state")
			}
			if got := state.AccountingEvidenceEnabled(); got != enabled {
				t.Fatalf("economic sideband state=%v, want %v", got, enabled)
			}
		})
	}
}

func TestManagedPrependForwardsUsageEvidence(t *testing.T) {
	inner := &usageSidebandManagedStream{
		FixedEventStream: lipapi.NewFixedEventStream(nil),
		evidence:         []lipapi.Event{{Kind: lipapi.EventUsageDelta, InputTokens: 3}},
	}
	wrapped := NewManagedPrependFirst(lipapi.Event{Kind: lipapi.EventResponseStarted}, inner)
	source, ok := wrapped.(lipapi.UsageEvidenceSource)
	if !ok {
		t.Fatal("managed prepend does not preserve V1 usage evidence source")
	}
	got := source.DrainUsageEvidence()
	if len(got) != 1 || got[0].InputTokens != 3 {
		t.Fatalf("usage evidence=%+v", got)
	}
	if second := source.DrainUsageEvidence(); second != nil {
		t.Fatalf("usage evidence drained more than once: %+v", second)
	}
}
