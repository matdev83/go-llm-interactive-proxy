//go:build race

package schemainfer_test

// raceCostFactor scales the wall-clock budget this suite asserts for the bounded
// declared-schema read. The race detector instruments every allocation and every
// memory access, and the read is allocation-heavy because it decodes each declared
// member value, so an uninstrumented cost becomes roughly an order of magnitude
// more expensive here. The factor is a property of the instrumented runtime, not of
// the bound being tested: it keeps the assertion meaning "the read is bounded", not
// "the read fits in this many nanoseconds on an idle machine".
const raceCostFactor = 12
