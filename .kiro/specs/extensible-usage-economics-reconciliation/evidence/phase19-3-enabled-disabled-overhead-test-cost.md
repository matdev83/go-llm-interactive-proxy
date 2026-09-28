# Phase 19.3 enabled/disabled overhead and test-cost certification evidence

Task: `.kiro/specs/extensible-usage-economics-reconciliation/tasks.md` 19.3 —
certify enabled and disabled overhead and test cost.
Branch: `feat/b-leg-usage-economics`. Baseline: `fe73f55b` plus the completed
19.1/19.2 worktree state (preserved; see file list below). Task 1 measurement
baseline: pristine `d1847d5e` numbers from
`.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/phase1-execution-baseline.md`.
Boundary: tests: performance and QA cost. Contracts: Performance; Baseline
Task 1.3.
Validation: focused Go benchmarks with `-benchmem`; `make test-cost` on
Windows; `make quality-checks`.
Requirements in scope: 4.6, 18.3, 18.5, 18.6.

## 1. Provenance and environment

- Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
- Branch: `feat/b-leg-usage-economics`, HEAD `fe73f55b` (plus uncommitted
  19.1/19.2/19.3 state; the 11 tracked modifications/deletions and 12
  untracked files are listed in section 2; no commit, rebase, merge, push or
  PR was performed).
- Measurement snapshot identity (throwaway temp clone only, never the real
  repo): `c7c619abef299ef7b9d8165e5def47955554b103`, verified byte-identical
  to the real worktree state (same 23 `git status` entries after copy).
- Host: Windows build 10.0.19045, AMD Ryzen 7 5800X 8-Core Processor,
  16 logical processors, Go `go1.26.6 windows/amd64`, benchmark suffix `-16`
  (GOMAXPROCS 16). C: `59,682,484,224` bytes free at start.
- Date: 2026-09-22. All benchmark suites ran sequentially (never in parallel
  with another heavy suite) to avoid contaminating benchmark data.
- `benchstat` via `golang.org/x/perf/cmd/benchstat@latest` (module cache only;
  `go.mod`/`go.sum` unchanged, verified clean).
- test-cost anchor (policy `scripts/test-cost-budget.json` `anchor_ref`):
  `6dbb831885341516117034923f0c3203373aded0`; policy protection passes
  unchanged against `-BaseSHA fe73f55b` (preflight OK).

## 2. Changed files

19.3-owned (new certification tests/benchmarks plus one minimal performance
fix; no 19.1/19.2 semantics altered):

- `internal/core/runtime/phase19_3_cost_certification_test.go` (new) —
  disabled-path RED tests plus the multi-MiB canonical accounting benchmark.
- `pkg/lipsdk/metering/phase19_3_bounded_metadata_test.go` (new) — bounded
  metadata limit tests plus max-bounds benchmark.
- `internal/infra/billingspool/phase19_3_terminal_spool_bench_test.go` (new) —
  durable terminal spool append/deliver benchmarks.
- `internal/core/runtime/attempt_session.go` (modified) — `billingLegRecordRequired`
  guard; skip discarded terminal record construction when no consumer exists.
- `internal/core/runtime/local_boundary.go` (modified) — return before
  observation-identity construction when no boundary accumulator exists.

Preserved 19.1/19.2 work (untouched): phase19-1/19-2 evidence files,
`phase19_1_lifecycle_certification_test.go`,
`phase19_2_restart_lifecycle_race_certification_test.go`,
`connectors/codex` account-window files, billingstore test/store edits,
`scripts/check-adhoc-goroutines.*` edits.

## 3. Disabled-path regression found and fixed (RED -> GREEN)

The Task 1.3 disabled benchmark re-run showed `+9–10 KiB/op` versus the frozen
baseline (159,465–160,300 B/op vs 149,312–150,228 B/op). An `-memprofile`
attribution run proved `billingLegRecord` (via
`attemptSession.TerminalizeAttempt.func3` → `observationsFromBillingEvidence`)
was constructed on the accounting-disabled path and then discarded: the
step-8 block ran whenever `claimBillingLegRecord()` succeeded, with no
`billingEnabled()` gate, while `turnTerminal.recordBillingLegForAttempt`
already had one. A second disabled-path allocation was the observation
identity (scope clone) built in `localBoundaryObservations` before the
nil-accumulator check.

RED tests (both failed before the fix, pass after):

- `TestPhase193DisabledAccountingSkipsDiscardedBillingLegRecord` — disabled
  session (`billingEnabled() == false`) with spy observer/append callbacks:
  before fix the observer fired once; after fix both counters stay 0.
- `TestPhase193DisabledLocalBoundaryDrainAllocatesNothing` — populated scope
  with labels, no accumulator: `testing.AllocsPerRun` was 4, now exactly 0.

Fix semantics preserved: capture-only configurations (observation sink set)
still run `finalizeBilling` and `flushEconomicCheckpointsAtTerminal`; a nil
`billingEnabled` predicate keeps the historical record path for direct test
wiring; `BudgetLegFn` branch untouched; full `internal/core/runtime` package
passes, as do billing/metering/billingspool/billingstore/binding/runtimebundle
suites (one load-sensitive runtimebundle timeout flaked once under parallel
load and passed on isolated and full reruns; see section 8).

benchstat (before-fix `p193-accounting.txt` count=3 vs after-fix
`p193-accounting-final.txt` count=5, `-benchtime=100x`):

- Disabled B/op: 155.8Ki -> 149.1Ki, **-4.32% (p=0.036)**.
- Disabled allocs/op: 785.0 -> 766.0, -2.42%.
- Enabled B/op and allocs/op: unchanged (p=0.786 / p=1.000).
- Terminal metrics: exactly 0.000 disabled / 1.000 enabled before and after
  (p=1.000). Latency: no significant change (p=0.786 / p=0.571).

Post-fix `pprof -top -sample_index=alloc_space` on the disabled path shows no
`billingLegRecord`/`observationsFromBillingEvidence`/metering-accounting
frames in the top 25; remaining allocations are generic runtime machinery
(recv views, response pipeline, secure session).

## 4. Task 1.3 benchmark re-runs at the final tree (repeated, -benchmem)

Commands (from the repository root; raw outputs retained as listed):

