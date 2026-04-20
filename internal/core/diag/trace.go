package diag

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type ctxKey int

const (
	keyTraceID ctxKey = iota + 1
	keyALegID
)

// WithTraceID returns a child context that carries traceID for diagnostics propagation.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, keyTraceID, traceID)
}

// TraceID returns the trace identifier from ctx, or empty string if unset.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(keyTraceID).(string)
	return v
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
	v, _ := ctx.Value(keyALegID).(string)
	return v
}

// NewTraceID generates a new opaque trace identifier (t_ prefix + random hex).
func NewTraceID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("diag: crypto/rand: " + err.Error())
	}
	return "t_" + hex.EncodeToString(b[:])
}
