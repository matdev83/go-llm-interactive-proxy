# Requirements Document

## Introduction

The usage, quota, rate, and budget authority feature turns LLM Interactive Proxy token accounting from passive measurement into enforceable operator control. Operators and service owners running the proxy in local or centralized enterprise deployments need to prevent runaway spend, enforce tenant/project/user allowances, throttle abusive or accidental traffic, and explain enforcement outcomes through the same safe evidence model used for sessions, attempts, usage, policy decisions, and audit records.

Today the Go runtime can count tokens, reconcile usage planes, estimate cost from pricing data, persist token-accounting ledger facts, expose safe principal/scope attribution, emit policy decision evidence, and query control-plane lifecycle records. It does not yet enforce spend budgets, token/request quotas, rate windows, or usage reservations. This feature defines the user-observable behavior required for enforceable accounting authority while preserving streaming-first execution, protocol compatibility, B2BUA lineage, safe attribution, and explicit fail-open/fail-closed posture.

## Boundary Context

- **In scope**: scope-attributed usage breakdowns, configured quota windows, configured rate windows, spend budgets and spend caps, preflight reservation and admission outcomes, post-stream reconciliation, estimated-vs-authoritative usage handling, concurrency-safe enforcement behavior, operator-visible readiness/degraded states, and policy/control-plane evidence for allowed, denied, clamped, reserved, reconciled, and unavailable accounting outcomes.
- **Out of scope**: invoices, payment collection, provider billing integration, marketplace billing, OAuth/SAML/SCIM provisioning, user-directory management, web administration UI, reporting charts, PII/prompt-injection/content-safety policy engines, and forwarding budget or quota state to backend providers or client protocols by default.
- **Adjacent expectations**: principal/scope attribution supplies safe grouping dimensions; admission-policy decision semantics supply stable allow/deny/annotate evidence; control-plane persistence and query supply durable evidence and historical windows; token accounting supplies usage and cost inputs; future admin/provisioning features may manage rules but are not required for this feature to enforce configured rules; provider-local quota headers or credential cooldowns remain provider-adapter facts unless explicitly projected into proxy-level authority evidence.
- **Revalidation triggers**: request admission timing, token accounting, usage observers, policy decision evidence, control-plane usage queries, secure-session authority, B2BUA attempt lineage, cancellation billing markers, startup security posture, streaming behavior, and no-retry-after-first-output semantics must be revalidated when this feature changes enforcement behavior.

## Requirements

### Requirement 1: Scope-Attributed Accounting Authority
**Objective:** As a platform operator, I want usage enforcement to rely on trusted request attribution, so that budgets, quotas, and rate limits apply to the correct principal, tenant, workspace, project, department, and cost center.

#### Acceptance Criteria
1. When a request reaches accounting enforcement, the LLM Interactive Proxy shall evaluate accounting rules using safe principal/scope attribution when that attribution is available.
2. When a rule targets principal, tenant, organization, workspace, project, department, cost center, credential, backend, model, route, or policy label dimensions, the LLM Interactive Proxy shall match the rule only against dimensions that are known or explicitly configured to match unknown values.
3. If a required attribution dimension is unknown for a rule that requires a known value, the LLM Interactive Proxy shall apply the rule's configured unknown-attribution behavior rather than inferring values from raw client payloads.
4. The LLM Interactive Proxy shall distinguish unknown attribution fields from known fields whose value is intentionally empty when evaluating and reporting accounting rules.
5. The LLM Interactive Proxy shall not use raw bearer tokens, API keys, OAuth tokens, resume tokens, raw transport headers, or unvetted claims as accounting authority dimensions.
6. When multiple accounting rules match the same request, the LLM Interactive Proxy shall make the set of matched rule identifiers and selected enforcement outcome visible in operator evidence.

### Requirement 2: Usage Breakdown and Accounting State
**Objective:** As an operator, I want usage to be broken down by safe business dimensions and time windows, so that enforcement decisions and usage views reflect the same accounting state.

