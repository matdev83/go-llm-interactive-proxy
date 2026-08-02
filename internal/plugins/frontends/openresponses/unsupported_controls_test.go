package openresponses_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// unsupportedFrontendControls mirror the protocol-level table: non-null values
// must fail in create frontend admission before any executor/upstream work, on
// both HTTP and WebSocket transports. Metadata is deliberately excluded.
var unsupportedFrontendControls = []struct {
	field string
	value string
}{
	{"instructions", `"Be brief"`},
	{"text", `{"format":"json_object"}`},
	{"reasoning", `{"effort":"low"}`},
	{"truncation", `"auto"`},
	{"service_tier", `"auto"`},
	{"safety_identifier", `"safety_001"`},
	{"prompt_cache_key", `"k1"`},
	{"prompt_cache_retention", `"recent"`},
	{"max_tool_calls", `5`},
}

// compactSchemaPermittedFrontendControls are permitted by the pinned compact
// schema (compactResponseMethodPublicBodySchema): accepted and ignored because
// compaction semantics treat them as intentionally optional.
var compactSchemaPermittedFrontendControls = []struct {
	field string
	value string
}{
	{"instructions", `"Be brief"`},
	{"prompt_cache_key", `"openresponses-compact-test"`},
}

// compactUnsupportedFrontendControls are absent from the pinned compact schema;
// a non-null value must fail compact admission instead of being silently dropped.
var compactUnsupportedFrontendControls = []struct {
	field string
	value string
}{
	{"text", `{"format":"json_object"}`},
	{"reasoning", `{"effort":"low"}`},
	{"truncation", `"auto"`},
	{"service_tier", `"auto"`},
	{"safety_identifier", `"safety_001"`},
	{"prompt_cache_retention", `"recent"`},
	{"max_tool_calls", `5`},
}

func TestFrontendHTTP_UnsupportedControlsRejectBeforeExecutor(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec})
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("non-null %s=%s: status=%d, want 400 (body=%s)", tc.field, tc.value, rec.Code, rec.Body.String())
			}
			if exec.executeCalls != 0 {
				t.Fatalf("non-null %s reached the executor: calls=%d", tc.field, exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_UnsupportedControlsNullAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":null}`)
			decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
				Auth: &mockAuthorizer{authenticated: true},
			})
			if err != nil {
				t.Fatalf("null %s must be accepted, got error: %v", tc.field, err)
			}
			if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
				t.Fatalf("null %s produced an invalid canonical call: %+v", tc.field, decoded.Call)
			}
		})
	}
}

