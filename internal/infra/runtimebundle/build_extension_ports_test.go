package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

type portsStubAttemptTransform struct {
	id  string
	ord int
}

func (s portsStubAttemptTransform) ID() string                      { return s.id }
func (s portsStubAttemptTransform) Order() int                      { return s.ord }
func (portsStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (portsStubAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type portsStubStreamObserverFactory struct {
	id  string
	ord int
}

func (s portsStubStreamObserverFactory) ID() string                      { return s.id }
func (s portsStubStreamObserverFactory) Order() int                      { return s.ord }
func (portsStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (portsStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

// TestBuildRuntimeSnapshot_featureBundlePortsReachSnap mirrors bootstrap wiring:
// MergeBundles → ExtensionsOptions → buildRuntimeSnapshot → sorted Snap accessors.
// Inventory occupancy IDs/stages for these ports are covered in package diag.
func TestBuildRuntimeSnapshot_featureBundlePortsReachSnap(t *testing.T) {
	t.Parallel()
	bundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			portsStubAttemptTransform{id: "z", ord: 2},
			portsStubAttemptTransform{id: "a", ord: 1},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			portsStubStreamObserverFactory{id: "z", ord: 2},
			portsStubStreamObserverFactory{id: "a", ord: 1},
		},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	merged := featurebundle.MergeBundles(bundle)
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		Extensions: ExtensionsOptions{
			AttemptTransforms:       merged.AttemptTransforms,
			StreamObserverFactories: merged.StreamObserverFactories,
		},
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	gotAT := snap.AttemptTransforms()
	if len(gotAT) != 2 || gotAT[0].ID() != "a" || gotAT[1].ID() != "z" {
		t.Fatalf("Snap.AttemptTransforms IDs=%v want [a z]", portAttemptIDs(gotAT))
	}
	gotSO := snap.StreamObserverFactories()
	if len(gotSO) != 2 || gotSO[0].ID() != "a" || gotSO[1].ID() != "z" {
		t.Fatalf("Snap.StreamObserverFactories IDs=%v want [a z]", portStreamIDs(gotSO))
	}
}

func portAttemptIDs(in []request.AttemptTransform) []string {
	out := make([]string, len(in))
	for i, tr := range in {
		if tr == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = tr.ID()
	}
	return out
}

func portStreamIDs(in []response.StreamObserverFactory) []string {
	out := make([]string, len(in))
	for i, f := range in {
		if f == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = f.ID()
	}
	return out
}
