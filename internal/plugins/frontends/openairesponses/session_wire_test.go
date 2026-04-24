package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestDecodeCreateRequest_sessionMetadataAndHeaders(t *testing.T) {
	t.Parallel()
	body := `{
	  "model": "gpt-4o-mini",
	  "input": "hello",
	  "metadata": {
	    "` + sessionwire.MetaKeyAuthoritativeSessionID + `": "meta-sid",
	    "` + sessionwire.MetaKeyResumeToken + `": "meta-tok"
	  }
	}`
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
	h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:x",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "hdr-sid" {
		t.Fatalf("AuthoritativeSessionID: %q", d.Call.Session.AuthoritativeSessionID)
	}
	if d.Call.Session.ResumeToken != "hdr-tok" {
		t.Fatalf("ResumeToken: %q", d.Call.Session.ResumeToken)
	}
}

func TestDecodeCreateRequest_sessionMetadataOnly(t *testing.T) {
	t.Parallel()
	body := `{
	  "model": "gpt-4o-mini",
	  "input": "hello",
	  "metadata": {
	    "` + sessionwire.MetaKeyAuthoritativeSessionID + `": "only-sid",
	    "` + sessionwire.MetaKeyResumeToken + `": "only-tok"
	  }
	}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "only-sid" || d.Call.Session.ResumeToken != "only-tok" {
		t.Fatalf("session: %+v", d.Call.Session)
	}
}

type sessionDenyExecutor struct{}

func (sessionDenyExecutor) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	_ = ctx
	_ = call
	return nil, lipapi.NewSessionDenialInvalidAuthority("super-secret-internal")
}

func (sessionDenyExecutor) WallClock() func() time.Time { return nil }

func TestHandler_sessionDenial_nonEnumeratingJSON(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{
		Exec:                 sessionDenyExecutor{},
		DefaultRouteSelector: "stub:x",
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload.Error.Message, "super-secret") {
		t.Fatalf("message leaked internal: %q", payload.Error.Message)
	}
	if payload.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type: %q", payload.Error.Type)
	}
}

var _ lipsdk.ExecutorView = sessionDenyExecutor{}
