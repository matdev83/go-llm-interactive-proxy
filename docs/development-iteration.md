# Development iteration performance

The development loop must stay scoped and reuse Go's native caches. Comprehensive
certification remains a separate, deliberate delivery step. This policy covers
tests, builds, linting, cache lifecycle, and the automated cost watchdog.

## Use during development

### Delivery command lookup

| Scope | Existing command |
| --- | --- |
| Focused regression | `go test -run TestName ./path/to/pkg` |
| One default root-module pass | `make test-quick` (one `go test ./...`); default unit target `make test-unit` |
| Quality gate | `make quality-checks` |
| Full default delivery | `make test` |
| SQLite/PostgreSQL parity | `make test-db-parity`; dialect lanes `make test-db-parity-sqlite` / `make test-db-parity-postgres-direct` |
| Cross-protocol/backend matrix | `make parity-checks` |
| Wide/release-grade change | `make qa` |
| Explicit Windows cost ratchet | `make test-cost` (opt-in, not part of `make test`) |
| Parser/decoder fuzz | `make test-fuzz` or targeted `go test -fuzz=FuzzName$ -fuzztime=30s -run=^$ ./path` |

Run focused checks during edits and applicable comprehensive gates after a
coherent change. Include affected consumers when contracts change. A fresh final
regression invocation may use `-count=1`; ordinary iterations preserve native
caches and avoid forced rebuilds. Race execution remains remote-only under root
`AGENTS.md`; this table grants no alternate local race path.

### Contract feedback and delivery slicing

Before accepting a task that changes shared runtime/state, composition, persistence
or public Go contracts, run `make dev-contract-check`. It selects **existing**
architecture checks for those surfaces and lints only direct changed packages in
their owning modules. Ordinary feature bodies and test-only edits get direct lint,
not reverse-dependency or full-suite expansion. The selector lives in
`tools/devcheck/internal/testscope/contracts.go`; update it when an existing
contract is renamed or its surface moves. A selected test that no longer exists
fails the command instead of silently passing an empty `-run` filter.

Use `DEV_PLAN=1` to inspect without executing, `DEV_BASE=<ref>` for a stacked base,
and `DEV_FRESH=1` only for deliberate fresh evidence. An invalid comparison base is
an error. Selection covers branch, staged, unstaged and untracked Go changes;
removed packages retain applicable contracts but cannot be linted. This is local
feedback, not SQLite/PostgreSQL parity, tagged/platform certification, or a
replacement for comprehensive delivery and CI. Hooks and CI keep their existing
scope. Module metadata and non-Go inputs still require their applicable gates.

Before coding, identify intended slice bases and immediate consumers. Run
`make dev-delivery-plan DEV_BASE=<base> DEV_CONSUMER='<consumer>'` and refresh it
after each accepted task and before preparing a PR. The JSON report includes
merge-base/head identity, path counts, added/deleted production and test Go lines,
and existing change-surface categories for dependency review. An empty initial
diff is not a forecast: estimate the proposed slice separately while planning.

The default report includes final tracked working-tree contents plus untracked
files. Use `DEV_HEAD=<commit>` to report a committed slice without local successor
edits; repeat with each slice's actual intended base, not always `origin/main`.
Rename targets count once and previous paths remain in dependency review.
Non-test/test line classification uses the `_test.go` suffix; binary paths have
no line count. JSON goes to stdout, diagnostics to stderr. The same report is
available through `go run ./tools/changesize --report --base <base>`.

The existing 100-Go-file hard limit is reported independently of approximate
delivery signals (~40 files, ~1500 added non-test Go lines, ~2x test additions).
Report generation does not enforce those advisory numbers, authorize overrides,
prove dependencies, or certify an independent build. Review the signals under
`.kiro/steering/delivery.md`, verify each slice independently, and require an
immediate real consumer for a substrate. Missing consumer declarations remain
explicit review work, not guessed dependencies.

### Config-source tests require an ext4 TMPDIR

The config-source integrity tests assert atomic rename and inode-reuse behaviour.
tmpfs does not provide those semantics, so a tmpfs `TMPDIR` makes them fail with
`source-integrity-failed` or `source_non_atomic_update` across several unrelated
packages. Those failures are environmental: they appear whether or not the staged
change touches config sources, and they read convincingly like a regression.

This host mounts `/tmp` as tmpfs, which is the default when `TMPDIR` is unset.
tmpfs is also RAM charged to the container's memory limit, and interrupted Go
runs leave multi-GB `go-build*` work directories behind in it.

On `agent-dev` this is handled automatically: the installed `go` guard and
`scripts/dev-cpu-defaults.sh` (sourced by the hooks and quality scripts) move a
tmpfs or unset `TMPDIR` to `$HOME/.cache/lip-tmp` on ext4 (override with
`LIP_DEV_TMPDIR`), and at most hourly prune `go-build*` directories older than six
hours there. Re-install the guard after it changes:
`install -m 755 scripts/go-dev-guard.sh "$HOME/.local/bin/go"`. Other hosts with a
tmpfs `/tmp` still need `export TMPDIR=/path/on/ext4`.

