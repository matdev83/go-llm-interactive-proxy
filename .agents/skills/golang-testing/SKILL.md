---
name: golang-testing
description: "Write and review reliable Go unit, integration, benchmark, fuzz, HTTP, and concurrent tests. Use when choosing test scope, fixtures, cleanup, race coverage, deterministic time, mocks, or diagnosing flaky tests."
---

# Go testing

Test observable behavior and contracts. Keep tests deterministic, isolated, and proportional to risk; coverage percentage is a signal, not a correctness target.

## Unit tests

- Table-driven tests are useful when cases share setup and assertions; ordinary named tests are fine when scenarios differ.
- Use `t.Helper()` in helpers and `t.Cleanup()` for resources owned by a test. Prefer `t.Context()` (Go 1.24+) when the code under test should stop with the test.
- Make subtest names stable and descriptive. Use `t.Parallel()` only when the test and all shared fixtures are actually isolated; parallelism is not mandatory.
- Assert public behavior, error classification, and important side effects—not private layout or incidental call order.
- Run focused tests first (`go test -run 'TestName(?:/case)?$' ./path`), then the package and relevant integration tests.

## Integration and HTTP tests

Use `httptest` for in-process HTTP. For external services, make dependencies explicit, gate tests with the repository’s chosen mechanism, and fail clearly when prerequisites are absent. Do not use arbitrary sleeps for readiness: poll a health endpoint with a deadline or use `DB.PingContext` for databases. Read fixtures with checked errors, and report teardown failures without masking the primary failure.

Integration tests may be tagged or separately configured, but tags and sub-millisecond timing targets are repository choices, not universal rules. Use real services when protocol behavior is the subject; use fakes for deterministic domain tests.

## Concurrency and time

Test cancellation, shutdown, channel closure, queue limits, and first-error behavior. Run `go test -race` on supported platforms; it detects races in exercised paths but cannot prove absence. Prefer injected clocks or `testing/synctest` (Go 1.25+) for timer/deadline ordering; avoid `time.Sleep` as synchronization. A timeout bounds a test; it does not make a racy test reliable.

## Mocks, fuzzing, benchmarks

Define small interfaces at the consumer and use a hand-written fake when it makes behavior clearer. Use a mock framework only when interaction assertions are the behavior under test. Fuzz parsers and invariants with bounded inputs and a minimal corpus. Benchmarks should isolate setup, use `b.Loop()` when the module supports it, call `ReportAllocs` when allocations matter, and compare distributions with `benchstat`.

## Cleanup and diagnostics

Every opened resource and started goroutine needs a test-owned shutdown path. Use `t.Cleanup`, context cancellation, and bounded waits. `goleak` can catch leaks in a controlled package but requires filtering unavoidable runtime goroutines. Use `-count` and deterministic seeds to reproduce flakes; do not weaken assertions to make CI green.

See [helpers](references/helpers.md), [HTTP tests](references/http-testing.md), [integration tests](references/integration-testing.md), and [mocking](references/mocking.md).
