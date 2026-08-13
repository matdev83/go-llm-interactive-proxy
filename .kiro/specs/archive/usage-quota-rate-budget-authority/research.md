# Implementation Gap Analysis: Usage, Quota, Rate, and Budget Authority

Generated: 2026-07-04T22:45:53.2564528+02:00

## Status

- Requirements source: `.kiro/specs/archive/usage-quota-rate-budget-authority/requirements.md`
- Metadata source: `.kiro/specs/archive/usage-quota-rate-budget-authority/spec.json`
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

---

# Implementation Gap Analysis Refresh: Usage, Quota, Rate, and Budget Authority

Generated: 2026-07-08T23:55:00+02:00

## Status

- Requirements source: `.kiro/specs/archive/usage-quota-rate-budget-authority/requirements.md`
- Metadata source: `.kiro/specs/archive/usage-quota-rate-budget-authority/spec.json`
- Requirements approval state: generated but not approved (`approvals.requirements.approved=false`). This analysis proceeds because gap validation can inform final requirement approval and design.
- Analysis scope: brownfield implementation gap against the current Go codebase after principal/scope, admission-policy decision, and control-plane event-ledger foundations landed.

## Current State Investigation

### Existing Foundations

1. **Token counting and model-limit preflight exist**
   - `internal/core/tokenaccounting/preflight/preflight.go:37` defines preflight configuration for max input, max output, context limits, strict/advisory posture, and max-output clamping.
   - `internal/core/tokenaccounting/preflight/preflight.go:76` evaluates token counts before an attempt and returns allow/deny/warning/clamp decisions.
   - `internal/core/runtime/executor_open_attempt.go:328` runs this token preflight before route-attempt acquisition and backend open.
   - Gap: this is not a quota/rate/budget authority. It has no rule matching by scope, no windows, no reservation, no budget state, and no remaining-limit query model.

2. **The same preflight checker is used for side-effect-free routing estimates**
   - `internal/core/runtime/executor_open_attempt.go:559` calls `Preflight.Check` to estimate request size for routing constraints.
   - Requirement 6.8 says routing estimates must not create, consume, or mutate quota, budget, rate, or spend reservations.
   - Constraint: accounting authority must keep estimate/check operations separate from side-effectful reservation or admission state changes.

3. **Token accounting ledger and stream reconstruction exist**
   - `internal/core/tokenaccounting/ledger/ledger.go:12` defines per-request/per-attempt usage ledger records by usage plane.
   - `internal/core/runtime/attempt_stream.go:931` records reconstructed/final usage events into the ledger after stream processing.
   - `internal/core/runtime/attempt_stream.go:988` records partial token-accounting evidence for unavailable/failure paths.
   - Gap: the ledger is append/list-oriented usage evidence. It does not provide atomic counters, reservation lifecycle, window reset behavior, spend state, or remaining-limit authority.

4. **Cost estimation exists but not spend enforcement**
   - `internal/core/accounting/accounting.go:21` defines token usage and provider cost inputs.
   - `internal/core/accounting/accounting.go:124` estimates cost from provider-reported cost or configured model pricing.
   - Gap: no configured budget rules, spend windows, currency conversion posture, budget reservations, or strict/advisory budget decisions exist.

5. **Safe scope attribution exists and is suitable for rule dimensions**
   - The principal/scope foundation provides `pkg/lipsdk/scope` and request-context scope snapshots.
   - Control-plane query contracts carry presence-aware scope filters in `pkg/lipsdk/controlplane/query.go:41`.
   - Gap: no accounting rule matcher consumes these dimensions to decide quota/rate/budget outcomes.

6. **Policy decision vocabulary and observers exist**
   - `pkg/lipsdk/policydecision/types.go:10` defines allow/deny/skip/error outcomes.
   - `pkg/lipsdk/policydecision/types.go:52` defines fail-open/fail-closed behavior.
   - `pkg/lipsdk/policydecision/record.go:14` defines safe decision evidence with trace, A-leg/B-leg, attempt, stage, outcome, effect, reason, client category/message, and scope.
   - Gap: there is no accounting-specific provider/service producing quota/rate/budget decisions or reason taxonomy through this vocabulary.

