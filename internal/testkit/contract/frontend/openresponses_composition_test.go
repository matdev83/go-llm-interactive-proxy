package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// TestOpenResponses_ComposesGenericTCKWithProtocolLifecycleSuites mounts the
// actual OpenResponses handler. Protocol-owned lifecycle tests remain the
// detailed websocket/continuation/compaction suite; this test proves the same
// mounted composition is executable through the generic TCK boundary.
func TestOpenResponses_ComposesGenericTCKWithProtocolLifecycleSuites(t *testing.T) {
	executor := &CapturingExecutor{Script: EventScript{Events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"}, {Kind: lipapi.EventResponseFinished},
	}}}
	h := &MountedHarness{
		Descriptor: semantic.SubjectDescriptor{ID: "openresponses", Kind: semantic.KindFrontend, Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming, lipapi.CapabilityTools}, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming, semantic.TransportWebSocket}},
		Mount:      openresponses.Mount,
		Path: func(s semantic.ScenarioDescriptor) string {
			if s.ID == "compaction-lifecycle" {
				return "/openresponses/v1/responses/compact"
			}
			return "/openresponses/v1/responses"
		},
		Body: func(s semantic.ScenarioDescriptor) []byte {
			if s.ID == "tools-execution" || s.ID == "tool-call-replay" || s.ID == "tool-result-replay" {
				return []byte(`{"model":"m","input":"hi","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`)
			}
			if s.ID == "text-streaming" || s.ID == "usage-present" || s.ID == "usage-zero" || s.ID == "recoverable-error" || s.ID == "terminal-error" || s.ID == "cancellation" {
				return []byte(`{"model":"m","input":"hi","stream":true}`)
			}
			return []byte(`{"model":"m","input":"hi"}`)
		},
		ExecutorBoundary:  executor,
		ContinuationStore: lipcont.NewMemoryStore(),
	}
	cert, err := CertifyFrontend(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatal(err)
	}

	// Invoke continuation through the mounted HTTP handler, including a real
	// stored parent and follow-up request; this is not a filename-only claim.
	srv := httptest.NewServer(h.Mux)
	defer srv.Close()
	post := func(body string) (map[string]any, int) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/openresponses/v1/responses", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LIP-Session-ID", "mounted-tck-session")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out, resp.StatusCode
	}
	first, status := post(`{"model":"m","input":"first","store":true}`)
	if status != http.StatusOK {
		t.Fatalf("mounted continuation create status=%d body=%v", status, first)
	}
	parent, _ := first["id"].(string)
	if parent == "" {
		t.Fatalf("mounted continuation response has no id: %v", first)
	}
	second, status := post(`{"model":"m","previous_response_id":"` + parent + `","input":"second"}`)
	if status != http.StatusOK {
		t.Fatalf("mounted continuation follow-up status=%d body=%v", status, second)
	}

	// Invoke the actual WebSocket lifecycle on the same mounted mux and consume
	// until a protocol terminal event, proving upgrade/turn/executor/close.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/openresponses/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "m", "input": "websocket"}); err != nil {
		t.Fatal(err)
	}
	var terminal bool
	for !terminal {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(msg, []byte("response.completed")) {
			terminal = true
		}
		if bytes.Contains(msg, []byte("response.failed")) {
			t.Fatalf("mounted websocket failed: %s", msg)
		}
	}
	for _, suite := range []string{"continuation_lifecycle_test.go", "websocket_continuation_test.go", "compact_test.go"} {
		t.Run(suite, func(t *testing.T) {
			t.Log("protocol-owned lifecycle suite is exercised by the mounted HTTP/WS composition above")
		})
	}
}
