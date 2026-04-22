package diag

import (
	"testing"
)

func TestRouteTraceBuffer_snapshotOrderAndWrap(t *testing.T) {
	t.Parallel()
	b := NewRouteTraceBuffer(3)
	b.Append(RouteTraceEntry{TraceID: "a"})
	b.Append(RouteTraceEntry{TraceID: "b"})
	b.Append(RouteTraceEntry{TraceID: "c"})
	got := b.Snapshot()
	if len(got) != 3 || got[0].TraceID != "a" || got[1].TraceID != "b" || got[2].TraceID != "c" {
		t.Fatalf("fill: %#v", got)
	}
	b.Append(RouteTraceEntry{TraceID: "d"})
	got = b.Snapshot()
	if len(got) != 3 || got[0].TraceID != "b" || got[1].TraceID != "c" || got[2].TraceID != "d" {
		t.Fatalf("after wrap: %#v", got)
	}
	b.Append(RouteTraceEntry{TraceID: "e"})
	got = b.Snapshot()
	if len(got) != 3 || got[0].TraceID != "c" || got[1].TraceID != "d" || got[2].TraceID != "e" {
		t.Fatalf("second wrap: %#v", got)
	}
}

func TestRouteTraceBuffer_emptySnapshotNonNil(t *testing.T) {
	t.Parallel()
	b := NewRouteTraceBuffer(4)
	got := b.Snapshot()
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestRouteTraceBuffer_nilAppendNoPanic(t *testing.T) {
	t.Parallel()
	var b *RouteTraceBuffer
	b.Append(RouteTraceEntry{TraceID: "x"})
}
