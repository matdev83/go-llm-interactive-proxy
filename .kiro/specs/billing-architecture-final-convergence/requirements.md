# Requirements Document

## Introduction

PR #340 moved Go-LIP from monetary authorization holds to operational call exposure and post-usage settlement, but the implementation retained substantial migration-era architecture. The first remediation spec, `billing-post-usage-correctness-hardening`, corrects B-leg order, model pricing, customer/provider snapshot coupling, and runtime call-state lifetime.

This second, chronologically dependent spec performs the deliberate deletion pass. It shall not invent another general accounting framework. Its purpose is to make the codebase match the simple operational model:

```text
credit check
 -> pessimistic exposure
 -> execute
 -> durable terminal usage
 -> deterministic customer rating
 -> double-entry customer settlement
 -> independent provider COGS
```

The target is not merely fewer lines. The final repository shall have one monetary authority, one current usage-record model, no runtime financial aggregation framework, no live reserved-balance/authorization-book lifecycle, and no compatibility bridge that converts the current billing records back into a superseded model.

## Dependency and Ordering

- Implementation MUST start only after `billing-post-usage-correctness-hardening` is complete on main.
- Phase 0 of this spec records a fresh post-correctness baseline SHA and measured production surface.
- The correctness regression suite from the predecessor becomes an immutable behavioral gate for every deletion phase.
- This spec may be reviewed and approved before the predecessor is implemented, but tasks remain blocked by that dependency.

## Boundary Context

- **In scope**: native customer rating over `CallUsageRecord`/`CallLegUsageRecord`, legacy TUR/LUR domain and persistence retirement, obsolete processing/shadow path deletion, `ReservedNano` domain cleanup, authorization-book current-model cleanup, narrowing UsageAuthority to non-monetary quota authority, provider COGS serialization independence, process-local durable terminal usage spool, removal of direct-central-append + retry-outbox layering, composition cleanup, architecture/LOC/deletion ratchets.
- **Out of scope**: changing customer price semantics proven by predecessor; changing prepaid/postpaid sign convention; adding payment acquisition; adding invoicing; adding in-flight hard cost cutoff; changing route syntax/protocol adapters; changing provider SDK public ABI unless a separate validation proves unavoidable; generic event sourcing/CQRS/Kafka; distributed workflow engines.
- **Design bias**: delete adapters and parallel representations before adding abstractions. Any new helper must remove more lifecycle/transport plumbing than it introduces.

## Requirement 1: Preserve the Correctness Baseline

**Objective:** As a maintainer, I want simplification to preserve the predecessor's proven financial behavior, so that deletion does not reintroduce wrong-charge defects.

### Acceptance Criteria

1.1. All predecessor regressions for actual B-leg sequence, mixed-model customer pricing, customer/provider snapshot independence, and bounded active-call state shall remain green throughout this spec.

1.2. Prepaid balance shall never settle below zero; postpaid balance shall never settle below its configured negative credit floor.

1.3. Customer settlement shall remain idempotent per account + `BillingCallID` and shall continue to enforce `actual <= CallExposure.max`.

1.4. Customer usage posting shall remain balanced double-entry financial journal activity; exact-zero calls shall retain durable processed-operation evidence without fake zero-value journal entries.

1.5. Provider COGS shall remain separately idempotent per `BillingCallID` + B-leg and shall not be customer charge unless the explicit customer charge policy selects that leg.

1.6. No monetary hold, reserved-balance admission, authorization-book call posting, stream-time financial mutation, or post-output provider retry shall return.

## Requirement 2: Make Current Call/Leg Records the Only Billing Usage Model

**Objective:** As a billing maintainer, I want post-usage policy to consume the same immutable records that runtime persists, so that no migration adapter can lose or reinterpret financial facts.

### Acceptance Criteria

2.1. Customer rating shall operate directly on `CallUsageRecord` and `[]CallLegUsageRecord` or one clearly named `CompleteCall` composed from them.

2.2. Customer rating shall not construct, require, or translate through legacy `TurnUsageRecord` or `LegUsageRecord`.

2.3. The native rating algorithm shall preserve the predecessor's sequence-aware leg-selection, surfaced-state, evidence-presence, mixed-model pricing, fixed/resource charge, and charge-scope semantics.

2.4. Provider COGS rating shall operate directly on `CallLegUsageRecord`.

2.5. Customer and provider rating shall share only small value helpers where semantics are genuinely identical; they shall not be recombined into an all-or-nothing “economic result”.

2.6. `TurnUsageRecord`, `LegUsageRecord`, their sealing/fingerprint helpers, and compatibility adapters shall be deleted from production code once no live consumer remains.

