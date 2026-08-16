# Design Document

## 1. Purpose

This design is the final deletion/convergence pass for authoritative billing. It assumes `billing-post-usage-correctness-hardening` has already established correct sequence-aware, model-aware, customer/provider-independent behavior.

The principal design rule is:

> Keep one representation and one authority for each fact.

| Fact/responsibility | Final authority |
|---|---|
| Customer settled balance / credit floor | billing financial journal + materialized account |
| In-flight worst-case customer exposure | `call_exposures` |
| Terminal call/leg usage | `CallUsageRecord` / `CallLegUsageRecord` |
| Customer rating | native current-record rating |
| Provider COGS | native leg rating + provider-cost operation |
| Request/token quota | non-money UsageAuthority |
| Terminal delivery retry | process-local durable usage spool |

## 2. Dependency Gate

The predecessor spec identity is already fixed:

```text
feature: billing-post-usage-correctness-hardening
spec PR: #346
spec merge SHA: 9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9
required state: implemented_on_main
implementation main SHA: <not known yet>
```

The known merge SHA above proves only that the predecessor **spec** was merged. It is not implementation evidence. `spec.json.dependencies[0]` carries both facts separately and remains `verification_status=pending_phase_0` with an empty `implementation_main_sha`.

Before implementation of this spec:

1. locate the final `main` SHA that actually implements `billing-post-usage-correctness-hardening`;
2. verify that commit contains the predecessor implementation rather than merely its spec artifacts;
3. rerun the predecessor's complete billing regression/certification suite against that SHA;
4. record the verified implementation SHA in `spec.json.dependencies[0].implementation_main_sha` and change dependency verification only after the suite passes;
5. create the canonical Phase-0 simplification baseline artifact defined in Section 12 from that exact implementation SHA;
6. update only names/paths in this spec if predecessor implementation changed them;
7. reopen requirements/design review for any semantic drift rather than weakening deletion or correctness requirements.

No implementation phase below may start before this gate passes. `ready_for_implementation` remains false until then.

## 3. Native Customer Rating

### 3.1 Final API

Prefer one domain entry point:

```go
type CustomerRatingInput struct {
    Call            CallUsageRecord
    Legs            []CallLegUsageRecord
    MaxCharge       Money
    DefaultPricing  PricingSnapshot
    ModelPricing    []ModelCustomerPricing
    Policy          ChargePolicy
}

type CustomerRatingResult struct {
    CallID BillingCallID
    Charge Money
    Fingerprint string
}

func RateCustomerCall(CustomerRatingInput) (CustomerRatingResult, error)
```

It operates directly on current records.

No `TurnUsageRecord` or legacy `LegUsageRecord` is constructed.

### 3.2 Selection algorithm

Use a small explicit selector:

```text
accepted = legs with provider-accepted customer evidence

if policy scope == all potential:
    selected = accepted
else if call completed:
    selected = surfaced accepted legs
else:
    if surfaced accepted exists:
        selected = surfaced accepted
    else:
        selected = accepted leg with highest authoritative AttemptSeq
```

The ordering above is normative: **evidence acceptance happens before charge-scope selection for every policy**. `all potential` means all provider-accepted, customer-billable B-legs that actually carry accepted customer evidence. It does not mean every planned candidate, never-started leg, rejected leg, or evidence-unavailable leg. Missing evidence is never converted into an implicit zero or a charge merely because the policy says `all potential`.

Differential tests must pin this definition for completed/surfaced calls, failover, parallel branches, interrupted calls, and sequence-unknown legacy rows before deleting the predecessor selector.

Legacy sequence-unknown behavior stays exactly as proven by predecessor.

### 3.3 Rating

For each selected leg:

1. resolve effective customer price by backend/model;
2. require evidence dimensions included by policy;
3. rate token/fixed/resource components;
4. accumulate with checked arithmetic;
5. validate currency;
6. compare total to `CallExposure.Max`.

Provider COGS is not part of this function.

### 3.4 Delete compatibility layer

After native tests match predecessor results:

- delete `TurnUsageRecord`;
- delete legacy `LegUsageRecord`;
- delete customer use of old `RatingInput.Record`;
- remove adapters from `call_rating.go` and `billingcompose`.

Provider-specific rating helpers may remain only if they directly consume `CallLegUsageRecord`.

## 4. Current Usage Persistence Only

Final central usage schema:

```text
usage_call_records
usage_leg_records
provider_cost_work
```

The call row is the immutable closure/completeness set. Leg rows contain exact attempt facts.

