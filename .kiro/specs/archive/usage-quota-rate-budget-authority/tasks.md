# Implementation Plan

- [x] 1. Establish public accounting-authority evidence contracts
- [x] 1.1 Add dedicated control-plane accounting authority DTOs and queries
  - Define additive safe DTOs for authority decisions, limit status, reservations, settlements, adjustments, availability, redaction, and bounded pages.
  - Keep existing usage rows and usage aggregates historical; live remaining-limit state is exposed only through the new accounting-authority query/status contract.
  - Done when contract tests prove stable JSON shape, unknown-vs-known-empty safe scope behavior, unsupported-filter reporting, and no raw rule internals, prompts, provider payloads, headers, or credentials are represented.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 12.1, 12.2, 12.3, 12.4, 12.5, 12.7, 12.8, 13.1, 13.2_
  - _Boundary: SDK/public contract_
  - _Validation: go test ./pkg/lipsdk/controlplane_

- [x] 1.2 Define policy-compatible accounting decision evidence
  - Establish bounded accounting reason codes and policydecision projection rules for allow, deny, advisory, clamp, reserve, reconcile, unavailable, and error outcomes.
  - Preserve existing policydecision record shape while using only safe annotations and stable client categories for accounting outcomes.
  - Done when SDK/core tests prove accounting evidence can be emitted without changing policy outcomes or exposing unsafe rule internals.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 13.1, 13.2, 13.6_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: go test ./pkg/lipsdk/policydecision ./pkg/lipsdk/controlplane_

- [x] 2. Build the pure usage-authority domain model
- [x] 2.1 Add safe amounts, dimensions, and fixed-window keys
  - Represent request counts, token dimensions, and monetary nano-unit amounts without mixing units or currencies.
  - Represent rule dimensions from safe scope fields, backend, model, route, and policy labels with explicit unknown and known-empty behavior.
  - Done when pure domain tests cover amount validation, dimension key determinism, fixed-window boundary calculation, and unsupported algorithm rejection.
  - _Requirements: 1.2, 1.3, 1.4, 2.3, 3.1, 4.1, 5.1, 5.7, 8.2, 13.1_
  - _Boundary: domain policy_
  - _Validation: go test ./internal/core/usageauthority/domain_

- [x] 2.2 Add quota, rate, budget, and spend-cap rule matching
  - Match rules against safe scope, backend, model, route, policy labels, authority requirements, unknown-attribution behavior, and advisory or strict posture.
  - Select deterministic outcomes when multiple rules match, including the most restrictive strict exceeded outcome while preserving evidence for all matched rules.
  - Done when domain tests show correct matching for known, known-empty, unknown, match-unknown, nonmatching, multiple-rule, and currency-mismatch cases.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 3.2, 3.3, 3.4, 3.5, 3.6, 4.2, 4.3, 4.4, 4.5, 4.6, 5.2, 5.3, 5.4, 5.5, 5.6_
  - _Boundary: domain policy_
  - _Depends: 2.1_
  - _Validation: go test ./internal/core/usageauthority/domain_

- [x] 2.3 Add decisions, reservations, settlements, and idempotency invariants
  - Model allow, deny, advisory, clamp, reserve, release, settle, overage, unavailable, and error outcomes separately from frontend rendering.
  - Define reservation and settlement idempotency keys using logical request, A-leg, B-leg, attempt, rule, and reservation identity.
  - Done when pure domain tests prove repeated settlement cannot double-count consumed usage, spend, released reservation, or overage state.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 7.1, 7.2, 7.3, 7.8, 7.9, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 11.2, 11.3_
  - _Boundary: domain policy_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/usageauthority/domain_

- [x] 2.4 Add domain status and validation behavior
  - Validate rule identifiers, dimensions, windows, limits, currencies, failure behaviors, authority requirements, and backing-capability posture without I/O dependencies.
  - Represent disabled, ready, degraded, unavailable, and advisory-only authority states in domain terms.
  - Done when validation tests reject unsafe or ambiguous rule definitions and status tests preserve safe reason codes only.
  - _Requirements: 10.1, 10.2, 10.3, 10.5, 10.8, 10.9, 10.10, 13.3, 13.4, 13.5, 13.6, 13.8_
  - _Boundary: domain policy_
  - _Depends: 2.3_
  - _Validation: go test ./internal/core/usageauthority/domain_

- [x] 3. Implement usage-authority application orchestration
- [x] 3.1 Define app-owned ports, stable errors, and status translation
  - Define consumer-owned ports for rule snapshots, live state, evidence, usage/cost inputs, clocks, and IDs without exposing SQL, Bun, HTTP, provider SDK, or concrete plugin types.
  - Add stable app errors for disabled, degraded, unavailable, reservation conflict, duplicate settlement, invalid query, unsupported filter, and evaluation timeout.
  - Done when compile-time tests prove memory/durable adapters and test fakes can satisfy the ports while core contracts stay infrastructure-free.
  - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.9, 10.10, 13.1, 13.6_
  - _Boundary: app orchestration_
  - _Depends: 1.1, 1.2, 2.4_
  - _Validation: go test ./internal/core/usageauthority/app_

