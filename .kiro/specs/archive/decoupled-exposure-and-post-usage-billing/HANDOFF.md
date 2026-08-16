# Handoff — decoupled exposure and post-usage billing

This file records the completed implementation state for a fresh agent with no prior session context. Work remains on the existing feature branch; do not create a new worktree or switch to `main`.

## Current state

The user explicitly authorized execution of **all phases/tasks**, overriding the original 1.4-only handoff scope. Phase 0 through Phase 7 are implemented and committed on `feat/decoupled-exposure-and-post-usage-billing`; the archived `tasks.md` marks every task `[x]` and `spec.json` is `phase: completed`.

- Phase 6.4 removed retired hold/authorization-book ownership from the normal billing path and added final schema-retirement/reconciliation handling.
- Phase 7.1 activated deletion/import/runtime architecture ratchets.
- Phase 7.2 added SQLite concurrency/crash-replay certification and PostgreSQL integration gating.
- Phase 7.3 updated steering and billing host-composition/failure-posture documentation.
- Phase 7.4 activated shrinkage certification and completed the final focused verification pass.

Do not revert the implementation to the earlier hold-based-only state or follow the obsolete 1.4-only instructions below.

## Final verification

Passing locally (Phase 7 review remediation pass):

- `go test ./internal/archtest ./internal/core/billing ./internal/infra/billingstore ./internal/infra/billingadmission ./internal/infra/billingcompose ./internal/infra/runtimebundle -count=1`
- `make docs-check`
- `go run ./cmd/lipstd --help`
- Dialect-shared `TestSQLiteBillingStoreContract` now covers exposure admission, independent usage append/replay, and call settlement (including actual>max reconcile).

Environment limitations:

- `TestPostgresBillingStoreContract` (`-tags=integration`) **skipped** here: `LIP_REQUIRE_POSTGRES=1` and `LIP_TEST_POSTGRES_DSN` were not set. Do **not** claim PostgreSQL certification until that gate is run against a live DSN. The shared contract helpers are wired so Postgres runs the same exposure/usage/settlement path when enabled.
- The broad `go test -race` runs for `internal/core/runtime` and `internal/infra/runtimebundle` now pass after synchronizing terminal tool-classification cleanup and removing concurrent `BuildOptions` mutation.
- The combined billing/runtime race suite also passes; the SQLite journal reversal retry budget was expanded to tolerate race-detector contention without returning transient busy errors.
- The complete feature branch exceeds the 100-path PR limit vs `main`; publishing requires stacked PR/base-branch handling.

## Review follow-up

A post-implementation Phase 7 adversarial review rejected checkbox-only certification. Remediation (this pass) closed those findings:

- Project memory (`AGENTS.md`, `.kiro/AGENTS.md`, `product.md`, `structure.md`) and `docs/billing-host-composition.md` now teach cheap credit → exposure admit → terminal usage → settlement, and document that `authorization_holds` is dropped after open inventory is empty.
- `SpendableNano` is Balance−CreditFloor only; ready accounts fail closed on nonzero `reserved_nano`; rebuild always writes `reserved_nano = 0` and no longer re-materializes reserved from authorization-book journals.
- Schema deletion ratchets expand migration-only inventories to production package files, allowlist explicit legacy-recovery readers for `authorization_holds`, forbid spendable reserved subtraction, and target `JournalBookLegacyAuthorization` on call-path writers.
- Dialect-shared store contract proves exposure admit, independent usage, and call settlement on SQLite (and on Postgres when the integration gate is configured).

Earlier post-implementation fixes retained:

- Provider-cost polling now consumes an indexed durable `provider_cost_work` queue instead of listing and re-rating all historical calls every tick. Leg append and queue enqueue are atomic; successful or idempotent provider-cost application marks the work processed, while unreconciled work remains pending for retry.
- Call closures now capture the minimum terminal-leg start and maximum terminal-leg finish observed by runtime, falling back to the terminal timestamp only when no leg span exists.
- Complete-call claims check `RowsAffected`; a lost pending claim returns `billing.ErrCallClaimConflict`, while already processed calls remain safe idempotent replays.
- Allocated B-legs that never open now append explicit `LegOutcomeNeverStarted` independent usage rows, including parallel losers, so frozen call closures remain joinable and can settle.
- Customer settlement journals now use the public `billing.CustomerSettlementSourceKey` contract; regression coverage verifies the journal source key matches the helper.
- Provider-cost work now requires the durable queue port at composition time, uses indexed exponential retry backoff, retains orphaned leg work until its call closure appears, and prunes old processed queue metadata while preserving operation snapshots.
- Failed terminal call or leg appends now enqueue their sealed payloads into a durable `usage_append_outbox`; an independent lifecycle worker retries those appends with backoff and marks replay conflicts terminal. Authoritative composition requires this outbox and wraps runtime appenders without reopening provider execution.

