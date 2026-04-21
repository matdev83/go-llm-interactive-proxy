package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestIntegration_refbackendStreamingText(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	call := lipapi.Call{
		ID: "int1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "agent"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendResourceEcho(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	call := lipapi.Call{
		ID: "int-res",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("x"),
				{Kind: lipapi.PartFileRef, FileRef: "file:///tmp/a.py", FileMIME: "text/x-python", FileName: "print(1)"},
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "agent"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "saw resource:") || !strings.Contains(col.Text.String(), "file:///tmp/a.py") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_sessionReuseExtensionSkipsNewSession(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cl := srv.Client()
	sid := mustInitAndSessionNew(t, srv.URL, cl)
	raw, _ := json.Marshal(sid)

	be := backend.New(backend.Config{BaseURL: srv.URL, HTTPClient: cl})
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "agent"}}

	first := lipapi.Call{
		ID: "a",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("one")},
		}},
		Extensions: map[string]json.RawMessage{"acp.sessionId": raw},
	}
	es1, err := be.Open(context.Background(), first, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es1); err != nil {
		t.Fatal(err)
	}

	second := lipapi.Call{
		ID: "b",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("two")},
		}},
		Extensions: map[string]json.RawMessage{"acp.sessionId": raw},
	}
	es2, err := be.Open(context.Background(), second, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es2); err != nil {
		t.Fatal(err)
	}
}

func mustInitAndSessionNew(t *testing.T, baseURL string, hc *http.Client) string {
	t.Helper()
	initBody := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	resp, err := hc.Post(baseURL+"/v1/acp", "application/json", strings.NewReader(initBody))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init %d", resp.StatusCode)
	}
	newBody := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`
	resp2, err := hc.Post(baseURL+"/v1/acp", "application/json", strings.NewReader(newBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatalf("no result: %v", m)
	}
	sid, _ := res["sessionId"].(string)
	if sid == "" {
		t.Fatal("empty sessionId")
	}
	return sid
}

func TestIntegration_cancelMidPrompt(t *testing.T) {
	t.Parallel()
	planReached := make(chan struct{}, 1)
	continuePrompt := make(chan struct{})
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnPromptUpdate: func(stage, sessionID string) {
			if stage != "plan" {
				return
			}
			select {
			case planReached <- struct{}{}:
			default:
			}
			<-continuePrompt
		},
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	call := lipapi.Call{
		ID: "cancel1",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("slow")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "agent"}}

	ctx, cancel := context.WithCancel(context.Background())
	es, err := be.Open(ctx, call, cand)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-planReached
		cancel()
		close(continuePrompt)
	}()
	_, err = lipapi.Collect(ctx, es)
	if err == nil {
		t.Fatal("expected error from cancellation")
	}
}