Legacy:

```text
turn_usage_records
leg_usage_records
usage_record_processing
```

become retirement targets.

### 4.1 Retirement migration

A forward retirement migration uses one migration-critical section; the safety proof must not run before a separately committed `DROP`.

Before entering that section, the release procedure must quiesce every current application writer to the retiring tables. Mixed-version deployments must stop/drain old processes that can still create legacy work before the schema retirement is allowed to start. Database locking is still mandatory as the race-safety backstop.

Inside the critical section the migration must:

1. detect whether legacy tables exist;
2. prevent a concurrent legacy writer from committing new work;
3. inspect pending/processing/error states;
4. deterministically convert any convertible retained usage/diagnostic state into current durable records;
5. re-run the unresolved-work proof after conversion;
6. fail with an actionable error if any unresolved financial/usage work remains;
7. drop legacy operational tables/indexes/triggers before releasing the migration lock;
8. update current schema verification;
9. commit the proof and destructive DDL together.

Dialect-specific baseline:

```text
PostgreSQL:
  BEGIN
  acquire billing-retirement advisory transaction lock
  LOCK retiring tables IN ACCESS EXCLUSIVE MODE
  inspect / convert / re-inspect
  DROP retiring objects
  COMMIT

SQLite:
  acquire one connection
  BEGIN IMMEDIATE (or stronger repository-supported writer-exclusion mode)
  inspect / convert / re-inspect
  DROP retiring objects
  COMMIT
```

The implementation must verify the actual Bun migration runner does not split the proof and drop across transactions/connections. If the generic migrator cannot provide the required critical section, use a narrowly scoped dialect-aware migration helper rather than assuming implicit transactional behavior.

Concurrency tests must race a legacy insert against retirement and prove that the writer either commits before the locked proof and is therefore observed, or waits/fails after retirement; it must never commit new unresolved work between the successful proof and `DROP`.

Never infer missing usage or money.

### 4.2 Fresh install

If the migration framework executes every historical migration for a fresh DB, creation then retirement is acceptable mechanically, but final `VerifySchema` must expose only the current contract. If the framework supports safe baseline compaction, it may be used only with explicit migration-history compatibility review.

## 5. Current Financial Domain

### 5.1 Account

Final current model:

```go
type Account struct {
    ID          string
    Currency    string
    Mode        AccountMode
    CreditLimit int64
    BalanceNano int64
    Version     uint64
    State       AccountState
}
```

No `ReservedNano`.

```text
CreditFloor =
    0             prepaid
    -CreditLimit  postpaid

SettledHeadroom = Balance - CreditFloor
SafetyMargin    = SettledHeadroom - SUM(OpenExposure.Max)
```

### 5.2 Operation snapshots

Current before/after snapshots retain:

- balance;
- credit limit/floor;
- settled headroom/spendable;
- version;
- mode/currency;
- correlation and journal sequence identifiers.

Remove reserved before/after fields from current commands and reports.

### 5.3 Historical authorization book

If historical journal rows with book=`authorization` must remain queryable, decode them through a migration/report compatibility layer that cannot be used by normal writers.

The normal `JournalTransaction.Validate` path should accept only current writable books. A separate `DecodeHistoricalJournalTransaction` or persistence DTO may map old rows for audit.

This prevents “legacy but accepted by writer” ambiguity.

## 6. UsageAuthority Becomes Non-Monetary

### 6.1 Final unit set

UsageAuthority supports:

```text
requests
input_tokens
output_tokens
cache_read_tokens
cache_write_tokens
reasoning_tokens
total_tokens
```

Remove `money_nano`.

### 6.2 Contract simplification

Remove financial fields from UsageAuthority command types where they no longer serve non-money behavior:

- `Spend`;
- `FinalCost`;
- `EstimatedCost`;
- cost authority/presence state;
- money reservation mapping.

Retain usage authority needed for token/request evidence.

### 6.3 Rule migration

Inventory configured rule kinds.

If `budget`/`spend_cap` exist solely as money limits, retire them from this subsystem.

On configuration load:

```text
legacy money rule detected
 -> explicit unsupported/migration-required error
```

Do not silently reinterpret money as tokens or customer credit.

A future financial-budget product belongs on top of the billing ledger/exposure model, not in this quota authority.

## 7. Provider COGS Journal Independence

### 7.1 Problem

Current provider-cost posting locks `billing_accounts` because journal transactions allocate `AccountSequence`.

COGS does not modify customer credit.

### 7.2 Final sequencing model