Regression coverage includes bounded provider-cost polling across multiple batches, queue SQL joins/limits/orphans, backoff and pruning, migration backfill/retry idempotency, never-started closure joining, closure-span derivation, claim conflicts, public settlement-key identity, conflicting settlement after exposure closure, docs/steering forbid markers, reserved-zero rebuild, and dialect-shared exposure/settlement contract coverage. The focused billing packages above pass after these remediations.

## Immediate follow-up

Implementation and local certification of the Phase 7 review remediations are complete on SQLite/docs/archtest surfaces. If publishing this feature, preserve the phase-sized commits and use stacked PRs or merge phase boundaries so each PR remains within the repository's 100-path policy. Do not use `git add -A`, do not claim PostgreSQL certification without rerunning `go test -tags=integration ./internal/infra/billingstore -run Postgres` with a live DSN, and do not push without explicit authorization.

## Historical task handoff

The original session began as a task-1.4-only implementation. The remaining historical instructions below are retained for context, but the current user authorization and the completed Phase 0–7 state above take precedence.

## Immediate assignment (historical)

The user originally asked: **`/kiro-impl Phase 0 and Phase 1`**.

- **Done at the original handoff:** 0.1–0.4 and 1.1–1.3.
- **Remaining at the original handoff:** task 1.4 only.
- **Superseded:** the original instruction not to start Phase 2–7.


## Where to work

| Item | Value |
|---|---|
| Machine | Windows 10, PowerShell |
| Repo root (this is the worktree) | `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-decoupled-exposure-and-post-usage-billing` |
| Git branch | `feat/decoupled-exposure-and-post-usage-billing` |
| HEAD at handoff | `600692bf` `feat(decoupled-exposure-and-post-usage-billing): add call-closure spool join` |
| Do **not** work on | `main` |
| Other worktrees | None required. Stay in this directory. |

Dirty file (unrelated, **do not commit** unless the user asks):

- `.kiro/specs/decoupled-exposure-and-post-usage-billing/spec.json` (approval/`updated_at` metadata only)

## Spec (approved, ready to implement)

Feature: `decoupled-exposure-and-post-usage-billing`

- `spec.json`: `phase: tasks-generated`, all of requirements/design/tasks **approved**, `ready_for_implementation: true`, language `en`
- Requirements: `.kiro/specs/decoupled-exposure-and-post-usage-billing/requirements.md`
- Design: `.kiro/specs/decoupled-exposure-and-post-usage-billing/design.md`
- Tasks: `.kiro/specs/decoupled-exposure-and-post-usage-billing/tasks.md`
- Also useful: `research.md`, `gap-analysis.md`, `design-review.md`

Steering (still describes the **old** hold+TUR invariant until task 7.3; **the spec is source of truth** for this branch):

- `.kiro/steering/product.md`, `tech.md`, `structure.md`, `testing.md`, `routing-and-orchestration.md`

Kiro / repo rules: root `AGENTS.md`, `.kiro/AGENTS.md`.

Target flow (not yet the live path):

```text
cheap credit screen → route + pessimistic quote → atomic open-exposure insert
  → execute with no billing/exposure mutation
  → durable per-leg usage + call closure
  → customer settlement + exposure close
  → independent per-leg provider COGS
```

Live path is still hold-based. Shadow is allowed; **exactly one** mechanism may mutate authoritative money. Holds stay until Phase 2 cutover / Phase 6 deletion.

## How to execute (kiro-impl)

Follow `C:\Users\Mateusz\.cursor\skills\kiro-impl\SKILL.md`.

For **1.4** (task numbers given): implementer TDD → independent reviewer (`kiro-review`) → parent verifies with **fresh** `go test` (`kiro-verify-completion`) → mark `[x]` in `tasks.md` → **selective commit**. Then stop.

