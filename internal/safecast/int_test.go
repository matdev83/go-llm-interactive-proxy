package safecast

import (
	"math"
	"testing"
)

func TestIntFromInt64Clamp_roundTrip(t *testing.T) {
	t.Parallel()
	if got := IntFromInt64Clamp(42); got != 42 {
		t.Fatalf("got %d", got)
	}
}

func TestIntFromInt64Clamp_clampMax(t *testing.T) {
	t.Parallel()
	if got := IntFromInt64Clamp(math.MaxInt64); got != math.MaxInt {
		t.Fatalf("got %d want %d", got, math.MaxInt)
	}
}

func TestIntFromInt64Clamp_clampMin(t *testing.T) {
	t.Parallel()
	if got := IntFromInt64Clamp(math.MinInt64); got != math.MinInt {
		t.Fatalf("got %d want %d", got, math.MinInt)
	}
}
