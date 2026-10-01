# Development gates and fixture performance

This follow-up implements five improvements against `bb3aa523`. Runtime code,
remote CI selection and cache policy are unchanged.

## Changes

1. `make qa` delegates lint to its comprehensive `lint` prerequisite once.
   Preliminary quality checks retain formatting, generated output, dependency
   and repository-policy checks. Standalone quality checks still run lint.
2. Changed/staged local lint selects changed packages and transitive production,
   internal-test and external-test consumers within each affected module. Shared
   public contracts, testkit, core configuration, dependency files and lint-policy
   changes force complete module lint. Discovery errors fail the command.
   Unscoped lint and CI retain comprehensive coverage. A clean changed-mode
   invocation retains the root-module gate; documentation-only work needs no lint.
3. Billing fixtures restore a SQLite database image created by real migrations,
   rather than parsing 131 CREATE statements per fixture. Each fixture retains
   its private database, shared-memory pool and connection pragmas. Existing
   databases still take the real restart/upgrade path. An independent migration
   comparison verifies schema equivalence, and a two-connection test checks
   pool visibility and foreign-key enforcement.
4. Real host fixtures reuse immutable billing/metering schema bytes in private
   files. Host construction, journal posting, close/reopen and ownership assertions
   remain intact. The two R5 relay fixtures use WAL/NORMAL, consistent with the
   existing fixture policy; they prove process reopen, not power-loss durability.
5. Package/test concurrency was measured separately. Native defaults remain the
   fastest tested setting. `LIP_TEST_PACKAGES` optionally controls package workers;
   `LIP_TEST_PARALLEL` continues to control parallel tests within each binary.

## Windows measurements

Go 1.26.6, Windows/amd64, Ryzen 7 5800X, 16 logical processors. Commands ran
sequentially with stable warm compiler/module caches. Fresh test execution used
`-count=1`; no cache was cleared. These single full-suite observations describe
this machine and do not predict GitHub runner performance.

The matched original uses a Go source overlay in the same worktree. New test
files are empty package stubs in that overlay, so the baseline retains the old
fixture implementations without including the new regression tests.

| Fresh root suite, `-p=16 -parallel=16` | Original | Candidate |
| --- | ---: | ---: |
| Command wall time, including compilation/startup | 84.1 s | 80.1 s |
| First-to-last test JSON event | 79.2 s | 75.2 s |
| Billingstore package | 49.4 s | 43.9 s |
| Runtimebundle package | 54.5 s | 50.7 s |

All 360 package results passed on both sides. The observed wall reduction is
4.8%. Package times overlap and include contention; unchanged packages also vary.
The sum of package durations increased from 447.4 to 466.1 seconds, so this pair
is not evidence of a proportional reduction in total CPU work.

| SQLite schema fixture benchmark, 30 iterations per repetition | Original | Candidate |
| --- | ---: | ---: |
| Warm repetition 2 | 7.003 ms | 0.559 ms |
| Warm repetition 3 | 6.941 ms | 0.543 ms |

Warm schema materialization is roughly 12.5 times faster. The first repetition
includes one-time template construction and is excluded from that comparison.
The focused R5C2b/R5C3a relay tests fell from 1.37/1.47 seconds to 0.08/0.14
seconds. The four-test host group remained around 4.2 seconds because its longer
R82 scenarios still set the critical path.

| Candidate root suite concurrency | Command wall time | Result |
| --- | ---: | --- |
| 16 package workers / 16 parallel tests | 72.4 s | Passed |
| 8 / 8 | 89.4 s | Passed |
| 4 / 8 | 107.1 s | Passed |
| 16 / 8 | 91.8 s | Passed |

These are separate sequential observations, not averaged benchmark results.
Lowering the default would have regressed this host. Leave the optional package
budget unset unless measurements on the target machine justify an override.

## Local lint scope

The selector regression fixtures use real Git repositories and Go import graphs.
They cover transitive/test-only consumers, independent modules, untracked work,
shared-contract fallback, documentation-only work, staged isolation, and discovery
failure. An alternate Git index measures a leaf `tools/devcheck` edit without
changing the actual staged work. Both shell adapters must select that package.

The QA orchestration check observes two lint invocations on the original Makefile
and one comprehensive invocation plus preliminary policy checks on the candidate.
The comprehensive lint command and analyzer set are retained.

| Native PowerShell lint command | Wall time |
| --- | ---: |
| Whole root, first call after fixture/tool edits | 109.7 s |
| Identical whole-root warm repeat | 5.7 s |
| Selected leaf, actual comment edit | 4.3 s |
| Whole root with the same leaf edit, after scoped lint | 5.7 s |
| Bash selected leaf adapter, warm | 4.0 s |

The 109.7-second first call and 4.3-second leaf call are different invalidation
scopes and must not be presented as a 25-fold speedup. With the current warm
analyzer cache, the measured leaf improvement is modest. The selector avoids
unrelated package analysis when those packages lack cached results; its benefit
depends on dependency fan-out and cache state. Broad test-consumer relationships
can legitimately expand an internal-package edit to most of the module.

## Verification

- Fresh Windows default root suites passed for all four concurrency settings and
  both matched source variants.
- `make qa` passed with `LIP_VERIFY_MODULE_CACHE=1`: generated output, formatting,
  module verification, tagged root tests/architecture, all 42 mandatory lint
  modules, vulnerability scanning and static release/compliance checks. The log
  contains exactly one comprehensive lint invocation.
- Focused QA/selector tests passed, both lint adapters executed the selected
  package, and invalid package budgets failed on Windows and Bash.
- Linux race checks passed for the schema clone/capture, private host fixture,
  R5 restart and selector regression tests in the three affected packages.
- `lipstd` build and `--help` smoke passed.

Configured external PostgreSQL services and the complete native multi-OS release
matrix were not run locally. These results do not certify remote cache behavior
or the paused historical Windows cost comparison.