| Benchmark (command) | Task 1.3 baseline (d1847d5e) | Final tree (c7c619ab state) | Verdict |
|---|---|---|---|
| `BenchmarkExecutorExecuteAndDrain32Deltas` `-count=5` | median 365,685 ns/op; ~643,181 B/op; 2,409 allocs/op (raw ns 379260/366003/362797/365685/358362; B 643176–643267) | 406766/393660/400613/401071/396521 ns/op; 681,709–681,781 B/op; 2,424 allocs/op | Enabled-path V2 record cost: +15 allocs (+0.6%), +38.5 KiB (+6.0%); latency +7%. Bounded constant; no budget relaxed. |
| `BenchmarkExecutor_TrafficDisabled` `-count=5` | median 138,925 ns/op; ~161,740 B/op; 873 allocs/op (raw 145850/138925/137051/138220/139194; B 161722–161764) | 168200/153997/153978/151318/150000 ns/op; 172,056–172,167 B/op; 888 allocs/op | +15 allocs (+1.7%), +10.3 KiB (+6.4%) on the (test-sink-bound) path. |
| `BenchmarkExecutor_TrafficEnabled` `-count=5` | median 166,933 ns/op; ~191,694 B/op; 943 allocs/op (raw 166737/167547/167023/166933/166641; B 191613–191711) | 180698/181560/182895/183016/180371 ns/op; 204,577–204,607 B/op; 958 allocs/op | +15 allocs (+1.6%), +12.9 KiB (+6.7%). |
| `BenchmarkRefinementAccountingBaseline/Disabled` `-benchtime=100x -count=5` | 157084/135233/140906 ns/op; 150228/149343/149312 B/op; 785/773/773 allocs/op; 0 terminal appends | 160639/127715/202305/216761/220468 ns/op; 153572/152492/152734/152648/152449 B/op; 778/765/766/766/765 allocs/op; 0 terminal appends | **Zero accounting I/O; allocs at/below baseline (766 vs 773–785).** Residual +2.2–3.3 KiB B/op is non-accounting (pprof proof). |
| `BenchmarkRefinementAccountingBaseline/Enabled` same | 154474/222516/209641 ns/op; 158189/158094/158132 B/op; 855 allocs/op; 1.0 terminal appends | 168507/218984/247519/245164/160176 ns/op; 170044/169958/170004/170043/170011 B/op; 878 allocs/op; 1.0 terminal appends | Bounded enabled capture: +23 allocs (+2.7%), +11.8 KiB (+7.5%); exactly 1 observer + 1 leg + 1 call per execution. |
| `BenchmarkLargePayloadBaseline_Preflight/5MiB` `-benchtime=1x -count=3` | 23870900/25361400/23774900 ns/op; 22018912–22018928 B/op; 45–46 allocs/op | 28463700/25915500/26804000 ns/op; 22018912/22018912/22019248 B/op; 45/45/48 allocs/op | Allocation-identical; latency within single-run noise. |
| `BenchmarkLargePayloadBaseline_DecodeResponses/5MiB` same | 52888900/47281400/47435000 ns/op; 15731408–15733592 B/op; 33–37 allocs/op | 57135400/54151100/54006800 ns/op; 15731088/15733592/15733608 B/op; 31/37/37 allocs/op | Allocation-identical; latency ~+8% single-run noise. |

Note: the executor/traffic benchmarks use `TestExecutor()`'s default no-op
terminal sink, so they exercise the enabled accounting path; the explicit
disabled configuration is `BenchmarkRefinementAccountingBaseline/Disabled`.

## 5. Enabled-path certification (new 19.3 benchmarks)

Multi-MiB canonical accounting
(`BenchmarkPhase193MultiMiBCanonicalAccounting`, `-benchtime=20x -count=3`,
Recv-drain without client collect accumulation):

- 1 MiB disabled: 3,166,495/2,720,660/4,178,690 ns/op; ~14.4 MB/op;
  1691/1631/1628 allocs/op; 0 terminal callbacks.
- 1 MiB enabled: 2,881,450/2,965,915/4,968,465 ns/op; ~14.4 MB/op;
  1744/1744/1741 allocs/op; 1.000 observer/leg/call per op.
- 5 MiB disabled: 16,069,460/12,242,990/17,284,380 ns/op; ~74.26 MB/op;
  5,167 allocs/op; 0 terminal callbacks.
- 5 MiB enabled: 16,136,805/14,607,135/15,442,330 ns/op; ~73.75–74.28 MB/op;
  5283/5283/5285 allocs/op; 1.000 terminal callbacks per op.
- Enabled accounting delta is ~+113–116 allocs/op and ~0 B/op delta within
  noise at both sizes: **response-size-independent constant capture cost; no
  response-size-proportional extra copies for accounting** (Req 18.5).

Fast-path multi-MiB traffic, 5 MiB (`-benchtime=1x -count=3`):

- Capture: 15,835,600/16,617,100/23,699,800 ns/op; 144,416 B/op; 48 allocs/op
  (identical B/op at 32 KiB–5 MiB: bounded spill memory contract holds).
- Proof: 98,902,600/98,795,100/99,565,300 ns/op; ~203 KiB/op; 304–310 allocs/op.
- Assessment: 3,500/3,700/3,800 ns/op; 896 B/op; 1 alloc/op (size-independent).
- WireEndToEnd: 112,831,300/114,195,300/113,663,000 ns/op; ~385 KiB/op;
  397–403 allocs/op.
- Shapes/CanonicalFallback 1 MiB (`-benchtime=3x -count=2`): 27,999,133/
  29,991,933 ns/op; ~10.2 MB/op; 875/333 allocs/op. ReplayFailover 1 MiB:
  26,168,333/27,567,467 ns/op; ~379 KiB/op; 395/392 allocs/op.
- Eligibility gate (`BenchmarkStaticDisposition_*`, `-count=3`): 3.77–3.80
  ns/op feature-disabled; 10.2–11.7 ns/op ineligible variants; **0 B/op,
  0 allocs/op everywhere** (Req 4.6: the accounting-blocked decline path
  costs nothing before upstream commitment).

Bounded metadata limits (`BenchmarkPhase193BoundedMetadataObservation`,
`-count=3`; fixture: 128 measures + 128 charges + 256 evidence fields,
normalized payload within the 64 KiB cap):

- Validate: 653,913/669,971/658,957 ns/op; ~216 KiB/op; 1,684 allocs/op.
- CanonicalJSON: 2,372,629/2,187,894/2,157,715 ns/op; ~912 KiB/op;
  17,000/17,000/16,999 allocs/op.
