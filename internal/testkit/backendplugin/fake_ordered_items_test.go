package backendplugin_test

import (
	"context"
	"encoding/json"
	"testing"

	fakebp "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestFake_ResolveProfile_advertisesDialectSupport(t *testing.T) {
	t.Parallel()

	svc := &fakebp.FakeService{Mode: fakebp.ModeValid}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "inst", FactoryKind: "fake",
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
		Negotiation:   backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.OrderedItems || len(profile.DialectSupport.CompactionDialects) == 0 {
		t.Fatalf("profile=%#v", profile)
	}
}

func TestFake_orderedItemWire_conformanceRoundTrip(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{Kind: lipapi.ItemKindItemReference, ID: "ref-1", Status: lipapi.ItemStatusCompleted, Reference: &lipapi.ItemReference{ID: "prev"}},
			{Kind: lipapi.ItemKindReasoning, ID: "rs-1", Status: lipapi.ItemStatusCompleted, Reasoning: &lipapi.ReasoningItem{Reasoning: &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: "chain",
			}}},
			{Kind: lipapi.ItemKindCompaction, ID: "cmp-1", Status: lipapi.ItemStatusCompleted, Compaction: &lipapi.CompactionItem{
				Dialect: "compact.v1", Opaque: json.RawMessage(`{"ok":true}`),
			}},
			{Kind: lipapi.ItemKindExtension, ID: "ext-1", Status: lipapi.ItemStatusCompleted, Extension: &lipapi.OpaqueExtension{
				Namespace: "ns", Type: "beta", Data: json.RawMessage(`{"k":1}`),
			}},
		},
	}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("x")}}}},
	}
	neg := backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil {
		t.Fatal(err)
	}
	p, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(p)
	if err != nil {
		t.Fatal(err)
	}
	if !back.ItemAuthority || len(back.Items) != 4 {
		t.Fatalf("back=%#v", back)
	}
}

func TestFake_orderedItemWire_rejectsOldMinor(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}},
	}}}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("x")}}}},
	}
	err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: 0,
	})
	if err == nil {
		t.Fatal("expected ABI rejection")
	}
}
