package metering

import (
	"context"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipsdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase6BoundaryPreparedInputUsesAdapterFinalRepresentation(t *testing.T) {
	acc := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64, MaxMediaEntries: 8})
	acc.PrepareCall(lipapi.Call{Messages: []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "before"}, {Kind: lipapi.PartImageRef, ImageRef: "ingress", ImageMIME: "image/png"}},
	}}})
	acc.ObservePreparedInput(PreparedInputSummary{
		TextBytes:        6,
		TextBytesPresent: true,
		Media:            []MediaSummary{{Kind: MediaAudio, Count: 1, DurationMillis: 2500, DurationPresent: true, Bytes: 400, BytesPresent: true}},
		MethodRef:        "adapter:test-transcode.v1",
	})
	acc.MarkAttempted()
	acc.MarkAccepted(true)

	snapshot := acc.Snapshot()
	if !snapshot.Input.Prepared || !snapshot.Input.Attempted || !snapshot.Input.Accepted || !snapshot.Input.AdapterProvided {
		t.Fatalf("prepared input lifecycle = %#v", snapshot.Input)
	}
	if got := snapshot.Input.TextBytes; got != 6 {
		t.Fatalf("adapter text bytes = %d, want 6", got)
	}
	if len(snapshot.Input.Media) != 1 || snapshot.Input.Media[0].Kind != MediaAudio || snapshot.Input.Media[0].DurationMillis != 2500 {
		t.Fatalf("adapter media = %#v", snapshot.Input.Media)
	}
	for _, observation := range snapshot.Observations(testBoundaryIdentity()) {
		if observation.Boundary != lipsdkmetering.BoundaryBackendEgress && observation.Boundary != lipsdkmetering.BoundaryBackendIngress && observation.Boundary != lipsdkmetering.BoundaryFrontendEgress {
			t.Fatalf("unexpected boundary: %q", observation.Boundary)
		}
		for _, measure := range observation.Measures {
			if measure.Key.Component == "raw_text" || measure.Key.Component == "raw_payload" {
				t.Fatalf("raw payload component retained: %#v", measure.Key)
			}
		}
		if err := observation.Validate(); err != nil {
			t.Fatalf("observation validation: %v", err)
		}
	}
	observations := snapshot.Observations(testBoundaryIdentity())
	if observations[0].Boundary != lipsdkmetering.BoundaryBackendEgress || observations[1].Boundary != lipsdkmetering.BoundaryBackendIngress || observations[2].Boundary != lipsdkmetering.BoundaryFrontendEgress {
		t.Fatalf("boundary order = %q, %q, %q", observations[0].Boundary, observations[1].Boundary, observations[2].Boundary)
	}
}

