# CI and iteration-speed tiers

The repository keeps correctness gates in CI while avoiding unnecessary full scans
for every local edit.

## Local fast loop

Use `make test-fast` for quality checks plus tests selected from staged Go files.
The pre-commit hook also runs staged tests and the staged race scan. Full lint and
vulnerability analysis are intentionally not run by default.

Use `make precommit-full` when a local commit should reproduce the full lint and
`govulncheck ./...` portion of the quality gate.

When a staged change touches `go.mod` or `go.sum`, the hook also runs
`scripts/tidy-all-modules.sh --check` so independent connector modules cannot
silently drift from the root dependency graph. Independent module tidy checks
use bounded parallelism (`LIP_MODULE_CHECK_JOBS`, default 4) and compile the
module-discovery helper once into a temporary directory. The temporary helper is
removed when the check exits.

The Windows PowerShell tidy path uses the same bounded worker setting and reuses
a temporary `lip-taskrunner` executable for the duration of the invocation. It
preserves per-module process-tree ownership and removes the helper after all
workers finish, including failure paths.

## Pull requests

Required CI, QA, CodeQL, security, and platform checks remain blocking. The
backend-plugin cross-platform workflow computes a connector selection from the
changed paths and narrows the compiler/package matrix when possible; shared
security, packaging, and lifecycle checks remain conservative. Root module, tool,
matrix, and workflow changes intentionally fall back to the full connector matrix.

The portable `cmd/lipstd` test/build evidence is owned by the CI OS matrix. The
Ubuntu QA job retains architecture and vet evidence without repeating that
package test, so the same command is not compiled twice on the PR path.

PR and branch workflows use branch-aware concurrency groups so a newer commit
cancels an obsolete run for the same change line. Release publishing keeps
cancellation disabled once a tag verification has begun.

Go setup caches include root and nested module metadata (`connectors/**/go.sum`,
`connector-support/**/go.sum`, `testdata/enterprise_module/go.sum`, and tool
module sums) wherever a workflow invokes Go. This makes dependency changes
invalidate the relevant cache without requiring unrelated source changes to
invalidate it.

OpenResponses coverage keeps the existing thresholds. The timing-sensitive
frontend package is measured with three serialized uncached runs and the coverage
profile is aggregated by Go; normal test and QA commands are unchanged.

## Scheduled and release validation

The full discovered-module synchronization check runs from
`.github/workflows/dependency-sync.yml`. It validates every `go.mod` under
`connectors/`, `connector-support/`, and `testdata/enterprise_module` with
`GOWORK=off`, bounded parallelism, tests, and command builds.

Security, release, race/fuzz, CodeQL, compatibility, and coverage workflows keep
independent ownership of their evidence. The optimization work normalizes their
cancellation/cache behavior but does not merge required security or release
proofs into a faster, weaker job.

Race/fuzz, full backend release gates, benchmarks, mutation, PostgreSQL proofs,
and long-running soak tests remain scheduled, manually dispatched, or release
validation tasks. They should not be moved into the local fast loop merely to
increase confidence; their cost and environment requirements are intentional.