7. **Control-plane event and query substrate exists**
   - `pkg/lipsdk/controlplane/details.go:108` defines usage detail with token, cost, accounting authority, and cost-source fields.
   - `pkg/lipsdk/controlplane/query.go:89` defines usage queries and `pkg/lipsdk/controlplane/query.go:99` defines usage aggregate queries.
   - `internal/core/controlplane/ports.go:13` defines append/query/retention/readiness ports for the event ledger.
   - `internal/infra/controlplane/observers/usage_observer.go:10` records usage observations into control-plane evidence and is intentionally fail-open.
   - `internal/infra/controlplane/observers/policy_observer.go:10` records policy decisions into control-plane evidence and is intentionally fail-open.
   - Gap: current control-plane detail/query shapes can show usage and policy facts, but not current remaining quota/budget/rate state, reserved amounts, settlement adjustments, reset/retry context, overage, or accounting rule identity as first-class accounting authority views.

8. **Runtime construction has accounting grouping but no authority seam**
   - `internal/core/runtime/executor_config.go:73` groups token-accounting admission, stream reconstruction, ledger hooks, and admin count service under `AccountingRuntime`.
   - `internal/infra/runtimebundle/token_accounting.go:37` builds token counters, preflight, stream usage reconstruction, memory/durable ledger, observability, and admin count service.
   - Gap: no executor field or runtimebundle builder exists for an accounting authority admission/reservation/settlement service.

9. **Config exists for passive accounting, not authority rules**
   - `internal/core/config/model.go:46` defines accounting config for enabled/mode/count timeout/tokenizer/preflight/ledger/admin/observability/strict authoritative/pricing.
   - There are no quota, rate, allowance, spend-cap, budget, reservation, or authority-rule config fields.
   - Existing control-plane tests intentionally keep enterprise enforcement fields out of `ControlPlaneConfig`.

10. **Only provider-specific quota/rate-like code exists**
    - `internal/plugins/backends/openaicodex/managed_oauth_quota.go:11` persists Codex quota headers for managed OAuth accounts.
    - `internal/plugins/backends/openaicodex/managed_oauth_store.go:173` marks a Codex account rate-limited for backend credential selection.
    - Constraint: these are backend-local provider-account behaviors and should not be mistaken for proxy-level scope-attributed quota/rate/budget enforcement.

## Requirement-to-Asset Map