#### Acceptance Criteria
1. When usage is recorded for a request or backend attempt, the LLM Interactive Proxy shall make token, request, and cost dimensions available for aggregation by supported safe scope, backend, model, route, accounting plane, and time-window dimensions.
2. When a query consumer asks for usage totals for a supported dimension and time window, the LLM Interactive Proxy shall return matching totals with availability state and accounting authority when available.
3. When usage data is partial, estimated, unavailable, redacted, expired, or unsupported for a requested aggregation, the LLM Interactive Proxy shall report that state instead of fabricating complete totals.
4. When multiple usage planes are available for the same request, the LLM Interactive Proxy shall identify which usage plane is used for enforceable accounting and which planes remain advisory evidence.
5. When provider-reported usage, local estimated usage, reserved usage, and reconciled usage differ, the LLM Interactive Proxy shall preserve enough operator evidence to explain the selected enforceable amount.
6. If no matching usage exists for a valid aggregation query, the LLM Interactive Proxy shall return an empty or zero result with a recorded availability state rather than inventing usage.
7. When historical usage totals and live enforcement state are both available, the LLM Interactive Proxy shall distinguish historical aggregates from remaining-limit authority so query consumers do not treat advisory evidence as active enforcement state.

### Requirement 3: Quota Window Enforcement
**Objective:** As a service owner, I want token and request quotas over time windows, so that teams and users cannot exceed configured allowances.

#### Acceptance Criteria
1. Where quota enforcement is enabled, the LLM Interactive Proxy shall support quotas over configured time windows for request counts, total tokens, input tokens, output tokens, cache-read tokens, cache-write tokens, and reasoning tokens.
2. When a request would keep all matching quotas within their configured limits, the LLM Interactive Proxy shall allow the request to continue subject to other enforcement rules.
3. When a request would exceed a strict matching quota before backend output starts, the LLM Interactive Proxy shall deny the request with a stable quota-exceeded reason and operator-visible evidence.
4. Where a quota rule is configured for advisory behavior, if a request would exceed the quota, the LLM Interactive Proxy shall allow the request and record advisory quota evidence instead of denying execution.
5. When a quota window resets according to its configured time boundary, the LLM Interactive Proxy shall evaluate later requests against the reset window without losing historical evidence from prior windows that remains query-visible.
6. If multiple strict quotas match a request, the LLM Interactive Proxy shall enforce the most restrictive exceeded outcome and preserve evidence for all matched exceeded quotas.

### Requirement 4: Rate Window Enforcement
**Objective:** As an operator, I want rate limits for request admission, so that accidental loops, abusive traffic, and noisy tenants can be throttled before expensive backend work starts.

#### Acceptance Criteria
1. Where rate enforcement is enabled, the LLM Interactive Proxy shall support request-rate windows for configured safe scope, backend, model, route, and policy-label dimensions.
2. When a request is within all matching strict rate windows, the LLM Interactive Proxy shall allow the request to continue subject to other enforcement rules.
3. When a request exceeds a strict matching rate window before backend output starts, the LLM Interactive Proxy shall deny or throttle the request with a stable rate-limit reason and operator-visible retry context when available.
4. Where a rate rule is configured for advisory behavior, if a request exceeds the rate window, the LLM Interactive Proxy shall allow the request and record advisory rate-limit evidence instead of denying execution.
5. When a rate limit denial is surfaced to a client, the LLM Interactive Proxy shall use a protocol-legal client-safe error category without exposing raw rule internals or unsafe attribution values.
6. If rate state is unavailable for a rule that requires strict enforcement, the LLM Interactive Proxy shall apply the rule's configured unavailable-state behavior rather than silently ignoring the rule.

### Requirement 5: Spend Budget and Spend Cap Enforcement
**Objective:** As a budget owner, I want monetary spend limits over configured windows, so that proxy traffic cannot create surprise provider costs.

#### Acceptance Criteria
1. Where budget enforcement is enabled, the LLM Interactive Proxy shall support spend budgets and spend caps over configured time windows for supported safe scope, backend, model, route, and policy-label dimensions.
2. When a request has enough known or reservable budget to proceed under all matching strict budget rules, the LLM Interactive Proxy shall allow the request to continue subject to other enforcement rules.
3. When a request would exceed a strict matching spend budget before backend output starts, the LLM Interactive Proxy shall deny the request with a stable budget-exceeded reason and operator-visible evidence.
4. Where a budget rule is configured for advisory behavior, if a request would exceed the budget, the LLM Interactive Proxy shall allow the request and record advisory budget evidence instead of denying execution.
5. If cost cannot be calculated because price, provider cost, currency, or usage inputs are unavailable, the LLM Interactive Proxy shall apply the matching rule's configured cost-unavailable behavior.
6. When provider-reported cost and estimated cost are both available, the LLM Interactive Proxy shall identify which cost source is enforceable and preserve the alternate cost source as evidence when available.
7. When currencies differ across a budget rule and observed cost evidence, the LLM Interactive Proxy shall refuse to treat the values as interchangeable unless an explicit supported conversion policy is available.

### Requirement 6: Preflight Reservation and Admission
**Objective:** As an operator, I want expensive work reserved or denied before backend execution, so that enforcement prevents overspend instead of only reporting it afterward.

