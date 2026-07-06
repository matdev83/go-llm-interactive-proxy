# Implementation Gap Analysis: Usage, Quota, Rate, and Budget Authority

Generated: 2026-07-04T22:45:53.2564528+02:00

## Status

- Requirements source: `.kiro/specs/usage-quota-rate-budget-authority/requirements.md`
- Metadata source: `.kiro/specs/usage-quota-rate-budget-authority/spec.json`
- Requirements approval state: generated but not approved (`approvals.requirements.approved=false`). This analysis can inform requirement revisions before design approval.
- Analysis scope: brownfield implementation gap only; no implementation decisions are final here.

## Current State Investigation

### Existing Foundations

1. **Safe principal/scope attribution exists**
   - `pkg/lipsdk/scope/view.go:29` defines `PrincipalScopeView` with principal, credential, tenant, organization, workspace, project, department, cost center, policy labels, origin, and parent trace fields.
   - `pkg/lipsdk/scope/context.go:13` stores and retrieves cloned scope snapshots from context.
   - This satisfies the attribution substrate for requirement 1, but accounting-specific rule matching does not yet exist.

2. **Policy decision vocabulary and observer evidence exist**
   - `pkg/lipsdk/policydecision/types.go:10` defines outcomes (`allow`, `deny`, `skip`, `error`), effects, fail-open/fail-closed behavior, visibility, and provider refs.
   - `pkg/lipsdk/policydecision/record.go:14` defines safe decision evidence with trace/A-leg/B-leg/attempt/stage/provider/outcome/effect/reason/scope metadata.
   - `pkg/lipsdk/policydecision/observe.go:12` defines a fail-open observer chain.
   - Runtime tests in `internal/core/runtime/policy_decision_prepare_test.go:26` prove pre-request denial can stop backend opens and emit policy evidence.
   - Gap: there is no accounting-specific decision provider, reason taxonomy, or enforcement service that emits accounting decisions through this vocabulary.

3. **Control-plane evidence and query substrate exists**
   - `pkg/lipsdk/controlplane/types.go:10` defines categories (`auth`, `session`, `attempt`, `usage`, `policy`, `audit`, `lifecycle`) and evidence/redaction/visibility states.
   - `internal/core/controlplane/ports.go:13` defines append, query, retention, readiness, and store ports.
   - `internal/core/controlplane/recorder.go:25` provides recording policy semantics with best-effort and required-pre-work behavior.
   - `internal/core/controlplane/queries.go:31` serves bounded query views over sessions, attempts, usage, usage aggregates, policy/audit, and events.
   - `internal/infra/controlplane/ledgerstore/schema.go:14` persists an append-only event ledger with correlation, scope dimensions, usage plane/availability, outcome/effect/reason, and indexes.
   - Gap: control-plane detail shapes do not currently represent reservation, remaining budget/quota/rate state, accounting rule identities, settlement adjustments, or rate-window status as first-class fields.

4. **Token accounting measurement exists**
   - `internal/core/tokenaccounting/preflight/preflight.go:37` defines token preflight config for max input/output/context and output clamping.
   - `internal/core/tokenaccounting/preflight/preflight.go:76` evaluates count availability and token/model limits before backend attempts.
   - `internal/core/tokenaccounting/domain/reconcile.go:9` defines billing-plane policy and reconciliation across provider/client/proxy usage planes.
   - `internal/core/tokenaccounting/streamusage/reconstructor.go:67` reconstructs local and provider usage for completed streams.
   - `internal/core/tokenaccounting/ledger/ledger.go:12` records per-request/attempt/plane token usage facts.
   - Gap: measurement has no budgets, quotas, rates, reservations, spend caps, scope-aware windows, or enforcement authority.

5. **Cost estimation exists but is not enforcement-ready**
   - `internal/core/accounting/accounting.go:41` defines `CostInput` and provider cost input.
   - `internal/core/accounting/accounting.go:124` estimates cost from provider-reported cost or model pricing catalog.
   - Gap: no spend windows, budget policies, currency mismatch policy beyond unavailable/estimated cost, or cost reservation semantics exist.