Choose one ordering contract; do not implement a second global sequence/counter.

**Customer-affecting financial transactions** retain the existing per-account ordering invariant:

```text
account_sequence IS NOT NULL
unique (account_id, account_sequence)
```

**Provider COGS transactions** use:

```text
account_sequence = NULL
provider/report order = (recorded_at, transaction_id)
```

`recorded_at` is assigned by the database at insert/commit time and is immutable. `transaction_id` remains the globally unique stable idempotency/tie-break identity, so equal timestamps still have deterministic ordering. Provider COGS does not increment customer account version and does not obtain the customer account row lock merely to allocate sequence.

Schema/index contract:

- make `journal_transactions.account_sequence` nullable;
- preserve uniqueness for non-null customer sequences (`UNIQUE(account_id, account_sequence)` semantics or a partial unique index where required by the dialect);
- index provider/report traversal by `(account_id, recorded_at, transaction_id)` and book/currency reporting by `(book, currency, recorded_at, transaction_id)` where current queries need it;
- current validation requires non-null `account_sequence` for customer-balance-affecting operation kinds and requires NULL for newly posted provider COGS;
- historical provider rows that already carry an account sequence remain readable/auditable and need not be rewritten, but current provider writers never allocate one.

Cross-dialect migration:

```text
PostgreSQL:
  ALTER account_sequence nullable
  create/verify non-null customer uniqueness + provider ordering indexes

SQLite:
  rebuild journal_transactions if required to change NOT NULL/constraint shape
  copy values exactly
  recreate uniqueness/provider-order indexes
  verify journal fingerprints/balances before commit
```

The migration must preserve transaction ids, existing account sequences, correction links, recorded timestamps and journal entries exactly. Do not synthesize new order for historical rows.

Do not create a second customer account version or a new global mutable sequence for provider COGS.

### 7.3 Reports and reconciliation

Customer account-state replay remains ordered by non-null `account_sequence` and must never use provider COGS rows as balance mutations.

Provider COGS/account correlation is ordered by `(recorded_at, transaction_id)` and correlated using `AccountID`, `BillingCallID`, A/B-leg fields. Trial balance remains book/currency based and does not require account sequence.

Tests must prove the two ordering domains are independently deterministic on SQLite/PostgreSQL and that provider COGS posting no longer serializes CallExposure admission on the customer row.

## 8. Process-Local Durable Terminal Usage Spool

### 8.1 Why a local spool

The terminal boundary should have one job: durably hand off an immutable usage record.

Current direct-central-append plus fallback-outbox behavior creates:

- central DB latency in terminal path;
- two delivery modes;
- retry wrappers plus outbox worker;
- central outage coupling.

### 8.2 Port and completeness contract

Runtime receives one sink:

```go
type TerminalUsageSink interface {
    AppendLeg(context.Context, CallLegUsageRecord) error
    AppendCall(context.Context, CallUsageRecord) error
}
```

This is a terminal transport port only; it has no rating, exposure, account or journal methods.

Legs and the call closure remain independently appendable because B-legs terminalize independently. The call closure contains the frozen exact `ExpectedBLegIDs` set established by terminal ownership. Delivery order is intentionally irrelevant.

The authoritative completeness barrier therefore remains central and explicit:

```text
central CallUsageRecord may exist
    + zero or more CallLegUsageRecords may exist

ClaimCompleteCall(callID):
    load sealed closure
    load sealed legs for callID
    require exactly one valid leg for every ExpectedBLegID
    reject/leave pending if any expected leg is absent or conflicts
    only then expose CompleteCall to customer rating
```

The spool flusher may deliver a call before one or more legs. That must never make the call claimable. A crash between independent local appends therefore leaves an incomplete call safely pending rather than partially rated; restart replays whatever records were durably accepted. Provider-cost processing may still consume each independently complete B-leg without waiting for call closure.

### 8.3 Production store and durability contract

The supported authoritative production baseline is `internal/infra/billingspool` (name may change after package review) using Bun/`internal/infra/db` with a process-local SQLite database. Alternative injected spool implementations are test seams only in this spec; production pluggability requires a separate conformance design rather than an undocumented “equivalent” store.

Production SQLite requirements:

- `PRAGMA journal_mode=WAL`;
- `PRAGMA synchronous=FULL` for the authoritative spool connection;
- repository-standard busy timeout and foreign-key settings;
- a stable configured file path under the process instance's durable state directory, not an OS temp directory;
- one stable spool file per process instance, reused across restart;
- local filesystem storage unless another filesystem has explicit durability validation;
- restrictive file/directory permissions (`0600` file and `0700` parent on POSIX, equivalent owner-only ACL posture on Windows where supported).

