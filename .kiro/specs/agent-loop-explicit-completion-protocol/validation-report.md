# Validation Report

- DECISION: NO-GO

Feature-level certification of `agent-loop-explicit-completion-protocol` (all 12 task groups, 34
sub-tasks, 118 Go files vs merge base `1fc49fe2`). Every mechanical gate is green and the artifact
boots, but feature-level GO requires the integration, coverage and enforcement checks to pass too, and
three of them do not.

## MECHANICAL_RESULTS

- Tests: **PASS** — `make quality-checks` exit 0; `make test` exit 0 with exactly 380 package results
  `ok` and zero `FAIL` tokens (re-verified by counting the saved log).
- TODO/TBD/FIXME grep over the 118 changed Go files: **CLEAN** (0 matches).
- Secrets grep: **CLEAN** — the single hit is `executor_open_attempt.go:824 Session.ResumeToken = ""`,
  which clears a token rather than embedding one.
- Smoke boot: **PASS** — `go build ./cmd/lipstd` exit 0; `lipstd --help` exit 0;
  `lipstd check-config` on the new preferred example reports `configuration is valid` exit 0;
  `lipstd routes` exit 0 builds the real host, composes the ALG generation and resolves
  `effective_default_route: alg-preferred-local:stub-default`.
- Gates that could not run here, reported not skipped: `make test-cost` (Windows-authoritative),
  `make test-db-parity` PostgreSQL half (no DSN), strict Linux race (nightly only).

## INTEGRATION

- Cross-task contracts: **CONSISTENT** — all four shared seams verified against real code. The
  SDK→feature seam is structural rather than merely asserted (`NewCompletionToolProvider()` has
  exactly one caller, `standardplugins/features_install.go:59`, guarded by the strategy). The
  feature-plane→extensions seam matches (`RequestRuntimeSnapshot.ControlToolProvider{Identity}`).
  The two completion-evidence fields are independent by construction.
- Shared state consistency: **CONSISTENT** — the steering overlay identity is now feature-owned;
  generic core names it no more.
- Boundary audit: **HELD** — every Boundary Commitment verified against code. Dependency direction is
  clean: `pkg/lipsdk`→`internal/plugins` and `internal/core`→`internal/plugins` are both zero in
  production files, `pkg/lipsdk/controltool` imports only `pkg/lipapi` and three other `lipsdk`
  packages, and a non-test grep for `agentloopguard`, `agent-loop-guard`, `agent_loop_guard`,
  `attempt_completion`, `alg-proto`, `alg-state`, `alg-rec` and `semantic_verifier` across
  `internal/core` returns **zero hits in all eight tokens**. The one pre-existing violation was
  deleted rather than allowlisted.

## COVERAGE

- Requirements mapped: **72 of 79** numbered acceptance criteria fully covered.
- Coverage gaps (7 partial, none wholly uncovered):

| Req | Gap | Owner |
|---|---|---|
| 1.6 | The two new example YAMLs are validated only by `TestConfigExamples_passInspectRoutes`; `make example-config-check` runs a selector naming a test that does not exist, so the CI gate proves nothing | UPSTREAM |
| 1.7 | Mixed-strategy rejection is implemented for enabled rows only. `internal/featurebundle/merge_surface.go:38` skips a row-level `enabled: false` before the factory runs, so its `config:` block is never decoded and never validated | UPSTREAM |
| 7.7 | No ALG-scoped test makes the platform **reject** a protocol-repair continuation and asserts the ALG accepts the conservative outcome without a second continuation. Every e2e cell wires admission to succeed | **LOCAL** |
| 10.3 | Newly-admitted turns are covered; the **in-flight pinning half is not**, by the cell's own documented choice | **LOCAL** |
| 10.6 | No-durable-state is proven by absence plus one `Extensions`-empty assertion, not behaviourally | **LOCAL** |
| 11.4 | Trace/lineage half covered; auxiliary **usage** attribution is vacuous because `internal/core/billing`'s `WorkloadIdentityFromAuxiliaryRole` allowlist has no verifier-role entry, so the request fails closed before provider admission | UPSTREAM |
| 12.7 | `make qa` is not claimed green and the strict race target is genuinely red (see OWNERSHIP) | mixed |

- Cross-cutting: 3.1/3.2/3.5 with 4.1-4.7, 6.3/6.4/6.7 with 11.3/11.5, 9.5, and 5.7 are fully covered
  across task boundaries. 1.7, 10.3 and 7.7 are not.

## DESIGN

- Architecture drift: **present, medium severity, all of it generic and ALG-free**
  - `internal/core/runtime/interleaved_stream.go` (+534/-66), `executor_settlement.go` (+342/-19),
    `executor_recv_loop.go` (+294/-29): a concurrency rework of shared streaming and settlement
    paths well beyond "generic integration", on seams the design itself lists as revalidation
    triggers.
  - `internal/infra/metering/journalstore/observation_store.go:333-389` moves observation identity
    resolution from pre-insert lookup to post-insert, changing replay/collision classification for
    **all** observation writes — entirely undeclared and outside the ALG surface.
  - A generic terminal-decision `Actions`/`ActionCount` projection change
    (`terminal_decision_evidence.go:146-259`) affecting every terminal provider, unamended in design.
  - Lower: new `EffectiveReplacement` field, largebody plane-count bump 27→28, the four adapters'
    `RoleDeveloper` encoding, and new race-check partitioning.
