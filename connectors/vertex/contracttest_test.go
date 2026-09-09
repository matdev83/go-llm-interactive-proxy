package vertex_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/vertex/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/contracttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func newVertexEmulator() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			chunk := `data: {"candidates": [{"content": {"parts": [{"text": "hello"}], "role": "model"}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}}` + "\n\n"
			_, _ = w.Write([]byte(chunk))
			if ok {
				flusher.Flush()
			}
			return
		}

		if strings.HasSuffix(r.URL.Path, ":generateContent") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			resp := `{"candidates": [{"content": {"parts": [{"text": "hello"}], "role": "model"}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}}`
			_, _ = w.Write([]byte(resp))
			return
		}

		if strings.HasPrefix(r.URL.Path, "/v1beta1/publishers/") && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			resp := `{"publisherModels": [{"name": "publishers/google/models/gemini-2.5-flash", "displayName": "Gemini 2.5 Flash"}]}`
			_, _ = w.Write([]byte(resp))
			return
		}

		http.NotFound(w, r)
	})
}

func bufconnHost(factory string, config []byte, secrets backendplugin.SecretBundle, tp service.TokenProvider) func(context.Context, backendplugin.Service) (contracttest.HostSession, func(), error) {
	return bufconnHostWithOffer(factory, config, secrets, backendplugin.ProtocolOffer{
		Major:                   1,
		Minor:                   backendplugin.ProtocolMinorCancellationHandshake,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
	}, tp)
}

func bufconnHostWithOffer(factory string, config []byte, secrets backendplugin.SecretBundle, offer backendplugin.ProtocolOffer, tp service.TokenProvider) func(context.Context, backendplugin.Service) (contracttest.HostSession, func(), error) {
	return func(ctx context.Context, svc backendplugin.Service) (contracttest.HostSession, func(), error) {
		lis := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
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
	t.Parallel()
	srv := httptest.NewServer(newVertexEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Appendf(nil, "project: test-proj\nlocation: us-central1\napi_origin: %s\nmodel: gemini-2.5-flash\n", srv.URL)
	secrets := backendplugin.SecretBundle{Values: map[string][]byte{}}
	tp := service.StaticTokenProvider("test-bearer-token")

	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID,
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.New(service.WithTokenProvider(tp)), nil, nil
		},
		StartHost: bufconnHost(service.FactoryKind, cfgYAML, secrets, tp),
	})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if !containsFeature(result.Negotiated.EnabledFeatures, backendplugin.FeatureCancellationHandshake) {
		t.Fatalf("negotiated handshake must be enabled for minor 8, got negotiated=%+v", result.Negotiated)
	}
	if result.Negotiated.NegotiatedMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("negotiated minor=%d want %d", result.Negotiated.NegotiatedMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
}

func TestSupportedContractTCK_LegacyFallback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newVertexEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Appendf(nil, "project: test-proj\nlocation: us-central1\napi_origin: %s\nmodel: gemini-2.5-flash\n", srv.URL)
	secrets := backendplugin.SecretBundle{Values: map[string][]byte{}}
	tp := service.StaticTokenProvider("test-bearer-token")

	legacyOffer := backendplugin.ProtocolOffer{
		Major:                   1,
		Minor:                   backendplugin.ProtocolMinorProxyOwnedSessionID,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
		},
	}
	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID + "-legacy",
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.New(service.WithTokenProvider(tp)), nil, nil
		},
		StartHost: bufconnHostWithOffer(service.FactoryKind, cfgYAML, secrets, legacyOffer, tp),
	})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if containsFeature(result.Negotiated.EnabledFeatures, backendplugin.FeatureCancellationHandshake) {
		t.Fatalf("legacy negotiation must NOT enable cancellation handshake, got %+v", result.Negotiated)
	}
	if result.Negotiated.Compatible && result.Negotiated.NegotiatedMinor >= backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("legacy negotiated minor=%d must be < %d", result.Negotiated.NegotiatedMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
}

func containsFeature(features []string, want string) bool {
	return slices.Contains(features, want)
}