| Requirement | Existing assets | Gap tag | Notes |
| --- | --- | --- | --- |
| 1. Scope-attributed accounting authority | PrincipalScopeView, control-plane scope filters, request scope propagation | Missing | Need rule matcher over safe scope/backend/model/route/label dimensions and known/unknown semantics. |
| 2. Usage breakdown and accounting state | token ledger, control-plane UsageRow/UsageAggregate, cost estimation | Partial / Constraint | Historical usage exists; live authority state, reservations, remaining limits, and authority selection do not. |
| 3. Quota window enforcement | token usage totals and request lineage | Missing | Need quota rules, window counters, reset behavior, strict/advisory outcomes, and atomic updates. |
| 4. Rate window enforcement | none for proxy-level traffic; Codex backend cooldown is provider-local | Missing | Need request-rate windows, retry context, and scoped counters. |
| 5. Spend budget and spend cap enforcement | price catalog and cost estimation | Partial | Need spend rules, budget windows, currency posture, strict/advisory decisions, and cost-unavailable behavior. |
| 6. Preflight reservation and admission | executor pre-backend point, token preflight, policy decision evidence | Partial / Constraint | Existing preflight can deny before backend open, but is also used for routing estimates and must remain side-effect-free there. |
| 7. Post-stream reconciliation and settlement | stream usage reconstruction, partial/final ledger writes | Partial | Need reservation settlement, adjustment, release, overage, and cancellation semantics. |
| 8. Estimated/authoritative/unavailable authority | UsageAuthority metadata, strict authoritative config, cost source strings | Partial | Need rule-level authority requirements and deterministic conflict resolution. |
| 9. Policy decisions, client outcomes, evidence | policydecision SDK, runtime policy observer, frontend error mapping patterns | Partial | Need accounting reason taxonomy, client-safe categories/messages, and legal denial mapping. |
| 10. Failure/degraded/startup posture | config validation, runtimebundle startup checks, control-plane status | Partial | Need authority readiness, backing capability checks, fail-open/fail-closed per rule, and strict startup posture. |
| 11. Concurrent requests, attempts, streaming invariants | B2BUA lineage, route attempt budget, no-retry-after-output tests | Partial / High risk | Need atomic admission under concurrency and no double counting for retries/races/losers. |
| 12. Operator visibility and query behavior | control-plane query/status, token admin count service | Partial | Need live remaining limit/rate/reservation state separate from historical usage aggregates. |
| 13. Privacy, safety, exclusions | scope safety, control-plane redaction, observer fail-open behavior | Partial | Need prove new authority evidence carries no raw prompts, secrets, provider payloads, or unsafe rule internals. |

## Key Integration Challenges

1. **Side-effect-free estimates vs side-effectful admission**
   - Routing uses token preflight for estimates. Authority reservation must not be hidden inside this path.
   - Design should distinguish estimate, evaluate, reserve, settle, and query operations.

2. **Atomicity under concurrency**
   - Requirements demand preventing concurrent requests from exceeding strict windows.
   - Existing ledgers append evidence; they do not provide compare-and-reserve semantics for live counters.

3. **Attempt lineage and reservation scope**
   - A logical request can create multiple B-legs before output through failover or racing.
   - Design must decide whether reservations are request-level, attempt-level, or both, and how losers/swallowed attempts release or settle state.

4. **Post-output non-interference**
   - After client-visible output begins, later accounting failures must not trigger retry or replacement.
   - Settlement failures should become operator-visible authority/control-plane evidence, not hidden execution control flow.

5. **Control-plane detail shape**
   - Current usage/policy DTOs lack fields for rule IDs, matched dimensions, reset time, retry context, limit/consumed/reserved/remaining amounts, and settlement adjustments.
   - Design must decide whether to extend usage/policy details, add accounting-specific details, or expose dedicated status/query DTOs.

6. **Configuration and management boundary**
   - Requirements need configured rules but explicitly exclude web admin/user provisioning/billing workflows.
   - Design should define a minimal operator-configured rule source without pre-implementing future GUI/provisioning features.

7. **Currency and authority posture**
   - Cost estimation supports provider-reported and estimated cost, but budget rules require explicit behavior for missing prices, currency mismatch, and authority requirements.

8. **Fail-open/fail-closed and readiness semantics**
   - Strict enforcement needs readiness before serving protected traffic; advisory mode can degrade safely.
   - Memory-only state may satisfy local/single-process operation but may be insufficient for multi-instance strict budgets unless explicitly reported.

## Implementation Approach Options

### Option A: Extend Existing Token Accounting Preflight and Ledger

**Shape**
- Add quota/rate/budget fields under existing accounting config.
- Expand token-accounting preflight to evaluate rules and possibly deny before backend open.
- Extend ledger/control-plane usage aggregates to approximate windows.

**Pros**
- Fastest path for advisory or single-process limits.
- Reuses current counting, pricing, stream reconstruction, ledger, and runtime wiring.
- Minimal new package surface.

**Cons**
- High risk of adding side effects to a checker that is already used for routing estimates.
- Existing ledger is not an atomic reservation/window store.
- Measurement and enforcement responsibilities would become tangled.
- Hard to support strict concurrency guarantees and post-stream settlement cleanly.