- Fingerprint: 2,149,412/2,143,853/2,123,562 ns/op; ~916 KiB/op; 17,001 allocs/op.
- At-limit test retains all 128/128/256 entries through canonicalization with
  a stable non-empty fingerprint (no silent drop); seven bound families fail
  closed with `ErrInvalidObservation` on both `Validate` and `CanonicalJSON`:
  129th measure/charge/evidence/supersedes, 17 dimensions, oversized
  dimension value, and — independently of all entry/dimension counts — a
  within-counts observation whose normalized serialization exceeds the 64 KiB
  cap (`TestPhase193BoundedObservationOverLimitFailsClosed` covers the first
  five; `TestPhase19R4ObservationSupersedesOverLimitFailsClosed` and
  `TestPhase19R4NormalizedSerializationCapFailsClosedIndependentOfEntryBounds`
  in `pkg/lipsdk/metering/phase19_r4_serialization_cap_test.go` cover
  supersedes and the independent serialization cap). The earlier draft of this
  section said "six over-limit variants"; the honest count is seven bound
  families (Req 18.5).

Terminal append/spool writes, durable SQLite spool (`-benchtime=100x -count=3`):

- AppendLeg: 2,228,288/2,774,682/3,357,788 ns/op; 20,461/19,698/19,308 B/op;
  227/219/218 allocs/op; pending-records 100.
- AppendCall: 3,215,405/2,863,140/3,014,271 ns/op; 16,730/16,762/16,916 B/op;
  234/233/234 allocs/op; pending-records 100.
- AppendAndDeliver: 7,444,241/7,107,506/7,679,659 ns/op; 37,711/37,565/37,501
  B/op; 440/439/439 allocs/op; pending-records 0 (delivery drains).
