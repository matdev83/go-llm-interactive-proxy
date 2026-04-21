package diag

import (
	"context"
	"testing"
)

func BenchmarkWithCallDiag(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for range b.N {
		_ = WithCallDiag(ctx, "trace-1", "aleg-1")
	}
}

func BenchmarkWithTraceIDThenALeg(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for range b.N {
		c := WithTraceID(ctx, "trace-1")
		_ = WithALeg(c, "aleg-1")
	}
}
