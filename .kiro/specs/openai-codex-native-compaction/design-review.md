# Design Validation Review

## Design Review Summary

The revised design is implementation-ready for an experimental, connector-private feature. It keeps provider-bound compaction state inside the optional Codex connector, preserves full client history as the authoritative fallback, and does not pre-empt the broader canonical compaction work owned by `openresponses-api-support`.

The initial validation identified three critical issues. Each was corrected in `requirements.md` and `design.md` before task generation.

## Critical Issues Found and Resolved

### Critical Issue 1: Checkpoint ownership before managed account selection

**Concern:** The first design concept applied checkpoint rewriting during common request preparation, before managed OAuth selected the actual account.  
**Impact:** An opaque item produced under one ChatGPT account could be replayed under another account after rotation, causing rejection or state leakage.  
**Resolution:** Checkpoint lookup, creation, and rewrite now occur per attempt after effective account and model selection. The checkpoint key includes account, model, connector instance, prompt-cache identity, client family, comp hash, instructions, and tools.  
**Traceability:** 4.2, 5.1, 5.4, 7.7  
**Evidence:** `design.md` — Existing Architecture Analysis; Checkpoint Key; Static/Managed Integration.

### Critical Issue 2: Ambiguous compaction boundary could duplicate the active turn

**Concern:** Compacting the complete client-supplied input could include the latest user instruction and tool continuation, then append that input again to the replacement.  
**Impact:** The model could receive duplicate instructions, lose live tool state, or compact the very work it must continue.  
**Resolution:** The planner now splits immediately before the latest user message. The latest user message and every later item form an immutable live tail. Splits that cross call/output pairing are rejected.  
**Traceability:** 3.5–3.7, 4.6, 6.1–6.2  
**Evidence:** `design.md` — CompactionPlanner; ReplacementBuilder; New Checkpoint Creation flow.

### Critical Issue 3: Old `previous_response_id` could be combined with a new checkpoint

**Concern:** Existing WebSocket continuation might attach an old response ID to the replacement history.  
**Impact:** The backend would receive two incompatible history authorities, producing rejection or semantic corruption.  
**Resolution:** Committing a checkpoint invalidates the prior continuation entry. The first normal request after installation omits `previous_response_id`; only its successful completion may establish a new baseline.  
**Traceability:** 4.3, 6.3–6.5, 6.8  
**Evidence:** `design.md` — ContinuationCoordinator; Existing Checkpoint Reuse; New Checkpoint Creation.

## Design Strengths

- The feature has a narrow, reversible ownership boundary and does not widen canonical/core contracts.
- Failure behavior is explicit: pre-output fail-open under the hard limit, deterministic failure when the original request cannot fit, and no post-output retry.
- Usage, privacy, concurrency, and live compatibility evidence are treated as release criteria rather than deferred operational details.

## Final Assessment

**Decision: GO**

The corrected design addresses the critical account-isolation, history-boundary, and response-chain risks. Remaining uncertainty is external compatibility with the live ChatGPT Codex V2 trigger; the design contains an environment-gated smoke test, default-off rollout, fail-open behavior, and failure cooldown specifically to contain that risk.

## Next Steps

Generate implementation tasks using contract-first TDD. No implementation should begin until the user or maintainer approves `requirements.md` and `design.md` in `spec.json`.