- [x] 3.2 Add pre-backend admission and reservation orchestration
  - Evaluate matching rules with safe scope, backend/model/route, request estimate, cost estimate, and live store state before protected backend work starts.
  - Reserve strict quota and budget exposure atomically when required, and return allow, deny, advisory, clamp, unavailable, or error decisions with evidence payloads.
  - Done when app tests prove strict denials produce no reservation, strict allows produce reservation references, advisory decisions do not block execution, and estimate-only inputs do not mutate store state.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 3.2, 3.3, 3.4, 3.5, 3.6, 4.2, 4.3, 4.4, 4.5, 4.6, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.9, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 9.1, 9.3, 10.1, 10.2, 10.3, 10.4, 11.1_
  - _Boundary: app orchestration_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/usageauthority/app_

- [x] 3.3 Add final surfaced-attempt settlement orchestration
  - Reconcile final provider-reported or locally reconstructed usage and cost against reservations for surfaced backend attempts.
  - Release unused reserved amount, record overage, preserve estimated and authoritative adjustments, and update live windows idempotently.
  - Done when app tests prove lower-than-reserved, equal, overage, provider-final-after-estimate, and duplicate settlement cases produce stable results.
  - _Requirements: 7.1, 7.2, 7.3, 7.6, 7.7, 7.8, 8.1, 8.2, 8.4, 8.5, 8.6, 11.2_
  - _Boundary: app orchestration_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/usageauthority/app_

- [x] 3.4 Add partial, unavailable, and cancellation settlement orchestration
  - Settle canceled, failed-accounting, unavailable, and partial usage outcomes according to rule authority and failure behavior.
  - Preserve operator-visible evidence without converting client cancellation into unrelated accounting denial.
  - Done when app tests prove cancellation, count-unavailable, cost-unavailable, reconstruction-failure, and post-output failure cases produce configured fail-open or fail-closed outcomes.
  - _Requirements: 7.4, 7.5, 7.7, 8.2, 8.3, 8.4, 10.1, 10.2, 10.3, 10.4, 11.7_
  - _Boundary: app orchestration_
  - _Depends: 3.3_
  - _Validation: go test ./internal/core/usageauthority/app_

- [x] 3.5 Add release behavior for swallowed and losing attempts
  - Release or mark reservations for pre-output swallowed attempts and parallel losing attempts without attributing surfaced usage to non-surfaced B-legs.
  - Preserve logical request, A-leg, B-leg, and attempt correlation for release evidence.
  - Done when app tests prove failover-swallowed and parallel-loser releases are idempotent and cannot reduce surfaced attempt accounting.
  - _Requirements: 7.8, 7.9, 11.2, 11.3, 11.4, 11.5_
  - _Boundary: app orchestration_
  - _Depends: 3.4_
  - _Validation: go test ./internal/core/usageauthority/app_

- [x] 3.6 Add live authority query and readiness services
  - Serve current usage, reserved usage, remaining quota, remaining budget, rate-window status, decision history, unsupported filters, and authority capability status through bounded query DTOs.
  - Keep live authority state separate from historical usage aggregates and control-plane evidence rows.
  - Done when query tests prove disabled, empty, unsupported, too-broad, advisory-only, degraded, unavailable, and ready responses are distinct.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 10.5, 10.6, 10.7, 10.10, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 12.7, 12.8_
  - _Boundary: query seam_
  - _Depends: 3.5_
  - _Validation: go test ./internal/core/usageauthority/app_

- [x] 3.7 Add policydecision and control-plane evidence projection
  - Project admission, reservation, settlement, release, overage, unavailable, advisory, and error outcomes into safe policydecision and control-plane accounting-authority evidence.
  - Use stable source keys so repeated evidence delivery deduplicates without hashing raw prompts, payloads, headers, or credentials.
  - Done when evidence tests prove policy outcomes are preserved, exact operator evidence remains queryable, and unsafe values are rejected or redacted.
  - _Requirements: 2.4, 6.4, 7.6, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 12.2, 12.4, 13.1, 13.2_
  - _Boundary: app orchestration_
  - _Depends: 3.6_
  - _Validation: go test ./internal/core/usageauthority/app ./pkg/lipsdk/controlplane ./pkg/lipsdk/policydecision_

- [x] 4. Build authority-state stores and contracts
- [x] 4.1 Add in-memory atomic reservation behavior
  - Store live windows, decisions, and reservations with atomic reserve semantics for single-process strict or advisory operation.
  - Prevent concurrent strict requests from exceeding matching windows except where configured overage behavior explicitly allows it.
  - Done when concurrent memory-store tests prove admitted totals, request counts, reserved spend, and remaining amounts stay consistent.
  - _Requirements: 3.1, 3.2, 3.3, 3.5, 3.6, 4.1, 4.2, 4.3, 4.6, 5.1, 5.2, 5.3, 5.5, 10.9, 11.1_
  - _Boundary: driven adapter_
  - _Depends: 3.1_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore_

- [x] 4.2 Add in-memory settlement, release, query, and readiness behavior
  - Settle and release reservations idempotently while preserving decision history and live status query rows.
  - Report disabled, ready, degraded, unavailable, and advisory-only store readiness states without raw infrastructure detail.
  - Done when memory-store tests prove duplicate settlement is no-op, loser release is safe, bounded queries page correctly, and readiness states classify correctly.
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.8, 7.9, 10.5, 10.7, 10.10, 11.2, 11.3, 12.1, 12.3, 12.4, 12.5, 12.7, 12.8_
  - _Boundary: driven adapter_
  - _Depends: 4.1_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore_

