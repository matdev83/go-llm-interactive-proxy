# Development iteration performance

The development loop must stay scoped and reuse Go's native caches. Comprehensive
certification remains a separate, deliberate delivery step. This policy covers
tests, builds, linting, cache lifecycle, and the automated cost watchdog.

## Use during development

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
files are staged (about 40 seconds for a one-package change on `agent-dev`).
It does not test their consumers: nearly every package reaches the
repository-wide suites through reverse dependencies, which made a
reverse-dependency scope cost as much as the full suite. Config-source
certification runs only when a config-source path is staged. CI's `Go suite (Linux)` and `Lint (Linux)` jobs run the complete
tagged root suite and lint on every PR, and the required `Repo hygiene` check
fails when either does. `LIP_PRECOMMIT_FULL=1 git commit` (or
`make precommit-full`) runs the old complete local gate. `scripts/hooks/pre-push`
re-checks release cleanliness and change size.

A commit touching many packages still takes minutes, so run commits as a background command
with no tool timeout: a harness timeout that kills the gate mid-run leaves the
commit unapplied and the index still staged.

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
leave package concurrency unset for Go's native default. These are per-command
budgets; simultaneous sessions can still consume multiple cores. Make and Bash
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
