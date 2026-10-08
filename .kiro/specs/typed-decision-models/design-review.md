# Design Review: typed-decision-models

## Design Review Summary
The design delivers the System One slice by adding one canonical operation to the existing event path, following the `context.compaction` precedent, and reaches three upstreams through a single data-driven profile family. It stays within budget (21 acceptance criteria, ~200 design lines, ~1,350 planned non-test Go lines). Three issues were found during review and are resolved in `design.md` as recorded below.

## Critical Issues

🔴 **Critical Issue 1**: Billing clamp could silently strand decision candidates
**Concern**: The System One wire cannot carry `MaxOutputTokens`, so `CanEnforceAuthorityMaxOutputTokens` excludes decision backends whenever a spend-cap clamp applies.
**Impact**: Billing-enabled hosts with active clamps would reject decisions; left undocumented this looks like a routing bug.
**Suggestion**: Keep the existing fail-closed rule (no false `EnforcesMaxOutputTokens`), surface the exclusion error, and defer clamp semantics for output-free tariffs.
**Traceability**: 4.3, 3.1
**Evidence**: design.md "Error Handling"; requirements.md "Deferred". **Resolved.**

🔴 **Critical Issue 2**: `Call.Validate` must not depend on `Invocation`
**Concern**: An earlier draft required `Invocation.Operation == decision.evaluate` inside `Validate`; `Invocation` is not serialized and the connector bridge rebuilds calls.
**Impact**: Valid calls could fail validation after cloning or bridging; tests constructing calls would break broadly.
**Suggestion**: `Validate` checks only decision authority and exclusivity; operation identity is asserted by the backend, and capability negotiation keys off `Decision != nil`.
**Traceability**: 1.3, 3.1, 3.5
**Evidence**: design.md "Modified Files" (`pkg/lipapi/call.go`). **Resolved.**

🔴 **Critical Issue 3**: Divergence from issue #804's proposed execution seam
**Concern**: Issue #804 §6.2 proposes a sibling `DecisionExecutorView` and `Backend.EvaluateDecision`; this design instead extends the canonical stream.
**Impact**: Implementers reading the issue could build the rejected shape.
**Suggestion**: Record the steering conflict and option evaluation (research.md), and state in the PR that the issue's §6.2 should be revised to match.
**Traceability**: 2.1, 3.2, 4.3
**Evidence**: research.md "Architecture Pattern Evaluation", "Design Decisions". **Resolved in spec; issue update pending maintainer.**

## Design Strengths
- Every authority (routing, failover, commitment, B-leg lineage, billing, terminal usage) is reused unchanged; core gains three small, testable touchpoints.
- The canonical types are vendor-neutral and positional, so the deferred OpenAI Decisions frontend extends them instead of forking a model.

## Final Assessment
**Decision: GO.** No remaining architectural conflict with steering, all 21 acceptance criteria trace to components, and residual risk (message-oriented stages meeting a message-free call) is covered by the runtime integration test in task 5.1.

**Next steps**: generate tasks; on implementation, re-run design validation if `pkg/lipapi` authority rules or `OutputCommitted` change.