`AppendLeg`/`AppendCall` success boundary:

```text
validate + seal current record
BEGIN local SQLite write transaction
  enforce local capacity/health gate
  INSERT new row
    or verify same stable key + same semantic fingerprint replay
COMMIT
return nil only after COMMIT returns successfully under synchronous=FULL
```

A record must never be reported successfully appended before that commit completes. Crash tests use a child process/fault hook around commit acknowledgement to prove: an append that returned success survives process restart; a crash before commit/ack may leave no row but cannot have returned success.

#### Stable identity and fingerprint

The spool does not invent a second identity/hash scheme.

- `kind=call`: `record_key` is the sealed `CallUsageRecord.Key`, i.e. `CallUsageKey(BillingCallID)`.
- `kind=leg`: `record_key` is the sealed `CallLegUsageRecord.Key`, i.e. `CallLegUsageKey(BillingCallID, BLegID)`.
- `fingerprint_version=1` means the semantic-fingerprint contract of the predecessor's sealed current records; if that algorithm changes later, the version must be bumped rather than silently reinterpreted.
- `fingerprint` is the sealed record's semantic fingerprint, not a hash of raw JSON bytes.
- `payload_json` is transport storage only; equivalent JSON encoding differences do not create identity differences.

Spec 1 guarantees the leg semantic fingerprint covers authoritative attempt sequence/presence, backend/model/provider identity, outcome/surfaced state and billing evidence. The spool must validate the decoded payload against its stored key/fingerprint before replay.

Replay behavior is exact:

```text
same kind + record_key + fingerprint
    -> idempotent success (even if JSON byte formatting differs)

same kind + record_key + different fingerprint
    -> integrity conflict; never overwrite; mark/emit explicit reconcile error
```

Minimal table:

```text
terminal_usage_spool
  spool_key            PK
  kind                 call|leg
  record_key
  fingerprint_version
  fingerprint
  payload_json
  payload_bytes
  status               pending|delivering|processed|error
  attempt_count
  next_attempt_at
  claimed_at
  last_error
  enqueued_at
  updated_at
```

Unique `(kind, record_key)` plus fingerprint conflict detection.

The local spool does not understand money.

#### Bounded growth and health

The authoritative spool has typed, configurable bounds with conservative defaults:

```text
MaxPendingRecords      = 100000
MaxPendingPayloadBytes = 512 MiB
MaxDatabaseBytes       = 1 GiB
MinFreeDiskBytes       = 256 MiB
ProcessedRetention     = 24h
```

Exact defaults may be tuned during implementation only with measured justification, but production must never mean “unbounded until disk full”. The writer checks pending record/payload capacity transactionally and checks database-file/free-disk watermarks before accepting a new record. Crossing any hard limit returns a typed durability/capacity error and marks spool health non-ready; authoritative terminal semantics handle it as critical durability failure and never trigger provider retry after output.

The flusher prunes `processed` rows after `ProcessedRetention`; pruning/checkpoint maintenance is process-owned background work, never request-path work. Health/metrics expose at minimum pending record count, pending payload bytes, database file bytes, free disk bytes, oldest pending age, error-row count, append-capacity failures, last delivery error and a healthy/degraded/full state.

### 8.4 Delivery worker and crash recovery

One process-owned worker:

```text
on spool startup:
  reset any rows left delivering by the prior crashed owner to pending

claim bounded pending rows
 -> set status=delivering and claimed_at=now
 -> append to central CallUsageAppender/CallLegUsageAppender
 -> identical replay counts as success
 -> mark processed
 -> retry with bounded backoff on transient failure
 -> mark error/reconcile on fingerprint conflict
```

Even with one worker, `claimed_at` is required for crash diagnostics and stale-claim recovery. Startup recovery is safe because the supported topology is one process owner per stable spool file. The worker may additionally reclaim a `delivering` row older than a bounded claim timeout to recover from an in-process worker failure. Central key/fingerprint idempotency makes replay safe.

### 8.5 Lifecycle

Open spool at process composition time, register close with existing process owner, recover stale delivery state, then start one owned flusher.

No per-request goroutine.

Runtime terminal append uses a bounded local I/O deadline sized for local disk, not a multi-minute central network timeout.

### 8.6 Cutover deletion and old-outbox drain