**Fit**
- Acceptable only for a narrow transitional/advisory MVP. Weak fit for the full approved requirements.

### Option B: Create a New Accounting Authority Capability

**Shape**
- Add a distinct accounting authority capability for rule matching, window state, reservations, admission decisions, settlement, status, and queries.
- Add memory and durable authority stores with explicit atomic reservation semantics.
- Integrate with runtime before backend open and after stream finalization.

**Pros**
- Clean separation between passive measurement and enforceable authority.
- Testable domain rules and store contracts.
- Better fit for strict concurrency, reservation, settlement, and readiness behavior.
- Avoids side effects in token preflight estimate path.

**Cons**
- More design and implementation surface.
- Needs careful integration with existing token accounting, control-plane evidence, and frontend error mapping.
- Must avoid duplicating usage facts already recorded by token ledger/control-plane.

**Fit**
- Strong fit for full requirements if delivered incrementally.

### Option C: Hybrid Authority Reusing Measurement and Evidence

**Shape**
- Create a distinct accounting authority domain/app service for rules, decisions, reservations, windows, and settlement.
- Reuse token accounting for estimates and final usage reconstruction.
- Reuse price catalog for spend calculations.
- Reuse policydecision/control-plane evidence for operator visibility.
- Add only narrow runtime and config wiring needed for authority admission and settlement.

**Pros**
- Keeps measurement and enforcement separate while reusing mature foundations.
- Preserves current preflight and routing-estimate semantics.
- Aligns with product boundaries: core-owned protocol-neutral authority, adapters own storage/query details.
- Supports incremental phases: rule model, in-memory store, runtime admission, settlement, durable store/query.

**Cons**
- Requires clear naming to avoid two competing accounting concepts.
- Requires careful correlation across token ledger, authority windows, policy evidence, and control-plane queries.
- More planning needed for store atomicity and multi-attempt settlement.

**Fit**
- Best balanced option for design exploration.

### Option D: Implement as Feature Plugin Only

**Shape**
- Use pre-request hooks/policy observers/usage observers to implement rules externally to core runtime.

**Pros**
- Keeps core smaller.
- Exercises extension-platform seams for custom enterprise policies.

**Cons**
- Usage observers are fail-open and post-facto; they cannot provide strict synchronous enforcement.
- Strict reservation and settlement require privileged runtime timing and lineage awareness.
- Hard to guarantee no double-counting across failover/racing attempts.

**Fit**
- Useful later for custom rule providers, but insufficient for core authority foundation.

## Effort and Risk

- **Estimated effort**: XL (2+ weeks). The feature spans rule semantics, config validation, runtime admission, stream settlement, live state, durable atomicity, evidence/query extensions, and frontend-safe denial mapping.
- **Risk**: High. Main risks are atomic reservations under concurrency, multi-attempt settlement correctness, strict fail-closed readiness, and preserving streaming/no-retry invariants.
- **Primary mitigation**: design and test the authority rule/window/reservation model separately before runtime wiring; keep reservation side effects out of token preflight and routing estimates.

## Research Needed for Design Phase

1. **Window algorithm and semantics**
   - Decide fixed window, sliding window, token bucket, or hybrid behavior per quota/rate/budget rule type.

2. **Reservation lifecycle**
   - Decide request-level vs attempt-level reservations, loser release, swallowed attempt handling, cancellation settlement, and overage policy.

3. **Atomic durable store strategy**
   - Research SQLite and PostgreSQL transaction/upsert patterns for scoped windows and reservations without leaking SQL/Bun types into core contracts.

4. **Config/rule source shape**
   - Decide whether rules live under `accounting.authority`, a sibling top-level block, or a future provider-managed rule source.

5. **Evidence/query model**
   - Decide whether to extend control-plane usage/policy DTOs or add accounting-specific decision/status DTOs for remaining limits, reservations, reset context, and settlements.

