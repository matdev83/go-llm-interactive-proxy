package reqbody_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

type errCloseBody struct {
	r      io.Reader
	err    error
	closed bool
}

func (e *errCloseBody) Read(p []byte) (int, error) { return e.r.Read(p) }

func (e *errCloseBody) Close() error {
	e.closed = true
	return e.err
}

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

func TestReadAll_closeError(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	body := &errCloseBody{r: bytes.NewReader([]byte("ok")), err: closeErr}
	r := httptest.NewRequest("POST", "/", body)
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil {
		t.Fatal("expected error from Close")
	}
	if !body.closed {
		t.Fatal("expected body Close to run")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected unwrap to close error, got %v", err)
	}
}

func TestReadAll_readErrorJoinedWithCloseError(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	// Short read: MaxBytesReader returns error; Close may also fail.
	r := httptest.NewRequest("POST", "/", &errCloseBody{
		r:   io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("a"), 500)), 500),
		err: closeErr,
	})
	w := httptest.NewRecorder()
	_, err := reqbody.ReadAll(w, r, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !reqbody.TooLarge(err) {
		t.Fatalf("expected too-large read error in chain, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error joined in chain, got %v", err)
	}
}