- Dependency direction: **clean**, no upward imports.
- File Structure Plan vs actual: **mismatch in both directions**. Planned-but-absent:
  `agentloopguard/protocol.go` (superseded by `protocolpolicy` + `preferred_provider` +
  `protocolstate`), and the planned new `response_pipeline_*` files (became `control_call_capture.go`,
  `response_control_interception.go`, `response_pending_completion.go`). Undeclared-but-present:
  `agentloopguard/completiontool_handle.go`, `pkg/lipsdk/controltool/{doc,errors}.go`,
  `internal/core/largebody/{authority_gate,eligibility}.go`,
  `internal/core/extensions/completion_run.go`, `internal/qa/race_check_partition_contract_test.go`
  + `scripts/race-check.sh`, the `RoleDeveloper` adapter work, and journalstore.
- Revalidation triggers: four fired (tool-call assembler order; conversation-view/PTB ordering;
  terminal-decision evidence contract; large-payload plane census) and revalidation was **genuinely
  performed**, not merely claimed — the task 1.1 characterization test predates the shared-ordering
  edits, and task 12.2 re-ran it.

## ENFORCEMENT GAP (the decisive finding)

Task 12.1 delivered ten ratchets with negative fixtures for the design's eight Architecture Ratchet
bullets, and all ten genuinely fire. But **three of the eight are only partially enforced**, for one
shared reason:

`ScanAgentLoopGuardOwnershipViolations` builds its `featureFiles` index with
`if algFeatureRootFile(rel)` (ownership_ratchet.go:266), and `algFeatureRootFile` requires
`PackageDirFromRel(rel) == "internal/plugins/features/agentloopguard"` (line 285). The index therefore
contains **only root-package files**. The terminal-owner census and both strategy-isolation walks
consume that index, so they never see `verifier/`, `progress/`, `causepolicy/`, `protocolpolicy/` or
`protocolstate/`. Consequences:

- Bullet 5 (no second terminal owner): a `Decide` method introduced in `protocolpolicy/` leaves the
  census green.
- Bullets 7 and 8 (no verifier reachable from preferred; no control provider reachable from legacy):
  the walk follows only same-package plain functions and stops at any selector call, so a verifier
  call inside `protocolpolicy.Evaluate` reached from the preferred strategy case body is invisible —
  which is the highest-risk direction for this rule.

Separately, bullet 4 (no direct client/A-leg append) lists only `Messages` and `Items` in
`algForbiddenClientAppendFields` (line 135) and scans only the feature tree, so it cannot see the real
canonical mutation site at `pkg/lipsdk/controltool/projection.go:125-131`, which also mutates
`out.Instructions` and `out.Tools`. That site is the approved generic owner, so this is a scope-of-
enforcement gap rather than a live violation.

## OWNERSHIP

- **LOCAL**: requirement 7.7's missing negative test; 10.3's in-flight half; 10.6's behavioural
  proof; and the 12.7 reporting gap for `make qa`.
- **UPSTREAM**: requirement 1.6 (broken `make example-config-check` selector);
  1.7 (`internal/featurebundle` row-enable skip); 11.4 (`internal/core/billing` auxiliary-role
  allowlist); and the red strict-race target, caused by a genuine data race at
  `internal/core/billing/billing_seam_equivalence_test.go:851` — maps allocated at 824-825 and mutated
  from `t.Parallel()` subtests with no mutex. That file is byte-identical at merge base and HEAD
  (`git diff --stat` = 0, zero commits in range, last commit `6e97e0d9` a verified ancestor of the
  merge base), and the whole-package race log contains exactly **one** `WARNING: DATA RACE`, at that
  file — so no branch-introduced race exists anywhere.
- **UNCLEAR**: none.

- UPSTREAM_SPEC: `internal/featurebundle` (1.7), `internal/core/billing` (11.4 and the race),
  repository Makefile/CI (1.6). These are shared-platform concerns, not this feature's; they should be
  repaired by their owners and this feature revalidated afterwards.

## BLOCKED_TASKS

None. No task carries a `_Blocked:_` annotation, and every sub-task is `[x]`.

## REMEDIATION

Ordered by what unblocks the most.

1. **Widen the feature-file index** so the census and both strategy-isolation walks cover the feature's
   subpackages (use the existing `algFeatureTreePackage` predicate, ownership_ratchet.go:291, which
   already includes them), and add a negative fixture placing a `Decide` receiver — and a verifier
   call reached through a subpackage function — in `protocolpolicy/`. This closes bullets 5, 7 and 8.
2. **Add requirement 7.7's negative test**: make the platform reject a protocol-repair continuation
   and assert the ALG accepts the conservative outcome without a second continuation, and without
   publishing a result.
3. **Decide and close 10.3's in-flight half**, or record it as an explicit, owned follow-up rather than
   a silent deferral. The current state — a cell that documents what it deliberately does not assert —
   is honest but leaves the requirement partial.
4. **Give 10.6 a behavioural proof** rather than an absence argument.
5. **Reconcile the File Structure Plan** in design.md with what was actually built: record the
   subpackage decomposition that replaced `protocol.go`, the four adapters' `RoleDeveloper` encoding
   and the Anthropic/Gemini lossy coercion, the largebody plane bump, the terminal-decision `Actions`
   projection change, the journalstore identity-resolution change, and the race-check partitioning.
6. **Route the three UPSTREAM items** to their owners: the `make example-config-check` selector, the
   billing auxiliary-role allowlist, and the racy billing test fixture. This feature should not patch
   them.
7. Before delivery: apply the `allow-large-change` PR label (118 changed Go files against a 100 limit,
   sanctioned by AGENTS.md) and let CI's PostgreSQL parity job confirm `internal/infra/metering/journalstore`,
   which this branch changed and whose PostgreSQL half could not run here.