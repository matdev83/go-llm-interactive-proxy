package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// TestFakeConnector_EquivalentCanonicalCreateAndCompact verifies the external
// fake connector (speaking the plugin ABI at minor 2) accepts canonical create
// and compaction invocations through the host adapter with identical ordered
// item authority: operation, items (including compaction payloads), and exact
// protocol requirements are delivered to Execute for both operations.
func TestFakeConnector_EquivalentCanonicalCreateAndCompact(t *testing.T) {
	t.Parallel()

	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "eq-cmp", FactoryKind: "fake",
		Negotiation:   neg,
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "eq-cmp", Negotiation: neg})
	t.Cleanup(func() { _ = br.Cleanup() })

	create := lipapi.Call{
		ID:         "eq-create",
		Session:    lipapi.SessionRef{ALegID: "aleg-eq"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenResponsesCreate},
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindMessage, ID: "msg-1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}},
		}},
	}
	compact := lipapi.Call{
		ID:         "eq-compact",
		Session:    lipapi.SessionRef{ALegID: "aleg-eq"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationContextCompaction},
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage, ID: "msg-1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "window"}},
			},
			{Kind: lipapi.ItemKindCompaction, ID: "cmp-1", Status: lipapi.ItemStatusCompleted, Compaction: &lipapi.CompactionItem{
				Dialect: "compact.v1", Implementor: "fake", Opaque: json.RawMessage(`{"ok":true}`),
			}},
		},
	}

	drain := func(name string, call lipapi.Call) {
		t.Helper()
		stream, err := br.Backend.Open(context.Background(), call, testCand())
		if err != nil {
			t.Fatalf("%s Open: %v", name, err)
		}
		defer func() { _ = stream.Close() }()
		for {
			_, err := stream.Recv(context.Background())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s Recv: %v", name, err)
			}
		}
	}

	drain("create", create)
	if fake.ExecuteCount.Load() != 1 {
		t.Fatalf("create ExecuteCount=%d want 1", fake.ExecuteCount.Load())
	}
	assertDeliveredInvocation(t, fake.LastStartInvocation, create)

	drain("compact", compact)
	if fake.ExecuteCount.Load() != 2 {
		t.Fatalf("compact ExecuteCount=%d want 2", fake.ExecuteCount.Load())
	}
	assertDeliveredInvocation(t, fake.LastStartInvocation, compact)
}

func assertDeliveredInvocation(t *testing.T, inv *backendplugin.Invocation, call lipapi.Call) {
	t.Helper()
	if inv == nil {
		t.Fatal("missing recorded invocation")
	}
	if !inv.ItemAuthority {
		t.Fatalf("invocation lost item authority: %#v", inv)
	}
	if inv.Operation != string(call.Invocation.Operation) {
		t.Fatalf("operation=%q want %q", inv.Operation, call.Invocation.Operation)
	}
	if len(inv.Items) != len(call.Items) {
		t.Fatalf("items=%d want %d", len(inv.Items), len(call.Items))
	}
	req, ok := backendplugin.ProtocolRequirementsFromInvocation(*inv)
	if !ok {
		t.Fatal("invocation lost protocol requirements")
	}
	if call.Invocation.Operation == lipapi.OperationContextCompaction {
		if len(req.CompactionDialects) != 1 || req.CompactionDialects[0].Dialect != "compact.v1" {
			t.Fatalf("compaction requirements=%#v", req.CompactionDialects)
		}
		got := inv.Items[len(inv.Items)-1]
		if got.Compaction == nil || got.Compaction.Dialect != "compact.v1" {
			t.Fatalf("compaction item not delivered: %#v", got)
		}
	}
	if call.Invocation.Operation == lipapi.OperationOpenResponsesCreate {
		if len(req.Capabilities) == 0 {
			t.Fatalf("create capabilities missing: %#v", req)
		}
	}
}