2.7. Architecture tests shall prevent reintroduction of a current-record -> legacy-record -> rating bridge.

## Requirement 3: Retire Legacy TUR/LUR Persistence and Processing

**Objective:** As an operator, I want the current schema contract to contain only tables used by the live billing architecture, so that old migration tables do not remain operational dependencies forever.

### Acceptance Criteria

3.1. `usage_call_records` and `usage_leg_records` shall remain the current durable customer/leg usage source.

3.2. After safe migration checks, forward migrations shall retire legacy `turn_usage_records`, `leg_usage_records`, and `usage_record_processing` tables if no remaining live feature requires them.

3.3. Historical migration files that originally created legacy tables shall remain immutable; retirement shall use new forward migrations.

3.4. `VerifySchema` and current contract tests shall stop requiring retired tables, indexes, triggers, and foreign keys.

3.5. Legacy store interfaces/workers/shadow processors whose only purpose is retired TUR/LUR processing shall be deleted, not feature-flagged.

3.6. Upgrade migration shall fail explicitly if legacy tables contain unprocessed financial work that cannot be proven migrated/obsolete; it shall never silently drop pending monetary evidence.

3.7. Read-only historical/report compatibility that genuinely needs old data shall migrate the needed data into current records or a narrowly named archival representation; it shall not keep the old processing engine alive.

3.8. Fresh databases shall create only the current billing schema plus migration history, not create-and-immediately-retire unused TUR/LUR operational tables where migration framework constraints allow consolidation safely.

## Requirement 4: Remove Reserved-Balance and Authorization-Book Concepts from Current Domain

**Objective:** As a maintainer, I want current billing types to express settled balance and operational exposure only, so that dead hold terminology does not remain part of everyday reasoning.

### Acceptance Criteria

4.1. `billing.Account` shall no longer expose `ReservedNano`.

4.2. Current `AccountSnapshot`, operation snapshots, customer/provider posting commands, and reports shall not expose a mutable reserved-balance field.

4.3. Spendable/headroom calculations shall remain `Balance - CreditFloor`; open operational exposure remains a separate derived/query value.

4.4. Historical database columns needed only for upgrade verification may be read through explicit legacy migration DTOs, not current domain types.

4.5. The current journal model shall expose only books used by current writers; `JournalBookLegacyAuthorization` may survive only in an isolated historical decode/migration layer if old rows must still be read.

4.6. No current writer, report default, rating command, or store interface shall accept authorization-book postings.

4.7. Forward migration shall retire the always-zero `reserved_nano` column where supported and safe; if a dialect requires table rebuild, the migration shall preserve account balances/versions/policies exactly.

4.8. Architecture tests shall forbid `ReservedNano` and legacy authorization-book symbols from returning to current core/runtime/normal store paths.

## Requirement 5: Eliminate the Second Money-Capable UsageAuthority

**Objective:** As a maintainer, I want UsageAuthority to enforce request/token/concurrency policy only, so that the repository contains one monetary accounting authority.

### Acceptance Criteria

5.1. `UsageAuthority` shall remain available for non-monetary request/token quota authority and existing concurrency coordination where applicable.

5.2. `AmountUnitMoneyNano` shall be removed from the live UsageAuthority domain after migration.

5.3. UsageAuthority admission/settlement/release contracts shall no longer carry financial `Spend`, `FinalCost`, `EstimatedCost`, money reservations, or money-specific measurement authority solely for monetary policy.

5.4. Budget/spend-cap rule variants that require monetary reservation shall be removed or migrated out of UsageAuthority; they shall not remain as a disabled/hidden second financial implementation.

5.5. Configuration containing retired monetary UsageAuthority rules shall fail startup with an explicit migration error rather than silently changing enforcement.

5.6. Current billing balance, CallExposure, customer journal, and provider COGS shall be the only code paths that interpret money for financial authority.

5.7. Metering may still observe provider/customer money for telemetry, but telemetry money shall not reserve capacity, mutate balances, or become a second customer settlement.

5.8. Architecture tests shall reject money unit/types and financial reserve/settle fields in UsageAuthority after cutover.

## Requirement 6: Decouple Provider COGS from Customer Credit Serialization

**Objective:** As an operator, I want provider-cost accounting to avoid locking customer affordability state, so that an operator-cost backlog cannot delay new customer call admission.

### Acceptance Criteria

6.1. Provider COGS posting shall not acquire the customer `billing_accounts` row lock solely to serialize account sequence.

6.2. Provider COGS shall not mutate customer balance, credit limit/floor, CallExposure, or customer operation version.