Hard rules:

- TDD: RED then GREEN. Capture RED output.
- Do **not** let the implementer update `tasks.md` or commit.
- Parent-only commit. **Never** `git add -A` / `git add .`. Stage explicit paths.
- Do **not** include `spec.json` in the commit.
- Max 2 implementer remediation rounds after REJECTED, then debug.
- Do not use `git checkout .` / `git reset --hard`.
- PR/commit size: **100 files** max. 1.4 should be far under that.
- Windows: bash HEREDOC commit messages fail in PowerShell. Write the message to `$env:TEMP\lip-commit-msg.txt` (utf8NoBOM) and `git commit -F`.
- Commit message style used on this branch: `feat(decoupled-exposure-and-post-usage-billing): <why>`

Suggested 1.4 message:

```text
feat(decoupled-exposure-and-post-usage-billing): require durable usage spool

Fail closed in authoritative composition unless the store can append independent call and leg usage records, and keep memory spool for tests/non-authoritative hosts only.
```

Skills for 1.4: `kiro-impl`, `kiro-review`, `kiro-verify-completion`, repo `golang-testing`, `golang-hexagonal-architecture`, `golang-dependency-injection` (composition wiring). Repo steering overrides generic “no ORM” database skill — this repo uses Bun in `internal/infra/billingstore`.

## Task 1.4 — exact remaining work

From `tasks.md`:

- **1.4 Require a durable usage spool in authoritative composition.**
  - Keep memory spool only for tests/non-authoritative mode; prove post-output append failure cannot trigger provider retry.
  - _Boundary:_ composition root / runtime terminal seam
  - _Depends:_ 1.2, 1.3 (both `[x]`)
  - _Validation:_ `go test ./internal/infra/runtimebundle ./internal/core/runtime ./internal/infra/billingstore`
  - _Requirements:_ 7.3, 7.5-7.6, 8.7-8.10, 15.8, 17.4

### What 1.4 must do

1. **`ComposeBilling`** (`internal/infra/runtimebundle/billing_compose.go`) currently requires `UsageRecordAppender`, `PostTurnStore`, `HoldReleaser`, `AccountProvisioner`, `AuthorizationLookup`, `AuthorizationStore`. **Keep hold requirements** (cutover is 2.4 / 6.x). **Add** fail-closed if the store does not implement:
   - `billing.CallUsageAppender`
   - `billing.CallLegUsageAppender`
2. **`BuildHost` / `build_executor.go`** authoritative path already assigns `BillingTerminalHandoff` from the store as `UsageRecordAppender`. Also wire **`CallUsageAppender` from the same durable store**. Do not default authoritative mode to `billing.NewMemoryHandoffOutbox` for the new spool.
3. Tests: memory-only / missing spool **cannot** satisfy authoritative composition.
4. Tests: post-output append failure must **not** Open another provider attempt (7.3, 8.8). Runtime already has `TestCallUsageAppenderGateReplacementAppendFailureDoesNotRetryProvider` in `internal/core/runtime/billing_call_closure_test.go`. Keep/extend; add a runtimebundle seam test if needed.
5. Stock **`lipstd` does not call `ComposeBilling`**. Public `pkg/lipruntime.Options` stays non-money. `go run ./cmd/lipstd --help` must still work (15.8).
6. 8.10: document in godoc/test that simultaneous total loss of all durable replicas before any append is outside the guarantee.

### What 1.4 must NOT do

- Replace `billingTurnCollector` (task 5.1)
- Cut admission from holds to exposure (2.4)
- Delete holds / flip `forbid_hold_symbols` or `require_net_loc_reduction` (7.1 / 7.4)
- Rewrite post-turn customer settlement (3.4)
- Make `lipstd` invent billing accounts

### Key existing seams

- `DurableStore.AppendCallLegUsage` → table `usage_leg_records` (not nested TUR `leg_usage_records`)
- `DurableStore.AppendCallUsage` / `JoinCompleteCall` / `ClaimCompleteCall` → table `usage_call_records`
- Runtime `BillingRuntime.CallUsageAppender` is additive, nil = no-op
- Call-closure is split from the TUR command filter so `CommandPanic` / `CommandGateReplacement` still seal a `CallUsageRecord` without driving provider retry
- 0.1 characterization: `internal/infra/runtimebundle/brownfield_compose_characterization_test.go` still asserts hold release/lookup. **Add** spool requirement alongside; do not remove hold freeze.

