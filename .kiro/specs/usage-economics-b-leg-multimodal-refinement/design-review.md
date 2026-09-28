# Design Review Summary

The refinement is aligned with the current B2BUA ownership model and materially improves the parent SDD in three areas: multimodal economics, open-ended session continuity, and B-leg-rooted inference usage. Review found two internal design defects during drafting and one delivery-risk concern; all have concrete repairs in the final artifacts or release procedure.

## Critical Issues

### Critical Issue 1: Direction was initially overloaded with economic subject scope

**Concern:** The first draft treated request/resource/gauge as `FlowDirection` values alongside input/output.

**Impact:** It would conflate orthogonal axes and make canonical meter identity harder to reason about or extend.

**Repair:** `FlowDirection` is now only `none`, `input`, or `output`. Request/resource/gauge meaning remains in component and subject identity. Tasks 2.1-2.2 explicitly certify this separation.

**Traceability:** 1.1-1.3, 2.4-2.5.

**Evidence:** `design.md` → Refined Component Identity; `tasks.md` → 2.1.

### Critical Issue 2: Incremental accounting could have implied speculative customer debits

**Concern:** A literal reading of “on the fly” could post retail usage from an early retry candidate before the surfaced/winning B-leg is known.

**Impact:** Customers could temporarily or permanently pay for internal failover/speculation even under a winner-only retail policy.

**Repair:** Evidence capture and valuation advance incrementally, and operator/provider COGS may accrue/post by authoritative B-leg revision. Default independent-retail customer settlement may wait for **BillingCallID closure**, when its B-leg selector is stable. Only explicit provisional retail policies may post earlier, and they must compensate selection changes idempotently. No path waits for A-leg/session finality.

**Traceability:** 4.1-4.6, 5.1-5.5.

**Evidence:** `requirements.md` 4.3; `design.md` → Posting Policy; `tasks.md` → 4.3.

### Critical Issue 3: A sibling refinement could be missed by parent-spec implementers

**Concern:** The parent SDD is already merged and approved; silently rewriting its historical design would reduce auditability, but a sibling refinement can be ignored if #620 does not make it normative.

**Impact:** Implementation could follow parent Requirement 8.1 or terminal-oriented prose without the refined authority semantics.

**Repair:** The refinement declares narrow normative precedence, Task 8.4 requires combined traceability, and the PR delivery procedure must update #620 to reference this refinement before implementation. The parent remains authoritative everywhere else.

**Traceability:** 6.6.

**Evidence:** `requirements.md` Introduction and 6.6; `design.md` → Parent-Spec Amendments; `tasks.md` Execution Contract and 8.4.

## Design Strengths

- The model cleanly separates **all-attributable B-leg operator COGS** from **policy-selected B-leg customer inference usage**, avoiding both lost retries and retry-overbilling.
- Multimodal accounting preserves native units, direction, transform boundaries, and provider uncertainty without inventing a universal media-to-token conversion.
- Session continuity remains orthogonal to accounting: call/B-leg execution can close deterministically while the A-leg remains resumable indefinitely.
- The refinement reuses the parent evidence, valuation, adjustment, fencing, persistence, and public-binding architecture rather than adding a competing subsystem.

## Final Assessment

**Decision: GO.**

The design is implementation-ready once the spec-only PR is merged and #620 identifies this refinement as normative alongside the parent SDD. No unresolved architectural blocker remains. Production tests are intentionally not claimed for this spec-only change; the implementation plan defines the required red-first, persistence, race, multimodal, and release certification gates.