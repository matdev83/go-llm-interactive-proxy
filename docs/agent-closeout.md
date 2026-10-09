# Approved-scope closeout

Completion covers the **whole approved scope**, not the last branch or a clean
current worktree. Keep one explicit session inventory beside the controller's
existing handoff execution index. Add objectives when scope expands; record
owned resources when creating them and required workflow runs when dispatching.
The inventory is data, not permission to merge, close an issue or delete anything.

## Inventory and evidence

Use version 1 with a session `id`, nonempty `objectives`, and exact `worktrees`
and local `branches` arrays. Each objective names its ID, requested `delivery`
(`submitted` or `merged`), PR numbers and **actual published head SHAs**, and any
issue, archived spec path, required runs and merged-main evidence. An objective
must declare at least one real deliverable or verification obligation.

Required runs declare run ID, exact head, required job/step names and any absolute
downloaded artifact paths. A run-level success cannot substitute for a skipped
required step. Artifact marker files must contain the expected `tested_sha`.
Missing, pending, failed, cancelled or stale evidence remains owned work until
resolved or explicitly removed from scope by the user—not silently forgotten.

Minimal shape (replace illustrative numbers/hashes/paths with actual evidence):

```json
{
  "version": 1,
  "id": "approved-work",
  "objectives": [{
    "id": "feature",
    "delivery": "merged",
    "prs": [{"number": 123, "head": "<published head SHA>"}],
    "issue": 120,
    "spec": ".kiro/specs/archive/example/spec.json",
    "main_evidence": "/absolute/scratch/main-result.json",
    "runs": [{
      "id": 456,
      "head": "<tested SHA>",
      "steps": [{"job": "race-fuzz (broad)", "name": "Tier-1 fuzz smoke"}],
      "evidence": ["/absolute/scratch/race-evidence.txt"]
    }]
  }],
  "worktrees": ["/absolute/container/worktrees/owned-task"],
  "branches": ["feat-owned-task"]
}
```

Merged-main evidence is an existing validated handoff result with real successful
commands/logs and its tested source identity. The tested main revision must
contain each declared PR merge. Evidence presence/identity is mechanical; the
reviewer still decides whether its scope proves the requested behavior.

## Reconcile in order

1. **Delivery:** use `make pr-status`/`make pr-watch` and the delivery skill within
   authorized scope. Record final heads after rebases and actual merge SHAs.
2. **Verification:** wait for registered runs using existing blocking watchers;
   inspect actual job/step results and downloaded artifacts. Attribute failures
   before repairing; shared/baseline fixes stay in separate slices.
3. **Issue/spec closure:** verify issue state explicitly, especially for stacked
   PRs. Archive completed specs through the archive skill, preserving approvals
   and Deferred scope; then verify the archive on merged main.
4. **Merged-main evidence:** run applicable scoped checks and real CLI/service
   smoke. Record source identity and successful command evidence.
5. **Owned cleanup:** verify clean/no-user/no-unique-work conditions before
   removing only the explicitly owned paths/refs under the delivery skill.

Check the complete inventory read-only:

```sh
go run ./tools/handoff -task=closeout -file=/absolute/scratch/session.json
```

Or link it while recording a normal task result with
`-inventory=/absolute/scratch/session.json`, then use
`-task=closeout -index=/absolute/scratch/execution.json` after restart. Other task
entries and the inventory link are preserved by atomic index updates.

The JSON report names every unresolved obligation; nonzero exit blocks a complete
claim. `-phase=submitted` checks publication/run obligations without requiring
issue/spec closure or resource deletion, and **always reports `complete: false`**.
Use it when only PR submission is authorized. It grants no further authorization.

The checker runs only fixed read-only Git/GitHub queries and file reads. It cannot
infer omitted objectives or ownership, validate semantic test adequacy, confer
review approval, mutate remote state or perform cleanup. Those remain explicit
controller/reviewer/user responsibilities. Keep artifacts outside the repository.
