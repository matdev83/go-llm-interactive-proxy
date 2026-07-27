package adapter_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestBufconn_RealServerExecuteWithInvocation(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1 << 20)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: "count_tokens"}, {Name: "finalize_billing"},
		},
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, &testkit.FakeService{Mode: testkit.ModeValid}))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///buf", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)

	neg, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{{Name: "count_tokens"}, {Name: "finalize_billing"}},
	})
	if err != nil || !neg.GetCompatible() {
		t.Fatalf("negotiate: %+v %v", neg, err)
	}
	_, err = client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "buf1", FactoryKind: "fake", ConfigYaml: []byte("k: v\n"),
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	sess := &adapter.GRPCSession{Client: client, InstanceID: "buf1"}
	profile, err := sess.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "buf1", RoutePrefixes: []string{"fake"}})
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
		}
	}
	if !sawText {
		t.Fatal("expected real GRPCServer execute path to deliver text")
	}
}
