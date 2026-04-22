package diag

import (
	"context"
	"fmt"
	"sync/atomic"
)

type TraceIDGenerator struct {
	seq uint64
}

func NewTraceIDGenerator() *TraceIDGenerator {
	return &TraceIDGenerator{}
}

func (g *TraceIDGenerator) Next() string {
	return fmt.Sprintf("t_%08d", atomic.AddUint64(&g.seq, 1))
}

var defaultTraceIDGen = NewTraceIDGenerator()

type ctxKey int

const (
	keyTraceID ctxKey = iota + 1
	keyALegID
	keyCallDiag
)

// callDiag carries trace and A-leg identifiers in a single context.Value for hot paths.
type callDiag struct {
	Trace string
	ALeg  string
}

// WithTraceID returns a child context that carries traceID for diagnostics propagation.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, keyTraceID, traceID)
}

// WithCallDiag attaches traceID and aLegID in one context layer (one allocation).
// Use on streaming hot paths instead of chaining WithTraceID and WithALeg.
func WithCallDiag(ctx context.Context, traceID, aLegID string) context.Context {
	return context.WithValue(ctx, keyCallDiag, callDiag{Trace: traceID, ALeg: aLegID})
}

// TraceID returns the trace identifier from ctx, or empty string if unset.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(keyCallDiag).(callDiag); ok {
		return v.Trace
	}
	if v, ok := ctx.Value(keyTraceID).(string); ok {
		return v
	}
	return ""
}

// WithALeg returns a child context that carries the A-leg identifier for lineage diagnostics.
func WithALeg(ctx context.Context, aLegID string) context.Context {
	return context.WithValue(ctx, keyALegID, aLegID)
}

// ALegID returns the A-leg identifier from ctx, or empty string if unset.
func ALegID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(keyCallDiag).(callDiag); ok {
		return v.ALeg
	}
	if v, ok := ctx.Value(keyALegID).(string); ok {
		return v
	}
	return ""
}

// NewTraceID generates a deterministic opaque trace identifier.
func NewTraceID() string {
	return defaultTraceIDGen.Next()
}
