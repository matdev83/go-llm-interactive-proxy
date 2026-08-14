---
name: lip-pr-delivery
description: "Deliver sequential LIP PRs: preflight, CI repair, merge order, smoke checks, and cleanup."
user-invocable: true
license: MIT
compatibility: Designed for pi, OpenCode, Codex, Cursor, and similar coding agents working in this repository.
metadata:
  author: go-llm-interactive-proxy
  version: "1.0.0"
allowed-tools: Read Edit Write Glob Grep Bash(git:*) Bash(gh:*) Bash(go:*) Bash(make:*) Agent AskUserQuestion
---

# LIP Sequential PR Delivery

Deliver repository changes through GitHub without bypassing local policy, remote gates, merge order, or final operational evidence.

Use this skill for PR preparation, stacked/sequential PRs, CI babysitting, merge repair, final verification, and worktree cleanup. Repository `AGENTS.md` and `.kiro/steering/` override this skill.

## Non-Negotiable Rules

- Never implement on `main`. Use one named feature worktree per PR.
- Preserve user changes. Never reset, discard, or overwrite unrelated work.
- Do not weaken tests, thresholds, assertions, architecture gates, or workflows to make CI green.
- TDD applies to behavior changes and CI repairs: reproduce the failure, add the smallest meaningful test or correction, then rerun the exact gate.
- Keep each PR under the repository's 100-path limit.
- Submit and merge sequential PRs in their approved order. Do not open a dependent PR against stale history unless explicitly requested.
- Merge only when the PR is current, mergeable, and every required check has completed successfully.
- Never claim delivery complete until merged `main` is verified.

## 1. Establish Ground Truth

Before editing, committing, rebasing, or merging:

```text
git status --short --branch
git worktree list --porcelain
git log -1 --oneline --decorate
git remote -v
gh auth status
```

Confirm explicitly:

- current absolute worktree path;
- current branch and intended PR;
- merge base and required PR order;
- clean/dirty/staged state;
- whether changes belong to the user or this task;
- current `origin/main`;
- GitHub authentication and API availability.

If a delegated worker appears hung, inspect the shared worktree and run status/tests before retrying. A blocked wrapper does not prove the work failed. Do not launch another mutating worker over completed changes.

## 2. Review Before Commit

Review the full diff, not only the worker report:

```text
git diff --check
git diff --stat
git diff
git diff --cached --check
git diff --cached
```

Check for:

- scope or architecture drift;
- changed wire behavior, status codes, lifecycle, cancellation, or limits;
- weakened assertions or deleted edge cases;
- stale comments and unused abstractions;
- error wrapping and `errors.Is` / `errors.As` behavior;
- accidental generated files, artifacts, or unrelated edits.

### Test Independence

Expected results must not be produced by the implementation under test.

Reject tautological tests such as:

- comparing a helper with a caller that now delegates to that helper;
- generating a golden value through the new codec;
- replacing exact shape/order assertions with presence-only checks.

Use pinned fixtures, independently constructed expected values, reference implementations, or post-mapping validators.

## 3. Run Repository Preflight

Run the exact repository gates relevant to the changed paths. At minimum:

```text
test -z "$(gofmt -l cmd internal pkg)"
git diff --check
go mod verify
git diff --exit-code -- go.mod go.sum
```

Then run focused uncached tests:

```text
go test -count=1 ./path/to/changed/package/...
```

Use the verification guidance in `AGENTS.md`:

- `make quality-checks` for formatting, vet, and architecture hygiene;
- `make test-unit` for broad unit coverage;
- `make parity-checks` for protocol/frontend/backend matrices;
- `make qa` for wide or release-grade changes;
- targeted race/fuzz tests for concurrency or parser changes.

### Coverage-Sensitive Packages

If a changed package is covered by a measured workflow, inspect `.github/workflows/*coverage*.yml` and reproduce its command and threshold locally before pushing. Never assume ordinary `go test` is sufficient.

Example pattern:

```text
go test -count=1 -coverprofile=coverage.out ./path/to/package
go tool cover -func=coverage.out
```

Add coverage only for meaningful changed behavior. Do not add unrelated tests merely to inflate the percentage, and never lower the threshold.

### Runnable Distribution

For changes that can affect composition, protocols, frontends, backends, config, or CLI startup:

```text
go build ./cmd/lipstd
go run ./cmd/lipstd --help
```