func TestFrontendHTTP_SupportedControlsPreserved(t *testing.T) {
	t.Parallel()
	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: exec})
	body := []byte(`{
		"model":"gpt-4o",
		"input":"hello",
		"temperature":0.7,
		"top_p":0.9,
		"max_output_tokens":256,
		"parallel_tool_calls":true,
		"metadata":{"tenant":"acme"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if exec.executeCalls != 1 {
		t.Fatalf("supported controls must reach the executor: calls=%d (status=%d)", exec.executeCalls, rec.Code)
	}
	call := exec.lastCall
	if call == nil {
		t.Fatal("no canonical call captured")
	}
	if call.Options.Temperature == nil || *call.Options.Temperature != 0.7 {
		t.Errorf("temperature=%v, want 0.7", call.Options.Temperature)
	}
	if call.Options.TopP == nil || *call.Options.TopP != 0.9 {
		t.Errorf("top_p=%v, want 0.9", call.Options.TopP)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 256 {
		t.Errorf("max_output_tokens=%v, want 256", call.Options.MaxOutputTokens)
	}
	if call.Options.ParallelToolCalls == nil || !*call.Options.ParallelToolCalls {
		t.Errorf("parallel_tool_calls=%v, want true", call.Options.ParallelToolCalls)
	}
	if got := call.Session.Metadata["tenant"]; got != "acme" {
		t.Errorf("metadata=%v, want tenant=acme", call.Session.Metadata)
	}
}

func TestFrontendHTTP_CompactSchemaPermittedControlsAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range compactSchemaPermittedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)

			decoded, err := openresponses.AuthenticateAndDecodeCompact(context.Background(), body, openresponses.DecodeCompactOptions{
				Auth: &mockAuthorizer{authenticated: true},
			})
			if err != nil {
				t.Fatalf("compact decode must accept schema-permitted %s=%s: %v", tc.field, tc.value, err)
			}
			if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
				t.Fatalf("schema-permitted %s produced an invalid compact call", tc.field)
			}

			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec})
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if exec.executeCalls != 1 {
				t.Fatalf("schema-permitted %s=%s compact request must reach the executor: calls=%d (status=%d)", tc.field, tc.value, exec.executeCalls, rec.Code)
			}
		})
	}
}

func TestFrontendHTTP_CompactUnsupportedControlsRejectBeforeExecutor(t *testing.T) {
	t.Parallel()
	for _, tc := range compactUnsupportedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec})
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact",
				bytes.NewReader([]byte(`{"model":"gpt-4o","input":"hello","`+tc.field+`":`+tc.value+`}`)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("compact non-null %s=%s: status=%d, want 400 (body=%s)", tc.field, tc.value, rec.Code, rec.Body.String())
			}
			if exec.executeCalls != 0 {
				t.Fatalf("compact non-null %s reached the executor: calls=%d", tc.field, exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_CompactUnsupportedControlsNullAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range compactUnsupportedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			decoded, err := openresponses.AuthenticateAndDecodeCompact(context.Background(),
				[]byte(`{"model":"gpt-4o","input":"hello","`+tc.field+`":null}`),
				openresponses.DecodeCompactOptions{Auth: &mockAuthorizer{authenticated: true}})
			if err != nil {
				t.Fatalf("compact null %s must be accepted, got error: %v", tc.field, err)
			}
			if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
				t.Fatalf("compact null %s produced an invalid canonical call", tc.field)
			}
		})
	}
}

func TestWebSocketTurn_UnsupportedControlsRejectBeforeExecutor(t *testing.T) {
	for _, tc := range unsupportedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []lipapi.EventStream{fixedStream(
				lipapi.Event{Kind: lipapi.EventResponseStarted},
				lipapi.Event{Kind: lipapi.EventMessageStarted},
				lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "alive"},
				lipapi.Event{Kind: lipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_unsup", now: time.Unix(1_700_002_000, 0)}, nil)
			conn := wsDial(t, srv, nil)

			msg := `{"type":"response.create","model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`
			wsText(t, conn, msg)
			f := wsReadTextFrame(t, conn, 3*time.Second)
			code, param := wsErrorEnvelope(t, f)
			if code != "invalid_request" {
				t.Fatalf("error code=%q, want invalid_request (frame: %s)", code, f.raw)
			}
			if param != "" {
				t.Fatalf("expected no param for %s rejection, got %q (frame: %s)", tc.field, param, f.raw)
			}
			if exec.count() != 0 {
				t.Fatalf("non-null %s reached the executor: calls=%d", tc.field, exec.count())
			}

			// The connection must survive the classified rejection.
			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"next"}`)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "alive") {
				t.Fatalf("connection did not survive the %s rejection: %v", tc.field, frameTypes(frames))
			}
			_ = conn.Close()
			if exec.count() != 1 {
				t.Fatalf("executor calls=%d after follow-up, want 1", exec.count())
			}
		})
	}
}

func TestWebSocketTurn_UnsupportedControlsNullAccepted(t *testing.T) {
	for _, tc := range unsupportedFrontendControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []lipapi.EventStream{fixedStream(
				lipapi.Event{Kind: lipapi.EventResponseStarted},
				lipapi.Event{Kind: lipapi.EventMessageStarted},
				lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "ok"},
				lipapi.Event{Kind: lipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_null", now: time.Unix(1_700_002_100, 0)}, nil)
			conn := wsDial(t, srv, nil)

			msg := `{"type":"response.create","model":"gpt-4o","input":"hello","` + tc.field + `":null}`
			wsText(t, conn, msg)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "ok") {
				t.Fatalf("null %s was rejected: %v", tc.field, frameTypes(frames))
			}
			_ = conn.Close()
			if exec.count() != 1 {
				t.Fatalf("executor calls=%d, want 1", exec.count())
			}
		})
	}
}
