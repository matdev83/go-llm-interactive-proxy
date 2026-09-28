# Phase 2 review

## Review Verdict
- VERDICT: REJECTED
- TASK: Parent 2.1–2.5 and refinement 2.1–2.2
- MECHANICAL_RESULTS:
  - Tests: Existing metering/economics package suites PASS with `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics`.
  - Review regressions: FAIL with `go test -count=1 -run '^TestPhase2Review' ./pkg/lipsdk/metering` (exit 1), two tests fail for the defects below.
  - Boundary: New SDK contracts remain unbound; no tracked V1 source changes.
  - Full suite: Not claimed; three deliberately RED Phase 1 regressions remain pending their owning implementation phases.
  - Static checks and remaining contract review: Pending following repair.
- FINDINGS:
  1. Important: `component_key.go` accepts `DirectionNone` for image/audio/video/file media. Refinement design section 5 and parent D2–D3 require explicit flow direction, with none reserved for non-directional quantities. Regression reproduces all four cases.
  2. Important: `validateIdentityText` accepts invalid UTF-8. Distinct component byte sequences containing 0xff and 0xfe both serialize to the same replacement character, violating exact canonical identity. Regression reproduces the collision precondition.
  3. Important: `observation_compat.go` copies RequestID into CallID, fabricates a request subject from FactID, and maps ParentWorkID into TraceID. Parent D1 requires these identities to remain separate; Task 2.5 forbids guessing lost provenance. A legacy marker does not authorize fabricated lineage.
- REMEDIATION: Add failing compatibility tests and fix these contract violations within the SDK boundary. Preserve V1 hashes and forbid fabricated attribution; reject conversions when required lineage cannot be proven. Retain the new review regressions. Re-run SDK and compatibility suites, vet, and the external-module compile fixture. Full phase review remains required after repair.
- SUMMARY: Passing happy-path tests do not yet establish identity-safe V2 contracts.

## Full review after identity repair

Verdict remains REJECTED. Parent reran `go test -count=1 -run '^TestPhase2Review' ./pkg/lipsdk/metering`: PASS. The independent reviewer also verified focused SDK/authority/core-metering tests, vet, enterprise-module compile, and clean placeholder/secret scans. Original three identity findings are repaired; full review identified remaining contracts work.

Required bounded repairs:

1. Safe evidence currently accepts arbitrary header/path names, including credential and content fields. Enforce explicit safe acquisition field selection and UTF-8-safe bounded lexemes without a provider registry in the SDK. Parent D2, requirements 5.6 and 16.1–16.2. Keep normalizer-specific mapping ownership in the later normalization phase.
2. Observations lack perspective and measurement boundary despite requirement 1.3 and D1/C1; retain these axes explicitly and preserve proven V1 values, never infer them from subject/origin. Lifecycle scope, where retained, must not imply A-leg finality.
3. Provider inference can use request subjects without B-leg ownership; AttemptSeq can stand alone; account-window utilization can be additive/directional. Enforce requirement 6.2 and 9.1–9.2. Reviewer overreach corrected: ProviderChargeID is NOT mandatory for every B-leg (D1 says where useful); resource costs are NOT universally gauges. Do not invent provider IDs or force genuine resource debits into gauge semantics.
4. V2 economics public refs and evidence lexemes still accept malformed UTF-8. Fix new V2 validation, preserving historical V1 behavior where necessary.
5. Line quantities permit fractional token/count units. Enforce integer-meter rules in V2 economics validation.
6. Compatibility projections silently turn gauge into delta and statement provenance into ordinary observed source; lifting drops proven V1 scope. Fail closed for nonrepresentable semantics and preserve scope where representable; add reservation/unavailable tests without changing V1 hashes.
7. Arbitrary V2 supersessions can use the legacy unknown-hash escape. Restrict to explicit legacy provenance and require exact V2 hashes.
8. Conflicting hashes for one observation revision and duplicate coverage nodes can pass. Reject conflicts, duplicate/contradictory coverage refs, and cross-store adjustment refs. Cross-record resolution belongs at a validation entry point that has the referenced records; do not claim a refs-only DTO can prove a closed graph.
9. Derived valuations lack required content identities for snapshots/qualifiers; add explicit validated V2 content references without changing old snapshot wire/hash contracts. D4 requires immutable resolvable content, not necessarily embedded snapshots.
10. Statement batches do not bind lines to the included statement observations/charges and account/period identities. Validate linkage or represent unmatched outcomes explicitly; do not infer runtime attribution from a statement.
11. V2 money fields permit hidden nonzero data when absent; exposure quote completeness is not validated. Apply strict V2 validation without casually modifying shared historical V1 semantics.
12. Quote observation refs and component schema relationships lack count bounds.
13. Preserve explicit payer ownership/unknown classification so customer-BYOK never silently creates operator payable. Requirement 6.6; SDK contract only, no policy execution in this phase.

Repair sequencing: SDK evidence/provenance/compatibility first, then remaining reference/valuation/public-port validation. Workers must capture RED before minimal GREEN fixes and report exact files and commands. Full phase review remains mandatory before advancing.

## Evidence repair review and debug round 1

Targeted existing repair and identity tests pass. Perspective/boundary/scope fields and compatibility rejection paths have been added. Review remains REJECTED: safe-location admission is a heuristic over words/prefixes, and `UnmarshalJSON` reconstructs a private legacy ownership exception from mutable wire fields.