For protocol/runtime changes, run the applicable full-path integration smoke tests. OpenResponses changes should include JSON, SSE, compact, and WebSocket round trips where relevant, plus reference client/backend suites.

## 4. Commit and Submit One PR

Stage only intended paths, then recheck the staged diff:

```text
git add <explicit-paths>
git diff --cached --check
git diff --cached --stat
git status --short
git commit -m "<conventional message>"
```

Before opening the PR, fetch and rebase onto current `origin/main` when the PR is independent:

```text
git fetch origin main
git rebase origin/main
```

For a branch originally based on an already squash-merged predecessor, transplant only the unique commit(s), rather than replaying predecessor commits:

```text
git rebase --onto origin/main <old-predecessor-tip> <branch>
```

After rebase, rerun focused tests and push with `--force-with-lease` only when history was intentionally rewritten.

Open the PR with a body listing behavior, constraints, and exact verification evidence. Record the PR number and head SHA.

## 5. Babysit Remote Checks

Monitor checks for the latest head SHA. A force-push may briefly report no checks; wait for the new workflow runs rather than reading stale results.

```text
gh pr view <pr> --json state,headRefOid,baseRefOid,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup
gh pr checks <pr> --watch --interval 20
```

For every failure:

1. Retrieve the exact failed job log:

   ```text
   gh run view <run-id> --job <job-id> --log-failed
   ```

2. Identify the precise command and failure.
3. Reproduce it locally.
4. Make the smallest production-grade repair.
5. Rerun the exact failed command plus focused regression tests.
6. Review the repair diff.
7. Commit and push.
8. Wait for checks attached to the new head SHA.

Do not dismiss formatting, coverage, platform, security, compliance, or architecture failures as incidental. Do not merge while checks are pending, stale, skipped unexpectedly, or attached to an old SHA.

If GitHub API/authentication fails, distinguish API credentials from Git transport credentials. Preserve committed/pushed work, report the blocker, restore `gh auth`, and resume without duplicating PRs.

## 6. Merge Sequentially

Before merge, require:

- `state=OPEN`;
- `mergeable=MERGEABLE`;
- `mergeStateStatus=CLEAN`;
- all required checks completed successfully;
- no unresolved substantive review findings;
- branch based on the intended predecessor state.

Then merge using the repository's established method, normally squash:

```text
gh pr merge <pr> --squash --delete-branch
```

Confirm the merged state and merge commit:

```text
gh pr view <pr> --json state,mergedAt,mergeCommit,url
```

Fetch `origin/main` before preparing the next PR. Rebase/transplant the next PR onto the newly merged `main`, rerun preflight, then submit it. Never merge dependent PRs out of order.

## 7. Verify Merged Main

Use the original `main` worktree, not a feature checkout:

```text
git -C <main-worktree> status --short --branch
git -C <main-worktree> pull --ff-only origin main
```

Run on merged `main`:

- focused changed-package tests;
- relevant contract and integration suites;
- reference client/server emulator suites for protocol changes;
- full-path smoke scenarios;
- `go build ./cmd/lipstd`;
- `go run ./cmd/lipstd --help`.

For OpenResponses, explicitly exercise applicable JSON, SSE, compact, and WebSocket paths. State clearly whether tests use the in-process deployment harness, independent emulators, or a separately spawned `lipstd serve` process; do not conflate them.

## 8. Clean Up

Only after merge and merged-main verification:

1. Confirm every feature worktree is clean.
2. Remove Git worktree registrations.
3. Delete merged local feature branches.
4. Fetch with prune and run `git worktree prune`.
5. Confirm `main` is clean and synchronized.

```text
git worktree remove <path>
git branch -D <merged-branch>
git fetch --prune origin
git worktree prune --verbose
git worktree list --porcelain
git branch --list '<task-pattern>'
git status --short --branch
```

On Windows, directory locks may prevent physical deletion. If so, remove/prune the Git registration and branches, verify they are gone from Git metadata, and report that the remaining directory must be deleted manually after locks release. Never use destructive resets to work around locks.

## Completion Report

Report:

- PR URLs and merge commits in order;
- CI failures encountered and how each was repaired;
- local and remote gates that passed;
- merged-main smoke evidence and its topology;
- any skipped tests or residual risks;
- branch/worktree cleanup status.

Delivery is complete only when all scoped PRs are merged, merged `main` passes the required verification, and Git metadata cleanup is complete (or a clearly stated OS lock is the sole remaining physical cleanup item).