6. **Client denial mapping**
   - Identify stable frontend error categories/messages for quota exceeded, rate limited, budget exceeded, accounting unavailable, and reservation failed.

7. **Strict-authoritative interaction**
   - Clarify how current `AccountingConfig.StrictAuthoritative` relates to rule-level authority requirements for estimated vs provider-reported usage/cost.

8. **Single-process vs multi-instance posture**
   - Decide which store modes may satisfy strict enforcement and how disabled/degraded/unavailable states are reported for unsupported deployments.

## Recommended Design Focus

Design should evaluate Option C first: a distinct accounting authority capability that reuses token accounting, price catalog, policydecision evidence, and control-plane recording/query, with explicit admission/reservation and settlement seams in runtime. Option A can remain a reduced-scope fallback for advisory-only MVP behavior; Option D should be reserved for custom policy providers after the core authority foundation exists.

---

# Design Discovery and Synthesis: Usage, Quota, Rate, and Budget Authority

Generated: 2026-07-09T00:05:45+02:00

## Summary

- **Feature**: `usage-quota-rate-budget-authority`
- **Discovery Scope**: Complex integration / existing-system extension
- **Key Findings**:
  - The feature should be a distinct `usageauthority` bounded context, not an expansion of token-counting preflight.
  - Existing token accounting, pricing, policydecision, scope, and control-plane evidence should be reused as inputs/projections rather than duplicated.
  - Strict enforcement requires atomic reservation and settlement semantics that the append-only token ledger and control-plane event ledger do not provide today.

## Research Log

### Runtime Integration Points
- **Context**: Requirements require deny/reserve before backend work and reconcile after stream finalization.
- **Sources Consulted**: `internal/core/runtime/executor_open_attempt.go`, `internal/core/runtime/attempt_stream.go`, `internal/core/runtime/executor_config.go`.
- **Findings**:
  - Token preflight already runs before route-attempt acquisition and backend open.
  - The same preflight checker is also used for side-effect-free routing size estimates.
  - Final and partial usage evidence is available from the stream path after provider/local reconstruction.
- **Implications**:
  - Authority admission must be a separate side-effectful runtime seam, not hidden inside token preflight.
  - Settlement must be idempotent and attached to logical request plus B-leg lineage.

### Existing Measurement and Evidence
- **Context**: Requirements need usage and cost inputs plus operator-visible evidence.
- **Sources Consulted**: `internal/core/tokenaccounting`, `internal/core/accounting`, `pkg/lipsdk/policydecision`, `pkg/lipsdk/controlplane`, `internal/infra/controlplane/observers`.
- **Findings**:
  - Token accounting provides counts, reconstructed usage, usage planes, durable ledger records, and strict ledger-write behavior.
  - Cost estimation provides provider-reported and price-catalog estimated cost sources.
  - Policydecision and control-plane can carry safe decision/evidence records, but current DTOs lack accounting-specific state such as remaining, reserved, reset, retry, and settlement fields.
- **Implications**:
  - Design should extend evidence/query contracts narrowly for accounting authority instead of creating a separate diagnostic universe.

### Hexagonal Boundary Evaluation
- **Context**: The feature combines pure policy, orchestration, stores, runtime seams, and admin query surfaces.
- **Sources Consulted**: `.kiro/steering/structure.md`, `.kiro/steering/tech.md`, `golang-hexagonal-architecture` skill.
- **Findings**:
  - Pure rule matching, window math, and reservation invariants belong in domain code.
  - Admission, reservation, settlement, evidence emission, and readiness sequencing belong in app/use-case code.
  - Stores and HTTP/query adapters must translate infrastructure and wire details at the edge.
- **Implications**:
  - Use bounded-context-first packages under `internal/core/usageauthority/{domain,app}` and `internal/infra/usageauthority/authoritystore`.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend token accounting | Add rules and windows into current token preflight and ledger | Fast for advisory limits | Side effects in estimate path, weak atomicity, responsibility bloat | Rejected for full requirements |