- [x] 4.3 Add durable authority-store migrations
  - Create durable window, reservation, decision, and status storage with idempotency keys, correlation fields, safe scope dimensions, window indexes, and bounded safe summaries.
  - Keep table shape separate from token-accounting and control-plane ledgers while preserving shared correlation identifiers.
  - Done when SQLite migration tests pass locally and Postgres migration tests are gated by existing integration settings.
  - _Requirements: 3.1, 4.1, 5.1, 7.8, 10.6, 10.9, 11.1, 12.6_
  - _Boundary: driven adapter_
  - _Depends: 4.2_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore -run Test.*Migration_

- [x] 4.4 Add durable reservation, settlement, release, and query behavior
  - Implement atomic reserve, idempotent settle, release, readiness, limit status, and decision history behavior against the durable store.
  - Map known infrastructure conflicts and unavailable backing capability into stable app errors without leaking SQL, DSN, driver, or Bun details.
  - Done when durable-store tests prove parity with memory behavior for reserve, settle, release, status, query, and duplicate source-key handling.
  - _Requirements: 3.2, 3.3, 4.2, 4.3, 5.2, 5.3, 7.1, 7.2, 7.8, 10.1, 10.5, 10.6, 10.9, 11.1, 12.1, 12.2, 12.8_
  - _Boundary: driven adapter_
  - _Depends: 4.3_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore_

- [x] 4.5 Add shared authority-store contract tests
  - Define a shared contract suite for memory and durable stores covering atomicity, idempotency, readiness, query bounds, unsupported filters, and safe value preservation.
  - Ensure strict backing capability failures are reported as unavailable or advisory-only according to store posture.
  - Done when the shared contract suite passes for memory and SQLite stores and Postgres contracts run under existing integration settings.
  - _Requirements: 10.5, 10.6, 10.7, 10.9, 10.10, 11.1, 12.3, 12.4, 12.5, 12.7, 12.8_
  - _Boundary: tests_
  - _Depends: 4.4_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore/contract ./internal/infra/usageauthority/authoritystore_

- [x] 5. Add configuration, composition, and protected query integration
- [x] 5.1 Add disabled-by-default authority configuration and validation
  - Add typed accounting authority configuration for enabled state, mode, store, rules, windows, limits, currencies, unknown-attribution behavior, failure behavior, startup posture, and query exposure.
  - Reject invalid or unsafe rules, strict rules without strict-capable backing, and accidental placement of billing/provisioning/admin-GUI concerns in this feature.
  - Done when config tests prove disabled defaults preserve current behavior and invalid strict/advisory combinations fail before serving traffic.
  - _Requirements: 3.1, 4.1, 5.1, 10.6, 10.8, 10.9, 10.10, 13.3, 13.4, 13.5, 13.6, 13.7, 13.8_
  - _Boundary: config/wiring_
  - _Depends: 2.4_
  - _Validation: go test ./internal/core/config_

- [x] 5.2 Add config-backed rule source adapter
  - Convert validated config into immutable authority rule snapshots consumed by the app layer.
  - Preserve future rule-source substitution by depending only on the app-owned rule-source port.
  - Done when adapter tests prove config snapshots produce expected safe dimensions, rule IDs, windows, limits, unknown behavior, and failure posture.
  - _Requirements: 1.2, 1.3, 3.1, 4.1, 5.1, 10.8, 13.8_
  - _Boundary: driven adapter_
  - _Depends: 5.1_
  - _Validation: go test ./internal/infra/usageauthority/configsource_

- [x] 5.3 Wire authority stores and services in the runtime bundle
  - Build memory or durable authority stores, rule source, admission service, settlement service, query service, evidence sink, readiness probe, and closers from typed config.
  - Keep authority disabled by default and avoid constructing stores or wrapping runtime paths when disabled.
  - Done when runtimebundle tests prove disabled mode is a no-op, enabled mode wires all dependencies, closers are owned by the bundle, and no provider telemetry path receives authority state.
  - _Requirements: 10.5, 10.6, 10.7, 10.9, 10.10, 13.6, 13.7_
  - _Boundary: composition root_
  - _Depends: 3.7, 4.5, 5.2_
  - _Validation: go test ./internal/infra/runtimebundle_

- [x] 5.4 Add startup readiness and fail-open/fail-closed posture
  - Fail startup when strict authority requires atomic backing, durable readiness, protected query posture, or required evidence that cannot be satisfied.
  - Expose advisory-only, degraded, unavailable, ready, and disabled states before operators rely on enforcement outcomes.
  - Done when startup tests prove strict unavailable backing fails closed, advisory-only backing starts with explicit status, and raw infrastructure details do not leak.
  - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.9, 10.10_
  - _Boundary: composition root_
  - _Depends: 5.3_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/core/config_

- [x] 5.5 Add protected authority status and query routes
  - Mount status, live limits, and decision-history routes only when authority query exposure and diagnostics shared-secret protection are explicitly configured.
  - Map filters, page bounds, continuation, unsupported filters, disabled state, empty results, and stable errors to safe JSON responses.
  - Done when HTTP tests prove routes are absent when disabled, protected when enabled, bounded in all responses, and do not expose privileged raw evidence by default.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 10.5, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 12.7, 12.8, 13.1, 13.2_
  - _Boundary: driving adapter_
  - _Depends: 5.4_
  - _Validation: go test ./internal/stdhttp/admin/controlplane ./internal/stdhttp_

