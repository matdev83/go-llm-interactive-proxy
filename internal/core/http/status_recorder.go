package http

import (
	"bufio"
	"errors"
	"io"
	"net"
	nethttp "net/http"
)

// ResponseStatusRecorder wraps [net/http.ResponseWriter] and records the first status passed to WriteHeader.
// It forwards optional [net/http.ResponseWriter] behaviors (Flusher, Hijacker, Pusher, [io.ReaderFrom])
// so streaming middleware preserves flush and HTTP/2 push support.
type ResponseStatusRecorder struct {
	nethttp.ResponseWriter
	Status int
}

func (rr *ResponseStatusRecorder) WriteHeader(code int) {
	if rr.Status == 0 {
		rr.Status = code
	}
	rr.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying [net/http.Flusher] when supported; otherwise it is a no-op.
func (rr *ResponseStatusRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(nethttp.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying [net/http.Hijacker] when supported.
func (rr *ResponseStatusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rr.ResponseWriter.(nethttp.Hijacker)
	if !ok {
		return nil, nil, errors.New("http: Hijacker not supported by underlying ResponseWriter")
	}
	return h.Hijack()
}

// Push forwards to the underlying [net/http.Pusher] when supported.
func (rr *ResponseStatusRecorder) Push(target string, opts *nethttp.PushOptions) error {
	p, ok := rr.ResponseWriter.(nethttp.Pusher)
	if !ok {
		return nethttp.ErrNotSupported
	}
	return p.Push(target, opts)
}

// ReadFrom forwards to the underlying [io.ReaderFrom] when supported.
func (rr *ResponseStatusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := rr.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(rr.ResponseWriter, r)
}