### LOC / archtest ratchet (update if production lines change)

- Lock file: `internal/archtest/testdata/architecture/billing_exposure_deletion_baseline.json`
- At 1.3: `baseline_total` **12367**; `forbid_hold_symbols` **false**; `require_net_loc_reduction` **false**
- `internal/core` LineBudget in `internal/archtest/budgets.go`: **76363** (measured+25). Bump the same way if `TestLineComplexityBudgets` fails.
- After production edits, run: `go test ./internal/archtest -run "BillingExposure|TestLineComplexityBudgets"`
- Earlier full `go test ./internal/archtest` failed on a **stale** 75303 ceiling; that is already fixed. Do not revert the budget.

Smoke: `go run ./cmd/lipstd --help`

## Already shipped on this branch (do not redo)

| Task | Commit | What landed |
|---|---|---|
| 0.1 | `eef803a1` | Brownfield characterization tests + deletion/LOC JSON lock |
| 0.2 | `9be2fb9e` | Pure `SettledHeadroom` / `SafetyMargin` / `CallExposure` in `internal/core/billing/exposure.go`. Live hold path unchanged. |
| 0.3 | `1675db3e` | `BillingCallID` (`bc_` + 32 hex), `stampBillingCallID` on `preparedRequest`. Hold TUR/AuthorizationID still A-leg based. |
| 0.4 | `3afc7fa8` | Planned ratchets in `internal/archtest/billing_exposure_ratchet.go`. Flags stay false. |
| 1.1 | `b85bc5ad` | Independent `CallUsageRecord` / `CallLegUsageRecord` + fingerprints. Nested TUR `LegUsageRecord` unchanged. |
| 1.2 | `a59ee774` | Bun `usage_leg_records`, `AppendCallLegUsage`, `LegOutcomeRejected` / `LegOutcomeNeverStarted` |
| 1.3 | `600692bf` | Bun `usage_call_records`, complete-call join, request-terminal freeze of expected B-legs |

Implementation notes (also in `tasks.md`):

- 7.1 must update `TestBillingExposureDeletionTargetsCurrentlyExist` (still requires `present=true`) when flipping `forbid_hold_symbols`.
- Independent **runtime** leg append (not just store API) is **5.1**.
- `CallExposure.CallID` is still a `string` (not `BillingCallID` typed) unless a later task changes it.
- 1.3 review initially **REJECTED**: Panic/GateReplacement skipped closure; NextBLeg never-opened IDs unproven; swallowed-attempt freeze unproven. Remediation is in HEAD. Do not regress those tests in `billing_call_closure_test.go`.

## After 1.4 (only if the user then asks to continue)

Next pending tasks in `tasks.md`, in order:

- **Phase 2:** 2.1 cheap credit screen → 2.2 quote-only path → 2.3 atomic Bun `CallExposure` admit → 2.4 cut authoritative admission from holds to cheap-screen + exposure (shadow, then **exactly one** hard-credit authority)
- **Phase 3:** customer post-usage + exposure close (3.1–3.4)
- **Phase 4:** independent provider COGS (4.1–4.4)
- **Phase 5:** runtime terminal simplification / collector deletion (5.1–5.4)
- **Phase 6:** reconcile, migrate, retire holds (6.1–6.4)
- **Phase 7:** activate ratchets, docs, certification (7.1–7.4)

Autonomous `/kiro-impl` for later phases: one subtask at a time, implementer then reviewer, commit per task.

## Invariants the next agent must not break

- No retry/failover after first downstream content event.
- Core billing must not import Bun, SQL, `lipapi`, or provider SDKs.
- `pkg/lipapi` has **no** `BillingCallID` field (req 2.8).
- Dual monetary authority is forbidden: exposure path must not post authorization journals or mutate `reserved_nano`.
- Default `go test` is SQLite in-process. Postgres is env-gated (`LIP_TEST_POSTGRES_DSN`); do not require it locally.
- `make test-race` skips on Windows.

## Suggested first commands

```powershell
cd C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-decoupled-exposure-and-post-usage-billing
git status --porcelain
git log -8 --oneline
git branch --show-current
```

Then implement **1.4** only, following kiro-impl.
