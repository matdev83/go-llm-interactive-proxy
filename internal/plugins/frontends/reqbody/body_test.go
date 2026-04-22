package reqbody_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

func TestReadAll_tooLarge(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("POST", "/", bytes.NewReader(bytes.Repeat([]byte("a"), 200)))
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil || !reqbody.TooLarge(err) {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestReadAll_ok(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"x":1}`)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	b, err := reqbody.ReadAll(w, r, int64(len(payload)+10))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(payload) {
		t.Fatalf("body mismatch")
	}
}

func TestTooLarge_falseOnOtherErrors(t *testing.T) {
	t.Parallel()
	if reqbody.TooLarge(nil) || reqbody.TooLarge(io.EOF) {
		t.Fatal("expected false")
	}
}

func TestTooLarge_maxBytesErrorWrapped(t *testing.T) {
	t.Parallel()
	base := &http.MaxBytesError{Limit: 99}
	wrapped := fmt.Errorf("upstream: %w", base)
	if !reqbody.TooLarge(wrapped) {
		t.Fatal("expected TooLarge for errors.As(*http.MaxBytesError)")
	}
}