After all runtime injections use the spool, quiesce the old direct/fallback append writers before deleting their persistence.

For every existing central `usage_append_outbox` row:

1. `processed` rows require no further delivery;
2. pending/deferred retryable rows are replayed idempotently into the current central call/leg tables and then marked processed;
3. replay conflicts, malformed payloads, or rows whose durable effect cannot be proven must block destructive cutover and enter explicit operator reconciliation; they are never discarded;
4. after draining, re-run a source-key/fingerprint reconciliation proving every non-error row has the expected current usage record;
5. inside the same migration-critical section used for destructive retirement, re-check that no unresolved outbox row exists before dropping its schema.

Only then delete:

- direct central call/leg appender injection from `BillingRuntime`;
- `RetryingCallUsageAppender`;
- `RetryingCallLegUsageAppender`;
- `UsageAppendWorker`;
- central `usage_append_outbox` live schema/store if no other consumer remains.

Central call/leg tables remain idempotent.

## 9. Final Runtime and Composition Shape

Final `BillingRuntime` should be close to:

```go
type BillingRuntime struct {
    BillingCreditGate        BillingCreditGate
    BillingExposureAdmission BillingExposureAdmission
    TerminalUsageSink        billing.TerminalUsageSink
    BillingIdentity          BillingIdentity
}
```

There is intentionally **no `BillingAuthoritative` runtime mode flag** in the converged contract. Production construction has one all-or-none invariant:

```text
non-billing host:
    all four billing ports/identity are absent

billing-enabled host:
    BillingCreditGate != nil
    BillingExposureAdmission != nil
    TerminalUsageSink != nil
    BillingIdentity is complete

any partial combination:
    construction/NewExecutor validation error
```

Whether the stock host enables billing is a composition decision, not a mutable executor mode. No boolean may select legacy/current billing, bypass one admission stage, or permit terminal billing without the complete authoritative path.

No customer rating resolver, provider rate resolver, settlement store or worker lives on Executor.

Composition root owns:

- billing durable store;
- snapshot catalog;
- local terminal spool;
- spool flusher;
- customer post-usage worker;
- provider COGS worker;
- reports/admin surfaces.

This keeps request execution ignorant of post-usage finance.

## 10. Reconciliation

### Financial balance

```text
RebuiltBalance =
    OpeningBalance
    + credits(customer financial account)
    - debits(customer financial account)
```

using financial journal only.

### Exposure

```text
OpenExposure = SUM(call_exposures.max WHERE status=open)
```

No reserved journal/book.

### Provider COGS

Sum provider COGS journal/operation rows independently.

Operation snapshots are diagnostic redundancy and must reconcile to journal/current account state.

## 11. Migration Ordering

Recommended implementation/cutover order:

1. verified predecessor implementation + canonical baseline artifact;
2. native customer rating parity;
3. remove legacy rating types;
4. add local spool and cut runtime handoff;
5. drain and delete direct/outbox delivery layering;
6. decouple provider COGS sequencing;
7. remove current ReservedNano/auth-book surfaces;
8. narrow UsageAuthority money;
9. retire old TUR/LUR processing/tables;
10. remove residual billing mode branching and activate final deletion/LOC ratchets.

Every destructive step follows the Section 4.1 migration-critical-section rule: application writers quiesced, dialect lock acquired, preserve/convert/drain performed, unresolved state rechecked, destructive DDL committed under the same lock, and post-migration reconciliation run before the release is certified.

Destructive database drops occur only after consumers are gone and migration checks are green.

## 12. Simplification Ratchet

### 12.1 Canonical Phase-0 artifact

Phase 0 creates exactly one checked-in baseline artifact after Spec 1 has been implemented and merged:

```text
internal/archtest/testdata/architecture/billing_final_convergence_baseline.json
```

The file is generated from the exact predecessor-complete main SHA and is the sole denominator for the 10% gate. It records at minimum:

```json
{
  "schema_version": 1,
  "baseline_sha": "<post-Spec-1 main SHA>",
  "counting_method": "physical-go-lines-v1",
  "denominator_loc": 0,
  "included_roots": [],
  "included_files": [],
  "included_declarations": [],
  "excluded_globs": [],
  "seed_symbols": [],
  "symbol_following_version": 1,
  "deletion_targets": []
}
```

`denominator_loc` is computed from the artifact inventory, never hand-entered independently.

### 12.2 Counting method

`physical-go-lines-v1` is deterministic:

