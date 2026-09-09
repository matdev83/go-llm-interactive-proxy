package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// TestAdapter_TransportNegotiation_StreamingConnector verifies that a connector declaring
// Capabilities.Streaming: true and TransportCapabilities.BidirectionalStream: true
// admits both streaming and non-streaming delivery modes, and executes both cleanly.
func TestAdapter_TransportNegotiation_StreamingConnector(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "streaming-inst", FactoryKind: "fake",
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
	if !profile.Capabilities.Streaming || !profile.TransportCapabilities.BidirectionalStream {
		t.Fatalf("expected profile with Streaming=true and BidirectionalStream=true, got %+v", profile)
	}

	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "streaming-inst", RoutePrefixes: []string{"fake"},
	})
	t.Cleanup(func() { _ = br.Cleanup() })
	be := br.Backend

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}

	// 1. Streaming delivery mode
	callStreaming := lipapi.Call{
		ID:      "req-stream",
		Session: lipapi.SessionRef{ALegID: "aleg-stream"},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello streaming")},
		}},
	}
	capsStream := be.ResolveTransportCaps(context.Background(), callStreaming, cand)
	if !capsStream.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("expected transport caps to support streaming")
	}
	if !capsStream.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected transport caps to support non-streaming")
	}
	admitStream := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:            callStreaming,
		Invocation:      callStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsStream,
		TransportPolicy: lipapi.TransportFallbackExact,
	})
	if admitStream.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected streaming candidate admission to succeed, got %v: %v", admitStream.Kind, admitStream.Transport.Err())
	}
	if admitStream.Transport.Selected != lipapi.TransportModeStreaming {
		t.Fatalf("expected Selected=streaming, got %s", admitStream.Transport.Selected)
	}

	// Execute streaming call through host adapter
	stream, err := be.Open(context.Background(), callStreaming, cand)
	if err != nil {
		t.Fatalf("be.Open streaming failed: %v", err)
	}
	var sawDeltaStream bool
	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		ev, err := stream.Recv(drainCtx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv error: %v", err)
		}
		if ev.Kind == lipapi.EventTextDelta && ev.Delta == "ok" {
			sawDeltaStream = true
		}
	}
	_ = stream.Close()
	if !sawDeltaStream {
		t.Fatal("streaming call did not receive expected text delta")
	}

	// 2. Non-streaming delivery mode
	callNonStreaming := lipapi.Call{
		ID:      "req-nonstream",
		Session: lipapi.SessionRef{ALegID: "aleg-nonstream"},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello non-streaming")},
		}},
	}
	capsNonStream := be.ResolveTransportCaps(context.Background(), callNonStreaming, cand)
	admitNonStream := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:            callNonStreaming,
		Invocation:      callNonStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsNonStream,
		TransportPolicy: lipapi.TransportFallbackExact,
	})
	if admitNonStream.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected non-streaming candidate admission to succeed, got %v: %v", admitNonStream.Kind, admitNonStream.Transport.Err())
	}
	if admitNonStream.Transport.Selected != lipapi.TransportModeNonStreaming {
		t.Fatalf("expected Selected=non_streaming, got %s", admitNonStream.Transport.Selected)
	}

	// Execute non-streaming call through host adapter (collecting canonical stream)
	streamNS, err := be.Open(context.Background(), callNonStreaming, cand)
	if err != nil {
		t.Fatalf("be.Open non-streaming failed: %v", err)
	}
	var sawDeltaNS bool
	drainCtxNS, cancelNS := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelNS()
	for {
		ev, err := streamNS.Recv(drainCtxNS)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("streamNS.Recv error: %v", err)
		}
		if ev.Kind == lipapi.EventTextDelta && ev.Delta == "ok" {
			sawDeltaNS = true
		}
	}
	_ = streamNS.Close()
	if !sawDeltaNS {
		t.Fatal("non-streaming call did not receive expected text delta")
	}
}

// TestAdapter_TransportNegotiation_NonStreamingOnlyConnector verifies that when a connector declares
// Capabilities.Streaming: false and TransportCapabilities.BidirectionalStream: true:
// - Non-streaming delivery mode is admitted (TransportModeNonStreaming).
// - Streaming delivery mode is rejected by candidate admission (NegotiationReject / ErrTransportReject).
func TestAdapter_TransportNegotiation_NonStreamingOnlyConnector(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "nonstream-only", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Override profile to simulate a non-streaming-only connector over bidirectional gRPC stream
	nonStreamingProfile := backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: false,
		},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{
			Cancellation:        true,
			BidirectionalStream: true,
		},
	}

	br := adapter.Build(inst, nonStreamingProfile, adapter.Options{
		InstanceID: "nonstream-only", RoutePrefixes: []string{"fake"},
	})
	t.Cleanup(func() { _ = br.Cleanup() })
	be := br.Backend

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}

	// 1. Non-streaming call MUST be admitted
	callNonStreaming := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
	}
	capsNS := be.ResolveTransportCaps(context.Background(), callNonStreaming, cand)
	if capsNS == nil {
		t.Fatal("expected non-nil transport caps for non-streaming connector")
	}
	if !capsNS.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("transport caps must support TransportModeNonStreaming")
	}
	if capsNS.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("transport caps must NOT support TransportModeStreaming when Capabilities.Streaming=false")
	}

	admitNS := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:            callNonStreaming,
		Invocation:      callNonStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsNS,
		TransportPolicy: lipapi.TransportFallbackExact,
	})
	if admitNS.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected non-streaming candidate admission to succeed, got %v: %v", admitNS.Kind, admitNS.Transport.Err())
	}
	if admitNS.Transport.Selected != lipapi.TransportModeNonStreaming {
		t.Fatalf("expected Selected=non_streaming, got %s", admitNS.Transport.Selected)
	}

	// 2. Streaming call MUST be rejected with ErrTransportReject
	callStreaming := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
	}
	capsStream := be.ResolveTransportCaps(context.Background(), callStreaming, cand)
	admitStream := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:            callStreaming,
		Invocation:      callStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsStream,
		TransportPolicy: lipapi.TransportFallbackExact,
	})
	if admitStream.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected streaming candidate admission to be rejected, got %v", admitStream.Kind)
	}
	if !errors.Is(admitStream.Transport.Err(), lipapi.ErrTransportReject) {
		t.Fatalf("expected ErrTransportReject, got: %v", admitStream.Transport.Err())
	}
}
