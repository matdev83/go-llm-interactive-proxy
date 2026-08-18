package backendplugin_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func TestForwardExecute_PromptCacheObservationIsHostOnlyAndSuccessGated(t *testing.T) {
	t.Parallel()
	buffer := &promptcache.ObservationBuffer{}
	observation := promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "i", TargetID: "t", GenerationID: "g", Lifecycle: promptcache.LifecycleBestEffort, Timing: promptcache.Timing{ObservedAt: time.Unix(1, 0)}, Renewable: false}
	if err := buffer.Add(observation); err != nil {
		t.Fatal(err)
	}
	buffer.Commit()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	managed := &observationManaged{source: buffer}
	if err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
		return managed, nil
	}); err != nil {
		t.Fatal(err)
	}
	var sideband, canonical int
	for _, frame := range stream.sent {
		switch frame.Kind {
		case backendplugin.ServerFramePromptCacheObservation:
			sideband++
		case backendplugin.ServerFrameEvent:
			canonical++
		}
	}
	if sideband != 1 || canonical != 0 {
		t.Fatalf("sideband=%d canonical=%d frames=%#v", sideband, canonical, stream.sent)
	}
	if got := buffer.DrainPromptCacheObservations(); got != nil {
		t.Fatalf("observation was not drained by forwarding: %#v", got)
	}
}

type observationManaged struct {
	source *promptcache.ObservationBuffer
}

func (m *observationManaged) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}
func (m *observationManaged) Close() error { return nil }
func (m *observationManaged) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}
func (m *observationManaged) DrainPromptCacheObservations() []promptcache.Observation {
	return m.source.DrainPromptCacheObservations()
}