- [x] 6. Integrate authority into runtime execution
- [x] 6.1 Add runtime accounting-authority handles with disabled no-op behavior
  - Add admission, settlement, release, and query handles to runtime construction without changing execution behavior when authority is nil or disabled.
  - Keep constructor grouping explicit and avoid post-construction mutation.
  - Done when runtime constructor tests prove disabled authority preserves existing token accounting and request execution behavior.
  - _Requirements: 10.7, 13.7_
  - _Boundary: core/runtime_
  - _Depends: 5.3_
  - _Validation: go test ./internal/core/runtime_

- [x] 6.2 Invoke pre-backend admission on committed backend-open paths
  - Call authority admission after token accounting preflight and before backend open while preserving route size estimates as side-effect-free.
  - Record that denied decisions commit no backend attempt and no provider call.
  - Done when runtime tests prove strict quota, rate, and budget denials happen before backend open and `requestSizeEstimateForRouting` does not mutate authority state.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 9.2, 11.1_
  - _Boundary: core/runtime_
  - _Depends: 6.1_
  - _Validation: go test -run Test.*Authority.*Admission ./internal/core/runtime_

- [x] 6.3 Map authority denials and clamps to client-safe protocol outcomes
  - Convert quota exceeded, rate limited, budget exceeded, accounting unavailable, reservation failed, and clamp decisions to stable internal errors before frontend rendering.
  - Preserve exact rule and accounting detail in operator evidence, not client messages.
  - Done when frontend/runtime tests prove legal error shapes for all supported frontend protocols and no raw rule internals or unsafe attribution are client-visible.
  - _Requirements: 4.3, 4.5, 5.3, 6.5, 9.2, 9.4, 9.5, 9.6, 13.1, 13.6_
  - _Boundary: core/runtime, frontend edge integration_
  - _Depends: 6.2_
  - _Validation: go test ./internal/core/runtime ./internal/plugins/frontends/...

- [x] 6.4 Settle final surfaced attempts after usage reconstruction
  - Invoke settlement for successful surfaced attempts using final provider-reported or locally reconstructed usage and cost evidence.
  - Preserve streaming order and ledger behavior while recording authority settlement and release evidence.
  - Done when runtime tests prove successful streaming and non-streaming requests reserve, emit output, settle final usage, release unused reservation, and expose queryable evidence.
  - _Requirements: 7.1, 7.2, 7.3, 7.6, 7.7, 8.1, 8.4, 11.2, 11.5, 11.6, 12.2, 12.4_
  - _Boundary: core/runtime_
  - _Depends: 6.3_
  - _Validation: go test -run Test.*Authority.*Settlement ./internal/core/runtime_

- [x] 6.5 Settle partial, cancellation, and unavailable usage paths
  - Invoke partial/unavailable settlement for canceled requests, failed usage reconstruction, ledger write failures, and post-output accounting failures.
  - Ensure post-output settlement failures degrade authority status without retrying or replacing committed output.
  - Done when runtime tests prove cancellation and unavailable usage produce configured authority evidence without changing already-surfaced client output.
  - _Requirements: 7.4, 7.5, 7.7, 8.2, 8.3, 10.1, 10.2, 10.3, 11.4, 11.7_
  - _Boundary: core/runtime_
  - _Depends: 6.4_
  - _Validation: go test -run Test.*Authority.*Partial ./internal/core/runtime_

- [x] 6.6 Release reservations for swallowed and losing attempts
  - Release or mark reservations for pre-output failover attempts and parallel race losers without attributing surfaced usage to non-surfaced B-legs.
  - Preserve B2BUA A-leg/B-leg correlation and attempt sequence in release evidence.
  - Done when runtime tests prove failover-swallowed and parallel-loser paths release safely, do not double-count, and keep surfaced attempt evidence distinct.
  - _Requirements: 7.8, 7.9, 11.2, 11.3, 11.4, 12.6_
  - _Boundary: core/runtime_
  - _Depends: 6.5_
  - _Validation: go test -run Test.*Authority.*(Swallowed|Loser|Race) ./internal/core/runtime_

- [x] 6.7 Verify stream, non-stream, and no-retry invariants with authority enabled
  - Exercise streaming and non-streaming collection over the same canonical path while authority records decisions and settlements.
  - Inject post-output authority failures and prove no transparent retry, failover, or replacement occurs.
  - Done when regression tests pass for stream order, non-stream collection, output commitment, attempt lineage, and no-retry-after-output behavior with authority enabled.
  - _Requirements: 9.4, 11.4, 11.5, 11.6, 13.7_
  - _Boundary: tests_
  - _Depends: 6.6_
  - _Validation: go test ./internal/core/runtime ./internal/testkit/conformance/...

- [x] 7. Add observability, privacy, and architecture guardrails
- [x] 7.1 Validate control-plane and policy evidence end to end
  - Run representative allow, deny, advisory, reserve, settle, release, overage, unavailable, and error flows through policydecision and control-plane recording.
  - Query live authority state and historical evidence together while preserving shared trace, A-leg, B-leg, rule, scope, backend, model, and reason identifiers.
  - Done when end-to-end tests prove operator-visible evidence explains selected enforceable amounts and keeps historical usage distinct from live authority state.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 8.5, 9.1, 9.3, 12.1, 12.2, 12.4, 12.6, 12.7, 12.8_
  - _Boundary: tests_
  - _Depends: 5.5, 6.7_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/stdhttp/admin/controlplane ./internal/core/usageauthority/...

