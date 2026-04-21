# Allocation profiling (streaming hot path)

Micro-benchmarks live next to each frontend encoder (`encode_bench_test.go`). From the repo root:

```sh
go test -bench=BenchmarkWriteStreamSSE_textDeltas -benchmem -run=^$ \
  ./internal/plugins/frontends/openailegacy/... \
  ./internal/plugins/frontends/gemini/... \
  ./internal/plugins/frontends/openairesponses/... \
  ./internal/plugins/frontends/anthropic/...
```

Or use `make bench`, which runs benchmarks for testkit and these frontend packages.

## CPU + allocation profile

```sh
go test -bench=BenchmarkWriteStreamSSE_textDeltas -benchmem -run=^$ \
  -memprofile=allocs.prof ./internal/plugins/frontends/openailegacy

go tool pprof -sample_index=alloc_space -http=:0 allocs.prof
```

Use `-alloc_objects` in `pprof` to focus on allocation counts. Shared SSE helpers live in `internal/core/stream/ssejson.go` (`FlushSSEDataJSON`, `FlushSSEEventJSON`).

## Interpreting streaming benchmarks

For `BenchmarkWriteStreamSSE_textDeltas` in frontend packages, **`ns/op` and `allocs/op` refer to one full benchmark iteration**, not a single token. A typical iteration allocates a new `httptest.ResponseRecorder`, wraps the stream (for example `WrapRecoveryKeepalive`, which starts a goroutine and channels), performs many `Recv` calls, and issues one SSE flush per text delta. To approximate **per-delta** behavior, divide `allocs/op` by the number of deltas in the bench (often 256), or run the narrower `BenchmarkGemini_flushTextDelta_only` in `internal/plugins/frontends/gemini`, which reuses a recorder and only exercises the JSON+SSE flush for one delta per iteration.

`encoding/json` still allocates while marshaling even with reused Go structs; eliminating that requires a hand-written serializer or a different codec, not only slice reuse.