6. **Runtime integration points exist**
   - `internal/core/runtime/executor.go:107` already carries an `AccountingPriceCatalog` and token accounting services on the executor.
   - `internal/core/runtime/executor_open_attempt.go:328` runs token preflight before attempt acquisition and backend open.
   - `internal/core/runtime/executor_open_attempt.go:337` acquires route attempt budget after token preflight.
   - `internal/core/runtime/executor_token_accounting_preflight_test.go:50` verifies strict preflight rejects before backend open.
   - `internal/core/runtime/executor_token_accounting_stream_test.go:116` verifies required ledger failures fail-close at completion, while best-effort ledger failures preserve response completion at `internal/core/runtime/executor_token_accounting_stream_test.go:140`.
   - Gap: no executor field/seam exists for an accounting authority distinct from token limit preflight. Care is needed because token preflight is also called for request-size routing estimates (`internal/core/runtime/executor_open_attempt.go:559`), which must not create reservations.

7. **Control-plane source adapters for usage and policy exist**
   - `internal/infra/controlplane/observers/usage_observer.go:10` records safe usage observations into control-plane evidence and is always fail-open.
   - `internal/infra/controlplane/observers/policy_observer.go:10` records policy decisions into control-plane evidence and is always fail-open.
   - `internal/infra/runtimebundle/build.go:537` prepends control-plane usage observers to operator-supplied observers.
   - `internal/infra/runtimebundle/build.go:557` prepends control-plane policy observers to operator-supplied observers.
   - Gap: observer chains are intentionally fail-open and cannot be the synchronous enforcement mechanism for strict budgets/quotas/rates.

8. **Configuration surfaces exist but exclude this feature today**
   - `internal/core/config/model.go:46` defines `AccountingConfig` with counting mode, preflight token limits, ledger, admin count, observability, strict authoritative mode, and pricing.
   - `internal/core/config/control_plane_test.go:351` explicitly forbids enterprise fields including `Quota`, `Allowance`, `SpendCap`, and `RateLimit` on `ControlPlaneConfig`.
   - `internal/core/config/control_plane_examples_test.go:80` forbids `quota` and `rate_limit` in control-plane example YAML.
   - Gap: the new feature should not put budget/rate/quota fields on `ControlPlaneConfig` without deliberately changing those tests. A sibling or accounting-owned config surface is less disruptive.

9. **Concurrency primitives exist only for unrelated frontend decode limits**
   - `internal/plugins/frontends/decodeqos/limiter.go:5` provides a simple channel-backed concurrent decode limiter.
   - Gap: no token bucket, sliding/fixed window counter, durable reservation store, or atomic window update mechanism exists for accounting authority.

## Requirement-to-Asset Map

| Requirement | Existing assets | Gap tag | Notes |
| --- | --- | --- | --- |
| 1. Scope-attributed accounting authority | `PrincipalScopeView`, scope context, control-plane scope flattening | Missing | Need rule matcher over safe dimensions and unknown-known semantics. |
| 2. Usage aggregation and accounting state | control-plane `Usage`, `UsageAggregate`; token accounting ledger; usage observer | Partial / Constraint | Aggregates exist for observed usage, but not enforceable windows, reservations, remaining limits, or authority selection. |
| 3. Quota window enforcement | token totals and request lineage | Missing | Need quota rules, counters, atomic window updates, advisory/strict behavior. |
| 4. Rate window enforcement | no relevant shared rate limiter | Missing | Need request-rate algorithm and scoped counters; decode limiter is unrelated and frontend-local. |
| 5. Spend budget/spend cap enforcement | `accounting.EstimateCost`, pricing config | Partial | Need budget rules, cost source policy, windows, reservations, currency behavior. |
| 6. Preflight reservation/admission | token preflight and pre-backend executor hook | Partial / Constraint | Existing preflight can deny before backend open, but is also used for routing estimates and must not be side-effectful. |
| 7. Post-stream reconciliation/settlement | stream usage reconstructor, ledger writes, billing finalization seam | Partial | Need reservation settlement, overage handling, adjustment evidence, and finalization reconciliation. |
| 8. Estimated/authoritative/unavailable authority | `lipapi.UsageAuthority`, reconciliation policy, cost source strings | Partial | Need explicit authority policy per accounting rule and conflict recording. |
| 9. Policy decisions/client outcomes/evidence | policydecision SDK, control-plane policy observer | Partial | Need accounting-specific reasons/effects and frontend-safe denial mapping. |
| 10. Failure/degraded/startup posture | control-plane recorder/status; token preflight advisory/strict; config validation patterns | Partial | Need accounting authority readiness, strict startup posture, and fail-open/fail-closed per rule. |
| 11. Concurrent requests/attempts/streaming invariants | B2BUA lineage, route attempt budget, no-retry tests, token accounting stream tests | Partial / High risk | Need atomic admission under concurrency and no double-counting across pre-output retries/races. |
| 12. Operator visibility/query behavior | control-plane query service, usage aggregate views, status routes | Partial | Need remaining quota/budget/rate status and accounting decision query projections. |
| 13. Privacy/safety/exclusions | scope safety, control-plane normalizer, observer tests dropping raw usage JSON | Partial | Need ensure new accounting evidence follows same bounded/safe rules. |

