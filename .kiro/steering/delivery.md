# Delivery (Steering)

AIProxer competes in a fast-moving market. Time-to-merge is a product requirement, weighed against every other requirement. A ~1,000-line feature takes about one working day of agent time, from issue to merge. When work runs longer, it is out of proportion: cut scope rather than add proof.

The leading word is **slice**: the smallest change that delivers observable value, degrades safely, and can merge on its own. Every issue, spec, task plan, and PR is shaped as a slice.

## Slice First

- Start from the simplest safe behaviour. For advisory or fail-open features, that means the V1 may return `unknown`, skip, or fall back to existing behaviour wherever the full answer is costly.
- Write the V1 slice and a **Deferred** list. Every deferred item becomes a one-paragraph follow-up issue, not a requirement.
- These are deferred by default; they enter V1 only when the user explicitly asks for them in the issue:
  - optional remote/external service integrations;
  - durability across restart, cross-node coordination, leases, or new persisted state for an advisory feature;
  - parity for secondary execution paths (for example wire/large-body) when the primary path delivers the value;
  - new extension stages, planes, or SDK contract surface when an existing seam can carry the feature;
  - metrics or diagnostics beyond a few counters on the decision itself;
  - operator docs beyond a config example and a short README section.
- A substrate feature (one whose value comes from other features consuming it) ships with its first real consumer, in the same PR or the immediately following one.

## Budgets

| Artifact | Budget |
| --- | --- |
| GitHub issue body | about one page (~60 lines): goal, V1 slice, deferred list, done criterion |
| Spec | at most 5 requirement areas and 25 acceptance criteria; `design.md` at most 300 lines; at most 12 leaf tasks |
| PR | at most ~1,500 lines of non-test Go and ~40 changed files |
| Tests in a feature PR | at most ~2x the production lines they cover |

When a budget is exceeded, split the work into slices and report the split. A budget is a design signal, not a gate to argue past: the slice is wrong, not the budget.

## Tests Serve The Slice

Test policy lives in `testing.md` (Test Proportionality). In short: tests prove the shipped behaviour; testing is never a requirement or a standalone task; new architecture checks extend existing generic rules.

## Red Main And Unrelated Failures

- Required checks (branch protection) gate merge. Other checks inform: a red non-required check is fixed in the branch only when the branch caused it.
- A failure that also reproduces on `origin/main` is not owned by the feature branch. Record the failing command in the PR body and continue the slice. When it blocks a required check, add the test to `.github/test-quarantine.txt` with its tracking issue (format and validation: `go run ./tools/devcheck -task=quarantine`); the fixing PR removes the entry.
- Fixes to shared gates, flaky tests, or other features go in their own small PR from `main`, then the feature branch rebases. A feature PR contains only its slice.
- Update a feature branch from `main` only when needed to merge: for conflicts. Staying current with `main` is automated for auto-merge PRs (root `AGENTS.md`, Merging).

## Large-Change Overrides Are Maintainer-Only

The change-size overrides described in the root `AGENTS.md` are applied by the maintainer, never by an agent. When the size gate fires, stop, propose a split into slices, and report.
