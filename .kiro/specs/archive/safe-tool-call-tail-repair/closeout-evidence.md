# Safe Tool-Call Tail Repair Closeout

- **Spec:** `safe-tool-call-tail-repair`
- **Status:** completed and archived
- **Implementation PR:** [#260](https://github.com/matdev83/go-llm-interactive-proxy/pull/260)
- **Exact implementation commits** (head `1c97cb19`):
  - `67bdbf02` — feat: implement bounded safe tool-call tail repair (spec #252)
  - `4f58a5db` — fix(toolcallrepair): address review and gate findings for safe-tail repair (CodeQL overflow, repo hygiene, CodeRabbit items)
  - `1c97cb19` — refactor(toolcallrepair): simplify tail scanner per review (scan dedup, by-value helpers, shared overflow-safe candidate builder)
- **Closeout date:** 2026-08-08

## Verification

### Focused unit tests (Windows host, Go 1.26.5)

`go test -parallel=8 -timeout=10m ./internal/core/toolcallrepair/...` — **PASS** (repeated after each commit; includes classifier states, candidate construction, engine ordering, reason codes, no-partial-mutation, fuzz-found root-array-close regression cases `[1]x`/`[1,2]x`).

`go test -parallel=8 -timeout=10m ./internal/core/runtime/... ./internal/stdhttp/... ./internal/testkit/conformance/... ./internal/infra/runtimebundle/...` — **PASS** (finalizer chaining, fail-open replay, dogfood smoke, conformance cells, effective-load opt-out contract).

### Fuzz campaigns

`go test -fuzz=FuzzJSONTail -fuzztime=8s -run='^$' ./internal/core/toolcallrepair` — **PASS**, 126,293 execs (23 new interesting inputs).
`go test -fuzz=FuzzPendingRootValue -fuzztime=8s -run='^$' ./internal/core/toolcallrepair` — **PASS**, 77,034 execs (22 new interesting inputs).

Fuzzing caught one real regression during review refactoring (index-out-of-range on root array close from `tailArrayCommaOrEnd`); it was fixed with the `len(stack)==0 → rootDone` guard and locked in with the refuse cases above. The failing input is not committed as a hash-named corpus artifact (repo convention: named seeds only; the regression is covered by unit cases).

### Benchmarks (`BenchmarkSafeTailRepair`, benchtime=300x, same host, before/after review refactor)

| Case | before ns/op / B/op / allocs | after ns/op / B/op / allocs |
|---|---|---|
| append_only | 4729 / 6432 / 72 | 4723 / 6432 / 72 |
| terminal_comma | 5120 / 6736 / 81 | 4949 / 6736 / 81 |
| pending_const | 17757 / 16163 / 248 | 18330 / 16146 / 248 |
| pending_default | 17472 / 16115 / 237 | 17941 / 16097 / 237 |

Alloc/op and B/op are unchanged (72/81/248/237) across the behavior-preserving refactor; the near-limit case is proven to take the refusal path before timing. No valid pass-through regression: same-host time/op deltas are within noise and allocs are identical, satisfying the 8.10 gate (byte/depth/operation bounds remain the pass/fail gate; elapsed time is evidence only).

### CI gates (all on PR #260 and #261)

- Repo hygiene — **PASS** per commit (`scripts/check-release-clean.sh`, 5142 approved files, mode=ref on every branch commit)
- Test (ubuntu / windows / macos) — **PASS**
- Analyze (Go) — **PASS**
- qa — **PASS** (formatting/module integrity, archtest, vet lipstd, cmd/lipstd tests)
- Go vulnerability check — **PASS**
- CodeQL — **PASS** (the allocation-overflow alert was fixed by uint64-safe candidate sizing)
- Pinned official 17-case suite + Measured OpenResponses coverage — **PASS** (parity surfaces)
- `make quality-checks` — **PASS** end-to-end (gofmt, go mod, build, vet, ad-hoc goroutine allowlist, regex hot-path, `./internal/archtest/...`). One Windows note: the taskrunner wrapper applies a 2-minute deadline to the archtest step; the archtest package itself passes in ~34s standalone, and the final gate run passed fully.

### Strict Linux race + Tier-1 fuzz

Strict Linux race (`scripts/race-check.sh --strict`) and Tier-1 fuzz smoke run on a schedule (nightly, `race-fuzz-nightly.yml`, not merge-blocking on PRs). A manual run was dispatched against implementation head `1c97cb19` to produce PR-specific evidence; the `strict-linux-race-<sha>` artifact records the tested SHA, command, and logs. Windows hosts cannot run the strict Linux race locally (`make test-race` delegates to the PowerShell wrapper), so this run is the race evidence source for this closeout.

## Known limitations

- Benchmarks and focused runs were executed on a Windows host; Linux race/fuzz evidence comes from the dispatched nightly workflow, not a local Linux box.
- No type-derived `null`, `{}` fallback, partial-token completion, nested pending path, or escape deletion is performed; unsafe shapes replay the exact original bytes under fail-open (spec-mandated, not a gap).
- Fuzz durations are 8s per target (local) and 2s Tier-1 (nightly); longer adversarial campaigns remain available via `FUZZTIME` without code changes.

## Scope protection

This closeout moves and completes safe-tool-call-tail-repair bookkeeping only. It does not modify the ongoing `extension-scalability-and-architecture-simplification` / `openai-codex-native-compaction` specifications or their implementations.
