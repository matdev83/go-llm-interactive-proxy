# Brownfield Requirements Gap Analysis

## Baseline

Target code is merged main after PR #340. The new exposure/post-usage architecture is live, but several current implementation choices do not satisfy the requirements generated for this remediation.

## Gap Inventory

| Gap | Current state | Requirement impact | Required remediation |
|---|---|---|---|
| G-01 | `CallLegUsageRecord` has no B-leg sequence | 2.1-2.8 | Add authoritative attempt sequence to new leg record |
| G-02 | Runtime knows `b2bua.BLegRecord.Seq` but drops it during independent append | 2.1-2.4 | Thread exact sequence into durable record |
| G-03 | `ExpectedBLegIDs` are sorted and later reused as if they implied order | 2.6-2.8 | Make set semantics explicit; forbid financial ordering from IDs |
| G-04 | `RateCall` assigns `Seq = index + 1` | 2.7-2.8 | Use persisted sequence, never slice order |
| G-05 | Interrupted/no-surfaced leg selection depends on `Seq` | 2.7, 8.4 | Add explicit regression with reverse lexical IDs |
| G-06 | Existing rows cannot reconstruct sequence from random `BLegID` | 3.1-3.7 | Nullable/legacy-unknown migration and fail-closed rating where order matters |
| G-07 | Current leg fingerprints do not include sequence | 2.3, 3.3 | Version new fingerprint contract while preserving old replay |
| G-08 | Catalog produces model-pricing cards but call resolver discards them | 4.1-4.8 | Carry model pricing into rating |
| G-09 | `CallRatingInput` lacks model-pricing input | 4.3-4.7 | Extend internal rating input |
| G-10 | Combined `SnapshotsFor` resolves operator rates during customer settlement | 5.1-5.7 | Split customer and provider snapshot resolution |
| G-11 | Customer rating type carries unused operator rates | 5.2, 5.6 | Remove coupling from customer path |
| G-12 | `billingTurnCollector` is executor-global | 6.1-6.9 | Introduce request/BillingCallID-scoped state |
| G-13 | Collector retains multiple per-call maps after terminal completion | 6.5-6.8 | Make whole state unreachable at call completion |
| G-14 | Finalization single-flight cache is process-lifetime shaped | 6.2, 6.6 | Move single-flight cache into call state |
| G-15 | Current tests prove freeze but not state destruction | 6.8-6.9, 8.3 | Add stress/race retained-state tests |
| G-16 | Existing hold-deletion ratchets do not cover the new failure classes | 8.6-8.7 | Add sequence/pricing/state-ownership architecture guards |

## Requirements Reconciliation

The first requirements draft was strengthened after gap analysis in four areas:

1. **Legacy persistence safety** became a first-class Requirement 3. Merely adding a non-null sequence column would either corrupt replay or force unsafe inference for existing rows.
2. **Sequence uniqueness and fingerprinting** were added to Requirement 2 because persistence of a correct value is insufficient if replay may silently change it.
3. **Customer/operator snapshot separation** was promoted from implementation advice to Requirement 5 because the current combined resolver can keep customer exposure open.
4. **Lifetime ownership** was specified as call-scoped rather than “add eviction” because patching executor-global maps would preserve the underlying leak-prone ownership model.

## Non-Gaps / Accepted Deviations

The following current behaviors are deliberately preserved:

- `ExpectedBLegIDs` may remain sorted because completeness is set-valued.
- Customer and provider processing remain separate workers.
- Durable append retry/outbox remains unchanged in this spec.
- `TurnUsageRecord`/`LegUsageRecord` may remain as temporary implementation adapters until the successor spec.
- `reserved_nano` dead-schema/domain residue is not remediated here.
- Generic UsageAuthority remains unchanged here except that no monetary authority may leak back into billing.
- No public SDK/canonical model change is required.

## Quality Gate

**Result: GO for design after requirements fixes.**

The requirements now cover every observed correctness defect without absorbing the successor simplification work. The implementation boundary is narrow enough to review independently and provides a stable baseline for the second spec.
