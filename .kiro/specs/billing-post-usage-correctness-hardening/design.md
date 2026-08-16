# Design Document

## 1. Overview

This design repairs four defects in the post-usage billing path while preserving the exposure architecture merged by PR #340:

1. preserve the real B2BUA attempt sequence in durable leg usage;
2. rate selected legs with their effective backend/model customer pricing;
3. resolve customer pricing without requiring provider/operator-rate readiness;
4. replace executor-lifetime billing-call maps with one request-scoped state object.

The design intentionally leaves the legacy `TurnUsageRecord`/`LegUsageRecord` rating bridge in place temporarily. The successor spec deletes that bridge after these corrections are proven.

## 2. Architectural Invariants

The request path remains:

```text
prepare request + BillingCallID
 -> cheap settled-credit screen
 -> route plan + pessimistic customer quote
 -> atomic CallExposure admission
 -> execute/failover/parallel B-legs
 -> append terminal CallLegUsageRecord(s)
 -> append CallUsageRecord closure
 -> post-usage customer rating
 -> atomic customer posting + exposure close
```

Hard invariants:

- one `BillingCallID` per incoming invocation;
- no financial mutation while streaming;
- no monetary hold lifecycle;
- terminal usage is immutable evidence;
- `ExpectedBLegIDs` is a completeness set, never an ordering source;
- actual B2BUA sequence is a financial fact when policy selection depends on order;
- provider COGS readiness cannot block customer settlement;
- executor memory is proportional to active work.

## 3. Data Model Changes

### 3.1 CallLegUsageRecord sequence

Add a field with domain terminology that makes its meaning explicit:

```go
type CallLegUsageRecord struct {
    ...
    AttemptSeq int
    ...
}
```

`AttemptSeq` is the exact `b2bua.BLegRecord.Seq`.

For newly produced records:

```text
AttemptSeq > 0
```

The value participates in the new semantic fingerprint.

Do not call this merely `Seq` in persistence if doing so makes migration ambiguity likely; `attempt_seq` is preferred for the database column.

### 3.2 Brownfield sequence presence

Existing rows have no sequence. The migration therefore adds nullable `attempt_seq`:

```text
usage_leg_records.attempt_seq NULL  -- pre-fix record, order unknown
usage_leg_records.attempt_seq > 0   -- corrected record
```

The in-memory persistence decoder may represent legacy unknown as zero plus an internal presence bit/nullable row field. The durable row, not a guessed value, is authoritative.

New appends must reject missing/non-positive sequence.

### 3.3 Fingerprint compatibility

Do not invalidate existing stored fingerprints.

Use versioned leg fingerprint behavior:

```text
legacy row with sequence absent -> validate existing v1 canonical body
new row with sequence present   -> v2 canonical body includes AttemptSeq
```

Implementation may use an internal fingerprint-version discriminator rather than expose a new public field, but the behavior must be explicit and tested.

A new write is always v2.

### 3.4 Sequence uniqueness

The store should enforce, within one call, uniqueness of known positive attempt sequences.

Preferred PostgreSQL/SQLite schema shape:

```text
UNIQUE(call_id, attempt_seq)
```

with NULL legacy rows allowed under normal SQL NULL uniqueness semantics.

The existing `UNIQUE(call_id, b_leg_id)` remains.

## 4. Runtime Ownership

### 4.1 Replace executor-global collector

Introduce one private call-scoped object:

```go
type billingCallState struct {
    callID billing.BillingCallID

    mu sync.Mutex

    allocated map[string]int // BLegID -> actual AttemptSeq
    minStarted time.Time
    maxFinished time.Time

    finalize map[string]*finalizeCacheEntry
    frozen   bool
}
```

Exact internal shape is flexible; ownership is not.

Allocate it once during `prepareRequest` immediately after `BillingCallID` creation.

Thread the pointer through the existing request/open/parallel/stream state. Retry and hidden interleaved continuation for the same incoming invocation reuse the pointer.

### 4.2 Allocation recording

When `NextBLeg` returns:

```text
record BLegID -> Seq
```

before any path can forget the sequence.

If an allocated leg never opens, its terminal record still uses the same sequence.

### 4.3 Closure

Call closure asks the call state for:

- canonical expected B-leg ID set;
- min start / max finish;
- no financial evidence aggregation.

It then seals and appends `CallUsageRecord`.

After request terminal ownership has completed the last billing handoff, the runtime stream/request objects release their references naturally. No process-global eviction API is required.

### 4.4 FinalizeBilling single-flight

Move `finalizeOnce` state inside `billingCallState`.

Key by B-leg ID where available. Multiple racing terminal paths for one B-leg share the same result.

When the call state becomes unreachable, finalization entries disappear automatically.

## 5. Customer Pricing Resolution

### 5.1 Separate snapshot products

Replace the customer use of combined `SnapshotCatalog.SnapshotsFor` with a customer-specific result:

```go
type CustomerRatingSnapshots struct {
    DefaultPricing billing.PricingSnapshot
    Policy         billing.ChargePolicy
    ModelPricing   []billing.ModelCustomerPricing
}

func (c *SnapshotCatalog) CustomerRatingSnapshots(
    call billing.CallUsageRecord,
    legs []billing.CallLegUsageRecord,
) (CustomerRatingSnapshots, error)
```

