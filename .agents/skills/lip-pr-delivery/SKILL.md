---
name: lip-pr-delivery
description: "Triage LIP PRs against current main, deliver sequential or stacked PRs through CI and merge, and clean up task-owned worktrees and branches."
license: MIT
metadata:
  author: go-llm-interactive-proxy
  version: "2.1.0"
---

# LIP PR Workflow

## Choose the operation

- **Triage:** decide whether the issue and proposed fix are valid against current main. A verdict does not authorize merging, closing, or patching the PR.
- **Delivery:** submit, repair CI, or merge within the user's requested scope and order.
- **Cleanup:** remove the specified task resources; a rejected PR can be cleaned up without a merge.

Follow only the applicable sections. Repository `AGENTS.md` and steering take precedence.
For verification, consult the root `AGENTS.md` Verification section and the workflows covering the changed paths.
Before writing a PR or planning a repair, consult `.kiro/steering/delivery.md` for slice budgets and red-main handling.
For local iteration, temp-storage requirements, and remote-only race coverage, use `docs/development-iteration.md`.

## Triage

1. Fetch current main and record its SHA, the PR head SHA, state, base, and changed paths. Check for dependencies on other PRs.
2. Read the changed behavior with its relevant callers and tests. Compare against current main, not just the original PR base: distinguish a real issue, an already-fixed issue, and a false positive.
3. Use focused reproduction or existing contract tests to assess the claimed defect and proposed remedy. Passing tests alone do not establish a benefit; label untested risks as inference.
4. Recommend merge, reject, or a separate replacement patch. State dependency order and CI readiness separately from technical validity.

Triage is complete when the verdict is supported by current-source evidence and any verification gaps are explicit. Remote mutation is a separate operation.

## Delivery

### Closed set and completion

- Record the target PR numbers and completion condition before remote mutation. For "all currently open PRs", snapshot the initial list; newly arriving PRs are reported separately and enter the run only when the user expands scope.
- Include only the smallest blocking repairs needed to deliver those targets, linked to the target and failing gate. Existing fixes on current main take precedence over new repair work.
- Before materially expanding a repair plan, adding further slices, or continuing repeated remediation that is not converging, explain the blocker and smallest next action and obtain the user's decision. Ordinary waiting for applicable CI is not scope expansion.
- Finish when the target set and necessary blocking repairs are merged and merged-main verification is complete. Report new arrivals and deferred work without adopting them; perform cleanup only within its requested scope.

### Sequence and history

- Establish predecessor order before submitting or merging dependent PRs.
- Measure each slice against its intended base using the delivery-report workflow in `docs/development-iteration.md`; review budget signals, dependencies, immediate consumers and independent build evidence before submission.
- Update a branch from main only when needed to merge, as specified by delivery steering; avoid routine rebases that invalidate otherwise current evidence.
- After a predecessor is squash-merged, fetch main and transplant only the successor's unique commits. Confirm the old predecessor tip before using:

  ```sh
  git rebase --onto origin/main <old-predecessor-tip> <successor-branch>
  ```

- Recheck the resulting diff and affected tests. If history was rewritten, push with `--force-with-lease` and obtain checks for the new head SHA.

### CI and merge gate

- Inspect checks for the latest head SHA and newest applicable workflow runs. A push can briefly show no checks; absence is not success.
- Inspect failing job logs and reproduce where supported. Failures also present on main belong in separate repair PRs under delivery steering, not in the feature slice.
- For skipped or bypassed jobs, inspect workflow scope detection. Accept a bypass only when that workflow intentionally permits it for these paths; failed scope detection is a blocker.
- Wait using a blocking check watcher rather than launching repeated background checks:

  ```sh
  gh pr checks <pr> --watch --interval 20
  ```

- Immediately before merge, refresh the head SHA, mergeability, required checks, approvals, and unresolved review findings. Satisfy the actual branch-protection/ruleset requirements without bypasses; `mergeStateStatus` alone is not a merge gate. Pending, stale, missing required checks or unknown requirements block merging.
- Merge in the approved order using the repository's established method and bind the operation to the inspected head SHA (`gh pr merge --match-head-commit <sha>`). If the head changes, revalidate before merging. Confirm the merged state and merge commit before proceeding to the successor.

### Merged-main verification

Update the clean main receiver with a fast-forward and verify the merged revision using the repository's applicable checks.
For runtime-affecting changes, include the relevant runnable-distribution and full-path smoke evidence, not just unit tests or CLI help.
Name the smoke topology: in-process harness, independent emulators, or spawned distribution.
Report merged-but-unverified separately from verified delivery; a green feature branch is not merged-main evidence.

## Cleanup

1. Identify exact worktree paths, local branches, and temporary refs created for this task or explicitly selected by the user. Similar names are not proof of ownership; leave other sessions' resources alone.
2. Check each worktree for staged, unstaged, and untracked changes and active users before removal. Preserve dirty or in-use worktrees and report the blocker.
3. Remove clean, unused worktrees with `git worktree remove <absolute-path>` without force. Review-only cleanup needs no merge; delivery cleanup follows merged-main verification unless the user explicitly requests otherwise.
4. Delete local branches only after checking for unique work and other worktree users. Prefer `git branch -d`; squash merges can fail its ancestry check, so verify equivalent delivered work before any explicitly authorized forced deletion. Preserve unmerged work without explicit discard authorization.
5. Remove only task-owned, unused temporary refs, then verify the exact resources are absent from Git metadata and the filesystem. Broad fetch/prune operations are not needed for ordinary task cleanup.

If an OS lock prevents removal, report the remaining path and registration rather than claiming cleanup succeeded.

## Report

For approved-scope completion (including earlier workstreams and manually
dispatched required verification), apply `docs/agent-closeout.md`; PR status alone
does not reconcile issues/specs or owned resources. Report submission, merged,
verified and fully closed-out states separately within the user's authorization.

State the outcome for the requested operation, the relevant SHAs, verification evidence and gaps, and any remaining resources or blockers. Separate recommendations from actions taken.