Fresh parent regression `go test -count=1 -run '^TestPhase2ReviewLiftedObservationCanReplay$' ./pkg/lipsdk/metering` fails (exit 1): `wire-controlled legacy markers bypass B-leg ownership in a non-legacy store`. The positive legacy JSON replay portion passes; changing only both store IDs still passes validation, which is the demonstrated defect.

Fresh-context kiro-debug verdict: ROOT_CAUSE is LOGIC_ERROR; NEXT_ACTION is RETRY_TASK, confidence HIGH. Generic JSON decoding cannot authenticate historical provenance. The smallest fix is a distinct trusted/versioned legacy reader, a strict private exception predicate including both legacy store identities, and no exception restoration in generic JSON. Update the replay test to exercise the explicitly trusted reader for historical replay, while generic decoding cannot confer legacy authority. Preserve historical V1 hashes. Replace safe-location word/prefix heuristics with finite declarative provider-neutral locations; provider-family mapping remains Task 3 ownership. No spec repair or runtime binding is needed.

## Trust-boundary remediation result

Bounded debug repair APPROVED after parent source inspection and fresh `go test -count=1 -run '^TestPhase2Review|^TestPhase2Repair' ./pkg/lipsdk/metering` PASS. Generic decoding no longer sets the legacy flag; `ReadLegacyV1Observation` is an explicitly trusted historical reader, and the private allowance rechecks the complete legacy tuple including both store IDs. Safe locations now use finite exact declarations instead of word/prefix heuristics. Full Phase 2 remains REJECTED pending the already enumerated graph/reference, economics, snapshot, statement, payer, and collection-bound repairs; no phase/task completion is claimed.

## Metering graph remediation result

Bounded graph repair APPROVED after source/test inspection and fresh `go test -count=1 -run '^TestPhase2Graph|^TestPhase2Review|^TestPhase2Repair' ./pkg/lipsdk/metering` PASS. Private legacy provenance is required on both resolved supersession records for the unknown-hash exception. Generic V2 corrections reject that exception before resolution. Coverage graphs reject duplicate revision nodes and use iterative cycle traversal; component schemas enforce a 128-relationship limit with at-limit/over-limit tests. Unresolved references remain pending, not proven closed. Economics-side reference and valuation repairs remain outstanding.

## Economics validation remediation result

Bounded validation repair APPROVED after source/test inspection and fresh `go test -count=1 -run '^TestPhase2EconomicsRepair' ./pkg/lipsdk/economics` PASS. Strict V2 helpers reject invalid UTF-8 and hidden absent-money data without changing shared V1 validators. Tests cover fractional token/count quantities, conflicting revision hashes, duplicate coverage/adjustment references, cross-store adjustments, quote completeness and quote reference bounds. Snapshot content identity, statement linkage and payer contracts remain outstanding; full Phase 2 is not yet approved.

## Full phase re-review

Independent full review remains REJECTED with three residual findings. Focused SDK/authority/core-metering suites, enterprise-module tests, vet, formatting and marker/secret scans all PASS. Prior safe evidence, provenance, B-leg/resource ownership, historical trust, graph, statement, payer, money-presence and bounds findings are addressed.

1. `snapshot_v2.go` rejects ContentRef equal to snapshot ID even when paired with a valid SHA-256 content hash. This exceeds D4; a content-addressed resolvable ID plus validated immutable content is permitted. Remove the inequality restriction and change its test to accept that valid case.
2. Strict string validation does not prevent encoding/json from replacing malformed raw UTF-8 before validation. New economics envelopes and standalone V2 metering objects need raw-byte validation at their public decoding boundaries (or an enforceable mandatory decoder). Add malformed-wire regressions; preserve V1 wire behavior.
3. `UnitBound.Validate` still accepts fractional token/count bounds. Apply the integer-meter rule there, retaining supported fractional native units.

Full review explicitly confirmed RED evidence and preserved the known Phase 1 RED baseline distinction. No root-suite success or phase completion is claimed.

## Final Review Verdict
- VERDICT: APPROVED
- TASK: Parent 2.1–2.5 and refinement 2.1–2.2
- MECHANICAL_RESULTS:
  - Tests: PASS. Parent freshly reran `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` after the final repair, exit 0 across all eight packages.
  - Independent closure review: SDK/retained regression/authority/core-metering tests PASS; vet PASS; enterprise fixture PASS; formatting/import-boundary/placeholder/secret checks CLEAN.
  - RED phase: VERIFIED for all three residual fixes, including behavioral malformed-wire and fractional-bound failures before implementation.
  - Boundary: WITHIN. No runtime/store/provider integration or rating-engine execution is implied.
- FINDINGS: None outstanding in reviewed Phase 2 SDK scope. The closure review confirms same-ID content references with valid hashes, raw UTF-8 rejection before JSON replacement, and exact integer token/count bounds. Combined with the preceding full review, all known phase findings are closed.
- REMEDIATION: None for this SDK phase.
- SUMMARY: V2 SDK task scope is approved. Refinement 2.3 remains pending the reference rating engine; full root-suite, integration and feature completion are not claimed.

## Verification Result
- STATUS: VERIFIED
- CLAIM_TYPE: TASK
- CLAIM: Parent tasks 2.1–2.5 and refinement 2.1–2.2 are complete within the SDK contract boundary.
- EVIDENCE: Final independent approval plus the fresh parent package-suite results above.
- GAPS: Three deliberate Phase 1 billing/runtime RED tests await their owning implementation phases. No feature-level GO or full-suite result is claimed.