#### Acceptance Criteria
1. When a request can be evaluated before backend execution, the LLM Interactive Proxy shall make a preflight accounting decision before committing protected upstream work.
2. When a strict quota or budget rule requires reservation, the LLM Interactive Proxy shall reserve the request's enforceable estimated usage or spend before allowing backend execution.
3. If an atomic reservation check determines that a matching strict window lacks capacity, the LLM Interactive Proxy shall deny before backend execution regardless of fail-open posture; reservation-failure behavior applies only when reservation infrastructure or required reservation state is unavailable.
4. When a preflight decision denies a request, the LLM Interactive Proxy shall record that no backend attempt was committed because of the accounting decision.
5. When a preflight decision clamps a requested maximum output or otherwise reduces reserved exposure, the LLM Interactive Proxy shall record the clamp reason and effective reserved amount in operator evidence.
6. Where the request estimate is unavailable, invalid, or outside supported accounting dimensions, the LLM Interactive Proxy shall apply the matching rule's configured estimate-unavailable behavior.
7. When no strict accounting rule requires reservation, the LLM Interactive Proxy shall not create a misleading enforceable reservation while still allowing advisory accounting evidence to be recorded.
8. When the LLM Interactive Proxy estimates request size for route planning, candidate eligibility, or diagnostics without committing backend execution, the LLM Interactive Proxy shall not create, consume, or mutate quota, budget, rate, or spend reservations.
9. When a preflight admission check reads existing usage or reservation state, the LLM Interactive Proxy shall report whether the decision used live enforceable state, historical evidence, estimated state, or unavailable state.

### Requirement 7: Post-Stream Reconciliation and Reservation Settlement
**Objective:** As an operator, I want reserved usage to be reconciled with actual usage, so that accounting windows remain accurate after streaming, cancellation, and provider finalization.

#### Acceptance Criteria
1. When a backend attempt produces final usage evidence, the LLM Interactive Proxy shall reconcile the final enforceable usage or cost against any reservation for the same logical request and attempt.
2. When final usage is lower than reserved usage, the LLM Interactive Proxy shall release or adjust the unused reserved amount so later accounting decisions are not over-constrained.
3. When final usage is higher than reserved usage, the LLM Interactive Proxy shall record the overage and apply configured post-reconciliation behavior for matching strict rules.
4. When a request is canceled before final usage is available, the LLM Interactive Proxy shall reconcile using available authoritative finalization, estimated cancellation evidence, or unavailable-state evidence according to configured accounting authority rules.
5. If usage reconstruction fails after client-visible output has started, the LLM Interactive Proxy shall record the accounting failure without silently retrying or replacing the committed backend attempt.
6. When final provider-reported usage arrives after estimated usage was used for an initial settlement, the LLM Interactive Proxy shall preserve both the prior estimate and the final authoritative adjustment in operator evidence.
7. If no reservation exists for a request that produces usage, the LLM Interactive Proxy shall still record usage evidence and update applicable accounting windows according to configured enforcement behavior.
8. If settlement is retried for the same logical request and backend attempt, the LLM Interactive Proxy shall avoid double-counting usage, spend, released reservations, or overage evidence.
9. When a backend attempt loses a race or is swallowed before client-visible output, the LLM Interactive Proxy shall release, settle, or mark any associated reservation according to configured accounting rules without attributing surfaced usage to that non-surfaced attempt.

### Requirement 8: Estimated, Authoritative, and Unavailable Accounting Authority
**Objective:** As a platform operator, I want enforcement to distinguish estimated and authoritative usage, so that policy posture is explicit when exact provider data is unavailable.

#### Acceptance Criteria
1. When accounting evidence is provider-reported or provider-counted and marked authoritative, the LLM Interactive Proxy shall be able to use that evidence as enforceable accounting authority for matching rules.
2. When accounting evidence is locally estimated, policy-reserved, proxy-adjusted, advisory, delegated, or unavailable, the LLM Interactive Proxy shall identify the authority level before using it for enforcement.
2a. The LLM Interactive Proxy shall track token/request usage authority independently from monetary-cost authority, and authoritative tokens shall not promote an estimated or absent monetary cost to authoritative cost.
2b. The LLM Interactive Proxy shall track whether an authoritative monetary cost value is present, including an explicit authoritative zero, so a missing cost remains reconcilable later.
2c. The LLM Interactive Proxy shall track authority independently for each enforceable token unit, preserve explicitly reported authoritative zero usage, and leave unreported units estimated and eligible for later reconciliation.
2d. Monetary settlement authority shall update only monetary state and shall not promote token authority for any unit absent from the provider reading.
3. Where a rule requires authoritative usage or cost, if only estimated or unavailable evidence exists, the LLM Interactive Proxy shall apply that rule's configured authority-unavailable behavior.
4. Where a rule allows estimated accounting authority, the LLM Interactive Proxy shall mark enforcement evidence as estimated and preserve later authoritative reconciliation when available.
5. If two accounting sources conflict for the same enforceable plane, the LLM Interactive Proxy shall resolve the enforceable amount deterministically and record the conflict in operator evidence.
6. The LLM Interactive Proxy shall not silently drop required accounting semantics when moving between estimated preflight decisions and final post-stream reconciliation.