func TestPhase6BoundaryOutputIsChunkInvariantAndSeparatesCustomerEgress(t *testing.T) {
	one := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64, MaxMediaEntries: 8})
	two := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64, MaxMediaEntries: 8})
	for _, acc := range []*BoundaryAccumulator{one, two} {
		acc.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"})
		acc.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: " world"})
	}
	two = NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64, MaxMediaEntries: 8})
	two.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello world"})
	one.ObserveCustomerEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"})
	two.ObserveCustomerEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"})
	one.ObserveCustomerEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: " world"})
	two.ObserveCustomerEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: " world"})

	a, b := one.Snapshot(), two.Snapshot()
	if a.ProviderOutput.TextBytes != b.ProviderOutput.TextBytes || a.ProviderOutput.TextTokens != b.ProviderOutput.TextTokens {
		t.Fatalf("chunk-sensitive provider output: %#v vs %#v", a.ProviderOutput, b.ProviderOutput)
	}
	if a.CustomerOutput.TextBytes != b.CustomerOutput.TextBytes || a.CustomerOutput.TextTokens != b.CustomerOutput.TextTokens {
		t.Fatalf("chunk-sensitive customer output: %#v vs %#v", a.CustomerOutput, b.CustomerOutput)
	}
	if a.ProviderOutput.TextBytes != 11 || a.CustomerOutput.TextBytes != 11 {
		t.Fatalf("output bytes = provider %d/customer %d, want 11/11", a.ProviderOutput.TextBytes, a.CustomerOutput.TextBytes)
	}
	if a.ProviderOutput.TextTokens == 0 || a.CustomerOutput.TextTokens == 0 {
		t.Fatal("expected bounded local token estimates")
	}

	media := MediaSummary{Kind: MediaVideo, Count: 1, DurationMillis: 1500, DurationPresent: true, Frames: 45, FramesPresent: true, Bytes: 900, BytesPresent: true}
	one.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventAssistantFileRef, AssistantMIME: "video/mp4"}, media)
	one.ObserveCustomerEvent(lipapi.Event{Kind: lipapi.EventAssistantFileRef, AssistantMIME: "image/jpeg"}, MediaSummary{
		Kind: MediaImage, Count: 1, Bytes: 120, BytesPresent: true, WidthPixels: 256, WidthPresent: true,
		HeightPixels: 256, HeightPresent: true,
	})
	s := one.Snapshot()
	if len(s.ProviderOutput.Media) != 1 || s.ProviderOutput.Media[0].Kind != MediaVideo || s.ProviderOutput.Media[0].Frames != 45 {
		t.Fatalf("provider media = %#v", s.ProviderOutput.Media)
	}
	if len(s.CustomerOutput.Media) != 1 || s.CustomerOutput.Media[0].Kind != MediaImage || s.CustomerOutput.Media[0].Bytes != 120 {
		t.Fatalf("customer transformed media must remain independent: %#v", s.CustomerOutput.Media)
	}
}

func TestPhase6BoundaryMarksUnknownProviderEconomicsUnavailable(t *testing.T) {
	acc := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64, MaxMediaEntries: 8})
	observations := acc.Observations(testBoundaryIdentity())
	if len(observations) != 3 {
		t.Fatalf("observation count = %d, want three local planes", len(observations))
	}
	var sawCache, sawReasoning, sawTools, sawCompute bool
	for _, observation := range observations {
		if observation.Origin != lipsdkmetering.OriginLocal || observation.Acquisition != lipsdkmetering.AcquisitionLocalMeasurement {
			t.Fatalf("local observation provenance = %s/%s", observation.Origin, observation.Acquisition)
		}
		for _, measure := range observation.Measures {
			if measure.Value != nil && measure.Key.Component == lipsdkmetering.ComponentCacheReadInputToken {
				t.Fatal("unknown cache disposition was reported as a value")
			}
			if measure.Value != nil && measure.Key.Component == lipsdkmetering.ComponentReasoningOutputToken {
				t.Fatal("hidden reasoning was reported as a value")
			}
			switch measure.Key.Component {
			case lipsdkmetering.ComponentCacheReadInputToken, lipsdkmetering.ComponentCacheWriteInputToken:
				sawCache = sawCache || measure.Quality == lipsdkmetering.QualityUnavailable
			case lipsdkmetering.ComponentReasoningOutputToken:
				sawReasoning = sawReasoning || measure.Quality == lipsdkmetering.QualityUnavailable
			case lipsdkmetering.ComponentToolQuery:
				sawTools = sawTools || measure.Quality == lipsdkmetering.QualityUnavailable
			case "compute_time":
				sawCompute = sawCompute || measure.Quality == lipsdkmetering.QualityUnavailable
			}
		}
	}
	if !sawCache || !sawReasoning || !sawTools || !sawCompute {
		t.Fatalf("unobservable coverage cache=%v reasoning=%v tools=%v compute=%v", sawCache, sawReasoning, sawTools, sawCompute)
	}
}

