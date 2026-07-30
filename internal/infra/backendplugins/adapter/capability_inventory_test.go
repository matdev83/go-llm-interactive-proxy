package adapter_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type flakyInventorySession struct {
	minimalSession
	fail atomic.Bool
}

type reasoningReplaySession struct {
	minimalSession
}

func (s *reasoningReplaySession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, ReasoningReplay: true},
		ReasoningReplaySupported: true,
	}, nil
}

func TestCapability_ReasoningReplayReturnsExactResponsesDialect(t *testing.T) {
	t.Parallel()

	sess := &reasoningReplaySession{}
	profile, err := sess.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "reasoning"})
	if br.Backend.ResolveReplaySupport == nil {
		t.Fatal("expected replay support resolver")
	}
	support := br.Backend.ResolveReplaySupport(context.Background(), testCall(), testCand())
	if len(support.Dialects) != 1 || support.Dialects[0] != lipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("dialects = %v", support.Dialects)
	}
}

func (s *flakyInventorySession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		SupportsDynamicInventory: true,
		EvidenceSource:           "test",
	}, nil
}

func (s *flakyInventorySession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	if s.fail.Load() {
		return backendplugin.ListModelsResponse{}, errors.New("refresh failed")
	}
	return backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "m1", NativeModelID: "m1", FactoryKind: "fake",
		}},
		InventorySource: "test",
		FetchedUnixMS:   1,
	}, nil
}

func TestInventory_RefreshFailureFailsClosed(t *testing.T) {
	t.Parallel()
	sess := &flakyInventorySession{}
	sess.fail.Store(true)
	profile, _ := sess.Resolve(context.Background(), nil)
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "inv"})
	if br.Backend.ModelInventory == nil {
		t.Fatal("expected inventory provider")
	}
	_, err := br.Backend.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("refresh failure must surface")
	}
}

func TestInventory_ProvenancePreserved(t *testing.T) {
	t.Parallel()
	sess := &flakyInventorySession{}
	profile, _ := sess.Resolve(context.Background(), nil)
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "inv2"})
	snap, err := br.Backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.Source("test") || len(snap.Models) != 1 {
		t.Fatalf("%+v", snap)
	}
}

type billingSession struct {
	minimalSession
	calls atomic.Int64
	keys  map[string]int
}

func (s *billingSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:            backendplugin.CapabilitySummary{Streaming: true},
		SupportsFinalizeBilling: true,
	}, nil
}

func (s *billingSession) FinalizeBilling(_ context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	s.calls.Add(1)
	if s.keys == nil {
		s.keys = map[string]int{}
	}
	s.keys[req.IdempotencyKey]++
	n := int64(1)
	return backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens: &n,
			Presence:    backendplugin.UsagePresence{InputTokens: true},
		},
	}, nil
}

func TestFinalizeBilling_IdempotentKeyForwarded(t *testing.T) {
	t.Parallel()
	sess := &billingSession{}
	profile, _ := sess.Resolve(context.Background(), nil)
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "bill"})
	if br.Backend.FinalizeBilling == nil {
		t.Fatal("expected finalize")
	}
	_, err := br.Backend.FinalizeBilling(context.Background(), execbackend.BillingFinalizationInput{
		ALegID: "a", BLegID: "b", Model: "m", TraceID: "idem-1", Reason: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = br.Backend.FinalizeBilling(context.Background(), execbackend.BillingFinalizationInput{
		ALegID: "a", BLegID: "b", Model: "m", TraceID: "idem-1", Reason: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.keys["idem-1"] != 2 {
		t.Fatalf("idempotency key not forwarded: %+v", sess.keys)
	}
}

func TestCapability_MaxOutputAndRoutePrefixes(t *testing.T) {
	t.Parallel()
	sess := &minimalSession{}
	profile := backendplugin.ResolvedProfile{
		Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		EnforceMaxOutput: true,
		RoutePrefixes:    []string{"from-profile"},
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "cap"})
	if !br.Backend.EnforcesMaxOutputTokens {
		t.Fatal("max output enforcement missing")
	}
	if len(br.Backend.BackendPrefixes) != 1 || br.Backend.BackendPrefixes[0] != "from-profile" {
		t.Fatalf("%v", br.Backend.BackendPrefixes)
	}
	_ = lipapi.CapabilityStreaming
}
