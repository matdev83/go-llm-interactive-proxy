---
name: go-simplify
description: "Go simplify: preserve behavior, reduce nesting, one-use helpers/interfaces, staticcheck/gosimple."
---

# Go Simplify

Simplify Go code while preserving exact behavior. Prefer no change over a simplification whose equivalence is not obvious.

## Boundaries

Use for Go cleanup only. Do not use for feature work or bug fixes. Do not modify generated code, docs, exported APIs, JSON/proto/GraphQL/database/wire contracts, or test expectations/fixtures unless the user explicitly asks and the change is still behavior-preserving. Do not add tests or docs as part of this skill.

Do not spawn subagents. One agent should keep the full simplification context.

Non-negotiable invariant: the observable behavior must remain identical. If equivalence depends on assumption instead of evidence, leave the code unchanged.

## Target Selection

Start with `git status --short`, then identify the exact target files. Do not touch unrelated dirty files.

- No args/default: uncommitted staged, unstaged, and untracked `.go` changes.
- `--base`: branch `.go` changes against `main`, falling back to `master`.
- Specific paths: expand only to `.go` files under those paths.
- Exclude generated files: `*.pb.go`, `*_grpc.pb.go`, `*.pb.gw.go`, `generated.go`, `models_gen.go`, `**/ent/*.go`.
- No targets: report `No Go files to simplify` and stop.

Useful commands:

```bash
{ git diff --name-only --diff-filter=d HEAD -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } | sort -u
git diff --name-only main...HEAD -- '*.go'
git diff --name-only master...HEAD -- '*.go'
```

## Protocol

1. Analyze first; do not edit.
 - Run `staticcheck -checks "S*"` for affected package dirs if available.
 - Filter staticcheck findings to target files only.
 - Treat unrelated package failures as context, not blockers.
 - For non-trivial targets, run affected package tests before editing when feasible to establish a baseline.

2. Look for concrete simplifications.
 - Signals: deep nesting, long functions, unhappy path indentation, naked returns, one-shot helpers, files over 800 lines, private one-use interfaces, redundant intermediate error wrapping.
 - A signal is not enough. Propose only changes with a clear local simplification and no scope expansion.

3. Apply the equivalence gate.
 - For each candidate, name the behavior that must remain unchanged.
 - Reject candidates that could change evaluation order, nil/zero-value behavior, error identity/wrapping/message text, logging/metrics/tracing, resource lifetime, lock/defer/transaction behavior, goroutine/channel timing, retries/backoff/timeouts, ordering, or public contracts.
 - Reject candidates not covered by tests unless the equivalence is mechanically obvious.

4. Present a numbered proposal before editing.
 - For each item: file, current problem, proposed change, why behavior is preserved, verification to run.
 - Ask which to apply: `all / 1,2,3 / none`.

5. Apply selected items one group at a time.
 - Keep each group patch-scoped and reversible without touching unrelated user edits.
 - Inspect the diff for the group before verification.
 - If verification finds a new failure attributable to that group, reverse only that group's patch. Do not use broad `git reset` or `git checkout` unless the user explicitly asks.

6. Verify.
 - Run affected package tests when feasible.
 - Then run `go build ./...`.
 - For large repos, prefer affected package tests first and state if full `go test ./...` is skipped.
 - Compare failures to the baseline. Reverse the group only for new failures attributable to the simplification.
 - If no meaningful tests exist, state that limitation; build success alone does not prove behavior preservation.

## Decision Rules

- Preserve behavior over aesthetics.
- Leave code unchanged when preservation is uncertain.
- Do not change control-flow shape around `defer`, locks, transactions, goroutines, channel operations, context cancellation, or cleanup unless equivalence is trivial and verified.
- Do not combine, reorder, or deduplicate conditionals when ordering affects side effects, error precedence, validation messages, logging, metrics, or nil safety.
- Do not remove or relocate mutable globals under this skill. Report them as outside simplify unless the change is a syntactic no-op.
- Inline or remove one-shot helpers only when inlining cannot change `defer` timing, `panic`/`recover` scope, argument evaluation order, nil checks, variable lifetime, resource ownership, or error/logging output.
- Avoid helper extraction unless all are true:
 1. Helper has 2 or fewer return values
 2. Helper does one thing
 3. Call site is easier to read than inline code
 4. Duplicated block is over 30 lines
 5. Original code remains readable top-to-bottom
- Remove a single-implementation interface only when it is private, local, one-use, and not a public API or test seam.
- Before changing error paths, trace source error -> wrap point -> classification owner -> log owner -> transport mapping. Preserve `errors.Is`/`errors.As` behavior and logging ownership.
- Staticcheck `S*` findings are candidates, not orders. Apply them only after the same equivalence gate and user approval.

## Common Mistakes

- Applying staticcheck fixes before user approval
- Treating file/function size as a refactor mandate
- Removing error wrapping without checking classification
- Extracting a helper that makes the call site harder to read
- Verifying only with build when affected tests are available
- Flattening control flow across cleanup, locks, transactions, or goroutines
- Treating "same output on happy path" as full behavior preservation
