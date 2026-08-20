---
name: golang-troubleshooting
description: "Diagnose Go build failures, panics, races, deadlocks, leaks, latency, memory growth, and production anomalies using reproduction, focused hypotheses, tests, profiles, traces, and debugger evidence. Use when behavior is surprising or failures are intermittent."
---

# Troubleshoot Go with evidence

Read the complete error, reproduce the symptom, measure one hypothesis, make the smallest fix, and verify the root cause. Do not optimize or add a workaround before establishing what failed.

## Triage

| Symptom | First evidence |
| --- | --- |
| Build or test compile failure | `go test`/`go build` for the package, `go env`, `go list -deps`, build tags |
| Wrong result or panic | Minimal failing test, full stack with `GOTRACEBACK=all`, input/zero-value checks |
| Intermittent failure or hang | `go test -race`, repeated focused runs, goroutine dump, channel/lock ownership |
| CPU or latency | Representative `pprof` CPU, mutex, block, and trace profiles |
| Memory growth | Comparable heap and allocation profiles over a controlled workload; inspect retained references |

Start with the smallest diagnostic that answers the question. Use `dlv` for a reproducible stateful failure, `runtime/pprof` or `net/http/pprof` for profiles, and `GODEBUG` only when the relevant runtime setting is documented for the installed Go version. Protect production diagnostics with authentication and network isolation.

## Method

1. Capture environment, revision, module/toolchain version, platform, inputs, and expected versus actual behavior.
2. Reduce to one package, one test, or one request. Preserve the failing input as a fixture.
3. Form one falsifiable hypothesis and collect the smallest confirming/disconfirming evidence.
4. Trace callers, validation, ownership, error handling, cleanup, goroutine lifecycle, and build constraints; a local-looking defect may be protected by an upstream invariant.
5. Fix the cause, add a regression test, inspect the diff, and rerun focused plus relevant broad checks. Compare profiles/benchmarks under the same workload.

## Common traps

- Check errors, including `Close`, fixture reads, and cleanup; do not replace a primary error silently.
- A typed nil inside an interface is non-nil. Nil maps panic on write; concurrent map mutation races or crashes.
- `time.After` in a loop can create allocation/timer churn, but since Go 1.23 unreachable timers can be collected; reuse a timer for hot loops when profiling justifies it, not because every use is a leak.
- Shadow analysis is separate from `go vet`’s default analyzers; run an explicitly installed shadow analyzer if the repository chooses it, not `go vet -shadow`.
- Lexical path-prefix checks do not confine filesystem access against `..`, separators, case, or symlinks. Prefer `os.Root` (supported Go versions) or an equivalent directory-scoped API.

See [methodology](references/methodology.md), [common bugs](references/common-go-bugs.md), [compilation](references/compilation.md), [test debugging](references/testing-debug.md), [concurrency debugging](references/concurrency-debug.md), [performance debugging](references/performance-debug.md), [diagnostic tools](references/diagnostic-tools.md), [pprof](references/pprof.md), and [production debugging](references/production-debug.md).
