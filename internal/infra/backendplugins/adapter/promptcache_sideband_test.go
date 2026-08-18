package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func TestManagedStream_PromptCacheSidebandCommitsOnlyOnSuccessfulTerminal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		terminal   backendplugin.TerminalStatus
		wantTarget bool
	}{
		{name: "success", terminal: backendplugin.TerminalSuccess, wantTarget: true},
		{name: "failure", terminal: backendplugin.TerminalFailure, wantTarget: false},
		{name: "cancelled", terminal: backendplugin.TerminalCancelled, wantTarget: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			neg := backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorPromptCacheResidency,
				EnabledFeatures: []string{backendplugin.FeaturePromptCacheResidency},
			}
			s := &managedStream{
				ctx: ctx, cancel: cancel, opt: Options{Negotiation: neg},
				events: make(chan lipapi.Event, 1), done: make(chan struct{}),
				maxFrame: int(backendplugin.DefaultMaxStreamFrameBytes),
			}
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1, 0).UTC()
			observation := &promptcache.Observation{
				ALegID: "a", BLegID: "b", BackendInstanceID: "instance",
				TargetID: "target", GenerationID: "generation",
				Lifecycle: promptcache.LifecycleBestEffort,
				Timing:    promptcache.Timing{ObservedAt: now},
			}
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: 1, PromptCacheObservation: observation}); err != nil {
				t.Fatal(err)
			}
			if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameTerminal, Sequence: 2, Terminal: &backendplugin.Terminal{Status: tc.terminal}}); err != nil {
				t.Fatal(err)
			}
			got := s.DrainPromptCacheObservations()
			if (len(got) == 1) != tc.wantTarget {
				t.Fatalf("observations=%#v, wantTarget=%v", got, tc.wantTarget)
			}
		})
	}
}