6.3. Provider COGS idempotency shall remain stable per B-leg usage identity.

6.4. Financial journal integrity shall remain balanced and queryable after provider COGS is decoupled from customer account sequence.

6.5. If journal ordering is required for reporting, provider COGS shall use a journal-global/provider-cost sequence or database-generated ordering that does not share the customer credit serialization point.

6.6. Customer account reports shall correlate provider COGS by `BillingCallID`/account correlation without requiring that COGS share the customer balance mutation sequence.

6.7. Concurrency tests shall prove slow/high-volume provider-cost posting does not block or serialize independent CallExposure admissions for the same customer beyond unavoidable database-wide SQLite writer constraints.

## Requirement 7: Replace Synchronous Central Terminal Append with One Durable Local Spool

**Objective:** As a runtime operator, I want request terminalization to perform one fast local durable handoff, so that central billing database latency/outage does not sit inside the live stream terminal path.

### Acceptance Criteria

7.1. Authoritative runtime terminal ownership shall append leg/call usage to one process-owned durable terminal spool before returning terminal completion.

7.2. The reference spool shall reuse Bun/`internal/infra/db` with process-local SQLite/WAL or an equivalently simple injected local durable store; no Kafka, message broker, workflow engine, or new ORM shall be introduced.

7.3. Runtime shall not synchronously attempt the central billing store and then fall back to another durable outbox.

7.4. A process-owned flusher shall deliver immutable spool records at least once to the central current usage store; central replay fingerprints shall provide idempotent effect.

7.5. The spool shall persist record kind, stable key, semantic fingerprint, payload, enqueue time, attempt/backoff state, and last error without interpreting customer/provider money.

7.6. Local append failure in authoritative mode shall be treated as a critical durability failure under existing terminal semantics; it shall never trigger provider retry after output.

7.7. Central billing database outage shall allow terminal spool appends to continue until local durable capacity/health policy is exhausted.

7.8. Process restart shall resume pending spool delivery without reconstructing records from runtime memory.

7.9. The process-local spool shall be process-lifetime owned through existing runtime composition lifecycle; no per-request goroutine or unowned background loop may be introduced.

7.10. The current direct-appender + `RetryingCallUsageAppender`/`RetryingCallLegUsageAppender` + central `usage_append_outbox` fallback layering shall be deleted after the spool cutover.

7.11. Central delivery, replay, retry, or reconciliation latency shall not block a concurrent local spool append; local append deadlines shall cover only local durable I/O.

7.12. One spool worker shall drain due records in bounded batches and wake promptly after a committed append; healthy central delivery shall not be limited to one record per scheduler interval.

7.13. The database-capacity admission gate shall account for live SQLite page allocation and reusable freelist space, while health telemetry may continue to expose physical database/WAL/SHM bytes and free-disk watermarks.

## Requirement 8: Simplify Post-Usage Workers and Composition

**Objective:** As a maintainer, I want the live billing pipeline to have one obvious worker per economic responsibility, so that retries and ownership can be traced without a mini-framework.

### Acceptance Criteria

8.1. One spool flusher shall own local-terminal-record delivery to central usage storage.

8.2. One customer post-usage worker shall claim complete calls, resolve customer snapshots, rate customer usage, and invoke atomic customer settlement.

8.3. One provider-cost worker shall process B-leg provider COGS independently.

8.4. Worker lifecycle shall use existing process/generation ownership primitives and shall not duplicate generic retry/scheduler infrastructure.

8.5. Retry/backoff state shall live durably with the work item it governs; runtime memory shall not be authoritative retry state.

8.6. Composition shall inject the minimum ports needed by runtime: cheap credit gate, exposure admission, terminal usage sink, billing identity. Customer/provider workers remain outside the request executor.

8.7. `BillingRuntime` shall not contain post-usage rating resolvers, settlement stores, provider-cost stores, or worker controls.

8.8. `ComposeBilling`/runtimebundle shall have one authoritative wiring path and shall not support concurrent legacy/current billing modes in production.

8.9. The spool, customer post-usage worker, and provider-cost worker shall be constructed and lifecycle-owned once by `ProcessServices`; generation retirement shall not stop or close them, and reload contexts shall not cancel them.

8.10. A complete-call worker batch shall yield incomplete closures by advancing their next eligibility time without consuming settlement retry attempts, so newer complete calls cannot be indefinitely starved by older incomplete rows.

## Requirement 9: Preserve Recoverability and Financial Reconstruction

**Objective:** As an operator, I want simplification to retain durable financial reconstruction, so that deleting migration architecture does not weaken accounting guarantees.