## Key Integration Challenges

1. **Side-effect-free estimates vs side-effectful reservations**
   - `Executor.requestSizeEstimateForRouting` calls `Preflight.Check` only to estimate size for routing constraints. If budget/quota reservation is added directly to token preflight, routing estimation could accidentally reserve capacity. Design needs separate estimate/check/reserve phases or explicit side-effect mode.

2. **Atomic windows and reservations**
   - Requirements demand concurrent requests not exceed strict windows except configured overage. Existing control-plane and token ledgers are append/query oriented, not atomic reservation counters. A new store or careful transactional adapter is likely needed.

3. **Control-plane evidence shape**
   - Current control-plane usage/policy details can express observed usage and policy decisions, but not remaining limits, reserved amounts, settled amounts, overage, reset time, retry context, or rule identity beyond generic provider/reason fields.

4. **Multi-attempt accounting**
   - Pre-output failover and parallel racing can create multiple B-legs for one A-leg. Existing token accounting records failed and surfaced attempts separately. Budget authority needs a clear rule for request-level reservation vs attempt-level settlement to avoid double counting or leaking reservations.

5. **Cost authority and currency**
   - Cost estimation works per model/catalog. Requirements forbid treating currencies as interchangeable without explicit policy. Design needs a posture for missing prices, provider costs, and currency mismatch.

6. **Startup and degraded behavior**
   - Control-plane has `required_pre_work` concepts and redacted store failure handling. Accounting authority needs equivalent readiness semantics, likely stricter for fail-closed enforcement. Memory-only stores may be acceptable for single-process advisory mode but questionable for strict durable budgets.

7. **Operator query semantics**
   - Existing `UsageAggregate` returns historical totals. Requirements need current/remaining limit status, reserved usage, and rate-window status. These are stateful authority reads, not just historical event aggregates.

8. **Config placement**
   - `ControlPlaneConfig` intentionally excludes budget/rate/quota fields. `AccountingConfig` already owns token accounting and pricing, but adding many enforcement rules there may bloat config. A sibling `usage_authority` or `accounting.authority` block should be evaluated in design.

## Implementation Approach Options

### Option A: Extend Existing Token Accounting Components

**Shape**
- Add quota/rate/budget fields to `AccountingConfig`.
- Expand `tokenaccounting/preflight.Checker` to evaluate quotas and budgets.
- Extend token accounting ledger/control-plane usage aggregates for windows.
- Emit policy/control-plane evidence from existing preflight and stream accounting paths.

**Pros**
- Minimal new package surface.
- Reuses existing count, preflight, stream reconstruction, ledger, and pricing code.
- Fastest path for an advisory or single-process MVP.

**Cons**
- High risk of bloating measurement code with enforcement policy.
- Existing `Preflight.Check` is used for routing estimates, so side effects would be dangerous.
- Existing ledger is not scope-rich enough for all grouping dimensions and not atomic enough for strict concurrent windows.
- Harder to keep fail-open/fail-closed policy per dimension clean.

**Fit**
- Viable only for narrow advisory limits or transitional work. Not ideal for full requirements.

### Option B: Create a New Accounting Authority Capability

**Shape**
- Add a new bounded context such as `internal/core/usageauthority` or `internal/core/accountingauthority` with pure rule/window/reservation domain logic and app-level admission/settlement services.
- Define consumer-owned ports for rule source, reservation/window store, evidence sink, readiness, and clock.
- Add infra adapters under `internal/infra/...` for memory and durable state.
- Integrate into runtime via a new executor seam before backend open and post-stream settlement hook.

**Pros**
- Clean separation between measurement and enforcement.
- Easier to test domain rules, reservation math, concurrency behavior, and failure policy independently.
- Avoids side effects in existing token preflight estimate path.
- Aligns with hexagonal guidance: app owns workflow, adapters own stores, core owns protocol-neutral policy.

**Cons**
- More new code and design surface.
- Requires careful integration with existing runtime and control-plane evidence paths.
- Needs new config and query/readiness surfaces.

**Fit**
- Strong fit for requirements 1-13 if designed incrementally.

### Option C: Hybrid Authority + Reuse Existing Measurement and Evidence

