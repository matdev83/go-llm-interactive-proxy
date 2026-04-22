package diag

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
)

// RouteTraceEntry is one structured routing decision snapshot.
type RouteTraceEntry struct {
	TraceID  string `json:"trace_id"`
	Decision string `json:"decision"`
	Detail   string `json:"detail"`
}

// RouteTraceBuffer keeps a bounded FIFO of recent route-plan entries (debug only).
type RouteTraceBuffer struct {
	mu    sync.Mutex
	cap   int
	buf   []RouteTraceEntry
	head  int
	count int
}

// NewRouteTraceBuffer creates a ring buffer with capacity n (minimum 1).
func NewRouteTraceBuffer(n int) *RouteTraceBuffer {
	if n < 1 {
		n = 32
	}
	return &RouteTraceBuffer{cap: n, buf: make([]RouteTraceEntry, n)}
}

// Append adds an entry (drops oldest when full).
func (b *RouteTraceBuffer) Append(e RouteTraceEntry) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cap < 1 {
		return
	}
	if len(b.buf) != b.cap {
		b.buf = make([]RouteTraceEntry, b.cap)
		b.head = 0
		b.count = 0
	}
	if b.count < b.cap {
		idx := (b.head + b.count) % b.cap
		b.buf[idx] = e
		b.count++
		return
	}
	b.head = (b.head + 1) % b.cap
	b.buf[(b.head+b.count-1)%b.cap] = e
}

// Snapshot returns a copy of recent entries (newest last).
func (b *RouteTraceBuffer) Snapshot() []RouteTraceEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count == 0 {
		return []RouteTraceEntry{}
	}
	out := make([]RouteTraceEntry, b.count)
	for i := 0; i < b.count; i++ {
		out[i] = b.buf[(b.head+i)%b.cap]
	}
	return out
}

// RouteTraceHandler serves GET JSON of buffered route traces.
func RouteTraceHandler(buf *RouteTraceBuffer) (http.Handler, error) {
	if buf == nil {
		return nil, errors.New("diag: RouteTraceHandler: nil buffer")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		if err := enc.Encode(buf.Snapshot()); err != nil {
			slog.Default().Error("diag: route trace encode", "err", err)
		}
	}), nil
}

// ContextBufferKey is a private context key for attaching a trace buffer (optional).
type ctxBufKey struct{}

// WithRouteTraceBuffer attaches buf to ctx for downstream logging helpers.
func WithRouteTraceBuffer(ctx context.Context, buf *RouteTraceBuffer) context.Context {
	return context.WithValue(ctx, ctxBufKey{}, buf)
}

// RouteTraceBufferFrom returns the buffer from ctx or nil.
func RouteTraceBufferFrom(ctx context.Context) *RouteTraceBuffer {
	if ctx == nil {
		return nil
	}
	v, ok := ctx.Value(ctxBufKey{}).(*RouteTraceBuffer)
	if !ok {
		return nil
	}
	return v
}