Provider cost keeps a separate lookup:

```go
func (c *SnapshotCatalog) OperatorRate(
    ref billing.VersionRef,
) (billing.OperatorRateSnapshot, error)
```

No operator-rate lookup occurs in `CustomerRatingSnapshots`.

### 5.2 Rating input

Extend internal call rating:

```go
type CallRatingInput struct {
    Call              CallUsageRecord
    Legs              []CallLegUsageRecord
    MaxCustomerCharge Money
    CustomerPricing   PricingSnapshot
    ModelPricing      []ModelCustomerPricing
    CustomerPolicy    ChargePolicy
}
```

Remove `OperatorRates` from this customer type.

### 5.3 Per-leg effective pricing

When adapting to the existing rating function during this spec:

```text
CallLegUsageRecord.AttemptSeq -> LegUsageRecord.Seq
ModelPricing                  -> RatingInput.ModelPricing
```

The adapter must be lossless for all facts customer rating consumes.

This adapter is explicitly temporary and becomes a deletion target in the successor spec.

## 6. Legacy Sequence Rating Policy

A pre-fix leg with unknown sequence is not automatically an error if sequence is irrelevant.

Customer selection can proceed when:

- the call is completed and selected surfaced leg(s) are unambiguous; or
- policy charges all accepted legs and therefore order does not choose a winner.

If a failed/canceled/incomplete call has no surfaced accepted leg and policy requires selecting the latest accepted leg, unknown sequence makes rating indeterminate.

Return a typed error such as:

```go
ErrBillingAttemptSequenceUnknown
```

The call processor shall retry according to existing policy and ultimately mark reconcile-required rather than guessing.

## 7. Persistence Migration

Add one forward billing migration after the current migration head.

SQLite:

```text
ALTER TABLE usage_leg_records ADD COLUMN attempt_seq INTEGER NULL
CREATE UNIQUE INDEX ... ON usage_leg_records(call_id, attempt_seq)
```

PostgreSQL equivalent uses nullable integer/bigint appropriate to existing sequence type and a unique index.

Do not backfill from:

- `b_leg_id`;
- `sealed_at`;
- timestamps;
- rowid;
- provider/model;
- lexical order.

Update schema verification to require the new column/index.

## 8. Customer/Provider Independence

The customer worker flow becomes:

```text
claim complete call
 -> load CallExposure
 -> resolve CUSTOMER snapshots only
 -> rate customer
 -> settle customer + close exposure
```

Provider flow remains:

```text
claim provider_cost_work per leg
 -> resolve operator rate only if provider authoritative cost unavailable
 -> post/mark provider COGS independently
```

There is no cross-call between these resolvers.

## 9. Failure and Concurrency Semantics

### Call state races

All mutable call-scoped fields remain protected by one small mutex or equivalent narrowly scoped synchronization. Never hold the mutex across:

- provider `FinalizeBilling`;
- database append;
- logger callbacks;
- external hooks.

Single-flight inserts an entry under lock, releases lock, performs finalization, then publishes result.

### Persistence failure

Existing append/outbox behavior remains. This spec must not change provider retry semantics after output.

### Repeated terminal paths

B-leg record idempotency remains keyed by `(BillingCallID, BLegID)` and semantic fingerprint. Sequence mismatch is a conflict.

## 10. TDD Verification Matrix

Minimum RED-before-GREEN cases:

1. sequential failover: real Seq 1/2, BLegID lexical order 2/1;
2. canceled call with no surfaced leg chooses real latest accepted attempt;
3. completed call with surfaced winner ignores lexical order;
4. charge-all mixed-model legs use distinct rate cards;
5. failover from cheap model to expensive model settles expensive model correctly;
6. missing operator rate does not prevent customer settlement;
7. legacy sequence-unknown completed surfaced record remains rateable;
8. legacy sequence-unknown interrupted call requiring latest selection becomes reconcile-required;
9. same call+BLegID replay with changed sequence conflicts;
10. many sequential calls on one executor do not grow retained call-state count;
11. parallel Recv/Close/finalization remains race-free;
12. SQLite/PostgreSQL sequence migration parity.

## 11. Architecture Ratchets

Add source/AST tests that reject:

- `Seq: i + 1` or equivalent positional sequence reconstruction in customer rating adapters;
- sorting/BLegID comparison used as customer leg selection;
- customer resolver calls that load operator rates;
- executor-owned `map[BillingCallID]...` or string-keyed lifetime call registries for billing state;
- reintroduction of monetary holds or stream financial writes.

Ratchets should target semantics/symbols, not brittle formatting.

## 12. Rollout

1. deploy schema supporting nullable sequence;
2. deploy corrected runtime writes with positive sequence and v2 fingerprint;
3. deploy corrected customer resolver/rating;
4. observe reconcile-required legacy edge cases explicitly;
5. run retained-state and B2BUA regressions;
6. declare this spec complete;
7. only then start implementation of `billing-architecture-final-convergence`.

No dual customer settlement path is needed during rollout.

## 13. Non-Goals Reaffirmed

This spec does not:

- delete legacy TUR/LUR code;
- drop old billing tables;
- redesign terminal spooling;
- remove money semantics from UsageAuthority;
- change account journals;
- change provider COGS account locking;
- add external dependencies;
- change public API.
