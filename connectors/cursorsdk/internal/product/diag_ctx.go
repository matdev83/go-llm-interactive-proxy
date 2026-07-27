package product

import "context"

type diagCtxKey int

const (
	diagTraceIDKey diagCtxKey = iota + 1
	diagALegIDKey
)

// WithCallDiag attaches connector-local correlation IDs for diagnostics tests and
// optional host-injected context. External plugin hosts typically pass empty
// values; production emit paths also accept explicit DiagCorr fields.
func WithCallDiag(ctx context.Context, traceID, aLegID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if traceID != "" {
		ctx = context.WithValue(ctx, diagTraceIDKey, traceID)
	}
	if aLegID != "" {
		ctx = context.WithValue(ctx, diagALegIDKey, aLegID)
	}
	return ctx
}

// TraceID returns a connector-local trace id from ctx, if present.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(diagTraceIDKey).(string)
	return v
}

// ALegID returns a connector-local a-leg id from ctx, if present.
func ALegID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(diagALegIDKey).(string)
	return v
}
