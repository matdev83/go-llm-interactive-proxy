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
	defer session.Close(context.Background())
	if profile.EvidenceSource == "" {
		t.Fatal("resolved profile lacks evidence source")
	}
	call := lipapi.Call{ID: "tck", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, TransportMode: lipapi.TransportModeStreaming}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}}}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "contract", RoutePrefixes: []string{"fake"}}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
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