- [x] 7.2 Add bounded metrics, status, and degraded-state visibility
  - Emit or expose authority outcome, reason, rule type, unit, store mode, and authority-source status using bounded labels and safe reason codes.
  - Avoid raw scope values, rule text, prompts, provider payloads, and unbounded model or user-controlled strings in high-cardinality outputs.
  - Done when observability/status tests prove ready, degraded, unavailable, disabled, and advisory-only states are visible with bounded safe dimensions.
  - _Requirements: 10.5, 10.7, 10.10, 12.4, 13.1, 13.2_
  - _Boundary: observability_
  - _Depends: 7.1_
  - _Validation: go test ./internal/infra/metrics ./internal/core/usageauthority/... ./internal/infra/runtimebundle_

- [x] 7.3 Add privacy and exclusion regression tests
  - Scan authority config, decisions, control-plane records, admin query responses, logs, and client-safe errors for raw bearer tokens, API keys, OAuth tokens, resume tokens, headers, unsafe claims, prompts, responses, and provider payloads.
  - Prove invoices, payment collection, OAuth/SAML/SCIM, web admin GUI, PII/prompt-injection engines, and provider-forwarding behavior are not introduced.
  - Done when privacy tests fail on unsafe literals and pass for safe scope, rule IDs, availability, redaction, and privileged visibility posture.
  - _Requirements: 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 13.7, 13.8_
  - _Boundary: tests_
  - _Depends: 7.2_
  - _Validation: go test ./internal/core/usageauthority/... ./internal/stdhttp/admin/controlplane ./internal/archtest/...

- [x] 7.4 Add architecture boundary guardrails
  - Prove `internal/core/usageauthority/domain` and `internal/core/usageauthority/app` do not import SQL, Bun, HTTP, provider SDKs, frontend plugins, backend plugins, or concrete runtimebundle packages.
  - Prove no provider-local quota headers or cooldown fields become proxy-level authority without explicit safe config mapping.
  - Done when arch tests pass and document the dependency direction from domain to app to adapters to composition root.
  - _Requirements: 13.6, 13.8_
  - _Boundary: tests_
  - _Depends: 7.3_
  - _Validation: go test ./internal/archtest/...

- [x] 8. Final feature validation
- [x] 8.1 Run concurrency and idempotency stress coverage
  - Exercise concurrent strict reservations against shared quota, rate, and budget windows using deterministic clocks and controlled goroutine scheduling.
  - Re-run settlement, release, and overage operations for the same logical request and B-leg to prove idempotency.
  - Done when stress tests prove configured strict windows are not exceeded except by explicit overage behavior and no duplicate settlement changes authority state.
  - _Requirements: 7.8, 11.1, 11.2, 11.3_
  - _Boundary: tests_
  - _Depends: 7.4_
  - _Validation: go test -race ./internal/core/usageauthority/... ./internal/infra/usageauthority/authoritystore

- [x] 8.2 Run end-to-end quota, rate, and budget authority flows
  - Run standard runtime flows for quota exceeded, rate limited, budget exceeded, advisory allow, strict allow with reservation, cancellation, failover, and parallel race.
  - Verify frontend responses are legal, backend attempts are committed only when allowed, and operator queries explain outcomes with safe evidence.
  - Done when end-to-end tests cover all major authority outcomes across streaming and non-streaming requests.
  - _Requirements: 3.2, 3.3, 4.2, 4.3, 5.2, 5.3, 6.1, 6.3, 7.1, 7.4, 9.2, 9.5, 11.5, 11.6, 12.1, 12.2_
  - _Boundary: tests_
  - _Depends: 8.1_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/stdhttp ./internal/core/runtime_

- [x] 8.3 Run the focused authority verification suite
  - Run the package tests that cover SDK contracts, domain policy, app orchestration, stores, config, runtimebundle, runtime integration, admin routes, observability, privacy, and architecture guardrails.
  - Repair only failures caused by this feature and preserve unrelated user changes.
  - Done when the focused command set passes and provides direct evidence for implementation readiness.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 7.8, 7.9, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.8, 10.9, 10.10, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 12.7, 12.8, 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 13.7, 13.8_
  - _Boundary: tests_
  - _Depends: 8.2_
  - _Validation: go test ./pkg/lipsdk/controlplane ./pkg/lipsdk/policydecision ./internal/core/usageauthority/... ./internal/infra/usageauthority/... ./internal/core/config ./internal/infra/runtimebundle ./internal/core/runtime ./internal/stdhttp/admin/controlplane ./internal/archtest/...

# Post-completion follow-ups

- [x] 9.1 Mirror limit row window bounds in decision history
  - Discovered during post-8.3 code review: `appendDecision` in `internal/infra/usageauthority/authoritystore/store.go` did not copy `WindowStart/End/ResetAt` from the matched limit row into the decision row, and `AccountingDecisionRow` in `pkg/lipsdk/controlplane/accounting_authority.go` lacked the three fields entirely. SDK consumers could not correlate decision history with the live limit window without a separate query.
  - Fix: add `WindowStart/End/ResetAt` to `AccountingDecisionRow` with `,omitzero` JSON tags (matching `AccountingLimitStatusRow`), and populate them in `appendDecision` from the matched limit row. Rules without a window definition leave the fields as the zero time.Time, preserving the existing "no window" semantic for `AccountingLimitStatusRow`.
  - Update the godoc on `AccountingDecisionRow` to document the new "zero = no window" contract (replacing the prior "known gap" note).
  - Done when shared contract tests prove decision rows with windowed rules carry the same `WindowStart/End/ResetAt` as the matched limit row, and the SDK control-plane tests accept the new JSON shape.
  - _Boundary: driven adapter + SDK/public contract_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore/contract ./pkg/lipsdk/controlplane/..._