### Acceptance Criteria

9.1. Customer financial balance shall remain reconstructible from the opening balance plus immutable financial journal entries.

9.2. Operational exposure shall remain reconstructible from open `call_exposures`, not from a reserved-balance journal/book.

9.3. Provider COGS shall remain reconstructible from immutable provider-cost operations/journal entries.

9.4. Account operation snapshots shall retain point-in-time before/after customer balance, credit floor/limit, spendable, version, and correlation needed for diagnostics, without obsolete reserved-balance fields.

9.5. Reconciliation shall validate journal balance, customer materialized balance, exposure consistency, processed call state, and provider-cost operation integrity using current records only.

9.6. Rebuild/repair shall never edit posted journal entries; corrections remain reversal/replacement transactions.

9.7. Removing old usage tables or authority money state shall not make historical posted customer/provider transactions unreconstructible.

## Requirement 10: Make Simplification Quantifiable and Non-Gameable

**Objective:** As a maintainer, I want the final architecture to be materially smaller rather than merely renamed, so that the refactor achieves its original maintenance objective.

### Acceptance Criteria

10.1. Phase 0 shall measure a post-predecessor production baseline across all current billing-convergence surfaces, including money-specific UsageAuthority code that contributes to the cognitive model.

10.2. Tests, docs, generated files, migrations retained solely for historical upgrade, and testdata shall not count as production simplification.

10.3. Moving code between counted packages shall not satisfy the ratchet; the measured surface shall follow relevant billing/economic symbols into new production paths.

10.4. Final production LOC across the defined convergence surface shall be at least 10% lower than the Phase-0 post-predecessor baseline.

10.5. Independent of LOC, the final tree shall satisfy explicit deletion targets for legacy TUR/LUR models, retired processing workers/tables, current `ReservedNano`, authorization-book writer surfaces, money-capable UsageAuthority, executor-global billing collector state, and central append fallback layering.

10.6. No new generic event bus, repository, CQRS layer, service locator, financial DSL, workflow engine, or reflection-based framework may be introduced to achieve the reduction.

10.7. A new helper/port shall remain only if final review shows it removes more branching/ownership plumbing than it adds and has exactly one authoritative role.

10.8. Architecture documentation shall present one authoritative flow without migration-era alternatives.

## Requirement 11: Brownfield Migration Must Be Explicit and Safe

**Objective:** As an operator upgrading from prior billing releases, I want retirement migrations to fail safely on unresolved legacy state, so that cleanup cannot discard money or usage evidence.

### Acceptance Criteria

11.1. Before dropping a legacy table/column, migration shall prove no unresolved work remains or perform a deterministic conversion to current durable records.

11.2. Migration shall be idempotent and support both SQLite and PostgreSQL repository-supported deployment modes.

11.3. Unresolved legacy authorization holds, usage processing rows, or pending monetary UsageAuthority reservations shall block destructive retirement with an actionable error.

11.4. No migration shall fabricate missing B-leg sequence, usage quantity, cost, customer charge, or provider cost.

11.5. Fresh-schema installation and upgraded-schema installation shall converge on the same current table/index/trigger contract after all migrations.

11.6. Roll-forward recovery shall be preferred; down migrations need not recreate retired financial authority if repository migration policy treats destructive retirement as forward-only.

## Requirement 12: Final Certification

**Objective:** As a reviewer, I want a strong end-state gate, so that the project does not declare architectural convergence while duplicate financial mechanisms remain.

### Acceptance Criteria

12.1. Search/architecture tests shall show exactly one customer balance mutation implementation and one operational exposure admission implementation.

12.2. Search/architecture tests shall show no production monetary hold/reserved-balance/authorization-book lifecycle.

12.3. Search/architecture tests shall show no live legacy TUR/LUR customer rating bridge or processing worker.

12.4. Search/architecture tests shall show no money-capable UsageAuthority path.

12.5. Search/architecture tests shall show runtime terminal billing has only call-scoped bookkeeping plus one durable spool sink and no customer/provider rating logic.

12.6. SQLite and configured PostgreSQL integration suites shall pass migration, concurrency, crash/replay, customer settlement, provider COGS, spool restart, and reconciliation tests.

12.7. Targeted race tests shall pass for runtime terminalization and process spool lifecycle.

12.8. `make test-unit`, `make quality-checks`, docs checks, architecture tests, and relevant full/race suites shall pass.

12.9. Final review shall compare the resulting architecture against the simple target flow and shall be NO-GO if multiple authoritative money models or unnecessary compatibility layers remain even if tests pass.
