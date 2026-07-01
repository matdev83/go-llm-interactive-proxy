package openaicodex

import (
	"strings"
	"testing"
)

func TestUpstreamHTTPError_truncatesLongBody(t *testing.T) {
	t.Parallel()

	err := upstreamHTTPError(400, []byte(strings.Repeat("x", 5000)))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, strings.Repeat("x", 300)) {
		t.Fatalf("error leaks long upstream body (len=%d)", len(msg))
	}
	if !strings.Contains(msg, "truncated") {
		t.Fatalf("expected truncated marker in error: %q", msg)
	}
	if !strings.Contains(msg, "400") {
		t.Fatalf("expected status 400 in error: %q", msg)
	}
}

func TestUpstreamHTTPError_preservesShortBody(t *testing.T) {
	t.Parallel()

	body := `{"error":"bad request"}`
	err := upstreamHTTPError(418, []byte(body))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, body) {
		t.Fatalf("expected short body preserved verbatim: %q", msg)
	}
	if strings.Contains(msg, "truncated") {
		t.Fatalf("short body must not be marked truncated: %q", msg)
	}
	if !strings.Contains(msg, "418") {
		t.Fatalf("expected status 418 in error: %q", msg)
	}
}
