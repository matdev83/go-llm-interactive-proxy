# PR D3 final benchmark notes

## Host / tooling

See `measurement-host.txt`. `benchstat` was installed successfully (`go install golang.org/x/perf/cmd/benchstat@latest`) and used for A/B comparison.

## Artifacts

| File | Role |
| --- | --- |
| `bench-final-runA.txt` | Raw `go test -bench=… -count=6` run A |
| `bench-final-runB.txt` | Raw `go test -bench=… -count=6` run B |
| `benchstat-final.txt` | `benchstat` comparison of A vs B |
| `bench-runA.txt` / `bench-runB.txt` | Pre-convergence baseline benches (historical) |

## Covered surfaces (existing benchmarks)

| Required surface | Benchmark | Status |
| --- | --- | --- |
| Manager Acquire/Release | `BenchmarkManager_AcquireRelease` | ran |
| Manager Publish | `BenchmarkManager_Publish` | ran |
| Generation dispatcher | `BenchmarkGenerationDispatcher_AcquireLease` | ran |
| Candidate/generation compilation | `BenchmarkCandidateCompilation` | ran |
| Retention overhead (extra) | `BenchmarkRetainedGenerationOverhead` | ran |

## Gaps (no dedicated benchmark in tree)

| Required surface | Gap |
| --- | --- |
| BuildHost | No `BenchmarkBuildHost`. `BenchmarkCandidateCompilation` calls `BuildHost` once in setup, then compiles candidates; setup cost is not the timed loop. |
| Successful reload | No `Benchmark*Reload*published*` / successful-reload hot path bench. |
| No-op reload | No `Benchmark*NoOp*Reload*` bench. |

These gaps are recorded truthfully; final evidence uses the available Manager/dispatcher/compilation benches plus full `make bench` (PR D4) rather than inventing synthetic reload benches.

## Noise note

Runs A and B were captured while `make test` was also executing on the same 2-vCPU host. Hot-path Manager/dispatcher benches stayed stable; `RetainedGenerationOverhead` and `CandidateCompilation` show A/B wall-time variance under that contention. Treat A/B as reproducibility evidence under load, not as a clean isolated regression signal versus the pre-convergence `bench-runA/B.txt` baseline.

## Command used for targeted runs

```bash
go test -bench='BenchmarkManager_AcquireRelease|BenchmarkManager_Publish|BenchmarkGenerationDispatcher_AcquireLease|BenchmarkCandidateCompilation|BenchmarkRetainedGenerationOverhead' \
  -benchmem -count=6 -run='^$' \
  ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/...
```
