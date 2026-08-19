package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func testCall() lipapi.Call {
	return lipapi.Call{
		ID:         "req-1",
		Session:    lipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func testCand() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}
}

func TestCapabilityInventoryCountFinalize(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "i1", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "i1", RoutePrefixes: []string{"fake"}, EnforcesMaxOutput: true,
	})
	be := br.Backend
	if _, ok := be.Caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("missing streaming cap")
	}
	if be.ResolveCaps == nil || be.ResolveTransportCaps == nil {
		t.Fatal("expected resolve seams")
	}
	caps := be.ResolveTransportCaps(context.Background(), testCall(), testCand())
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("transport caps must derive from profile+call operation")
	}
	if be.TransportCaps != nil {
		t.Fatal("static transport caps must not invent operations")
	}
	if be.ModelInventory == nil {
		t.Fatal("expected inventory")
	}
	if be.ProviderCounter == nil {
		t.Fatal("expected counter")
	}
	if be.FinalizeBilling == nil {
		t.Fatal("expected billing")
	}
	count, err := be.ProviderCounter.CountText(context.Background(), accountingapp.CountTextInput{Model: "fake-model"})
	if err != nil {
		t.Fatal(err)
	}
	if count.TotalTokensPresent {
		t.Fatal("must not invent TotalTokens from InputTokens")
	}
	_ = be.Open
	_ = br.Cleanup()
}

func TestStream_AcceptedDoesNotCommit_PrePostOutput(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "i2", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "i2"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventTextDelta {
			sawText = true
			if !lipapi.OutputCommitted(ev) {
				t.Fatal("text should commit")
			}
		}
		if ev.Kind == lipapi.EventUsageDelta && lipapi.OutputCommitted(ev) {
			t.Fatal("usage must not commit")
		}
	}
	if !sawText {
		t.Fatal("expected text delta from fake")
	}
	ms, ok := stream.(interface {
		OutputCommitted() bool
		Attempts() int64
	})
	if !ok {
		t.Fatal("stream missing instrumentation")
	}
	if !ms.OutputCommitted() {
		t.Fatal("expected committed")
	}
	if ms.Attempts() != 1 {
		t.Fatalf("attempts=%d", ms.Attempts())
	}
}

func TestPostOutput_NoReplay(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeCommitThenFail}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "i3", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated bool
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "i3",
		InvalidateGeneration: func() { invalidated = true },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != lipapi.EventTextDelta || !lipapi.OutputCommitted(ev) {
		t.Fatalf("expected committed text, got %+v", ev)
	}
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected post-output classified failure")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("%T %v", err, err)
	}
	if !ce.OutputCommitted || ce.Retryable {
		t.Fatalf("%+v", ce)
	}
	ms, ok := stream.(interface{ Attempts() int64 })
	if !ok {
		t.Fatalf("stream %T missing Attempts", stream)
	}
	if ms.Attempts() != 1 {
		t.Fatalf("host must not restart: attempts=%d", ms.Attempts())
	}
	if !invalidated {
		t.Fatal("expected generation invalidation callback")
	}
}

func TestUnadvertisedOptionalNil(t *testing.T) {
	t.Parallel()
	sess := &minimalSession{}
	profile := backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{Streaming: true},
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "m"})
	if br.Backend.ModelInventory != nil || br.Backend.ProviderCounter != nil || br.Backend.FinalizeBilling != nil {
		t.Fatal("unadvertised seams must stay nil")
	}
	if br.Backend.ResolveReplaySupport != nil {
		t.Fatal("replay seam only when advertised")
	}
}

func TestBuild_PooledLifecycleCallbacksRemainNil(t *testing.T) {
	t.Parallel()
	br := adapter.Build(minimalSession{}, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "pool-compatible"})
	t.Cleanup(func() { _ = br.Cleanup() })

	tests := []struct {
		name  string
		isSet func(execbackend.Backend) bool
	}{
		{name: "Close", isSet: func(be execbackend.Backend) bool { return be.Close != nil }},
		{name: "Start", isSet: func(be execbackend.Backend) bool { return be.Start != nil }},
		{name: "Stop", isSet: func(be execbackend.Backend) bool { return be.Stop != nil }},
		{name: "CleanupIdleTransports", isSet: func(be execbackend.Backend) bool {
			return be.CleanupIdleTransports != nil
		}},
		{name: "PreflightCapability", isSet: func(be execbackend.Backend) bool {
			return be.PreflightCapability != nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.isSet(br.Backend) {
				t.Fatalf("adapter.Build returned %s callback; standard executable adapters must remain pool-compatible", tt.name)
			}
		})
	}
}

