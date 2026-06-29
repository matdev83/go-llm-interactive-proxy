**Results Before Optimization:**

- `BenchmarkAssistantPartsToContentBlocks`: 9185 ns/op, 6816 B/op, 106 allocs/op
- `BenchmarkSystemPartsToContentBlocks`: 8913 ns/op, 6864 B/op, 108 allocs/op (not optimized)
- `BenchmarkBuildMessages`: 24220 ns/op, 19696 B/op, 208 allocs/op
- `BenchmarkUserPartsToContentBlocks`: 8595 ns/op, 6816 B/op, 106 allocs/op

**Results After Optimization:**

- `BenchmarkAssistantPartsToContentBlocks`: 6809 ns/op, 4192 B/op, 101 allocs/op (improvement: 25.8% speedup, 38.4% less memory allocated)
- `BenchmarkBuildMessages`: 14752 ns/op, 8864 B/op, 201 allocs/op (improvement: 39% speedup, 55% less memory allocated)
- `BenchmarkUserPartsToContentBlocks`: 6681 ns/op, 4192 B/op, 101 allocs/op (improvement: 22.2% speedup, 38.4% less memory allocated)
