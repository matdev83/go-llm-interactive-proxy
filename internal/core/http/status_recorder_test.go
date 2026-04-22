package http_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
)

type flushSpy struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushSpy) Flush() {
	f.flushes++
}

func TestResponseStatusRecorder_implementsFlusherWhenUnderlyingDoes(t *testing.T) {
	t.Parallel()
	base := httptest.NewRecorder()
	spy := &flushSpy{ResponseRecorder: base}
	var w http.ResponseWriter = &corehttp.ResponseStatusRecorder{ResponseWriter: spy}
	fl, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("expected ResponseStatusRecorder to implement http.Flusher")
	}
	fl.Flush()
	if spy.flushes != 1 {
		t.Fatalf("Flush calls = %d want 1", spy.flushes)
	}
}

func TestResponseStatusRecorder_flushNoOpWhenUnderlyingNotFlusher(t *testing.T) {
	t.Parallel()
	base := struct{ http.ResponseWriter }{httptest.NewRecorder()}
	var w http.ResponseWriter = &corehttp.ResponseStatusRecorder{ResponseWriter: base.ResponseWriter}
	fl, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("expected wrapper to implement http.Flusher")
	}
	fl.Flush() // must not panic
}

func TestResponseStatusRecorder_ReadFromForwards(t *testing.T) {
	t.Parallel()
	base := httptest.NewRecorder()
	rr := &corehttp.ResponseStatusRecorder{ResponseWriter: base}
	n, err := rr.ReadFrom(bytes.NewBufferString("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("ReadFrom n = %d", n)
	}
	if base.Body.String() != "hello" {
		t.Fatalf("body = %q", base.Body.String())
	}
}

func TestResponseStatusRecorder_ReadFromFallsBackToCopy(t *testing.T) {
	t.Parallel()
	base := struct{ http.ResponseWriter }{httptest.NewRecorder()}
	rr := &corehttp.ResponseStatusRecorder{ResponseWriter: base.ResponseWriter}
	n, err := rr.ReadFrom(bytes.NewBufferString("x"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n = %d", n)
	}
}

var _ io.ReaderFrom = (*corehttp.ResponseStatusRecorder)(nil)
