//go:build !race

package schemainfer_test

// raceCostFactor is 1 for an uninstrumented run; see costbudget_race_test.go for
// why the instrumented build needs a larger multiplier on the same assertion.
const raceCostFactor = 1
