# Brownfield Requirements Gap Analysis

## Scope

Baseline: current `main` after the merged usage-record billing implementation/refactors and billing host composition (#304–#311).

The implementation is materially better than the former stream-time accounting architecture. The simplification target is narrower: remove the **monetary authorization-hold lifecycle and runtime billing aggregation** while retaining the financial journal, immutable economic snapshots, B2BUA attribution, Bun persistence, reconstruction and reporting.

## Gap Inventory

| ID | Current state | Gap | Target |
|---|---|---|---|
| G-01 | Max quote ends in `AuthorizationStore.Authorize`. | Quote is fused to monetary hold creation. | Pure quote followed by operational exposure admission. |
| G-02 | `DurableStore.Authorize` inserts a hold, posts authorization journal entries and mutates `reserved_nano`. | Call setup is a financial transaction. | Account lock + `SUM(open exposure)` + exposure insert only. |
| G-03 | Spendable balance subtracts `ReservedNano`. | Settled money and risk exposure are mixed. | Financial balance/floor separate from exposure rows. |
| G-04 | Authorization-book postings model hold/release. | Non-money risk is represented as accounting. | Remove authorization book from the normal call path. |
| G-05 | Authorization/lookup/releaser/expiry/remainder are core lifecycle concepts. | Large state machine exists solely because pessimistic admission is a hold. | One immutable open `CallExposure`, closed only by customer settlement. |
| G-06 | Runtime releases unused holds on pre-open abort. | Runtime must clean financial state. | Every admitted call emits terminal usage; post-usage processing closes exposure. |
| G-07 | `BillingRuntime` includes admission, handoff, hold releaser, outbox and identity. | Runtime surface remains broad. | Cheap screen + exposure admission + terminal usage sink. |
| G-08 | `billingTurnCollector` keeps `evidenceByALeg` and parallel barriers. | Billing completeness is reconstructed in runtime memory. | Append terminal B-legs independently; call closure lists expected B-leg IDs. |
| G-09 | Default handoff outbox is process-local memory. | It cannot prove crash-surviving at-least-once usage delivery. | Authoritative mode requires durable Bun usage spool. |
| G-10 | TUR embeds all B-legs after a barrier. | Call completeness requires runtime aggregation/order coordination. | Separate immutable leg records and call closure; storage joins them. |
| G-11 | Stock TUR/authorization identity is account/A-leg or session/A-leg. | A-leg is a long-lived continuity/session anchor and may span later calls. | Generate one `BillingCallID` per incoming inference invocation. |
| G-12 | Billing admission runs only after route planning. | Obviously insolvent accounts consume routing/rating work. | Cheap account screen before routing. |
| G-13 | Existing estimator is useful but trapped in hold adapter. | Quote is not independently terminal. | Preserve estimator; terminate it in exposure admission. |
| G-14 | Customer settlement loads/validates hold and releases remainder. | Post-usage customer billing depends on setup lifecycle. | Validate against admitted exposure; close exposure atomically with customer posting. |
| G-15 | Customer charge and every provider COGS are one all-or-nothing settlement input. | Missing provider cost can strand user credit. | Customer settlement independent from per-leg provider COGS. |
| G-16 | Reconciliation includes reserved/hold state. | Financial rebuild carries non-money state. | Journal rebuilds settled balance; exposure reconciliation is separate. |
| G-17 | `ComposeBilling` requires hold release/lookup/store capabilities. | Composition enlarged by hold lifecycle. | Narrow account/exposure, usage-spool and financial capabilities. |
| G-18 | Rating resolver uses authorization hold to recover snapshot refs. | Rating depends on hold lookup. | Persist refs directly on exposure/call closure. |
| G-19 | Existing concurrency lease system has TTL/renew/release semantics. | Reusing it wholesale would reintroduce lifecycle complexity. | Minimal exposure row; no heartbeat/renew/set semantics. |
| G-20 | Root/Kiro steering says billing seams are hold-authorize + TUR handoff. | New design intentionally changes durable project invariant. | Update steering after cutover. |
| G-21 | Zero monetary outcomes already use durable operation evidence without zero journal entries. | This is correct and reusable. | Preserve. |
| G-22 | Journal fingerprints/account sequence/rebuild are strong. | Not a simplification target. | Retain. |
| G-23 | Current billing migrations own hold/reserved/session/processing state. | Schema contraction must be safe on upgrade. | Reconcile/cut over before retiring hold schema. |

## Requirements Review Round 1 — NO-GO

**Concurrency:** a read-only balance/current-call check is racy across proxy processes. Requirements 5–6 therefore require one account-scoped transaction that serializes `balance/floor + SUM(open exposure) + INSERT`.

**Completed-but-unbilled calls:** removing exposure at call end creates a credit window before billing posts. The exposure now stays OPEN unchanged until customer settlement atomically closes it.

**Billing identity:** A-leg can outlive one inference invocation. BillingCallID is now per call; A-leg/session are correlation only.

**Durability:** process-local retry is not at-least-once after crash. Authoritative mode now requires a durable usage spool.

## Requirements Review Round 2 — NO-GO

**Provider COGS coupling:** internal cost reconciliation does not need to block customer balance settlement. Provider-cost posting is now independent and may remain `unreconciled_cost` after customer settlement succeeds.

**Free-route screening:** a fixed positive pre-route threshold would reject zero-priced routes. `MinPreRouteHeadroom` is typed/configurable; zero permits detailed routing.

**Pre-provider abort:** direct runtime exposure release would recreate a release state machine. Every admitted call instead reaches terminal usage processing, including zero-use failures.

## Requirements Review Round 3 — PASS

Final requirements define one financial truth (posted journal), one operational risk truth (open exposure rows), one per-call identity, two admission stages, zero financial/exposure mutation during execution, durable leg/call terminal evidence, independent customer/provider financial effects, explicit recovery, and forced deletion of the hold/runtime-aggregation architecture.
