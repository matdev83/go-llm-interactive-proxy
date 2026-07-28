## 2025-03-05 - Optimize fmt.Sprintf in Ledger Store
**Learning:** `fmt.Sprintf` with simple `%s` and `%d` format verbs in hot paths (like SQL query builders) causes unnecessary reflection and parsing overhead. Simple string concatenation and `strconv.Itoa` can significantly reduce runtime latency and allocations, speeding up operations like `BenchmarkConcat-4` vs `BenchmarkSprintf-4` by over 4x (29ns vs 118ns) and `BenchmarkPlaceholderItoa-4` vs `BenchmarkPlaceholderSprintf-4` by over 2x (56ns vs 132ns).
**Action:** When working in hot path components like database layers, replace basic `fmt.Sprintf` usages with direct concatenation and `strconv` conversions. Keep complex `fmt.Sprintf` usage where it is beneficial for readability (e.g. error strings), but refactor inner-loop query generation.

## 2025-03-05 - Replace fmt.Sprintf with direct string concatenation and strconv for basic strings

**Learning:** `fmt.Sprintf` is heavy due to reflection and causes significantly more allocations and latency when concatenating simple strings and integers. In hot paths like metering (`LegacySourceEventKeyPhase31`), limits bounding, and environment variable iteration in the proxy codebase, using direct string concatenation (`+`) with `strconv.Itoa` or `strconv.FormatInt` provides a 3-4x performance improvement by avoiding this overhead.
**Action:** Always prefer direct string concatenation combined with `strconv` package functions over `fmt.Sprintf` for performance-critical hot paths when only simple strings and numbers are being joined.
