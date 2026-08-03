//go:build integration

package conformance

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// Connector-column executable evidence: the OpenRouter/NVIDIA matrix cells are
// driven through the actual connectors/openrouter and connectors/nvidia
// executable processes via the backendplugin host adapter (connector_host.go),
// so connector-specific headers, credentials, inventory, and error behavior
// have matrix-level executable proof. The observing origin inspects the real
// request headers/body (never metadata), and every assertion runs against the
// same connector process the general matrix and row deployments launch.

// TestConnectorColumns_HeadersAndCredentials proves the connector processes
// forward the synthetic credential as Authorization: Bearer and inject their
// provider-specific headers on the real matrix path. The OpenRouter connector
// attaches its attribution headers (HTTP-Referer, X-OpenRouter-Title defaults);
// the NVIDIA connector attaches no provider-specific headers beyond the bearer
// credential.
func TestConnectorColumns_HeadersAndCredentials(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{BackendOpenRouter, BackendNVIDIA} {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			rec := newHeaderRecorder()
			d := DeployConnectorColumnWithOrigin(t, FrontendOpenResponses, backend, TransportJSON, rec.handler())
			if d == nil {
				t.Fatalf("connector column deploy failed for %s", backend)
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("%s connector round trip: %v", backend, err)
			}
			if !strings.Contains(res.Text, "provider-mode-ok") {
				t.Fatalf("%s text = %q, want provider-mode-ok", backend, res.Text)
			}

			reqs := rec.requests()
			if len(reqs) == 0 {
				t.Fatalf("%s connector made no upstream request", backend)
			}
			last := reqs[len(reqs)-1]
			if got := last.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Fatalf("%s Authorization = %q, want Bearer sk-test (connector must forward the credential)", backend, got)
			}
			switch backend {
			case BackendOpenRouter:
				if got := last.Header.Get("HTTP-Referer"); got == "" {
					t.Fatal("openrouter connector did not attach the HTTP-Referer attribution header")
				}
				if got := last.Header.Get("X-OpenRouter-Title"); got == "" {
					t.Fatal("openrouter connector did not attach the X-OpenRouter-Title attribution header")
				}
				if strings.Contains(string(last.Body), `"previous_response_id"`) || strings.Contains(string(last.Body), `"store"`) {
					t.Fatalf("openrouter upstream body leaked proxy-owned fields: %s", string(last.Body))
				}
			case BackendNVIDIA:
				// NVIDIA carries no provider-specific header beyond the credential.
			}
		})
	}
}

// TestConnectorColumns_Inventory proves the connector processes serve dynamic
// model inventory with provider-prefixed canonical IDs through the host adapter
// on the matrix path. Each connector's ListModels maps native IDs to the
// canonical "openrouter/<id>" / "nvidia/<id>" form and the origin's /models
// discovery endpoint is queried.
func TestConnectorColumns_Inventory(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{BackendOpenRouter, BackendNVIDIA} {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := DeployConnectorColumn(t, backend, TransportJSON)
			if d == nil {
				t.Fatalf("connector column deploy failed for %s", backend)
			}
			defer d.Close()

			be := d.Backend(backend)
			if be.ModelInventory == nil {
				t.Fatalf("%s connector backend declares no dynamic inventory", backend)
			}
			snap, err := be.ModelInventory.LoadModels(context.Background())
			if err != nil {
				t.Fatalf("%s connector inventory load: %v", backend, err)
			}
			want := backend + "/gpt-4o-mini"
			found := false
			for _, m := range snap.Models {
				if m.CanonicalID == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s inventory did not surface %q (got %+v)", backend, want, snap.Models)
			}
			if d.RequestCount(backend) < 1 {
				t.Fatalf("%s inventory load produced no origin request", backend)
			}
		})
	}
}

// TestConnectorColumns_ErrorBehavior proves connector error surfaces on the
// matrix path: an upstream 401 surfaces as a stable client-visible failure (never
// a silent success) with exactly one upstream attempt, and the connector columns
// are not constructible without a credential (the api_key is required at
// configure time, so the host adapter fails closed before any matrix cell can
// mount).
func TestConnectorColumns_ErrorBehavior(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{BackendOpenRouter, BackendNVIDIA} {
		backend := backend
		t.Run(backend+"_unauthorized", func(t *testing.T) {
			t.Parallel()
			d := DeployConnectorColumnWithFail(t, FrontendOpenResponses, backend, TransportJSON, OriginFailUnauthorized)
			if d == nil {
				t.Fatalf("connector column deploy failed for %s", backend)
			}
			defer d.Close()
			if _, err := d.Client.RoundTrip(context.Background(), "ping"); err == nil {
				t.Fatalf("%s upstream 401 unexpectedly round-tripped as success", backend)
			}
			if got := d.RequestCount(backend); got != 1 {
				t.Fatalf("%s 401 produced %d upstream attempts, want exactly 1 (no retry)", backend, got)
			}
		})
		t.Run(backend+"_requiresCredential", func(t *testing.T) {
			t.Parallel()
			if err := connectorHostConfigureWithoutCredential(t, backend); err == nil || !strings.Contains(err.Error(), "api_key") {
				t.Fatalf("%s connector accepted configuration without an api_key (err=%v)", backend, err)
			}
		})
	}
}

// headerRecorder records the request headers and bodies the observing origin
// receives so tests can assert on the actual wire (the harness capture redacts
// Authorization, so header assertions must inspect the raw request).
type headerRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

type recordedRequest struct {
	Header http.Header
	Body   []byte
}

func newHeaderRecorder() *headerRecorder {
	return &headerRecorder{}
}

func (r *headerRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		req.Body = io.NopCloser(bytes.NewReader(body))
		r.mu.Lock()
		r.reqs = append(r.reqs, recordedRequest{Header: req.Header.Clone(), Body: body})
		r.mu.Unlock()
		// Serve the OpenAI-compatible wire the connector requests per operation
		// flavor (chat-completions for openresponses.create, Responses for
		// openai.responses) plus /models inventory discovery.
		(&connectorWire{text: "provider-mode-ok"}).ServeHTTP(w, req)
	})
}

func (r *headerRecorder) requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, 0, len(r.reqs))
	out = append(out, r.reqs...)
	return out
}

// connectorHostConfigureWithoutCredential launches backendID's real connector
// executable and drives the host-adapter configure with an empty credential
// bundle, returning the configure error. The OpenAI-compatible connector columns
// must reject it (api_key is required), proving the matrix never constructs them
// without a credential.
func connectorHostConfigureWithoutCredential(t *testing.T, backendID string) error {
	t.Helper()
	spec := connectorHostSpecRequired(t, backendID)
	bin := connectorHostBinary(t, backendID)
	cmd, addr := connectorHostLaunch(t, bin, spec)
	defer connectorHostKill(cmd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("dial %s connector: %v", backendID, err)
	}
	defer conn.Close()
	policy := backendplugin.RuntimePolicy{
		MaxRequestBytes:     backendplugin.DefaultMaxMessageBytes,
		MaxStreamFrameBytes: backendplugin.DefaultMaxStreamFrameBytes,
	}
	_, _, err = adapter.DialConfiguredSession(ctx, conn, spec.instanceID, backendID,
		[]byte("base_url: http://127.0.0.1:1\n"), backendplugin.SecretBundle{}, policy)
	return err
}