- [x] 9.2 Remove unwired authority app ports
  - Discovered during post-completion hexagonal review: `internal/core/usageauthority/app/ports.go` defined `UsageReader`, `CostEstimator` (plus `UsageInput`/`CostInput`), and `IDGenerator` ports that were never wired. The runtime driving adapter already computes cost/usage (`accounting.EstimateCost`, `accountingpreflight.Decision`) and passes the values into `AdmissionInput`/`SettleInput`, so the app never called `UsageReader`/`CostEstimator`. `idGeneratorFromTesting()` always returned nil, so reservation IDs derived from `ReservationKey.String()` inside the store.
  - Fix: delete `UsageReader`, `CostEstimator`, `UsageInput`, `CostInput`, and `IDGenerator` from `ports.go`; remove the `ids` field, the `ids` parameter from `NewService`, and the `nextID()` method from `service.go`; remove the dead `nextID()` fallbacks in `admission.go`; drop the `idGeneratorFromTesting()` argument and helper from `internal/infra/runtimebundle/usage_authority.go`; update the two test call sites in `authority_test_helpers_test.go` and `authority_mount_test.go`.
  - Amend `design.md` so the approved spec artifact matches the implemented architecture: cost/usage inputs flow in via `AdmissionInput`/`SettleInput` from the runtime driving adapter and reservation IDs derive from `ReservationKey` (no `IDGenerator` port).
  - Done when the app defines only `RuleSource`, `StateStore`, `EvidenceSink`, and `Clock` ports; `NewService` takes four dependencies; the focused suite and architecture boundary tests pass; and `cmd/lipstd` still builds.
  - _Boundary: app orchestration + composition root + spec artifact_
  - _Validation: go test ./internal/core/usageauthority/... ./internal/infra/runtimebundle/... ./internal/core/runtime/... ./internal/stdhttp/... ./internal/archtest/..._

- [x] 9.3 Usage-authority review polish
  - Post-review cleanup now reflected in the tree: durable-store errors preserve the wrapped driver cause, `appendDecision` records released/overage/adjustment deltas in each decision row, `serveAuthorityPage` is generic over the row type, and nil-receiver timestamp fallbacks are normalized through `s.now()`.
  - Config-backed rule sources stamp `RuleSnapshot.FetchedAt`; the application snapshot helper only fills `FetchedAt` when a source returns the zero value, preserving source-owned freshness evidence.
  - Done when the focused suite and architecture boundary tests pass and `cmd/lipstd` still builds.
  - _Boundary: driven adapter + driving adapter + app/query + spec artifact_
  - _Validation: go test ./internal/infra/usageauthority/... ./internal/stdhttp/admin/controlplane/... ./internal/stdhttp/... ./internal/core/usageauthority/... ./internal/archtest/..._

# Remediation tasks

- [x] 10.1 Credential + policy-label authority dimension
  - Make `Credential` a first-class authority dimension in the domain model alongside principal/tenant/workspace/project/department/cost center, and ensure the runtime copies safe policy labels and the credential identifier from the scope view into the authority `Dimensions` used for matching, instead of reading raw bearer/API/OAuth tokens.
  - Update rule matching, dimension key derivation, and config validation so credential and policy-label dimensions are matched with the same known/known-empty/unknown semantics as the other safe scope dimensions.
  - Done when domain, app, runtime, and config tests prove credential and safe policy labels flow from the scope view into matched dimensions, raw tokens never become authority dimensions, and config validation accepts/rejects credential and policy-label rules correctly.
  - _Requirements: 1.2_
  - _Boundary: domain policy + app orchestration + runtime integration_
  - _Depends: 9.2_
  - _Validation: go test ./internal/core/usageauthority/domain ./internal/core/usageauthority/app ./internal/core/runtime ./internal/core/config_

- [x] 10.2 Authority-unavailable outcome + clamp effective-max
  - Make a rule whose `AuthorityRequirement` is unmet by the available evidence MATCHED (not silently excluded); the matched rule yields an authority-unavailable outcome and the app resolves it via the rule's `FailureBehavior` (fail-open/fail-closed). Matched-but-unavailable rules still appear in the matched-rule identifier set and operator evidence.
  - Make clamp (spend cap) carry an effective max / reduced-exposure amount, not just a label; the admission decision records the original requested max, the effective max after clamping, and the clamp reason, and the runtime mutates the call's max output so downstream backend work respects the reduced exposure.
  - Done when app and runtime tests prove authority-unavailable rules are matched and resolved via `FailureBehavior`, clamp decisions carry and apply an effective max, and operator evidence records both the original and clamped amounts.
  - _Requirements: 6.5, 8.3_
  - _Boundary: domain policy + app orchestration + runtime integration_
  - _Depends: 10.1_
  - _Validation: go test ./internal/core/usageauthority/... ./internal/core/runtime_