func TestPhase6BoundaryBoundsCaptureAndContextObserver(t *testing.T) {
	acc := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 4, MaxMediaEntries: 1})
	acc.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "0123456789"})
	acc.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventAssistantImageRef, AssistantMIME: "image/png"})
	acc.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventAssistantImageRef, AssistantMIME: "image/jpeg"})
	snapshot := acc.Snapshot()
	if snapshot.ProviderOutput.TextBytes != 4 || !snapshot.ProviderOutput.TextTruncated || !snapshot.ProviderOutput.Truncated {
		t.Fatalf("bounded output = %#v", snapshot.ProviderOutput)
	}
	if len(snapshot.ProviderOutput.Media) != 1 || !snapshot.ProviderOutput.MediaTruncated {
		t.Fatalf("bounded media = %#v", snapshot.ProviderOutput)
	}

	called := false
	ctx := WithPreparedInputObserver(context.Background(), func(summary PreparedInputSummary) {
		called = summary.TextBytes == 3 && summary.TextBytesPresent
	})
	ObservePreparedInput(ctx, PreparedInputSummary{TextBytes: 3, TextBytesPresent: true})
	if !called {
		t.Fatal("prepared-input context observer was not called")
	}
}

func TestPhase6BoundaryObservationMeasureBound(t *testing.T) {
	acc := NewBoundaryAccumulator(BoundaryConfig{MaxMediaEntries: DefaultBoundaryMaxMediaEntries})
	for i := 0; i < DefaultBoundaryMaxMediaEntries; i++ {
		acc.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventAssistantImageRef, AssistantMIME: "image/png"}, MediaSummary{
			Kind: MediaImage, Count: 1, Bytes: int64(i + 1), BytesPresent: true,
			WidthPixels: 1024, WidthPresent: true, HeightPixels: 1024, HeightPresent: true,
		})
	}
	for _, observation := range acc.Observations(testBoundaryIdentity()) {
		if len(observation.Measures) > lipsdkmetering.MaxObservationMeasures {
			t.Fatalf("measure count = %d, want <= %d", len(observation.Measures), lipsdkmetering.MaxObservationMeasures)
		}
		if err := observation.Validate(); err != nil {
			t.Fatalf("bounded observation validation: %v", err)
		}
	}
}

func TestPhase6BoundaryLifecycleStatesAreDurableAndReplayStable(t *testing.T) {
	tests := []struct {
		name          string
		mark          func(*BoundaryAccumulator)
		attempted     string
		accepted      string
		acceptedValue string
	}{
		{
			name: "prepared-only",
			mark: func(acc *BoundaryAccumulator) {
				acc.PrepareCall(lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("prepared")}}}})
			},
			attempted: "0",
			accepted:  "unavailable",
		},
		{
			name: "open-failed",
			mark: func(acc *BoundaryAccumulator) {
				acc.PrepareCall(lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("failed")}}}})
				acc.MarkAttempted()
				acc.MarkAccepted(false)
			},
			attempted:     "1",
			accepted:      "observed",
			acceptedValue: "0",
		},
		{
			name: "open-accepted",
			mark: func(acc *BoundaryAccumulator) {
				acc.PrepareCall(lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("accepted")}}}})
				acc.MarkAttempted()
				acc.MarkAccepted(true)
			},
			attempted:     "1",
			accepted:      "observed",
			acceptedValue: "1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := NewBoundaryAccumulator()
			tt.mark(acc)
			identity := testBoundaryIdentity()
			first := acc.Observations(identity)
			if len(first) != 3 {
				t.Fatalf("observations=%d, want three durable boundary planes", len(first))
			}
			input := first[0]
			seenPrepared, seenAttempted, seenAccepted := false, false, false
			for _, measure := range input.Measures {
				if measure.Key.Component != lipsdkmetering.ComponentRequest || len(measure.Key.Dimensions) != 1 || measure.Key.Dimensions[0].Name != "state" {
					continue
				}
				value := "unavailable"
				if measure.Value != nil {
					value = measure.Value.Coefficient
				}
				switch measure.Key.Dimensions[0].Value {
				case "prepared":
					seenPrepared = value == "1" && measure.Quality == lipsdkmetering.QualityObserved
				case "attempted":
					seenAttempted = value == tt.attempted && measure.Quality == lipsdkmetering.QualityObserved
				case "accepted":
					if tt.accepted == "unavailable" {
						seenAccepted = value == "unavailable" && measure.Quality == lipsdkmetering.QualityUnavailable
					} else {
						seenAccepted = value == tt.acceptedValue && measure.Quality == lipsdkmetering.QualityObserved
					}
				}
			}
			if !seenPrepared || !seenAttempted || !seenAccepted {
				t.Fatalf("lifecycle measures missing/unstable prepared=%v attempted=%v accepted=%v: %#v", seenPrepared, seenAttempted, seenAccepted, input.Measures)
			}
			for _, observation := range first {
				if err := observation.Validate(); err != nil {
					t.Fatalf("durable observation validation: %v", err)
				}
			}
			second := acc.Observations(identity)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("replayed observations changed: first=%#v second=%#v", first, second)
			}
		})
	}
}