**Shape**
- Create a new accounting authority domain/app package for rules, decisions, reservations, windows, and settlement.
- Reuse token accounting service for preflight estimates and stream reconstruction.
- Reuse `accounting.PriceCatalog` for cost estimation.
- Reuse policydecision records for enforcement evidence and control-plane recorder/query for safe operator evidence.
- Keep `tokenaccounting/preflight.Checker` for model/token-limit checks; add a separate executor seam for authority admission/reservation.

**Pros**
- Separates enforcement from measurement while still reusing mature existing pieces.
- Preserves current preflight semantics and avoids reservation side effects during routing estimates.
- Fits control-plane and policy decision groups without reinventing evidence contracts.
- Allows incremental implementation: domain rules -> in-memory store -> runtime admission -> settlement -> durable store/query.

**Cons**
- More planning needed to avoid duplicate usage records between token ledger, control-plane events, and authority windows.
- Requires clear names and boundaries to avoid two competing accounting concepts.

**Fit**
- Best balanced approach for design exploration. It is not a final decision, but it appears most consistent with repo architecture and requirements.

### Option D: Implement as Feature Plugin Only

**Shape**
- Build an official feature plugin that uses pre-request/policy hooks and usage observers to deny/advice/record usage.

**Pros**
- Keeps core smaller.
- Exercises extension platform.

**Cons**
- Strict preflight reservation and post-stream settlement need privileged runtime timing and atomic state.
- Usage observers are fail-open and post-facto; they cannot provide synchronous strict enforcement.
- Hard to guarantee no double-counting across B2BUA attempts/races without core lineage hooks.

**Fit**
- Viable later for custom rule providers or admin-managed policies, but not sufficient as the core authority foundation.

## Effort and Risk

- **Estimated effort**: XL (2+ weeks). The feature spans domain rules, config, runtime admission, stream settlement, storage, query/status, and evidence integration.
- **Risk**: High. Main risks are atomic reservations under concurrency, multi-attempt settlement correctness, strict fail-closed posture, and avoiding side effects in routing estimate paths.
- **Risk mitigations for design**:
  - Keep token counting and authority reservation as distinct phases.
  - Start with a pure domain rule/window model and deterministic in-memory store tests.
  - Add contract tests for memory and durable stores before runtime plumbing.
  - Add executor tests for no backend open on strict denial, no retry after output, and no double-settlement across failover/racing.

## Research Needed for Design Phase

1. **Window algorithm selection**
   - Decide fixed window, sliding window, token bucket, or hybrid per rate/quota type. Requirements do not mandate an algorithm.

2. **Reservation semantics**
   - Decide whether reservations are request-level, attempt-level, or both; define release/adjustment behavior for pre-output retries, parallel losers, cancellation, and final provider usage.

3. **Durable atomicity strategy**
   - Research SQLite/Postgres transaction/upsert patterns for atomic window reservation and settlement without leaking SQL types into core ports.

4. **Config and management boundary**
   - Decide whether rules live under `accounting.authority`, a sibling `usage_authority`, or a future admin/provisioning-managed source. Keep Group 5 admin/provisioning out of scope.

5. **Control-plane evidence extension**
   - Decide whether to extend `PolicyDetail`, `UsageDetail`, add a new detail category, or encode accounting decision state through safe audit/policy records.

6. **Client-safe denial mapping**
   - Identify existing frontend error classification paths to map budget/quota/rate denials consistently without provider leakage.

7. **Strict-authoritative interactions**
   - Clarify how existing `AccountingConfig.StrictAuthoritative` relates to rule-level authority requirements.

8. **Currency posture**
   - Decide if v1 only supports same-currency budgets, and how unavailable conversion is surfaced.

9. **Operator query shape**
   - Define read models for current remaining quota/budget/rate status separate from historical `UsageAggregate` rows.

## Recommended Design Focus

The design phase should evaluate Option C first: a new accounting authority capability that reuses token accounting, pricing, policydecision evidence, and control-plane storage/query, with narrow runtime seams for admission/reservation and settlement. Option A can be retained for small token-limit extensions, and Option D can be reserved for future custom policy/rule providers.

Key design decisions to make early:

1. Name and package boundary for the authority capability.
2. Rule model and matching semantics over safe scope dimensions.
3. Reservation/window store contract and atomicity guarantees.
4. Executor integration points for preflight admission and post-stream settlement.
5. Evidence/query model for rule decisions, remaining limits, reserved amounts, and reconciliations.
6. Startup/degraded behavior for strict vs advisory accounting authority.
