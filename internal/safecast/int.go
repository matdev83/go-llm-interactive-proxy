// Package safecast provides bounded conversions for numeric values at protocol boundaries.
package safecast

import "math"

// IntFromInt64Clamp converts v to int, clamping when v does not fit the platform int size.
// Use for telemetry and usage counters where silent truncation is worse than saturation.
func IntFromInt64Clamp(v int64) int {
	if v > int64(math.MaxInt) {
		return math.MaxInt
	}
	if v < int64(math.MinInt) {
		return math.MinInt
	}
	return int(v)
}