- [x] 10.3 StateStore multi-rule contract + atomic multi-rule reserve/settle/release
  - Update the `StateStore` port contract so `ReserveCommand`, `SettleCommand`, and `ReleaseCommand` carry the full set of matched strict rules (rule IDs, per-rule units, per-rule amounts, dimension keys) for the logical request and B-leg, not a single rule.
  - Enforce that `Reserve` updates all matched strict windows and reservation rows atomically in a single transaction or returns no reservation (no partial multi-rule reservation); `Settle` and `Release` apply across every matched rule for the same idempotency key.
  - Update the shared contract suite so memory and durable adapters prove multi-rule atomicity, partial-reserve rejection, and idempotent multi-rule settle/release.
  - Done when contract tests prove a multi-rule reserve either commits all matched windows or none, a failed multi-rule reserve leaves no partial reservation, and repeated multi-rule settle/release is idempotent across all matched rules.
  - _Requirements: 1.6, 3.6, 5.2, 6.2, 11.1_
  - _Boundary: app orchestration + driven adapter_
  - _Depends: 10.2_
  - _Validation: go test ./internal/core/usageauthority/app ./internal/infra/usageauthority/authoritystore/contract ./internal/infra/usageauthority/authoritystore_

- [x] 10.4 Per-rule unit selection for budget vs quota
  - Ensure each rule carries its explicit unit (requests, input/output/cache-read/cache-write/reasoning/total tokens, or money nano-units with currency) and that budget and spend-cap rules reserve and settle money nano-units while quota rules reserve and settle request or token units.
  - Make reserve and settlement use the rule's unit for amount validation, window counters, and remaining-limit queries; reject unit mixing or currency mismatch at validation unless an explicit supported conversion policy is available.
  - Done when domain and app tests prove budget rules reserve/settle money, quota rules reserve/settle request/token counts, mixed-unit rules are rejected, and remaining-limit queries report the correct per-rule unit.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.2, 7.1_
  - _Boundary: domain policy + app orchestration_
  - _Depends: 10.3_
  - _Validation: go test ./internal/core/usageauthority/domain ./internal/core/usageauthority/app_

- [x] 10.5 Fixed-window rollover
- Implement fixed-window rollover at configured boundaries: when the current window expires, create a new live limit row with zero counters for the next window period and evaluate subsequent admission against it.
- Keep expired window rows query-visible via decision history and historical aggregates so operators can reconstruct prior-window enforcement without conflating expired counters with live remaining-limit authority.
- Reconcile durable limit rows with the current rule templates on startup and locked reload: preserve consumed/reserved facts for matching logical windows, apply changed limits, seed added rules, and keep removed or superseded rows out of new admission matching.
- Keep durable mutations proportional to matched live state: hydrate and lock only the relevant reservation, usage fact, state, and limit rows; apply same-authority amount corrections transactionally; and use insert-only initial seeding so concurrent startup cannot reset live counters.
- Make reservation creation cross-instance idempotent with conditional reservation updates, insert-only creation, and a single rollback-and-reload retry; preserve the failing rule through multi-rule errors so fail-open removes only that rule while healthy strict rules remain reserved.
- Bound detached settlement, release, reconciliation, advisory usage, and compensation with the independently configurable `cleanup_timeout` (default `2s`).
- Isolate spend-cap clamp reads by exact configured scope filters plus normalized-dimension row verification; serialize missing usage facts with post-limit-lock reload, conditional writes, one retry, and monotonic authority/stage precedence; give fallback release a fresh cleanup deadline.
- Route spend-cap admission through an exact `ActiveLimit` store operation rather than historical status pagination; retry concurrent rolled-over row creation for reserve, usage, and startup reconciliation; deep-copy transaction-local templates and window maps.
- Classify atomic capacity exhaustion as a stable quota/rate/budget denial regardless of fail-open posture; restrict failure behavior to unavailable infrastructure/timeouts, and deny required pre-work evidence failures even when no reservation exists.
- Query durable decision history through transactionally maintained normalized filter rows, bounded legacy backfill, and query-bound keyset pagination rather than loading and decoding the complete decision ledger.
- Deny spend-cap admission when fixed input cost already exceeds live capacity regardless of fail-open posture; track settlement authority and explicit usage presence per token unit, preserve authoritative mixed/zero usage, and serialize terminal settle/release/reconciliation operations through one lifecycle state owner.
- Keep the complete lifecycle payload (attempt state and candidate) inside the same mutex as terminal and authority state; prove reset/finalization ordering cannot mix reservation generations, and prove money authority cannot mutate token-unit authority.
- Refine strict spend-cap admission with live remaining capacity and follow active-window query cursors instead of assuming the first status page contains the current row.
  - Done when store and query tests prove window expiry creates a new zero-counter live row, admission after rollover evaluates against the new window, and expired rows remain visible in decision history and historical queries.
  - _Requirements: 3.5_
  - _Boundary: driven adapter + query seam_
  - _Depends: 10.3_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore ./internal/core/usageauthority/app_

- [x] 10.6 Advisory/no-reservation usage recording
  - Ensure advisory and no-reservation usage paths update accounting windows through the app's `ApplyUsage` settlement-adjacent path, so live windows, decision history, and remaining-limit queries reflect actual usage rather than only strict-reserved usage.
  - Done when app and store tests prove a request that produces usage without a strict reservation still updates the matching accounting windows and records usage evidence, and remaining-limit queries reflect the update.
  - _Requirements: 7.7_
  - _Boundary: app orchestration + driven adapter_
  - _Depends: 10.3_
  - _Validation: go test ./internal/core/usageauthority/app ./internal/infra/usageauthority/authoritystore_

