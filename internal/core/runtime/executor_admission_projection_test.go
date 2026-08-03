package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_projectionOnlyRejectBeforeOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"legacy": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "legacy:g"},
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindItemReference, ID: "ref-1", Status: lipapi.ItemStatusCompleted,
			Reference: &lipapi.ItemReference{ID: "msg-prev"},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("backend opened despite admission reject, opens=%d", opens)
	}
}

func TestExecutor_nativeOrderedAdmissionBeforeOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"ordered": {
			Caps: lipapi.NewBackendCaps(
				lipapi.CapabilityStreaming,
				lipapi.CapabilityOrderedItems,
				lipapi.CapabilityItemReferences,
			),
			DialectSupport: lipapi.DialectSupport{
				ItemDialects: []lipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}},
			},
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "ordered:g"},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindItemReference, ID: "ref-1", Status: lipapi.ItemStatusCompleted,
			Reference: &lipapi.ItemReference{ID: "msg-prev"},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = lipapi.Collect(context.Background(), stream)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("expected one backend open for native ordered candidate, opens=%d", opens)
	}
}

func TestExecutor_legacyProjectionDeliveredAtOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotCall lipapi.Call
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"ordered": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems, lipapi.CapabilityTools),
			DialectSupport: lipapi.DialectSupport{
				ItemDialects: []lipapi.DialectRequirement{{Kind: "item", Dialect: "message"}},
			},
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				gotCall = call
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "ordered:g"},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind: lipapi.PartJSON, ToolCallID: "call_1", ToolName: "weather", Content: []byte(`{"city":"SF"}`),
			}}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = lipapi.Collect(context.Background(), stream)
	}
	if len(gotCall.Messages) != 0 {
		t.Fatalf("expected ordered view at Open, still has messages=%#v", gotCall.Messages)
	}
	if len(gotCall.Items) != 2 {
		t.Fatalf("projected items=%#v", gotCall.Items)
	}
	if gotCall.Items[1].Kind != lipapi.ItemKindToolCall || gotCall.Items[1].ToolCall.CallID != "call_1" {
		t.Fatalf("projected items=%#v", gotCall.Items)
	}
}

func TestExecutor_itemAuthorityLegacyProjectionDeliveredAtOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotCall lipapi.Call
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"legacy": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				gotCall = call
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "legacy:g"},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hello"}},
			},
			{
				Kind: lipapi.ItemKindToolCall, ID: "tc1", Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "weather", Arguments: []byte(`{"city":"SF"}`)},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = lipapi.Collect(context.Background(), stream)
	}
	if len(gotCall.Items) != 0 {
		t.Fatalf("expected legacy messages at Open, still has items=%#v", gotCall.Items)
	}
	if len(gotCall.Messages) != 2 {
		t.Fatalf("projected messages=%#v", gotCall.Messages)
	}
	if gotCall.Messages[1].Parts[0].ToolCallID != "c1" {
		t.Fatalf("projected tool call=%#v", gotCall.Messages[1].Parts[0])
	}
}