- count only git-tracked UTF-8 production `.go` source;
- exclude `_test.go`, `testdata`, `vendor`, `.worktrees`, generated files carrying the standard `Code generated ... DO NOT EDIT.` marker, docs and non-Go assets;
- exclude migration source files only when the artifact explicitly classifies them as immutable historical-upgrade-only code with no runtime/reconciliation consumer;
- for whole-file entries, count physical lines by splitting the exact blob on `\n`; a trailing newline does not create an extra synthetic line; blank/comment lines count;
- for declaration entries outside whole-file roots, parse with `go/parser` and count the inclusive `token.Pos` line span of the recorded top-level declaration; overlapping declaration spans are counted once.

The widened whole-file roots include:

- `internal/core/billing`;
- `internal/infra/billingstore`;
- `internal/infra/billingcompose`;
- `internal/infra/billingadmission`;
- terminal spool package once introduced;
- runtime `billing_*.go` and exact billing fields/declarations in executor configuration;
- runtimebundle billing composition.

### 12.3 Deterministic symbol following

Money-specific UsageAuthority and any economic compatibility code outside the whole-file roots are included through an AST fixed-point inventory, not an ad-hoc reviewer list:

1. Phase 0 records explicit initial seed identifiers for the migration-era monetary/usage concepts (for example the predecessor's money unit, money reservation fields, reserved/auth-book symbols, legacy usage model symbols and direct/outbox types).
2. Parse every non-excluded production Go file.
3. Include a top-level declaration when it declares a seed identifier or its AST references a current seed identifier.
4. Add names declared by each newly included declaration to the seed set and repeat until no declaration is added.
5. Record every included declaration with file, declared names, source line range, baseline LOC and the seed/reference that caused inclusion.
6. Final certification reruns the same versioned algorithm from the same initial seed set against the final tree and takes the union of still-existing baseline entries plus newly discovered followed declarations.

This makes movement/adapter indirection visible while keeping unrelated non-money quota code outside the denominator. Any scanner-rule change requires a schema/version bump and explicit design review; the implementation may not alter the denominator algorithm merely to pass the target.

### 12.4 Target

Final target:

```text
final production LOC <= floor(denominator_loc * 0.90)
```

plus all structural deletion targets.

LOC is secondary to correctness: meeting 10% cannot waive a migration, financial, durability or architectural failure.

## 13. Validation Matrix

Required high-value scenarios:

- all predecessor wrong-charge regressions;
- native-vs-predecessor rating differential tests before deleting old rating, including `all potential` evidence-first semantics and sequence-unknown cases;
- fresh + upgraded SQLite/Postgres schema equivalence;
- provider COGS journal migration preserves historical rows while new provider postings use NULL account sequence and deterministic `(recorded_at, transaction_id)` order;
- partial billing runtime wiring fails construction and no `BillingAuthoritative` mode selector remains;
- concurrent writer cannot create legacy work between retirement proof and drop;
- old pending legacy row blocks drop;
- pending/error `usage_append_outbox` work blocks deletion until safely delivered/reconciled;
- current domain has no ReservedNano;
- old authorization rows remain auditable but cannot be posted by current writer;
- money UsageAuthority config fails with migration error;
- token/request quota behavior unchanged;
- 1000+ provider COGS postings do not serialize customer exposure admissions on PostgreSQL;
- local spool successful append survives process restart under the defined SQLite durability contract;
- crash before local commit acknowledgement is never reported as successful append;
- central billing DB unavailable while local spool continues accepting only within configured capacity/health limits;
- processed spool retention/pruning and disk-watermark health are observable;
- process restart/stale claim recovery flushes prior `delivering`/pending local records;
- call closure delivered before legs remains unclaimable until every frozen expected B-leg exists;
- same record key/fingerprint replays idempotently while changed sequence/model/evidence conflicts;
- no post-output retry on local spool error;
- customer/provider workers process independently;
- canonical Phase-0 baseline scanner reproduces its denominator from the pinned SHA;
- final source/deletion/LOC ratchets.

## 14. Non-Goals and Rejection Criteria

Reject implementation proposals that:

- add a generic event-sourcing layer;
- introduce Kafka or a distributed broker as baseline;
- keep old and new usage models behind a feature flag;
- keep monetary UsageAuthority “just in case”;
- move legacy code to another package to satisfy LOC;
- add cache/aggregate state before measurements justify it;
- create another mutable exposure counter;
- re-couple provider COGS to customer credit;
- retain `BillingAuthoritative` or another mutable legacy/current monetary mode selector;
- make runtime aware of rating/journal internals.
