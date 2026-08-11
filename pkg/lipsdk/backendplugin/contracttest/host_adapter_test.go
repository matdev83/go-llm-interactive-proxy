package contracttest

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// TestRun_UsesRealHostAdapter proves the public entry point exercises the same
// GRPC host adapter used by executable connectors, rather than an optional seam.
//
//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_UsesRealHostAdapter(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorExactOpenResponsesFields, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}}}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, &testkit.FakeService{Mode: testkit.ModeValid}))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "contract", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	if profile.EvidenceSource == "" {
		t.Fatal("resolved profile lacks evidence source")
	}
	call := lipapi.Call{ID: "tck", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, TransportMode: lipapi.TransportModeStreaming}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}}}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "contract", RoutePrefixes: []string{"fake"}}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var text, usage bool
	for {
		ev, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		text = text || ev.Kind == lipapi.EventTextDelta
		usage = usage || ev.Kind == lipapi.EventUsageDelta
	}
	if !text || !usage {
		t.Fatalf("host adapter lost canonical evidence: text=%v usage=%v", text, usage)
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor6SemanticExtensionCarrier(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	fakeSvc := &testkit.FakeService{
		Mode:          testkit.ModeValid,
		ProtocolMinor: backendplugin.ProtocolMinorSemanticExtensions,
		ExtraFeatures: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "minor6-contract", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := lipapi.Call{
		ID: "tck-minor6", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, TransportMode: lipapi.TransportModeStreaming},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		SemanticExtensions: []lipapi.SemanticExtension{
			{Namespace: "lip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: []byte(`"cache-minor6"`)},
		},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "minor6-contract", RoutePrefixes: []string{"fake"}}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for {
		_, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
	}
	lastInv := fakeSvc.LastStartInvocation
	if lastInv == nil || len(lastInv.SemanticExtensions) != 1 || string(lastInv.SemanticExtensions[0].Data.Bytes()) != `"cache-minor6"` {
		t.Fatalf("minor-6 connector TCK carrier not transmitted: %#v", lastInv)
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor6SemanticExtensionCarrier_NegotiationMismatch(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureAccountingEvidence},
		},
	}
	fakeSvc := &testkit.FakeService{
		Mode:          testkit.ModeValid,
		ProtocolMinor: backendplugin.ProtocolMinorAccountingEvidence,
	}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "minor6-mismatch", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := lipapi.Call{
		ID: "tck-minor6-mismatch", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, TransportMode: lipapi.TransportModeStreaming},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		SemanticExtensions: []lipapi.SemanticExtension{
			{Namespace: "lip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: []byte(`"cache-minor6"`)},
		},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "minor6-mismatch", RoutePrefixes: []string{"fake"}, Negotiation: session.Negotiation()}).Backend
	_, err = backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err == nil {
		t.Fatal("expected Open to fail due to semantic extension ABI negotiation mismatch on Minor 5")
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor6SemanticExtensionCarrier_ErrorPropagation(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	fakeSvc := &testkit.FakeService{
		Mode:          testkit.ModeMalformedFrame,
		ProtocolMinor: backendplugin.ProtocolMinorSemanticExtensions,
		ExtraFeatures: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "minor6-err", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := lipapi.Call{
		ID: "tck-minor6-err", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, TransportMode: lipapi.TransportModeStreaming},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		SemanticExtensions: []lipapi.SemanticExtension{
			{Namespace: "lip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: []byte(`"cache-minor6"`)},
		},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "minor6-err", RoutePrefixes: []string{"fake"}, Negotiation: session.Negotiation()}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	_, recvErr := stream.Recv(context.Background())
	if recvErr == nil {
		t.Fatal("expected error propagated from ModeFail fake service")
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_PostTerminalRecv_ReturnsEOF(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}},
	}
	fakeSvc := &testkit.FakeService{Mode: testkit.ModeValid}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "post-term", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := lipapi.Call{
		ID: "tck-post-term", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenResponsesCreate, TransportMode: lipapi.TransportModeStreaming},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "post-term", RoutePrefixes: []string{"fake"}, Negotiation: session.Negotiation()}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	for {
		_, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("unexpected Recv error before EOF: %v", recvErr)
		}
	}

	_, postErr := stream.Recv(context.Background())
	if !errors.Is(postErr, io.EOF) {
		t.Fatalf("post-terminal Recv() error = %v, want io.EOF", postErr)
	}
}
