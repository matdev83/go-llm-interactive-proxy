# Execution control

## Current state

- Worktree: `go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch: `feat/b-leg-usage-economics`.
- Baseline checkpoints: `c985a102` (Phase 1) and `2bd80f25` (Phase 2); baseline source: `d1847d5e`.
- Parent tasks 2.1–2.5 and refinement 2.1–2.2: reviewed and verified SDK contracts. No runtime or monetary integration is implied.
- Phase 1 baseline/RED artifacts are reviewed; its broader unchecked task criteria and certification gaps remain explicit in `phase1-review.md`.
- Three intentional billing/runtime regression failures remain for later implementation owners. Root-suite success is not claimed.
- Parent tasks 3.1–3.4: root-reviewed and accepted; source-isolated normalization, reduction, replay and the family TCK pass focused verification.
- Parent tasks 4.1–4.4: root-reviewed and accepted; durable V2 evidence, valuation/reconciliation projections, atomic composition and rebuild support pass focused dual-dialect evidence.
- Parent tasks 5.1–5.4: root-reviewed and accepted; source-separated B-leg terminal evidence, strict durability propagation and independent all-leg COGS/retail selection pass focused verification.
- Parent tasks 6.1–6.4 and refinement tasks 3.1–3.3: root-reviewed and accepted; final provider-tree input measurement, separate provider/customer output planes, honest unobservables and bounded fast-path behavior pass focused verification.
- Parent tasks 7.1–7.4: root-reviewed and accepted; negotiated V2 connector evidence, V1 partial bridge, host drains, durable coverage dispositions and reusable conformance pass focused verification.
- Parent tasks 8.1–8.5: root-reviewed and accepted; real provider evidence, negotiated V1/V2 authority, multimodal native units, Codex account gauges, auxiliary dispositions and the closed producer census pass focused verification.
- Parent tasks 9.1–9.5 and refinement task 2.3: delegated-review accepted; immutable tariff snapshots, exact component rating, scope-safe tiers, independent E/Q/P valuations, asymmetric multimodal fixtures, replay-safe identities, and canonical effective-charge COGS pass focused verification.
- Refinement group 7 remains open because its dependency on retail policy and non-request resource allocation is intentionally later; parent producer migration does not satisfy those downstream criteria by itself.
- Next implementation: parent task group 10, independent customer policies and submission charging.
- The refinement has 8 groups and its required parent has 20. They overlap; do not report these as 28 independent phases or infer a completion percentage from group count.

## Process correction

The initial phase suffered from broad assignments, incomplete behavioral tests, repeated repair handoffs and redundant review dispatches. Scope mistakes included a lexical safe-field heuristic and an unsupported snapshot-reference inequality. Correctness review remains mandatory; redundant orchestration does not.

1. A separate Luna/max reviewer performs `kiro-review` independently of the implementation author, per the user's later override. Reuse that phase reviewer for bounded remediation checks; re-review the repair delta and affected invariants, not unchanged code.
2. Keep fresh Luna workers at `max`, blocking/sequential, no pool, no concurrent implementation and no interim micromanagement. One worker per phase by default; split a demonstrated oversized or struggling phase into bounded dependent tasks.
3. Before dispatch, the root resolves architecture ownership and writes a compact brief: task IDs, exact ownership, observable acceptance cases, negative cases, dependencies, exclusions and tests. Do not push an unresolved design choice into a broad coding assignment.
4. Freeze accepted shared contracts. Change them only when the next real consumer demonstrates a required defect or missing approved behavior. No speculative aliases, generic frameworks, convenience APIs or review-invented restrictions.
5. Strict TDD remains. Compile RED is valid for introducing a type but does not prove behavioral acceptance. Validators, reducers and adapters require failing behavioral assertions. Check an invariant at all applicable entry points rather than patching one failing fixture.
6. Workers finish with files, exact commands/results, behavioral RED evidence, acceptance-case coverage and residual risks. Self-review includes cross-contract consistency. Keep evidence concise; one updated phase record rather than repeated narrative reports.
7. The root checks the actual diff, runs fresh phase-relevant verification, records the verdict and checkpoints accepted files immediately. Preserve V1 behavior, unrelated worktrees, the 100-Go-file gate, shadow mode and single-writer cutover constraints.
8. Expensive suite, database, race and cost gates run at their approved milestones and final certification. Preserve known baseline failures explicitly; neither repeatedly rerunning them without a relevant change nor relabeling them green is useful.
9. Report accepted behavior, current assignment, actual blockers and next dependency. Waiting is not progress. Do not substitute repeated heartbeat prose for an accurate status ledger.

## Throughput check

For the next two implementation assignments record dispatch/completion times, changed Go-file count, worker verification result, root review duration, repair count and accepted task IDs. Use those measurements to adjust decomposition and provide a grounded estimate; do not promise an unmeasured total completion time.

This document changes execution discipline only. It does not remove requirements, alter approved dependencies, waive phase review or certification, authorize parallel workers, or change the user's model/reasoning override.

### Phase 3 measurement

- Dispatch: `2026-09-12T20:39:04Z`; accepted worker completion: `2026-09-12T21:28:35.8697196Z` (includes one infrastructure stream-disconnect and same-agent resume).
- Worker-owned Go files: 8; worker focused tests/vet: PASS.
- Root review: started `2026-09-12T21:29:22Z`; one bounded review pass, zero repair handoffs.
- Accepted tasks: parent 3.1–3.4.

### Phase 4 measurement

- Dispatch: `2026-09-12T22:38:21Z`; accepted worker completion after one repair: `2026-09-12T23:33:29Z`.
- Worker-owned Go files: 21; worker focused tests/vet: PASS.
- Root review completed `2026-09-12T23:35:33Z`; one consolidated repair handoff for PostgreSQL boolean parity.
- Accepted tasks: parent 4.1–4.4.

### Phase 5 measurement

- Dispatch: `2026-09-12T23:55:46Z`; accepted worker completion after one repair: `2026-09-13T02:49:20Z`.
- Worker-owned Go files: 35; worker focused tests/vet: PASS.
- Root review used one consolidated repair handoff for trusted store identity; no repeated audit loop.
- Accepted tasks: parent 5.1–5.4.

### Phase 6 measurement

- One Luna/max phase worker was dispatched and awaited only through blocking completion events.
- Root issued one consolidated repair for actual provider-tree measurement, durable lifecycle state, disabled-path allocation proof, and no-transaction parallel lineage.
- Root fresh package, adapter, architecture and vet verification passed.
- Accepted tasks: parent 6.1–6.4 and refinement 3.1–3.3.

### Phase 7 measurement

- One Luna/max phase worker was dispatched and awaited only through blocking completion events.
- Root issued one consolidated repair for durable coverage-disposition preservation and empty-V2 old-peer compatibility.
- Root fresh SDK, adapter, billing, billingstore, targeted runtime and vet verification passed; the broad runtime run reproduced only the recorded unrelated cancellation flake.
- Accepted tasks: parent 7.1–7.4.

### Phase 8 measurement

- One Luna/max phase worker was dispatched and awaited only through blocking completion events.
- Root issued three consolidated repair passes for replay/cumulative evidence, V1/V2 authority selection, and architecture-baseline/attempt-ownership regressions.
- Root fresh provider/runtime/SDK/QA/architecture verification and all six changed connector-module suites passed.
- Changed Go files: 85, below the 100-file gate.
- Accepted tasks: parent 8.1–8.5. Refinement group 7 remains open pending its declared dependencies.

### Phase 9 measurement

- The initial rating assignment was too broad and required repeated repair/review cycles across rating, replay, correction graphs, COGS, identity and persistence.
- The final unresolved COGS work was split into five sequential Luna/max assignments: effective coverage, correction taint, overlap validation, cardinality-independent supersession and normalized lineage.
- The same delegated Phase 9 reviewer verified the five repairs together, found one retained-charge coverage composition defect, and approved the focused remediation.
- Fresh affected-package tests, shuffled adversarial tests, `go vet`, SQLite parity, targeted architecture/QA, formatting and diff checks passed.
- Accepted tasks: parent 9.1–9.5 and refinement 2.3.