func testBoundaryIdentity() ObservationIdentity {
	return ObservationIdentity{StoreID: "store-1", RequestID: "request-1", CallID: "call-1", BillingCallID: "billing-1", ALegID: "a-leg-1", BLegID: "b-leg-1", AttemptID: "attempt-1", AttemptSeq: 1}
}

// TestObservePreparedInputObserverMatrix pins the fail-safe boundary contract:
// nil contexts never panic, absent observers are no-ops, a directly stored
// typed-nil observer reports disabled and is never invoked, and a valid
// observer fires exactly once with the exact summary.
func TestObservePreparedInputObserverMatrix(t *testing.T) {
	t.Parallel()
	summary := PreparedInputSummary{TextBytes: 7, TextBytesPresent: true}

	t.Run("nil context", func(t *testing.T) {
		t.Parallel()
		calls := 0
		// Must not panic and must not call back.
		ObservePreparedInput(nil, summary) //nolint:staticcheck // deliberately exercises the nil-context guard
		if calls != 0 {
			t.Fatalf("nil context produced %d callbacks, want 0", calls)
		}
		if PreparedInputObservationEnabled(nil) { //nolint:staticcheck // deliberately exercises the nil-context guard
			t.Fatal("nil context must report disabled")
		}
	})

	t.Run("absent observer", func(t *testing.T) {
		t.Parallel()
		calls := 0
		ObservePreparedInput(context.Background(), summary)
		if calls != 0 {
			t.Fatalf("absent observer produced %d callbacks, want 0", calls)
		}
		if PreparedInputObservationEnabled(context.Background()) {
			t.Fatal("absent observer must report disabled")
		}
	})

	t.Run("typed-nil observer", func(t *testing.T) {
		t.Parallel()
		var fn PreparedInputObserver
		// Inject directly: WithPreparedInputObserver intentionally elides
		// nil, so only a direct store can place a typed-nil func value.
		ctx := context.WithValue(context.Background(), preparedInputObserverKey{}, fn)
		if PreparedInputObservationEnabled(ctx) {
			t.Fatal("typed-nil observer must report disabled")
		}
		calls := 0
		// Must not panic and must not call back into the nil func value.
		ObservePreparedInput(ctx, summary)
		if calls != 0 {
			t.Fatalf("typed-nil observer produced %d callbacks, want 0", calls)
		}
	})

	t.Run("valid observer once", func(t *testing.T) {
		t.Parallel()
		calls := 0
		var got PreparedInputSummary
		ctx := WithPreparedInputObserver(context.Background(), func(s PreparedInputSummary) {
			calls++
			got = s
		})
		if !PreparedInputObservationEnabled(ctx) {
			t.Fatal("attached observer must report enabled")
		}
		ObservePreparedInput(ctx, summary)
		if calls != 1 {
			t.Fatalf("valid observer produced %d callbacks, want exactly 1", calls)
		}
		if !reflect.DeepEqual(got, summary) {
			t.Fatalf("observer summary = %#v, want %#v", got, summary)
		}
	})
}
