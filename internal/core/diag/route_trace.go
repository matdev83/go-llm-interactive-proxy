package diag

import (
	"context"
	"encoding/json"
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
	mu   sync.Mutex
	cap  int
	ring []RouteTraceEntry
}

// NewRouteTraceBuffer creates a ring buffer with capacity n (minimum 1).
func NewRouteTraceBuffer(n int) *RouteTraceBuffer {
	if n < 1 {
		n = 32
	}
	return &RouteTraceBuffer{cap: n, ring: make([]RouteTraceEntry, 0, n)}
}

// Append adds an entry (drops oldest when full).
func (b *RouteTraceBuffer) Append(e RouteTraceEntry) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.ring) >= b.cap {
		copy(b.ring[:b.cap-1], b.ring[1:])
		b.ring = b.ring[:b.cap-1]
	}
	b.ring = append(b.ring, e)
}

// Snapshot returns a copy of recent entries (newest last).
func (b *RouteTraceBuffer) Snapshot() []RouteTraceEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]RouteTraceEntry, len(b.ring))
	copy(out, b.ring)
	return out
}

// RouteTraceHandler serves GET JSON of buffered route traces.
func RouteTraceHandler(buf *RouteTraceBuffer) http.Handler {
	if buf == nil {
		panic("diag: RouteTraceHandler: nil buffer")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		_ = enc.Encode(buf.Snapshot())
	})
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
	v, _ := ctx.Value(ctxBufKey{}).(*RouteTraceBuffer)
	return v
}