### Requirement 9: Policy Decisions, Client Outcomes, and Evidence
**Objective:** As an operator and client integrator, I want accounting enforcement outcomes to surface consistently, so that denials are understandable without breaking protocol compatibility.

#### Acceptance Criteria
1. When accounting enforcement allows, denies, skips, clamps, reserves, reconciles, or errors, the LLM Interactive Proxy shall emit policy-compatible operator evidence with trace correlation, lifecycle position, outcome, effect, reason code, safe scope attribution, and accounting rule identity when available.
2. When a strict accounting decision denies a request before backend output starts, the LLM Interactive Proxy shall surface the denial through the legal error shape of the active frontend protocol.
3. When accounting enforcement records advisory evidence, the LLM Interactive Proxy shall preserve the original client-visible request outcome unless another feature changes it.
4. When an accounting decision occurs after client-visible output has started, the LLM Interactive Proxy shall not perform transparent retry or failover because of that later decision.
5. If a frontend cannot represent an accounting denial exactly, the LLM Interactive Proxy shall map the outcome to a stable client-safe error category and preserve exact accounting evidence for operators.
6. The LLM Interactive Proxy shall not expose raw prompts, raw provider payloads, secrets, unsafe claim values, raw rule internals, or privileged accounting details in client-facing accounting messages.

### Requirement 10: Failure, Degraded, and Startup Posture
**Objective:** As an operator, I want accounting authority failures to behave predictably, so that availability and cost-control trade-offs are explicit.

#### Acceptance Criteria
1. Where an accounting rule declares fail-closed behavior, if required accounting state, reservation infrastructure, usage, cost, or best-effort evidence recording is unavailable before protected upstream work starts, the LLM Interactive Proxy shall deny or fail the affected lifecycle step with a stable accounting-failure reason.
2. Where an accounting rule declares fail-open behavior, if required accounting state, reservation infrastructure, usage, cost, or best-effort evidence recording is unavailable, the LLM Interactive Proxy shall continue the affected lifecycle step and record skipped enforcement evidence; deterministic capacity exhaustion and evidence configured as required pre-work prerequisites shall still deny.
3. When accounting enforcement exceeds its configured evaluation budget, the LLM Interactive Proxy shall apply the applicable rule's failure behavior rather than waiting indefinitely.
4. When the client request context is canceled, the LLM Interactive Proxy shall stop accounting work that is no longer needed and shall not convert cancellation into an unrelated accounting denial.
5. When accounting authority is enabled, the LLM Interactive Proxy shall expose whether the authority is ready, degraded, unavailable, or disabled before operators rely on enforcement outcomes.
6. If startup configuration requires strict accounting authority and the required accounting capability is unavailable, the LLM Interactive Proxy shall fail closed before serving protected traffic.
7. While accounting authority is disabled, the LLM Interactive Proxy shall preserve existing token accounting and request execution behavior without applying quota, rate, or budget enforcement.
8. When accounting authority rules are configured, the LLM Interactive Proxy shall validate rule identifiers, scope dimensions, time windows, limits, currencies, authority requirements, and failure behavior before serving protected traffic.
9. If strict accounting authority requires atomic window or reservation behavior that the active backing capability cannot provide, the LLM Interactive Proxy shall report the authority as unavailable or fail closed according to configured startup posture rather than silently downgrading strict enforcement.
10. Where accounting authority is enabled with an advisory-only backing capability, the LLM Interactive Proxy shall expose that enforcement is advisory rather than strict before operators rely on it.

### Requirement 11: Concurrent Requests, Attempts, and Streaming Invariants
**Objective:** As a client and operator, I want enforcement to stay correct under concurrent traffic and multi-attempt routing, so that accounting controls do not corrupt streaming or lineage behavior.