func TestInvocationFromCall_Validates(t *testing.T) {
	t.Parallel()
	inv, err := adapter.InvocationFromCall(testCall(), testCand(), backendplugin.Negotiation{})
	if err != nil {
		t.Fatal(err)
	}
	if inv.CanonicalModelID != "fake-model" || inv.BLegID != "fake:fake-model" {
		t.Fatalf("%+v", inv)
	}
	if len(inv.Messages) != 1 || inv.Messages[0].Parts[0].Text == nil {
		t.Fatalf("%+v", inv)
	}
}

func TestOrderedItemAuthorityCapabilityNoNetwork_rejectsOldMinorBeforeExecute(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		ID:         "req-ordered",
		Session:    lipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenResponsesCreate},
		Items: []lipapi.Item{{
			Kind:    lipapi.ItemKindMessage,
			ID:      "msg-1",
			Status:  lipapi.ItemStatusCompleted,
			Role:    lipapi.RoleUser,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}},
		}},
	}
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "ord-old", FactoryKind: "fake",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
			EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
		},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "ord-old",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: 0,
		},
	})
	_, err = br.Backend.Open(context.Background(), call, testCand())
	if err == nil {
		t.Fatal("expected ABI rejection before Execute")
	}
	if fake.ExecuteCount.Load() != 0 {
		t.Fatalf("ExecuteCount=%d want 0", fake.ExecuteCount.Load())
	}
	_ = br.Cleanup()
}

func TestOrderedItemAuthorityCapabilityNoNetwork_executesViaAdapterSession(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		ID:         "req-ordered",
		Session:    lipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenResponsesCreate},
		Items: []lipapi.Item{
			{Kind: lipapi.ItemKindItemReference, ID: "ref-1", Status: lipapi.ItemStatusCompleted, Reference: &lipapi.ItemReference{ID: "prev"}},
			{Kind: lipapi.ItemKindReasoning, ID: "rs-1", Status: lipapi.ItemStatusCompleted, Reasoning: &lipapi.ReasoningItem{Reasoning: &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: "chain",
			}}},
			{
				Kind: lipapi.ItemKindMessage, ID: "msg-1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartJSON, Text: `{"k":1}`},
					{Kind: lipapi.ContentPartToolResult, Text: "72F"},
				},
			},
		},
	}
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	neg := backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "ord-new", FactoryKind: "fake",
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
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "ord-new", Negotiation: neg})
	stream, err := br.Backend.Open(context.Background(), call, testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventTextDelta {
			sawText = true
		}
	}
	if fake.ExecuteCount.Load() != 1 {
		t.Fatalf("ExecuteCount=%d", fake.ExecuteCount.Load())
	}
	if !sawText {
		t.Fatal("expected text delta from fake Execute path")
	}
	if fake.LastStartInvocation == nil || !fake.LastStartInvocation.ItemAuthority {
		t.Fatal("expected ordered item invocation at Execute")
	}
	if fake.LastStartCall == nil || len(fake.LastStartCall.Items) != 3 {
		t.Fatalf("reconstructed call=%#v", fake.LastStartCall)
	}
	_ = br.Cleanup()
}

func TestOrderedItemAuthorityCapabilityNoNetwork_rejectsMinor2WithoutFeature(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		ID:         "req-ordered",
		Session:    lipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenResponsesCreate},
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindMessage, ID: "msg-1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}},
		}},
	}
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "ord-m2", FactoryKind: "fake",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
			EnabledFeatures: nil,
		},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "ord-m2",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		},
	})
	_, err = br.Backend.Open(context.Background(), call, testCand())
	if err == nil {
		t.Fatal("expected ABI rejection before Execute")
	}
	if fake.ExecuteCount.Load() != 0 {
		t.Fatalf("ExecuteCount=%d want 0", fake.ExecuteCount.Load())
	}
	_ = br.Cleanup()
}

type minimalSession struct{}

func (minimalSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (minimalSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (minimalSession) Execute(backendplugin.ExecuteStream) error { return nil }
func (minimalSession) Close(context.Context) error               { return nil }
