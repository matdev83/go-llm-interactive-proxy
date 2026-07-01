package openailegacy_test

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
)

// TestHandler_acceptsGzipContentEncoding proves the frontend transparently decompresses
// Content-Encoding: gzip request bodies before preflight/decode, so gzip-compressed clients
// (e.g. some AI coding agents) are not bounced with "invalid request JSON".
func TestHandler_acceptsGzipContentEncoding(t *testing.T) {
	t.Parallel()

	plain := readGolden(t, "create_text_nonstream.json")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(plain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor was not called for gzip-encoded body")
	}
}