- [x] 10.7 Authoritative re-settlement
  - Implement authoritative re-settlement as an adjustment to a prior estimated settlement, not a replacement: when final provider-reported usage arrives after an estimated settlement, adjust the live windows by the delta and record the authoritative amount.
  - Preserve the prior estimated amount in evidence alongside the final authoritative amount, the adjustment delta, and the authority source, so operator queries can explain the difference between the estimated and authoritative enforceable amounts.
  - Done when app and store tests prove an estimated settlement followed by an authoritative re-settlement adjusts windows by the delta, does not double-count, and evidence preserves both the estimated and authoritative amounts.
  - _Requirements: 7.6, 8.4, 8.5, 8.6_
  - _Boundary: app orchestration + driven adapter_
  - _Depends: 10.6_
  - _Validation: go test ./internal/core/usageauthority/app ./internal/infra/usageauthority/authoritystore_

- [x] 10.8 Failure behavior enforcement
  - Enforce fail-open/fail-closed behavior consistently across every authority-unavailable, cost-unavailable, reservation-failure, estimate-unavailable, and evaluation-budget-exceeded path, not just the admission path.
  - Fail-closed denies or fails the affected lifecycle step with a stable accounting-failure reason before protected work; fail-open continues and records skipped-enforcement evidence. Evaluation-budget exhaustion applies the rule's failure behavior rather than waiting indefinitely.
  - Done when app and runtime tests prove each unavailable/failure path applies the configured failure behavior, fail-closed produces no backend attempt, fail-open records skipped evidence, and budget exhaustion does not hang.
  - _Requirements: 4.6, 5.5, 6.3, 6.6, 8.3, 10.1, 10.2, 10.3_
  - _Boundary: app orchestration + runtime integration_
  - _Depends: 10.2_
  - _Validation: go test ./internal/core/usageauthority/app ./internal/core/runtime_

- [x] 10.9 Cancellation reservation leak
  - Run cancellation settlement on a non-canceled context (`context.WithoutCancel`) so finalization accounting completes after the client request context is canceled; do not convert cancellation into an unrelated accounting denial.
  - Set the lifecycle `settled` flag only on successful settle or release; a canceled or failed settlement leaves the reservation in its prior state and emits degraded/unavailable evidence rather than marking it settled, so canceled requests release reservations without leaking.
  - Done when app and runtime tests prove canceled requests settle/release on a non-canceled context, the `settled` flag is set only on success, canceled reservations are released (no leak), and cancellation never produces an unrelated accounting denial.
  - _Requirements: 10.4, 11.7_
  - _Boundary: app orchestration + runtime integration_
  - _Depends: 10.7_
  - _Validation: go test ./internal/core/usageauthority/app ./internal/core/runtime_

- [x] 10.10 Durable store Bun rewrite + cross-instance atomicity + Postgres integration contract test
  - Rewrite the durable store to use Bun with a transactional locked read-modify-write pattern for cross-instance atomicity: each `Reserve`/`Settle`/`Release` opens a Bun transaction, acquires row locks on every affected live window before reading counters (`SELECT ... FOR UPDATE` on Postgres, `BEGIN IMMEDIATE` on SQLite), applies the matched-rule mutations, and commits atomically.
  - Ensure a flush failure leaves the in-memory projection unchanged: the store does not publish partial window updates or reservation state to the app layer when the durable transaction rolls back.
  - Add a Postgres integration contract test (gated by existing integration settings) proving the locked read-modify-write prevents concurrent cross-instance admitted totals from exceeding configured limits.
  - Done when durable-store tests prove locked read-modify-write atomicity, flush failure leaves the in-memory view unchanged, concurrent cross-instance reservations do not exceed limits, and the Postgres contract test runs under existing integration settings.
  - _Requirements: 10.6, 10.9, 11.1_
  - _Boundary: driven adapter + tests_
  - _Depends: 10.3_
  - _Validation: go test ./internal/infra/usageauthority/authoritystore ./internal/infra/usageauthority/authoritystore/contract_

- [x] 10.11 Architectural stabilization and proof gate
  - Keep capacity exhaustion, duplicate replay, unavailable infrastructure, and evidence failure as distinct typed outcomes through admission; fail-open is permitted only for unavailable infrastructure/evidence allowed by the rule.
  - Keep token usage authority independent from monetary-cost authority, including authoritative-zero and missing-cost cases; preserve partial-to-final-to-authoritative convergence for reservations and unreserved facts.
  - Require complete scope identity for every live lookup, serialize missing-row creation through a stable coordination row, and keep mutation/query work bounded to affected SQL rows rather than reconstructing historical state.
  - Give settlement, release, compensation, and reconciliation independent bounded cleanup contexts. Add differential memory/SQLite/PostgreSQL sequences and execute PostgreSQL concurrency tests with a configured DSN.
  - Done when all accumulated P1/P2 regressions are covered, PostgreSQL integration tests run rather than skip, adapter state parity holds across retries/restarts/rollovers/rule changes, and a fresh semantic review finds no protected-work bypass.
  - _Requirements: 4.6, 5.5, 6.3, 6.6, 7.6, 7.7, 8.4, 8.5, 8.6, 10.1, 10.2, 10.4, 10.6, 10.9, 11.1, 11.7_
  - _Boundary: runtime lifecycle + durable adapter + integration proof_
  - _Depends: 10.10_
  - _Validation: make test-authority-postgres (with LIP_TEST_POSTGRES_DSN), make quality-checks, make test-unit_

STATUS: COMPLETE