`scripts/require-ext4-tmpdir.sh` is the single source of truth for the check. The
pre-commit quality gate runs it before the expensive suites, so a wrong `TMPDIR`
is reported once with the remedy instead of surfacing later as scattered test
failures. `scripts/configsource-certify.sh` delegates to the same script rather
than repeating the logic.

### Commit runs an affected-scope gate; CI runs the full suite

`scripts/hooks/pre-commit` builds, vets, tests and lints only the packages whose
files are staged. Timings depend on the package, cache state and host load.
It does not test their consumers: nearly every package reaches the
repository-wide suites through reverse dependencies, which made a
reverse-dependency scope cost as much as the full suite. Config-source
certification runs only when a config-source path is staged. CI's `Go suite (Linux)` and `Lint (Linux)` jobs run the complete
tagged root suite and lint on every PR, and the required `Repo hygiene` check
fails when either does. The module-wide owner-callback escape gate
(`TestRuntimebundle_NoCompleteOwnerCallbackEscapes`, ~2 min cold, seconds with a warm
cache) runs in its own parallel `Owner callback gate (Linux)` job with its own `ci-owner-gate` cache
lane (it type-checks the module for Linux and Windows, so it cannot share the suite's Linux-only lane)
instead of inside the suite, and the suite itself runs
as three shards (`heavy`: the few slow packages; `rest` and `rest-2`: everything else, split by a stable hash;
`scripts/ci-suite-shard.sh` assigns every package to exactly one) so no single runner compiles and runs
every package. `LIP_PRECOMMIT_FULL=1 git commit` (or
`make precommit-full`) runs the old complete local gate. `scripts/hooks/pre-push`
re-checks release cleanliness and change size.

Fast staged commits to runtimebundle also defer that whole-module owner gate to
CI. The owner-callback fixture tests still run locally. Full pre-commit mode and
ordinary scoped test commands retain the complete gate; the hook prints the
exclusion explicitly.

The fast hook uses `quality-checks.sh --staged`: formatting checks only existing
staged Go files, and tidy checks affected modules with `go mod tidy -diff`,
without rewriting metadata. Build and full vet run on direct packages in their
own modules; test-only packages retain vet and tests. Metadata changes check the
whole affected module, and removing a module with surviving sources expands
checks to its parent module. Root metadata still triggers all-module tidy.
Feature-plane and protobuf generation run when their inputs or checking scripts
change. The cheap goroutine and regex guardrails remain repository-wide.
Standalone quality checks and full pre-commit mode retain their previous scope.

Measured against `eef98f56` on Linux `agent-dev`, Go 1.26.6, golangci-lint
2.14.0, a two-CPU quota, stable shared caches, and the normal host Go guard:

| Staged change | Baseline hook | Scoped hook |
| --- | ---: | ---: |
| `connectors/localstub/service_test.go` comment | 44.13 / 44.00 s | 5.63 / 5.20 s |
| `internal/core/jsonpresence/null.go` comment | 21.68 / 19.71 s | 7.11 / 7.34 s |
| `connectors/localstub/go.mod` comment | 49.84 s | 4.57 / 4.35 s |

These are complete `bash scripts/hooks/pre-commit` wall times with identical
staged edits before and after; every reported run passed. Source cases ran twice
per version, sequentially, with normal build/test/lint cache reuse. No cache was
cleared and no forced rebuild or fresh-test flag was added. The initial connector
baseline took 564.75 seconds while warming a new worktree and competing with
indexing; it is excluded from the warm comparison. These observations do not
predict broad-package, cold-cache, or Windows timings. Raw timestamped hook logs
and elapsed-time JSON remain in `~/.cache/local-commit-speed/`.

`bash scripts/test-quality-checks.sh` checks staged isolation, module/package
selection, renames/deletions, tool failures, generator inputs and standalone
compatibility using fake tools, plus offline Go build semantics. PR preflight
runs it so the fast path cannot silently return to a whole-root fallback.

A commit touching many packages still takes minutes, so run commits as a background command
with no tool timeout: a harness timeout that kills the gate mid-run leaves the
commit unapplied and the index still staged.

### Architecture package loading

Native runtimebundle/runtimehost architecture checks analyze the CGO variant
recorded in the test binary's build information. This lets them reuse exports
compiled by the outer `go test`, rather than forcing a second CGO-disabled build.
The canonical Linux/Windows matrix in the `precommit` suite still uses CGO off.

Overlay mutation checks ask the Go command to select files and build dependency
exports with the overlay applied, then parse and type-check the selected package
against those exports. This avoids `go/packages` reparsing every dependency when
an overlay is present. New files, changed dependency types, type errors, import
cycles and unreadable export data remain checked. Existing tests and the
canonical platform matrix retain their selection.

Measured against `0985f2ff` on Linux/amd64 `agent-dev`, Go 1.26.6, x/tools
v0.50.0 and a two-CPU quota:

| Workload | Before wall / CPU | After wall / CPU |
| --- | ---: | ---: |
| Native exports after the CGO-on outer build | 252.32 / 328.49 s | 2.44 / 2.41 s |
| Complete archtest phase after that outer build | 384.65 / 678.58 s | 47.48 / 76.22 s |

The outer build seeded an isolated cache in 461.75 seconds; both versions started
from separate copies of that same cache. The complete phase used the normal Go
PATH guard, `-mod=readonly -p=2 -parallel=2 -timeout=10m -count=1 -json`, and a
Go `-exec` wrapper selecting the before/after test binaries against identical
repository inputs. Both passed, retained all 570 existing top-level test names
and the same three opt-in generator skips; the candidate added three contracts.
These are one paired cold run, excluding the common outer compile. Other host
work and memory pressure were higher during the baseline, so the exact wall
percentage is not a prediction for every commit. The baseline built 365 export
variants absent from the seed; the matching native load built none.

With already warm exports, four alternating helper-only full-suite runs measured
median wall times of 68.04 versus 58.45 seconds and CPU times of 80.26 versus
62.62 seconds. Raw requests, cache provenance, logs, wait4 descendant accounting
and system snapshots remain under `~/.cache/arch-export-cache/`.

### Sharing architecture build caches between worktrees

The fast local hook uses `-trimpath` when building, vetting, testing and linting
an explicit root `./internal/archtest` scope. Go can then reuse identical compiler
entries across worktree directories. Native architecture loaders and the two large
external-module fixture commands match the test binary's trimpath setting, so
each module-version variant can be reused across worktree directories. The
feature-plane generator uses that setting too, so its shared `featureplanegen`
package matches the scoped architecture build variant.

`LIP_LOCAL_ARCH_TRIMPATH=0 git commit` retains the untrimmed local path. To opt in
for manual feedback, use
`LIP_LOCAL_ARCH_TRIMPATH=1 make dev-test PKGS='./internal/archtest'`.
Ordinary packages, nested modules, broad scopes such as `./...`, CI and full
precommit retain their default build settings. The setting does not change
global `GOFLAGS` or omit tests or analyzers. The canonical Linux/Windows
architecture matrix still uses CGO off and untrimmed paths.

Trimmed archives are a separate Go cache variant: the first use must populate
them and uses more cache space. The benefit applies to later worktrees sharing
that cache; a warm command in the same worktree already reuses untrimmed builds.

Measured against `374c099c` on Linux/amd64 with Go 1.26.6, `GOMAXPROCS=2`,
the normal Go guard, and an ext4 TMPDIR:

| Fresh-worktree staged archtest hook | Untrimmed | Trimmed |
| --- | ---: | ---: |
| Complete hook wall time | 222.14 s | 183.68 s |
| Complete hook waited-process CPU | 342.57 s | 255.40 s |
| Feature-plane generator wall time | 52.91 s | 12.38 s |

This is one complete paired `bash scripts/hooks/pre-commit` run with identical
sources and the same staged archtest edit, using the opt-out on the first side.
Each started from an independent copy of the same Go and lint caches. Both
passed all 570 top-level tests with the same three opt-in generator skips and
completed mandatory build, vet and lint. The test step itself was slower in the
trimmed run (60.81 versus 73.78 seconds), so the 38.46-second hook saving includes
that cost. Other Go tasks were active on the shared four-CPU container; these
timings are observations, not a prediction for every commit.

Priming the trimmed outer build and fixture exports took 338.81 seconds in this
experiment, outside the paired timings. The seeded Go cache containing both
variants was 3.53 GB. A subsequent untrimmed dry run still planned 210 compiler
actions after the trimmed hook, so switching back to untrimmed checks can pay
another build cost. Raw commands, source/cache provenance, test logs and wait4
resource measurements remain under `~/.cache/arch-worktree-cache-20261008/`.

### Feature-plane generator feedback

The feature-plane generator lives in `internal/featureplanegen`, which imports
only the standard library. Its command no longer compiles the architecture
package's host dependencies just to parse a manifest and emit Go source.
Architecture reports retain their existing generator API through thin wrappers.

Run `make dev-test PKGS='./internal/featureplanegen'` for the generator's focused
contracts. The architecture package retains checks against the committed output
and repository diagnostic inventory. Edits to either package still trigger the
generated-file check in the staged hook.

Measured against `28b4a022` using the complete fast precommit hook with a staged
comment-only edit to `pkg/lipsdk/feature/plane_manifest.go`:

| Complete SDK hook, reverse-order pair | Original generator | Extracted generator |
| --- | ---: | ---: |
| Wall time | 246.73 s | 120.82 s |
| Waited-process CPU | 299.24 s | 186.45 s |
| Generator check wall time | 115.41 s | 0.70 s |

The 125.91-second wall saving (51%) includes build, vet, all 167 top-level SDK
test/example passes, guardrails and mandatory lint. All test names and statuses
matched across four complete hook runs. An earlier pair measured 289.96 versus
152.11 seconds and 302.81 versus 216.72 seconds CPU, but its baseline had a
Go-slot wait of unknown duration. Neither run in the reverse-order pair emitted
a slot-wait warning. Other agent work was active on the shared four-CPU host.

Both versions used the same worktree directory, staged blob and Go settings:
Go 1.26.6, CGO enabled, `GOMAXPROCS=2`, the normal Go guard and ext4 TMPDIR.
Each run started from an independent copy of identical frozen caches
(7.21 GB of Go entries and 9.19 MB of lint data, counting file contents).
Cache-copy setup took about 70 seconds per run and was excluded; no candidate
priming run was needed, so its first compilation is included in the timings.

The generator's dependency list fell from 1,040 packages to 79, including
225 project packages down to one. The SDK test dependency list stayed identical
at 1,179 packages. Raw hook logs, wait4 measurements, staged-source provenance,
cache inventories and dependency lists remain under
`~/.cache/feature-plane-generator-20261008/`. These measurements describe this
fresh-directory workload with shared caches; warm-worktree gains can differ.

### Creating a task worktree

`make worktree-create WORKTREE_BRANCH=fix-short-description` creates the worktree at
the validated location. Add `WORKTREE_BASE=<ref>` to branch from something other
than `origin/main`, or `SETUP=1` to run `make dev-doctor` in the new worktree.

The directory name is the branch with slashes folded to dashes
(`fix/short-description` becomes `worktrees/fix-short-description`), matching the
existing layout. Pass `WORKTREE_BASE` only after fetching: a base ref that does not
resolve locally is refused rather than silently resolved to something else.

Every check runs before git does, and a refusal leaves nothing behind:

- the destination must be absolute and under `<container>/worktrees/`, with its
  parent symlinks resolved
- an existing worktree of this repository at that path is reported and reused
  rather than recreated, so in-progress work is never discarded
- an existing directory that is *not* this repository's worktree is refused; so is
  a branch that already exists locally, on `origin`, or checked out in another
  worktree
- a branch name that could escape its directory is rejected outright

This exists because the layout is a hard constraint enforced at commit time by
`scripts/check-worktree-layout.sh`. A wrong path is otherwise discovered only after
the work is done. The bootstrap script runs that same guard immediately after
creating the worktree, so a violation is immediate and attributable rather than a
surprise at commit.

### Reading PR delivery status

`make pr-status PR=<n>` prints one PR's head and base SHA with their OIDs,
mergeability, per-check buckets, stacking, and a one-line verdict. `make pr-watch
PR=<n>` polls it and stops on a terminal verdict. Both are read-only: they never
merge, close, comment, edit, or push, and the self-test greps the script for those
verbs so the guarantee cannot rot.

Only required checks are classified. Intentional skips are accepted; absent or
unreadable required-check evidence stays blocked. Readiness also requires GitHub's
`CLEAN` merge state, so behind branches and unresolved protection requirements
cannot become ready merely because the reported checks are green. The revision
is re-read after checks to detect movement during observation.

The exit code is the contract, and the distinction between `failed` and `blocked`
is the point:

| code | meaning |
| ---- | ------- |
| 0 | ready: every required check passes on this head |
| 1 | failed: at least one required check failed |
| 2 | usage: bad arguments, or `gh`/`jq` missing |
| 3 | blocked: checks pending, draft, conflicting, base PR unmerged, or head branch gone |
| 4 | stale: the verdict was green, but the head or base moved since the last observation |

Every invocation re-reads the checks for the head it observes, so what it prints
is never itself stale. Staleness is about the conclusion carried over from an
earlier invocation: a green result is the only verdict a reader carries forward,
so it is downgraded to `stale` when the revision moved. A failure or a block is
equally true of the head in front of the reader and keeps its own code.

Stacked PRs are resolved through their base: green checks on a PR whose base has
not landed are `blocked`, not `ready`, because they were computed against a base
that is about to change. Full JSON is kept under `LIP_PR_LOG_DIR` (default
`~/.cache/lip-pr-logs`); last-observed revisions under `LIP_PR_STATE_DIR`. Both
default under the XDG cache, never inside a worktree.

### Merging with auto-merge

`main` requires branches to be up to date, and this repository has no merge
queue (that needs an organization-owned repository). Instead, enable
auto-merge on a ready PR (`gh pr merge <n> --auto --squash`, maintainer
decision). `.github/workflows/auto-update-prs.yml` then updates the oldest
auto-merge PR that is behind `main`, one at a time; when its required checks
pass, GitHub merges it and the next one is updated. It needs the
`AUTO_UPDATE_TOKEN` repository secret (fine-grained token, this repository,
Contents and Pull requests read/write), because branch updates made with
`GITHUB_TOKEN` do not start CI.

When a gate fails, fix the cause. `--no-verify` also skips secret scanning.

### Development-host race guard

Race verification runs in remote GitHub CI, not on interactive development
machines. `scripts/race-check.sh` protects the Make target; a PATH-level Go
wrapper also prevents accidental direct race-enabled commands before compilation.

On this Linux development host, the real Go command is `/usr/local/bin/go`
(the toolchain symlink). With `~/.local/bin` first on PATH, install the wrapper:

```sh
# Inspect any existing user Go shim before replacing it.
test ! -e "$HOME/.local/bin/go" && test ! -L "$HOME/.local/bin/go" &&
  install -m 755 scripts/go-dev-guard.sh "$HOME/.local/bin/go"
hash -r
command -v go
go version
bash scripts/test-go-dev-guard.sh
```

The wrapper rejects race-enabled test/build/run/install/list/vet/generate/tool
commands on `agent-dev` and `DESKTOP-I2CAJ6V`, including flags supplied through
`GOFLAGS` or the persisted Go environment file. It preserves ordinary argument
boundaries and exit statuses. CI markers and the Make-script override do not
disable the development-host wrapper. Tests use a fake toolchain; they never
run the race detector. The wrapper is an accident guard, not a security sandbox:
agents must respect it rather than invoke the real toolchain directly. Native
Windows shells require an equivalent executable shim; this installer is POSIX.

```sh
make dev-test PKGS='./internal/core/routing/...'
make dev-build PKGS='./cmd/lipstd'
make dev-lint PKGS='./internal/core/routing/...'
make dev-test MODULE=connectors/openrouter PKGS='./...' DEV_REPEAT=2
make dev-doctor
make dev-test-changed
make dev-test-changed DEV_PLAN=1
```

### Automatic local test selection

`make dev-test-changed` compares the branch against its merge base with local
`origin/main`, including staged, unstaged and untracked changes. Committing a
change does not remove it from selection. The command does not fetch; update
remote-tracking refs separately when needed. `DEV_BASE=<ref>` overrides the
comparison reference. Use `DEV_PLAN=1` to inspect the plan without executing
tests, `DEV_FULL=1` to run complete default tests in every maintained module,
and `DEV_FRESH=1` for deliberate fresh execution. `DEV_JOBS` and `DEV_REPEAT`
retain their existing meanings; the plan is built once per invocation.

Production changes select their owning package, transitive production consumers,
and consumers that import affected production code only in tests. Test-only
changes select their owning package. Test edges do not propagate production
impact. Every default test in each selected package runs; individual test names
are not filtered. Package-owned embedded inputs and `testdata` are included.
An ordinary connector change stays within its independent module, with
`GOWORK=off` for discovery and execution.

Shared SDK, connector-support, testkit, configuration and build/dependency policy
changes trigger complete default tests across the maintained module inventory:
the root, connectors, connector-support and the four external fixture modules
used by the module-check scripts. Unmapped inputs, unresolved deleted packages,
missing default-base information and failed package discovery also fall back
to that full run. An invalid explicitly supplied base is an error. Known
prose-only documentation changes and a clean branch select no tests.

The report shows the comparison revision, changed-file count, selected modules
and packages, selection reasons and fallback reason. Planning and execution
times are reported separately, followed by package pass/cache/failure counts.
Modules execute sequentially with bounded package concurrency. Test failures
and invalid telemetry fail the command.

Automatic selection cannot be combined with `PKGS` or a non-default `MODULE`;
use `make dev-test` for manual scope. Selection follows the current host's Go
build configuration. It does not certify other operating systems, tagged suites,
external services, or dependencies that are not represented in Go imports and
the input policy. It is local feedback only: `make test`, `make qa`, hooks,
and GitHub checks retain their existing comprehensive scope. Full fallback
runs default tests, not tagged or external-service certification targets.

Measured on Windows/amd64 with Go 1.26.6 against `e969634c`, representative
production edits selected these scopes:

| Changed input | Selected default tests |
| --- | --- |
| CLI runner | `cmd/lipstd` only |
| Isolated localstub connector command | One package in `connectors/localstub` |
| Billing-store accounting cutover | 13 root packages, including runtime, host and architecture consumers |
| Shared canonical API | Complete default tests in all 42 maintained modules |

For the CLI example, a sequential comparison with warmed build caches and fresh
tests (`-count=1`, `-p=4`, `-parallel=4`, `-timeout=10m`, `-mod=readonly`) took
7.57 seconds for the compiled developer runner, including selection, versus
123.54 seconds for `go test -json ./...` with those same flags. The selected
package passed; the complete root run passed 360 packages and reported 27
packages without tests. Windows Job Object accounting measured 12.66 versus
690.55 CPU seconds, including descendant processes. Selection itself took
about 3.1 seconds. This is one matched comparison of a CLI-only edit, not a
guaranteed speedup for shared/core changes or a comparison against all nested
module suites. Invoking through `make` also compiles/starts the runner via
`go run`, using the normal Go build cache.

The selector's deterministic policy, ownership and dependency tests run in the
default suite. Its real Git/Go process-boundary fixtures use `//go:build integration`;
run them locally with `go test -tags=integration ./tools/devcheck/...`. The existing
full Linux race check and `make qa` already enable `precommit,integration`, so
remote certification includes these fixtures without changing workflow scope.

The heavier runtimebundle end-to-end host, HTTP, billing, and reload tests live behind `integration`; run them with `go test -count=1 -tags=integration ./internal/infra/runtimebundle/`. The whole-module owner-callback source gate uses `precommit` and runs in its dedicated required CI job.

### Explicit local scope

`PKGS` is required for test/build/lint. There is no silent fallback to the full
repository. Patterns are relative to `MODULE`, which defaults to the root module.
`DEV_JOBS` defaults to 1 on local Linux `agent-dev`, and 4 elsewhere; tune it for the actual machine and keep it stable during
comparisons. `DEV_REPEAT=2` runs the identical operation twice and prints elapsed
time. Tests report passed/cached/failed/skipped package counts. Child-process or
telemetry errors fail the command. `dev-lint` requires golangci-lint and does not
substitute a weaker analyzer or silently skip missing tooling.

The explicit-scope commands do not infer reverse dependencies, certify other modules, or run
tagged integration/architecture tiers. Include affected consumers while iterating
on shared contracts. Before delivery, use the applicable `make test`, `make qa`,
module-local, parity, persistence, race, and platform gates from `AGENTS.md`.
`test-fast` retains its complete root-graph contract; it is not package selection.

Local `lint-all-modules` changed/staged modes infer the changed packages and
transitive production and test consumers within each module. Shared SDK, testkit,
configuration, dependency and lint-policy changes force comprehensive module
lint. Unscoped `make lint` and CI still lint complete modules. `make qa` runs
that comprehensive lint once, after the preliminary policy checks.

`LIP_TEST_PACKAGES` optionally sets Go package-process concurrency (`-p`),
independently of `LIP_TEST_PARALLEL` (`-parallel`, within each test binary). On local Linux
`agent-dev`, suite Make targets and Bash quality/hook entry points default to one package
process, `GOMAXPROCS=2`, and two parallel tests per binary. Scoped `dev-test`
commands use `DEV_JOBS` for both package jobs and test parallelism (default 1). The installed
`go-dev-guard.sh` also defaults direct Go commands to `-p=1` and `GOMAXPROCS=2`,
preserving persisted `GOENV` flags and explicit budgets. Local Go and Bash gate
processes use an absolute niceness floor of 10; an inherited value of 10 or
higher is retained. To update an existing guard, inspect and back up the installed file, then run
`install -m 755 scripts/go-dev-guard.sh "$HOME/.local/bin/go"`. CI markers and Windows retain their existing resource defaults; race
blocking on development hosts remains active even with CI markers. Elsewhere,
leave package concurrency unset for Go's native default. Across sessions, the
guard budgets `LIP_GO_SLOTS` (default 2) heavy commands (`build`, `test`,
`vet`, `install`) host-wide; others print a waiting notice and queue for a free
slot (`flock` on `$HOME/.cache/lip-go-slots`). Commands started by a slotted
command inherit its slot. `LIP_GO_SLOT_MODE=advisory` (default) waits up to
`LIP_GO_SLOT_WAIT` seconds (default 900), then explicitly reports that it is
running without a slot. `LIP_GO_SLOT_MODE=hard` instead returns **75**
(`EX_TEMPFAIL`, resource-blocked), without starting the toolchain. A zero wait
attempts every slot once before applying that policy. Invalid modes return 2.
Nested commands retaining `LIP_GO_SLOT_HELD` reuse their parent's slot under
either policy; children that scrub that variable must retain advisory policy
or a bounded wait to avoid waiting indefinitely behind their own parent.
For a hard, immediate admission check:
`LIP_GO_SLOT_MODE=hard LIP_GO_SLOT_WAIT=0 go test ./path/to/package`.
`scripts/lint-all-modules.sh` shares these slots through the standalone guard's
`--lip-resource-run <command> [args...]` mode. Direct analyzer invocations,
including `dev-lint`, must use that wrapper explicitly to participate in this
slice's coordination. CI and non-agent-dev analyzer runs retain native behavior.
For scoped lint feedback: `bash scripts/go-dev-guard.sh --lip-resource-run make
dev-lint PKGS='./path/to/package'`.

Participating compiler/analyzer commands also hold a shared cache lock, including
advisory slot-overflow runs. `make dev-cache-maintenance` is dry by default;
`CACHE_APPLY=1` explicitly clears the Go build and lint caches under an exclusive
lock. It uses the same `LIP_GO_SLOT_DIR` (default `~/.cache/lip-go-slots`) and wait
budget. Cache-lock timeout always returns 75 without executing the command,
regardless of advisory slot policy. Maintenance invoked within a resource-owning
parent is rejected instead of upgrading its lock and deadlocking. Its child Go
commands inherit the exclusive owner and do not reacquire locks.

The lock protocol is cooperative: reinstall the updated standalone guard before
relying on it, and use the maintenance command instead of uncoordinated cron
cleanup. This repository change does not install the guard, alter cron, delete
module caches, or clear any caches by default. Existing unwrapped processes and
older guard installations do not participate. A pending exclusive lock has no
fairness guarantee; bounded timeout makes that a resource blocker, not permission
to clean concurrently. Tests exercise the protocol with real flock contention
and fake tools; they never clean host caches.
Make and Bash
gates pass explicit package flags, which take precedence over `GOFLAGS`; use
`LIP_TEST_PACKAGES`, `LIP_TEST_PARALLEL`, and `DEV_JOBS` for their budget overrides,
or replace Make's complete `GO_TEST_FLAGS` string.
For example, `make test-unit LIP_TEST_PACKAGES=8 LIP_TEST_PARALLEL=8` selects an
explicit budget on both platforms. See [the measured follow-up](development-gates-performance.md).

Keep `GOCACHE`, `GOMODCACHE`, and the lint cache in stable, writable locations
outside disposable worktrees. Do not clear them as routine troubleshooting. Do
not routinely add `-a`, `-count=1`, race, coverage, or alternating build tags.
For final regression proof, intentionally run focused fresh tests, for example
`go test -count=1 ./path/to/package`. Build cache reuse still applies to that run.
Local `dev-build` omits VCS stamping to avoid linking again solely after a commit;
release commands retain their existing VCS/trimpath policy.

For unexplained misses, compare identical scoped runs first. `make dev-doctor`
prints the effective Go version, flags, cache paths, CPU budget, linter version,
and lint cache status. It checks that Go caches are writable. When the Go cache is
disabled so even the diagnostic cannot compile, start with `go env GOCACHE`.
Use `GODEBUG=gocachetest=1 go test ./path/to/package` for result-cache miss reasons
and `go build -x ./path/to/package` to distinguish compilation from linking.
Record exact commands, OS, toolchain, scope, and cold/warm state with measurements.

### Verification manifests

Set `DEV_EVIDENCE=<path>` on a verification `dev-*` target (including
`dev-contract-check`) to record what the check actually
proved, as JSON beside a per-step log per command:

```
make dev-test-changed DEV_EVIDENCE=~/scratch-ci/verification.json
```

The manifest names the tested revision (`head`, `branch`, `merge_in_progress`)
and its dirty identity (`dirty`, `dirty_path_count`, `dirty_go_files`, and a
`dirty_digest` over the porcelain payload *and* the bytes of the dirty files),
the requested scope (kind, module, packages, base, jobs, repeat), the toolchain
(`go_version`, `lint_version`, `goos`, `goarch`, `gomaxprocs`), and one step per
command with its exit code, duration, test counters, and log path.

Read `outcome` before anything else. `passed` and `failed` are code verdicts.
`blocked` is not: it means an infrastructure condition (missing tool, unusable
module path, unopenable log) prevented the check from producing a verdict, and
its `failure_reason` names the blocker. A `passed` manifest over a dirty tree
still describes a tree nobody else has, so quote the revision and digest with
the result. This is developer feedback, not a delivery gate; CI remains
authoritative.

`dev-delivery-plan` is a planning report, not verification; it rejects
`DEV_EVIDENCE` rather than emitting a passing manifest with no checks. Task
handoff artifacts use the separate acceptance/recovery protocol in
`docs/agent-handoffs.md`; native verification manifests remain command evidence,
not reviewer approval or interchangeable source fingerprints.

## Why earlier improvements stopped being sufficient

Audited against `main` at `b1e81926`; source history, not prior claims alone:

| Earlier work | What remains in the current tree |
| --- | --- |
| #218 | Shared staging fixtures, helper builds, isolated store tests, cheaper timeout tests |
| #231 | Shared architecture `go list` query cache |
| #291/#293 | Deduplicated compilation/vet/archtest; complete cached root test path |
| #294/#296/#297 | Dedicated CI cache restore/save actions; setup-go caching disabled in those jobs |
| #417 | Per-test-process connector/helper build reuse and AST loading singleflight |
| #449 | Core-count test parallelism, bounded module/fuzz/parity pools, cheaper default precommit |
| #558 | Windows-authoritative CPU/process/I/O/wall/package cost ratchet |
| #665 | Fast file-backed SQLite test DSNs, concurrent architecture plan, test-quick |

The inspected mechanisms have not been wholesale reverted. This does not prove
that every historical test optimization survived unchanged, or that a specific
developer's cache was configured correctly. The local Windows environment was not
available for direct inspection.

| Revision | Go source files | Test files | Go modules |
| --- | ---: | ---: | ---: |
| #218 (`49c379aa`) | 3,603 | 1,846 | 19 |
| #293 (`c08260f2`) | 4,023 | 2,096 | 19 |
| #449 (`f936b8f1`) | 4,958 | 2,642 | 22 |
| #665 (`d196469b`) | 6,316 | 3,517 | 42 |
| Audited main (`b1e81926`) | 6,366 | 3,552 | 42 |

Counts include tracked Go files across independent modules, excluding agent-skill
examples. Growth increases cold compile/analyzer cost and the scope invalidated by
shared-package changes. The report in `docs/test-suite-performance.md` also records
~56 seconds of link/startup work on its Windows machine and overlapping tagged
passes. Native caches do not eliminate arbitrary subprocess/test setup work.

The root production import graph also has substantial fan-out: `pkg/lipapi` has
232 transitive production consumers, `pkg/lipsdk` 102, `internal/core/runtime` 30,
and `internal/core/config` 86. Additional test-only consumers are 24, 35, 48, and
38 respectively. These counts describe dependency scope, not measured compiler
invalidation counts. Newly added Go code is automatically covered by native
caching; central contract changes can legitimately affect many consumers. Longer
term, reduce unnecessary dependency fan-out and package coupling at demonstrated
boundaries rather than replacing Go's build system or adding cache whitelists.

Three concrete infrastructure defects were present:

1. Dedicated CI snapshots still used dependency-only immutable keys. An exact
   key hit prevented saving new source build progress until dependencies changed.
   CI test and database parity jobs also shared one namespace; parity never saved
   its own accumulated cache. New snapshots include OS, architecture, actual Go
   version, job, dependency fingerprint, and source revision. Restore prefixes
   prefer the same dependency fingerprint, then the same toolchain/job. Each
   lane saves its own progress; caches still obey GitHub's branch/ref isolation.
2. The lint module pool defaulted to CPU count, while each golangci-lint process
   also used parallel analyzers. Both scripts now default to two module workers
   and two analyzer workers each. Override with `LIP_LINT_JOBS` and
   `LIP_LINT_CONCURRENCY`; defaults are a contention bound, not a claim of optimal
   throughput on every machine.
3. The QA cross-platform selector test mutated one Git checkout from parallel
   subtests, and its untracked selector disappeared after checkout. Both failures
   reproduced on unchanged main. Cases now run serially and the selector belongs
   to the base fixture commit; assertions are unchanged.

The live public cache inventory contained 16 entries totaling ~10.55 GB during
this investigation. Fresh snapshots alone would worsen storage pressure. The
trusted default-branch retention workflow removes superseded Go snapshots, keeps
the newest per workflow family/OS/architecture/job/ref, retires legacy keys only
after a replacement exists in that ref, and removes recognized closed-PR Go
caches. Unknown keys and unrelated caches are untouched. The selector is tested
offline; deletion is isolated in a workflow with `actions: write` and never runs
PR code or consumes PR artifacts. Cleanup also runs periodically. This bounds
obsolete accumulation, but cannot guarantee that the repository's configured
capacity fits all simultaneously active lanes and PRs.

Evictions are otherwise silent, so two diagnostics surface them. The retention
run summary (workflow `Go cache retention`) lists repository cache usage against
the 10 GiB cap and the newest main snapshot of every lane, and warns when usage
passes 80%, when retention removes the only main snapshot of a lane, or when a
lane in `policy.json` has none. Any job whose lane restores no compiler snapshot
emits a `Go cache <lane> restored no compiler snapshot` warning annotation:
a slow job with that warning is a cold lane, not a regression.

## Regression prevention

- QA guards require source-advancing, toolchain/job-isolated cache keys, matching
  restore/save lanes, bounded lint scheduling, and discoverable scoped commands.
- Remote Windows historical comparisons are temporarily paused: default CI
  always uses the ordinary scoped Windows unit/build lane, including policy edits
  and labeled PRs. `Development cost watchdog` is disabled in GitHub and its job
  is guarded off in source. `make test-cost` remains available locally with its
  existing anchor and budgets; remote performance certification is paused.
- Retention prevents the source-refresh fix from accumulating unbounded obsolete
  snapshots. GitHub's capacity and cache eviction still need to be considered
  when active parallel workloads exceed the minimum retained footprint.
- Agent instructions specify focused checks during edits and comprehensive
  evidence after coherent changes. Avoid duplicating equivalent gates after each
  edit or treating a cached successful check as evidence after a new mutation.

The existing cost anchor predates #665's speedup and contains permissive legacy
store-package allowances. Recalibrating it against a clean Windows measurement is
a follow-up; changing those numbers without measurement would create false
assurance. The watchdog provides recurring evidence instead of waiting for a
developer to notice a slowdown.

## Validation measurements

On this Linux container (Go 1.26.6, effective 8 CPU budget, pinned linter v2.12.2),
with writable shared caches and no forced cache invalidation:

| Operation | First measured call | Identical repeat |
| --- | ---: | ---: |
| Scoped devcheck package tests | 0.147 s | 0.048 s, result cached |
| Warm local `cmd/lipstd` development build | 0.197 s | 0.195 s |
| Scoped lint of devcheck + QA | 4.464 s | 0.337 s |

The initially cold root command build took 53.098 seconds. These are cache-state
observations, not before/after Windows speedup claims. CLI bootstrap adds a small
Go-run overhead outside the printed child elapsed time. A sandbox/root full-suite
attempt was unsuitable for certification: sockets were blocked and the host
security policy rejects administrative execution. Verification uses a disposable
non-root checkout with normal subprocess access instead. No Windows-authoritative
cost improvement or remote cache hit-rate improvement is claimed from these local
measurements; the new workflows must provide that evidence after landing.

The complete tooling/QA packages passed in the non-root checkout, and archtest
passed in 62.962 seconds. The broader root suite had 348 passing package lines
and five failing tracing-related packages; representative `core/extensions`
failures reproduced on unchanged main. Full-root mandatory lint found four
existing govet inline findings in billing/adapter tests, also reproduced against
unchanged main. Changed-package lint passed. Full-suite/full-lint certification
is therefore not claimed.