- Each append performs exactly one seal + one pending-capacity aggregate scan
  + one insert + one commit (the per-append aggregate scan remains the
  recorded #394 task-6.5 candidate; it is measured here, not modified, as it
  belongs to #394's own tasks).

## 6. Windows make test-cost evidence (two repeats)

Method: the ratchet requires a clean checkout, so a throwaway temp clone
(`p193-snapshot`, hardlinked, verified identical to the real worktree state)
received a local snapshot commit `c7c619ab` (temp clone only; the real repo
was never committed to). Command per run:

```powershell
$env:TEST_COST_BASE_SHA="fe73f55b"
$env:TEST_COST_OUTPUT_ROOT="C:\Users\Mateusz\AppData\Local\Temp\opencode\p193-testcost-run<N>"
$env:TEST_COST_PARALLEL="4"
make test-cost
```

Both runs (`p193-testcost-run1`, `p193-testcost-run2`) exit 1 at the head
`test-unit` measurement (wrapper exit 3): the head tree cannot complete the
ratchet because 19 tests fail. The failure set is **byte-identical across both
runs**:

- `internal/archtest` (16): TestBillingCoreStaysProviderAndPersistenceFree,
  TestBillingFinalConvergenceLOCRatchetActive,
  TestCompactionContinuitySecurity_ContentFreePublicSurfaces,
  TestConversationViewChangedGoFileCountUnderGate, TestCriticalFileBudgets,
  TestHexagonalMigrationBaselineMatchesGoList (+runtimebundle sub),
  TestLineComplexityBudgets (+core/infra-runtimebundle/stdhttp subs),
  TestPackageTreeBudgetsExact (+infra-runtimebundle/stdhttp subs),
  TestPackageTreeBudgetsReportSection,
  TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject,
  TestPhase51AttemptSequenceAuthorityRatchets,
  TestRequestAttemptStateBaselineMatchesCurrentAST,
  TestRequestAttemptStateRatchetsPassOnCurrentCode,
  TestRequestAttemptStateTargetRatchetFailsIfTypeReappearsOnCurrentAST,
  TestShrinkage_ConnectorOverlayExactMeasured,
  TestShrinkage_NetReductionMeetsRequirement115.
- `internal/qa` (2): TestPhase8ProducerCensusHasExplicitDisposition,
  TestQAFastPreflight_TestCost_ArchtestGoListCallsUseDedicatedCache.
- `internal/providerprofiles` (1):
  TestSyntheticCatalog_1000ProfilesIsBoundedAndIndependent — passes 3/3 in
  isolation and at pristine fe73f55b; fails only under full-suite parallel
  load (goroutine-count bound vs parallel-test background goroutines):
  recorded as load-sensitive flake, not a regression.

Attribution: the same 16 archtest + 2 qa tests fail at pristine `fe73f55b`
(verified in a detached temp worktree `p193-pristine-fe73f55b`; providerprofiles
passed there under light load). **Zero new test failures are introduced by
the 19.1/19.2/19.3 worktree state.** The debt belongs to the branch-vs-anchor
budget drift (shrinkage/convergence/package budgets, A-leg authority,
request-attempt ratchets, census) and is Phase 20 release-gate material, not
19.3 scope; no budget was relaxed.

Quantitative cost (run 1; anchor JSON `measurements/anchor-test-unit.json`
vs head package timings parsed from the 55 MB head stdout log):

| Scope | Anchor 6dbb8318 | Head c7c619ab state | Note |
|---|---|---|---|
| Packages measured | 341 | 349 | +8 packages from the feature program |
| Wall | 83.8 s | 302.6 s | dominated by billingstore 295.19 s (19.1/19.2 file-backed certification suites) vs anchor 19.41 s |
| CPU total | 1,024.8 s | (measurement aborted; package-elapsed sum 949.9 s) | anchor/head not directly comparable while red |
| Processes | 2,454 | — | anchor only |
| I/O ops | 8,674,953 | — | anchor only |
| runtime pkg | 9.08 s | 9.7 s | +0.6 s, within variance |
| lipsdk/metering pkg | 0.15 s | 0.47 s | +0.3 s absolute (includes new 19.3 bounds tests) |
| billingspool pkg | 5.67 s | 8.47 s | +2.8 s (19.x spool suites) |
| billingstore pkg | 19.41 s | 295.19 s | 19.1/19.2 integrated suites; reported, not 19.3-caused |
| archtest pkg | 75.06 s | 66.61 s | lower |
| qa pkg | 11.07 s | 4.88 s | lower |

Disk: no exhaustion; pre-existing `Za mało miejsca na dysku` Task 1.3 failure
mode did not recur (59.7 GB free at start; temp artifacts retained).

## 7. make quality-checks

`make quality-checks` on the real worktree exits 1: exactly one guardrail
fails, `quality-checks:archtest`, with the same 16 pre-existing budget tests
documented above (verified identical at pristine fe73f55b). The `lint`
guardrail reports repo-wide pre-existing findings (235 issues across untouched
files: modernize/paralleltest/revive/staticcheck/thelper); a scoped
`golangci-lint run` over the three 19.3-touched packages found exactly one
new instance in a 19.3 file (`forvar` copy in
`BenchmarkPhase193MultiMiBCanonicalAccounting`), which was removed
(Go 1.22+ per-iteration semantics; benchmark re-verified green). No other
19.3 file appears in any lint finding. `go vet` and `gofmt` are clean on all
five 19.3 files. `gofumpt` flags `attempt_session.go:1620` (call-formatting
nit) identically at fe73f55b; left untouched as out-of-scope cleanup.

No `LIP_ALLOW_LARGE_CHANGE` override and no `--no-verify` were used at any
point. The dirty Go-source count (13 files) stays far below the 100-file gate.

## 8. Affected #394 benchmark refresh (determination + record)

Issue #394 (high-concurrency audit) states accounting-path measurements signed
off before the #620 cutover must be refreshed afterward. The refreshed
scenarios, with environment (section 1) and repetitions, are:

| #394 scenario family | Refreshed benchmark | Repetitions | Result |
|---|---|---|---|
| OBSERVE | `BenchmarkExecutor_TrafficDisabled/Enabled` | count=5, -benchmem | section 4 |
| START/DELTA | `BenchmarkExecutorExecuteAndDrain32Deltas` | count=5, -benchmem | section 4 |
| BODY (canonical) | `BenchmarkLargePayloadBaseline_Preflight/DecodeResponses` 5 MiB | benchtime=1x count=3 | section 4 |
| BODY (fast path) | `BenchmarkLargePayloadStages_Capture/Proof/Assessment/WireEndToEnd` 5 MiB; `Shapes/CanonicalFallback + ReplayFailover` 1 MiB; `BenchmarkStaticDisposition_*` | 1x/3x count=2–3 | section 5 |
| COMPLETE | `BenchmarkRefinementAccountingBaseline` Disabled/Enabled; `BenchmarkPhase193TerminalSpoolAppend{Leg,Call,AndDeliver}` | 100x count=3–5 | sections 4–5 |
| COST envelope | anchor-vs-head test-cost package timings | 2 ratchet runs | section 6 |

Determination: no #394-owned evidence/fixture/budget file required
modification. `benchmark-scratch.md` remains a template (its task 2.2 baseline
matrix is pending under #394's own tasks); `seamview_bench_baseline_w0.*`
(request-path seam views) is untouched by accounting work; the durable-spool
per-append aggregate scan (#394 task 6.5 candidate) is measured in section 5
and deliberately not modified (belongs to #394's own tasks). No unrelated
budget was rewritten.

## 9. Focused verification commands (all exit 0)

- `go test -count=1 -run '^TestPhase193' ./internal/core/runtime` — PASS
  (disabled-record guard + zero-allocation drain).
- `go test -count=1 -run '^TestPhase193' ./pkg/lipsdk/metering` — PASS
  (at-limit retention + five fail-closed entry/dimension over-limit cases;
  supersedes and the independent 64 KiB serialization cap are certified by
  `TestPhase19R4ObservationSupersedesOverLimitFailsClosed` and
  `TestPhase19R4NormalizedSerializationCapFailsClosedIndependentOfEntryBounds`).
- `go test -count=1 ./internal/core/runtime/` — PASS (full package, no
  terminalization regression).
- `go test -count=1 ./pkg/lipsdk/metering/... ./internal/infra/billingspool/...
  ./internal/core/billing/... ./internal/core/metering/...` — PASS.
- `go test -count=1 ./internal/infra/billingstore/... ./internal/infra/billingbinding/...
  ./internal/infra/runtimebundle/...` — PASS (one load-sensitive
  `TestRefinement4StockObservationToEconomicSettlement` 25 s-timeout flake
  observed once under a parallel multi-package invocation; passes in
  isolation and on full-package rerun).
- `go vet` on the three touched packages — clean. `gofmt -l` on all five
  19.3 files — clean. Scoped `golangci-lint` — no findings in 19.3 files.

## 10. Requirements trace

- 4.6 (fast-path fallback when measurement evidence cannot be supplied within
  the bounded-memory contract): covered by the zero-allocation
  `StaticDisposition` decline gate, the `CanonicalFallback`/`ReplayFailover`
  shape measurements, and the pre-existing `wire_billing_composition` tests
  (unchanged); no new fallback behavior was needed.
- 18.3 (bounded family-level certification, no Cartesian matrix): each
  certification dimension has one named benchmark family; frontend/backend
  cross-products were not introduced.
- 18.5 (bounded stream-time work; no per-token DB writes/rating; disabled vs
  enabled overhead vs fresh baseline): disabled accounting performs zero
  callbacks/appends with allocations at/below the frozen baseline; enabled
  capture is a response-size-independent constant (terminal record only, ~+112
  allocs/op, exactly 1 observer/leg/call per execution); metadata caps fail
  closed; terminal durable writes are bounded and counted.
- 18.6 (quality/unit/parity/race gates + Windows test-cost without silent
  budget growth): focused suites pass; `make test-cost` (2 repeats) and `make
  quality-checks` outcomes are recorded with the pre-existing red-gate
  attribution (identical at fe73f55b; zero new failures); no budget file or
  threshold was relaxed.

## 11. Residual variance and handoff notes

- `ns/op` latency on short (`100x`) accounting repetitions is noisy on this
  host (127–220 µs disabled); allocation metrics (`B/op`, `allocs/op`) are
  deterministic and carry the certification.
- `TestSyntheticCatalog_1000ProfilesIsBoundedAndIndependent` and the single
  `TestRefinement4StockObservationToEconomicSettlement` timeout are
  load-sensitive flakes (pass in isolation and on rerun), unrelated to
  accounting changes.
- The branch-level archtest/qa budget red gate (16+2 tests, identical at
  fe73f55b) and the billingstore 295 s test-unit cost remain open for the
  Phase 20 release gate; they are not 19.3 regressions and were not relaxed.
- Temp artifacts (outside the repo): `p193-snapshot` clone + snapshot commit
  `c7c619ab`, `p193-pristine-fe73f55b`, `p193-testcost-run1/run2`,
  `p193-*.txt` raw benchmark outputs, `p193-benchstat-disabled-fix.txt`,
  memprofiles. Key numbers are embedded above so the evidence stands without
  them.

## 12. Final-tree refresh (supersedes c7c619ab for Task 19.3)

Parent Phase 19 review noted the retained Windows 19.3 snapshot `c7c619ab`
predates retail/provenance/claim/PG/traceability remediations. This section
records final-tree evidence at the exact current worktree. No threshold was
relaxed. No checklist/status/commit/rebase/merge/push/PR was performed.

### 12.1 Final snapshot identity

- Source HEAD: `fe73f55baa57801be40a62cffedb2fe70d6327f7`
  (`feat/b-leg-usage-economics`).
- Tracked diff hash: `git diff | git hash-object --stdin` =
  `1627b5737801102505d7e8a0ff949ad15cf8653d` (identical before and after;
  real worktree untouched, still 47 `git status` entries).
- Source status entries: 47 (21 tracked: 2 deletions + 19 modifications; 26
  untracked Phase 19 files). The two deletions
  (`connectors/codex/.../account_windows.go{,_test.go}`) plus the two
  untracked `account_window_snapshots.go{,_test.go}` collapse to renames on
  `git add -A`; the temp commit contains 45 files.
- Temp snapshot (throwaway clone only, never the real repo):
  `C:\Users\Mateusz\AppData\Local\Temp\opencode\p193-final-20260922`,
  cloned `--local` from the main repo, checked out detached at `fe73f55b`,
  then byte-copied all 47 entries. Verified: dest `git diff` hash
  `1627b573...` matches source; every untracked file SHA-256 matches;
  dest `git status` shows the same 47 entries before commit.
- Temp snapshot commit (temp clone only): `e6ef17d545fb5b1f3fdaf60d560d82ab4076de54`
  (`p193-final-tree snapshot of fe73f55b plus Phase19 worktree state`).
  Post-commit `git status` is clean (0 entries). Real worktree HEAD/diff
  re-verified unchanged after all measurements.
- Environment (same host/class as section 1): Windows
  `Microsoft Windows NT 10.0.19045.0`, AMD Ryzen 7 5800X 8-Core Processor,
  16 logical processors, Go `go1.26.6 windows/amd64`, benchmark suffix `-16`.
  C: `60,643,184,640` bytes free at bench start (vs 59.7 GB prior).
  Date: 2026-09-23. All suites ran sequentially on the snapshot; never in
  parallel with another heavy suite. `go.mod`/`go.sum` verified clean after
  all runs. No `LIP_ALLOW_LARGE_CHANGE`, no `--no-verify`, no allow env.
- Raw artifacts (outside the repo, retained):
  `p193-final-bench/01-12*.txt` (benchmarks),
  `p193-final-bench/testcost-run1-console.txt`,
  `p193-final-bench/testcost-run2-console.txt`,
  `p193-final-testcost-run1/` (anchor JSON + head 55,345,785 B stdout log),
  `p193-final-testcost-run2/` (anchor JSON + anchor 39,217,447 B log + head
  55,342,221 B log). Key numbers are embedded below.

### 12.2 Benchmark re-runs at the final tree (sequential, -benchmem)

Same counts/environment as sections 4–5. Retail rating has no dedicated
Task 1.3 or current-tree `Benchmark*Retail/*Rating/*Rater` target
(`git grep` in `internal/core/billing` returns none); selection impact is
covered functionally by the R1/R4 and lifecycle passes in section 12.4, with
accounting allocation/terminal metrics below proving no stream-time cost
regression.

| Benchmark (package, flags) | Task 1.3 baseline (d1847d5e) | Prior final (c7c619ab state) | Final tree (e6ef17d5 state) | Verdict |
|---|---|---|---|---|
| `BenchmarkExecutorExecuteAndDrain32Deltas` `internal/core/runtime` `-count=5` | median 365,685 ns/op; ~643,181 B/op; 2,409 allocs/op | 406766/393660/400613/401071/396521 ns/op; 681,709–681,781 B/op; 2,424 allocs/op | 399106/384329/388125/388826/391641 ns/op; 681,687–681,776 B/op; 2,424 allocs/op | Same +15 allocs (+0.6%), +38.5 KiB vs baseline; ~3% faster than prior final. No regression. |
| `BenchmarkExecutor_TrafficDisabled` `-count=5` | median 138,925 ns/op; ~161,740 B/op; 873 allocs/op | 168200/153997/153978/151318/150000 ns/op; 172,056–172,167 B/op; 888 allocs/op | 161199/149485/147193/148440/149517 ns/op; 172,031–172,172 B/op; 888 allocs/op | Same +15 allocs, +10.3 KiB vs baseline. No regression. |
| `BenchmarkExecutor_TrafficEnabled` `-count=5` | median 166,933 ns/op; ~191,694 B/op; 943 allocs/op | 180698/181560/182895/183016/180371 ns/op; 204,577–204,607 B/op; 958 allocs/op | 187650/179816/188161/176911/175442 ns/op; 204,578–204,663 B/op; 958/958/958/958/959 allocs/op | Same +15–16 allocs, +12.9 KiB vs baseline; 1-alloc first-run variance is noise. No regression. |
| `BenchmarkRefinementAccountingBaseline/Disabled` `-benchtime=100x -count=5` | 150,228/149,343/149,312 B/op; 785/773/773 allocs/op; 0 terminals | 153572/152492/152734/152648/152449 B/op; 778/765/766/766/765 allocs/op; 0 terminals | 153412/152606/152459/152555/152614 B/op; 778/766/765/766/766 allocs/op; 0 terminals | Allocs at/below baseline; residual +2.2–3.3 KiB non-accounting as before. No regression. |
| `BenchmarkRefinementAccountingBaseline/Enabled` same | 158,189/158,094/158,132 B/op; 855 allocs/op; 1.0 terminals | 170044/169958/170004/170043/170011 B/op; 878 allocs/op; 1.0 terminals | 170021/169993/169975/169958/169994 B/op; 878 allocs/op; 1.0 terminals | Same +23 allocs (+2.7%), +11.8 KiB; exactly 1 observer + 1 leg + 1 call. No regression. |
| `BenchmarkLargePayloadBaseline_Preflight/5MiB` `-benchtime=1x -count=3` | 23.8–25.4 ms/op; 22,018,912–22,018,928 B/op; 45–46 allocs/op | 28.5/25.9/26.8 ms/op; 22,018,912/22,018,912/22,019,248 B/op; 45/45/48 allocs/op | 27.96/26.37/27.42 ms/op; 22,019,024/22,018,928/22,018,912 B/op; 46/46/45 allocs/op | Allocation-identical; latency single-run noise. No regression. |
| `BenchmarkLargePayloadBaseline_DecodeResponses/5MiB` same | 52.9/47.3/47.4 ms/op; 15,731,408–15,733,592 B/op; 33–37 allocs/op | 57.1/54.2/54.0 ms/op; 15,731,088/15,733,592/15,733,608 B/op; 31/37/37 allocs/op | 60.41/56.84/57.12 ms/op; 15,731,408/15,733,720/15,733,608 B/op; 36/38/37 allocs/op | B/op identical; allocs 36–38 vs 31–37 single-run noise; ns +5% vs prior final within single-run noise. No alloc regression. |
| `BenchmarkPhase193MultiMiBCanonicalAccounting` `-benchtime=20x -count=3` | — (new 19.3 family) | 1 MiB dis 3.17/2.72/4.18 ms, ~14.4 MB, 1691/1631/1628 allocs, 0 terminals; 1 MiB en 2.88/2.97/4.97 ms, ~14.4 MB, 1744/1744/1741 allocs, 1.0 terminals; 5 MiB dis 16.07/12.24/17.28 ms, ~74.26 MB, 5167 allocs; 5 MiB en 16.14/14.61/15.44 ms, ~73.75–74.28 MB, 5283/5283/5285 allocs | 1 MiB dis 2.95/2.72/2.57 ms, 14,409,892/14,404,165/14,298,408 B, 1694/1628/1627 allocs; 1 MiB en 2.40/4.18/4.55 ms, 14,527,570/14,422,666/14,316,342 B, 1744/1743/1742 allocs; 5 MiB dis 15.42/14.27/18.39 ms, 74,259,352/74,259,601/74,259,926 B, 5166/5167/5168 allocs; 5 MiB en 13.12/16.17/15.69 ms, 74,278,649/74,278,451/74,277,969 B, 5284/5284/5282 allocs | Enabled delta still ~+113–118 allocs/op and ~0 B/op at both sizes: response-size-independent constant capture. No regression. |
| Fast-path 5 MiB `BenchmarkLargePayloadStages_Capture/Proof/Assessment/WireEndToEnd/5MiB` `-benchtime=1x -count=3` | — | Capture 15.84/16.62/23.70 ms, 144,416 B, 48 allocs; Proof 98.90/98.80/99.57 ms, ~203 KiB, 304–310 allocs; Assessment 3.5/3.7/3.8 µs, 896 B, 1 alloc; WireEndToEnd 112.83/114.20/113.66 ms, ~385 KiB, 397–403 allocs | Capture 17.63/19.23/16.33 ms, 144,984/144,416/144,416 B, 65/48/48 allocs; Proof 103.54/101.87/103.87 ms, 244,256/203,160/203,208 B, 909/309/310 allocs; Assessment 22.2/3.3/3.6 µs, 896 B, 1 alloc; WireEndToEnd 116.24/114.08/115.24 ms, 389,904/385,448/385,496 B, 411/402/403 allocs | Steady-state (counts 2–3) identical to prior final on B/op and allocs; first-count warmup outliers (Capture +17 allocs/+568 B, Proof 909 allocs, Assessment 22 µs, Wire +8 allocs/+4 KiB) are cold-start noise, not a bounded-contract breach. The bounded spill-memory contract (identical B/op 32 KiB–5 MiB for Capture) still holds on steady-state counts. No material regression. |
| `BenchmarkLargePayloadStages_Shapes/(CanonicalFallback|ReplayFailover)` `-benchtime=3x -count=2` (1 MiB target) | — | CanonicalFallback 27.999/29.992 ms, ~10.2 MB, 875/333 allocs; ReplayFailover 26.168/27.567 ms, ~379 KiB, 395/392 allocs | CanonicalFallback 30.594/29.891 ms, 10,897,746/12,264,005 B, 883/345 allocs; ReplayFailover 24.820/26.824 ms, 379,269/379,653 B, 396/401 allocs | Same warmup-split pattern (first-count high allocs) as prior; ReplayFailover B/op within 400 B and allocs within 9. CanonicalFallback B/op 10.9/12.3 MB vs ~10.2 MB prior is heap noise on the fallback canonicalization path (still size-proportional fallback, not the bounded fast path). No fast-path contract regression. |
| `BenchmarkStaticDisposition_*` `internal/core/largebody` `-count=3` | — | 3.77–3.80 ns/op feature-disabled; 10.2–11.7 ns/op ineligible; 0 B/op, 0 allocs/op everywhere | 3.749/3.731/3.750 ns/op feature-disabled; LocalTurn 10.22–10.25, SecretGuard 10.19–10.28, CanonicalTraffic 11.63–11.64, MissingTwoPhase 11.59–11.63, BelowThreshold 10.93–11.17, PotentiallyEligible 10.84–10.98 ns/op; 0 B/op, 0 allocs/op everywhere | Exact match. Req 4.6 decline gate still costs nothing. |
| `BenchmarkPhase193BoundedMetadataObservation` `-count=3` | — | Validate 653,913/669,971/658,957 ns, ~216 KiB, 1,684 allocs; CanonicalJSON 2,372,629/2,187,894/2,157,715 ns, ~912 KiB, 17,000/17,000/16,999 allocs; Fingerprint 2,149,412/2,143,853/2,123,562 ns, ~916 KiB, 17,001 allocs | Validate 653,948/661,618/659,643 ns, 216,185/216,596/216,734 B, 1,684 allocs; CanonicalJSON 2,346,097/2,372,391/2,122,513 ns, 913,441/910,409/910,533 B, 16,999 allocs; Fingerprint 2,138,189/2,135,166/2,128,948 ns, 912,594/913,222/914,869 B, 17,001 allocs | Exact match within <0.5% on B/op; allocs identical (CanonicalJSON 16,999 all three vs 17,000/17,000/16,999 is 1-alloc noise). Caps still fail closed per section 5 (seven bound families). No regression. |
| Terminal spool `BenchmarkPhase193TerminalSpoolAppend{Leg,Call,AndDeliver}` `-benchtime=100x -count=3` | — | AppendLeg 2.229/2.775/3.358 ms, 20,461/19,698/19,308 B, 227/219/218 allocs, pending 100; AppendCall 3.215/2.863/3.014 ms, 16,730/16,762/16,916 B, 234/233/234 allocs; AppendAndDeliver 7.444/7.108/7.680 ms, 37,711/37,565/37,501 B, 440/439/439 allocs, pending 0 | AppendLeg 2.288/2.291/2.285 ms, 20,748/19,409/19,706 B, 227/218/219 allocs, pending 100; AppendCall 2.240/2.288/2.200 ms, 16,980/16,670/16,627 B, 235/233/232 allocs; AppendAndDeliver 6.529/6.610/6.185 ms, 37,104/37,585/37,766 B, 439/439/440 allocs, pending 0 | B/op within ~1 KB and alloc sets permuted by 1; latency stable-to-faster than prior. Per-append seal + aggregate scan + insert + commit behavior unchanged (#394 task 6.5 candidate still measured, not modified). No regression. |

### 12.3 Windows make test-cost evidence (two repeats on e6ef17d5)

Method: clean snapshot `e6ef17d5` (section 12.1) satisfies the ratchet’s
clean-checkout requirement. Commands per run (from the snapshot root):

```powershell
$env:TEST_COST_BASE_SHA="fe73f55b"
$env:TEST_COST_OUTPUT_ROOT="C:\Users\Mateusz\AppData\Local\Temp\opencode\p193-final-testcost-run1"
$env:TEST_COST_PARALLEL="4"
make test-cost
# second run identical except OUTPUT_ROOT=...p193-final-testcost-run2
```

Both runs exit 1 at the head `test-unit` measurement (wrapper exit 3);
the head tree cannot complete the ratchet. No `LIP_ALLOW_LARGE_CHANGE`,
no `--no-verify`.

- Run 1 wall: 613 s (`make test-cost` end-to-end). Anchor `test-unit`
  measurement: wall 101.5 s, CPU 1,253.9 s, processes 3,534, I/O ops
  9,244,592, packages 330 (anchor JSON `anchor-test-unit.json`). Head
  package timing: `internal/infra/billingstore` 312.986 s (vs anchor
  19.41 s class; vs prior-final 295.19 s). Head stdout log 55,345,785 B.
- Run 2 wall: 525 s. Anchor: wall 70.5 s, CPU 702.8 s, processes 2,063,
  packages 330. Head: `billingstore` 310.112 s; head stdout log
  55,342,221 B; anchor stdout log 39,217,447 B (passes).
- Anchor variance (same `6dbb8318` commit, same `-Parallel 4`): wall
  83.8 s (old) vs 101.5 s (run 1) vs 70.5 s (run 2); CPU 1,024.8 s vs
  1,253.9 s vs 702.8 s; processes 2,454 vs 3,534 vs 2,063. This is host
  load noise on the anchor_defaults; the anchor commit is unchanged and
  the policy `anchor_ref` protection still passes unchanged against
  `-BaseSHA fe73f55b` (preflight OK). No budget was relaxed.
- Failure sets are **not** identical to the prior `c7c619ab` set. Parsed
  from `test-unit-stdout.log` (`"Action":"fail"` keys):

  Run 1 (30 keys): the same 16 archtest tests (plus subtest keys) and 2 qa
  tests as sections 6–7, **plus 2 new deterministic `internal/testkit`
  failures and no `providerprofiles` failure**:
  `TestCleanupStatementsJournalUsesStoreScopedFilters`
  (`journal cleanup statements=5 want 2`) and
  `TestAssertNoSearchPathInDualPlanePostgresSources`
  (`phase19_r3_presence_boolean_upgrade_postgres_test.go must not reference
  search_path`). `TestSyntheticCatalog_1000ProfilesIsBoundedAndIndependent`
  passed under full-suite load in both final runs.

  Run 2 (32 keys): same as run 1 **plus one load-sensitive
  `internal/infra/runtimebundle`
  `TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding`
  package+test failure**. It passes in isolation (`go test -count=1 -run
  '^TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding$'
  ./internal/infra/runtimebundle` → PASS, 2.16 s), so it is recorded as a
  parallel-load flake like the prior
  `TestRefinement4StockObservationToEconomicSettlement` flake, not a
  regression.

- Attribution: the 16 archtest + 2 qa failures remain identical to pristine
  `fe73f55b` (branch-vs-anchor budget drift, Phase 20 material, not 19.3
  scope). The 2 `internal/testkit` failures are **new task-owned
  regressions from the post-`c7c619ab` remediation**:
  `internal/testkit/postgres_harness.go` added three journal cleanup
  statements (outbox, components, supersessions) while
  `postgres_harness_test.go:92` still requires exactly 2, and the new
  `phase19_r3_presence_boolean_upgrade_postgres_test.go:41,57,61` uses
  `search_path` instead of the required unique `store_id` + admin cleanup.
  Both fail deterministically in isolation on the snapshot. This breaks the
  prior “zero new test failures” claim and blocks final 19.3 sign-off until
  the harness guard and the new PG test are reconciled by the owning tasks.
  No budget file or threshold was relaxed to hide this.
- Quantitative cost (run 1 anchor JSON vs head log; head comparison aborts
  while red): runtime pkg ~9.7 s class, lipsdk/metering ~0.47 s class,
  billingspool ~8.5 s class, billingstore ~311–313 s (19.1/19.2 file-backed
  suites plus remediation suites; reported, not 19.3-caused beyond the two
  guard failures above), archtest ~66 s class, qa ~5 s class. Disk: no
  exhaustion; Task 1.3 `Za mało miejsca na dysku` mode did not recur.

### 12.4 Focused verification on the snapshot (all exit 0 except the two guards)

- `go test -count=1 -run '^TestPhase193' ./internal/core/runtime` — PASS.
- `go test -count=1 -run '^TestPhase193' ./pkg/lipsdk/metering` — PASS.
- `go test -count=1 -run 'TestPhase19R1|TestPhase19R4' ./internal/core/billing` — PASS (7 tests: provenance ×2, retail selection ×4, tolerance ×1).
- `go test -count=1 -run 'TestPhase19R4Chunk|TestPhase19R1Provenance|TestPhase19R3|TestPhase19R4' ./internal/core/metering ./internal/infra/metering/journalstore ./pkg/lipsdk/metering` — PASS (3 packages).
- `go test -count=1 -run 'TestPhase19R2|TestPhase19R4' ./internal/infra/billingstore` — PASS (5.376 s; claim-prefix + traceability).
- `go test -count=1 -run '^TestPhase191LifecycleFullEconomicsCertification$' ./internal/infra/billingstore` — PASS (0.826 s; retail selection impact shows no functional regression).
- `go test -count=1 -run 'TestCleanupStatementsJournalUsesStoreScopedFilters|TestAssertNoSearchPathInDualPlanePostgresSources' ./internal/testkit` — FAIL (both, deterministically; see section 12.3).
- `go vet` on runtime/metering/billingspool/billing/testkit — clean.
  `gofmt -l` on sampled new/touched files — clean. Full `make
  quality-checks` was not rerun as a separate gate here because its
  `archtest` guardrail outcome is already captured identically by both
  `test-cost` head measurements (same 16 failures); the cheap vet/fmt
  attribution above is clean and no lint override was used.

### 12.5 Verdict for the final tree

Benchmarks: no material task-owned performance regression at `e6ef17d5`
vs `c7c619ab` or the Task 1 baseline; allocation/terminal contracts hold
(zero disabled callbacks/appends, bounded constant enabled capture,
metadata caps fail closed, spool writes bounded and counted). Variance
notes above are honest first-count warmup/single-run noise, not relaxed
budgets.

Test-cost: **BLOCKED**. Two new deterministic task-owned guard failures
in `internal/testkit` appear at the final tree that were absent from the
`c7c619ab` evidence. They must be reconciled by the owning remediation
tasks (either the harness expectation or the new PG test/cleanup design)
before Phase 19.3 can claim “zero new failures.” The remaining 16+2
archtest/qa red gate stays Phase 20 material as before.

Temp artifacts for this refresh (outside the repo): `p193-final-20260922`
clone + commit `e6ef17d5`, `p193-final-bench/`, `p193-final-testcost-run1/`,
`p193-final-testcost-run2/`. To be removed after this evidence is recorded;
no user worktrees/data were touched.

### 12.6 Corrected-tree re-certification (supersedes the 12.5 BLOCKED verdict)

The two deterministic `internal/testkit` guard failures from section 12.3
were fixed semantically in the real worktree (no other files touched) and
re-certified on a fresh snapshot with one authoritative `make test-cost` run.

Semantic fixes (TDD: RED captured in isolation before each fix):

1. `TestCleanupStatementsJournalUsesStoreScopedFilters`
   (`internal/testkit/postgres_harness_test.go`): the guard now enumerates
   the full ordered 5-statement journal cleanup contract — outbox,
   components, supersessions, filters, facts — asserting each statement is
   exactly `DELETE FROM <table> WHERE store_id = ?` with args `[storeID]`
   and that `metering_facts` is last (children before parents; components
   carries FK `(store_id, observation_row_id)` → `facts(store_id, id)`).
   No brittle bare count remains and no predicate was weakened.
2. `TestAssertNoSearchPathInDualPlanePostgresSources`
   (`internal/infra/metering/journalstore/phase19_r3_presence_boolean_upgrade_postgres_test.go`):
   the isolated-schema helper (`CREATE SCHEMA` + DSN `search_path` param /
   `SET search_path`) is removed. The test now follows the shared-schema
   repository pattern: `ensureDirectJournalSchema`, unique
   `testkit.UniquePostgresStoreID("r3-upgrade-pg")` with
   `testkit.CleanupPostgresStoreByID` admin cleanup, and a store-owned
   `bunDB` handle. The file contains zero `search_path` occurrences
   (code or comments). DDL under test runs sequentially in-package and
   `Migrate` restores the shared schema before exit.

Targeted verification on the real worktree (all exit 0; PG-gated tests
with `LIP_REQUIRE_POSTGRES=1`):

- Both guards in isolation — PASS.
- `go test -count=1 ./internal/testkit/` — PASS (1.15 s).
- `go test -count=1 -run 'TestPhase19R3|TestPhase19R1'
  ./internal/infra/metering/journalstore` — PASS (SQLite R3 + provenance).
- `go test -tags=integration -count=1 -run
  '^TestPhase19R3_Postgres_PresenceUpgradeFromInt4$'
  ./internal/infra/metering/journalstore` — PASS (6.93 s, shared schema).
- `go run ./internal/testkit/dbparity/cmd postgres-direct -component
  metering-journal` — exit 0 (2.77 s).
- `go test -count=1 ./internal/infra/metering/journalstore/` — PASS.
- Pooled `TestPhase34_PostgresPooled|TestPostgresPooled_` — SKIP (no
  pooler attestation in this shell; not falsely attested).
- `go vet` on testkit + journalstore — clean. `gofmt -l` on both touched
  files — clean. `git diff --check` — clean.

Corrected snapshot identity:

- Source HEAD still `fe73f55b`; corrected tracked diff hash
  `c717f63a2ae44db31b045f2ca9e9dabbad8829c4` (was `1627b573...`; delta is
  exactly the two semantic fixes above), 48 `git status` entries.
- Temp snapshot (throwaway clone only): `p193-final-fix-20260923`,
  detached at `fe73f55b`, all 48 entries byte-copied (diff hash match +
  SHA-256 of untracked files verified), commit
  `a335dff40905f8bcefec45a208315a341bc7f8d6`, clean post-commit.
  (During staging the real worktree HEAD was briefly detached at the same
  commit by a bare `git checkout` and immediately re-attached to
  `feat/b-leg-usage-economics`; working-tree content never changed —
  re-verified 48 entries and identical diff hash `c717f63a...`.)
- Command (from the snapshot root):
  `TEST_COST_BASE_SHA=fe73f55b TEST_COST_PARALLEL=4 make test-cost`
  → `p193-final-fix-testcost-run1`, wall 559 s, exit 1 at head `test-unit`
  (wrapper exit 3). Anchor: wall 67.0 s, CPU 737.8 s, processes 2,113,
  packages 330. Head: `billingstore` 284.812 s. No allow env,
  no `--no-verify`.

Failure-set attribution (parsed `"Action":"fail"` keys, 29 keys):

- Same 16 archtest tests (plus subtest keys) and 2 qa tests as sections
  6–7/12.3 — identical to pristine `fe73f55b`, Phase 20 material.
- **Neither deterministic `internal/testkit` failure remains**
  (`internal/testkit` package verdict: pass, 9.66 s in-head).
- One load-sensitive `internal/infra/runtimebundle`
  `TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding`
  package+test failure, also present in the pre-fix run 2. Passes in
  isolation on the corrected tree (`go test -count=1 -run ...` → PASS,
  2.04 s): parallel-load flake, not a regression.
- `TestSyntheticCatalog_1000ProfilesIsBoundedAndIndependent` passes under
  load. Disk: no exhaustion.

Verdict for the corrected final tree: **no task-owned deterministic
failure remains**. The red gate is exactly the pre-existing 16+2
branch-vs-anchor budget drift plus one isolated-verified load flake.

Temp artifacts for this correction (outside the repo):
`p193-final-fix-20260923` clone + commit `a335dff4`,
`p193-final-fix-testcost-run1/`,
`p193-final-fix-bench-testcost-console.txt`. Only the throwaway clone is
to be removed after recording; no user worktrees/data were touched.
