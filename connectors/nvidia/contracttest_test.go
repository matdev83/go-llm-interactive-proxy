package nvidia_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/nvidia/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/contracttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func bufconnHost(factory string, config []byte, secrets backendplugin.SecretBundle) func(context.Context, backendplugin.Service) (contracttest.HostSession, func(), error) {
	return func(ctx context.Context, svc backendplugin.Service) (contracttest.HostSession, func(), error) {
		lis := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
		offer := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorProxyOwnedSessionID, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}, {Name: backendplugin.FeatureExactOpenResponsesFields}, {Name: backendplugin.FeatureProxyOwnedSessionID}}}
		backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, svc))
		go func() { _ = server.Serve(lis) }()
		conn, err := lis.Dial()
		if err != nil {
			server.Stop()
			_ = lis.Close()
			return nil, nil, err
		}
		sess, _, err := host.DialConfiguredSession(ctx, conn, "contract", factory, config, secrets, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
		if err != nil {
			server.Stop()
			_ = lis.Close()
			return nil, nil, err
		}
		return sess, func() { _ = sess.Close(context.Background()); server.Stop(); _ = lis.Close() }, nil
	}
}

func TestSupportedContractTCK(t *testing.T) {
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: false}))
	t.Cleanup(srv.Close)
	result := contracttest.Run(t, contracttest.Config{PluginID: "nvidia", Version: "test", Timeout: 10 * time.Second, FactoryKind: service.FactoryKind, ConfigYAML: []byte("base_url: " + srv.URL + "\n"), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("test")}}, Start: func(context.Context) (backendplugin.Service, func(), error) { return service.New(), nil, nil }, StartHost: bufconnHost(service.FactoryKind, []byte("base_url: "+srv.URL+"\n"), backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("test")}})})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}