#### Acceptance Criteria
1. When concurrent requests match the same strict accounting window, the LLM Interactive Proxy shall prevent the combined admitted usage, request count, or reserved spend from exceeding the configured limit except where configured overage behavior explicitly allows it.
2. When one logical request creates multiple backend attempts before client-visible output, the LLM Interactive Proxy shall correlate reservations, usage, and reconciliation with the logical request and each attempted B-leg without double-counting surfaced usage.
3. When parallel backend racing creates losing attempts, the LLM Interactive Proxy shall keep accounting evidence for losers distinguishable from surfaced attempts.
4. When client-visible output has begun for a backend attempt, the LLM Interactive Proxy shall preserve the no-retry-after-first-output invariant regardless of later accounting state changes.
5. While streaming a response, the LLM Interactive Proxy shall preserve canonical event ordering around usage, accounting evidence, and enforcement outcomes.
6. The LLM Interactive Proxy shall keep non-streaming behavior as collection over the same canonical stream path while preserving the same accounting enforcement and evidence requirements.
7. If accounting work continues after request cancellation only to settle usage or evidence, the LLM Interactive Proxy shall make that continuation visible to operators without changing already-surfaced client output.

### Requirement 12: Operator Visibility and Query Behavior
**Objective:** As an operator, I want accounting decisions and remaining limits to be inspectable, so that I can understand enforcement behavior and current usage posture.

#### Acceptance Criteria
1. When accounting authority is enabled, the LLM Interactive Proxy shall make current usage, reserved usage, remaining quota, remaining budget, rate-window status, and enforcement availability query-visible for supported safe dimensions and time windows.
2. When an accounting decision is recorded, the LLM Interactive Proxy shall make the decision query-visible with stable correlation identifiers, matched rule identity, outcome, reason code, availability state, and safe scope attribution when available.
3. When a query asks for unsupported accounting dimensions, unsupported time windows, unavailable state, or too broad a result set, the LLM Interactive Proxy shall return a stable unsupported or bounded-query indication rather than widening the query silently.
4. When accounting evidence is redacted, expired, unavailable, estimated, authoritative, advisory, reserved, reconciled, or adjusted, the LLM Interactive Proxy shall expose that state in query results when known.
5. If accounting authority is disabled, the LLM Interactive Proxy shall report the capability as disabled rather than returning misleading zero limits or empty enforcement state.
6. When existing token-accounting ledger, usage observer, policy decision, or control-plane records describe the same accounting fact, the LLM Interactive Proxy shall keep shared identifiers and safe attribution consistent across operator-visible views.
7. When current enforcement state is queried, the LLM Interactive Proxy shall distinguish live or reserved accounting state from historical usage aggregates so consumers do not mistake past usage totals for remaining-limit authority.
8. When accounting decision or status evidence is query-visible, the LLM Interactive Proxy shall expose rule identity, matched scope dimensions, window boundary or reset context, limit amount, consumed amount, reserved amount, remaining amount, and availability state when those values are known and safe.

### Requirement 13: Privacy, Safety, and Explicit Exclusions
**Objective:** As a security-conscious operator and delivery planner, I want enforcement evidence to stay safe and scope-limited, so that accounting authority does not become an unrelated billing, identity, or content-policy feature.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall keep raw bearer tokens, API keys, OAuth tokens, resume tokens, raw transport headers, unsafe claims, raw prompts, raw responses, and provider payloads out of default accounting enforcement evidence and query results.
2. Where privileged accounting evidence includes richer detail, the LLM Interactive Proxy shall require explicit privileged visibility posture before that detail is available.
3. The LLM Interactive Proxy shall not implement invoice generation, payment collection, provider billing settlement, marketplace billing, or customer charge calculation as part of this feature.
4. The LLM Interactive Proxy shall not implement OAuth, SAML, SCIM, user-directory management, tenant provisioning, or web administration workflows as part of this feature.
5. The LLM Interactive Proxy shall not implement PII detection, prompt-injection detection, harmful-content detection, dangerous-tool policy, or other content-safety policy engines as part of this feature.
6. The LLM Interactive Proxy shall not forward accounting rule definitions, budget state, quota state, rate state, or control-plane evidence to backend providers or client-facing protocol responses by default.
7. The LLM Interactive Proxy shall preserve existing routing, capability negotiation, secure-session authority, protocol translation, and streaming behavior unless an accounting decision explicitly denies, clamps, reserves, or reports advisory evidence at a legal lifecycle position.
8. The LLM Interactive Proxy shall not treat backend-provider quota headers, provider account cooldowns, or provider-specific rate-limit metadata as proxy-level tenant, project, department, user, or budget authority unless an explicit safe mapping is configured.