| New authority capability | Separate domain/app/store/query authority | Clear ownership, strict concurrency possible | More files and contracts | Viable but should reuse existing evidence |
| Hybrid authority | New authority domain/app, reuse counting, pricing, policydecision, control-plane | Best boundary fit, preserves existing semantics | Requires careful correlation | Selected for design |
| Feature plugin only | Implement rules via hooks and observers | Keeps core smaller | Cannot guarantee strict synchronous reservations or settlement | Deferred to future custom providers |

## Design Decisions

### Decision: Use a distinct `usageauthority` bounded context
- **Context**: Requirements cover enforcement authority, not passive measurement.
- **Alternatives Considered**:
  1. Extend `tokenaccounting/preflight` with budget/quota/rate rules.
  2. Create `internal/core/usageauthority` with domain and app subpackages.
- **Selected Approach**: Create `usageauthority` for rules, windows, reservations, admission, settlement, readiness, and status/query DTOs.
- **Rationale**: Keeps measurement separate from enforcement and matches the hexagonal bounded-context guidance.
- **Trade-offs**: More new files, but cleaner task boundaries and lower risk of corrupting routing estimate semantics.
- **Follow-up**: Add architecture tests that prevent SQL/Bun/provider SDK imports from `internal/core/usageauthority`.

### Decision: Build authority state instead of relying on append-only ledgers
- **Context**: Strict quota/rate/budget enforcement must be atomic under concurrent requests.
- **Alternatives Considered**:
  1. Query token/control-plane ledgers for every decision.
  2. Maintain live authority windows and reservations with idempotent settlement.
- **Selected Approach**: Use a live authority state store for windows and reservations, and emit ledgers/control-plane evidence as projections.
- **Rationale**: Historical event ledgers are evidence sources, not atomic admission counters.
- **Trade-offs**: Requires store contracts and durable migrations, but supports strict enforcement.
- **Follow-up**: Validate memory and SQLite store contracts first; gate Postgres strict mode behind integration tests.

### Decision: Expose accounting evidence through policydecision and control-plane extensions
- **Context**: Operators need decision, remaining limit, and settlement visibility.
- **Alternatives Considered**:
  1. Encode everything as generic policy records.
  2. Add accounting-specific control-plane detail/query DTOs while emitting policy-compatible decision records.
- **Selected Approach**: Emit policydecision records for allow/deny/advisory effects and add control-plane accounting detail/status/query DTOs for live authority state.
- **Rationale**: Policydecision remains the denial/effect vocabulary, while control-plane handles query-ready accounting state.
- **Trade-offs**: Requires control-plane contract shape changes and revalidation.
- **Follow-up**: Keep raw rule internals and provider payloads out of public DTOs.

## Risks & Mitigations

- Atomic reservation bugs under concurrency - mitigate with domain tests, store contract tests, and race-oriented runtime tests.
- Double settlement across retries or parallel losers - mitigate with reservation IDs and idempotency keys based on logical request, B-leg, and rule.
- Side effects during routing estimates - mitigate with separate estimator and authority admission seams plus tests proving estimates do not mutate state.
- Strict mode on insufficient backing store - mitigate with startup readiness validation and explicit advisory/degraded status.
- Evidence contract sprawl - mitigate by adding only accounting-specific control-plane fields required by requirements and keeping policydecision reason taxonomy bounded.

## References

- `.kiro/steering/product.md` - product control-plane and routing priorities.
- `.kiro/steering/tech.md` - explicit wiring, context, persistence, startup posture, and dependency policy.
- `.kiro/steering/structure.md` - package map and token accounting / usage ownership guidance.
- `.kiro/steering/api-standards.md` - protocol legality and frontend error mapping rules.
- `.kiro/steering/routing-and-orchestration.md` - B2BUA lineage, no-retry-after-output, and routing estimate constraints.
