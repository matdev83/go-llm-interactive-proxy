# Refinement 4.3 operator/provider-cost evidence

## Scope

This slice implements only operator/provider-cost accrual. Customer retail
closure and provisional customer policy remain unchanged. Provider posting is
driven by the provider economic-revision queue after durable pure valuation;
receive callbacks still only append evidence.

## TDD evidence

RED was established by the new refinement tests before the revision posting
port and implementation existed: the focused package run failed to compile
because `ProviderCostRevisionStore`, the provider revision input, and the
worker constructor were undefined.

GREEN evidence:

```text
go test ./internal/core/billing ./internal/infra/billingstore -run '^TestRefinement43' -count=1
ok   internal/core/billing
ok   internal/infra/billingstore

go test ./internal/core/billing ./internal/infra/billingstore -count=1
ok   internal/core/billing
ok   internal/infra/billingstore

make test-db-parity-sqlite
ok   internal/infra/billingstore
ok   all other registered SQLite parity components
```

The focused tests cover pre-call-closure posting from one B-leg, exact late
correction delta, duplicate/reordered replay, non-operator/partial exclusion,
fault rollback and retry, parallel same-revision convergence, all-payable
failed/retry/loser-shaped B-leg inclusion, queue isolation, and restart
recovery without re-rating. Equivalent selected-cost diagnostic sets are
detached and canonicalized before fingerprinting, so reordered duplicate
deliveries remain replays. The durable worker test asserts that no
`usage_call_records` closure row is needed.

The legacy/revision cutover regression was RED before the fence: feeding the
same sealed B-leg through `CallProviderCostWorker` and the revision worker
produced two `provider_call_cogs` journals. It is GREEN with the durable
posting fence: legacy-first and revision-first orderings, concurrent loser,
restart, resolver-failure bypass, and legacy-fence correction retry each leave
one initial journal and only the exact later correction delta.

Repeated verification also passed:

```text
go test ./internal/core/billing -run '^TestRefinement43' -count=20
ok

go test ./internal/infra/billingstore -run '^TestRefinement43ProviderCostRevision' -count=20
ok

go vet ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle
ok

git diff --check
ok
```

The cross-path review follow-up was RED before the execution-level fence was
added: a partial/unavailable V2 revision returned before claiming the B-leg,
and a provider-charge-suffixed revision could overlap a legacy aggregate
posting. The new execution-fence tests are GREEN for both worker orderings,
restart, rollback, repeated concurrency, unavailable-to-payable transition,
and a provider-charge 10-to-6 correction. The correction emits one exact
negative four-unit delta; duplicate and reordered deliveries remain no-ops.
The provider-charge/legacy race, recovery, and correction tests also passed at
`-count=100`.

The authority refinement was independently RED before the typed boundary was
added: the new focused test could not compile because
`ProviderCostRevisionInput.Evidence` and the authority error type did not yet
exist. It is GREEN after the boundary was wired through both the builder and
the durable store. Provider COGS now requires a provider-origin,
`observed_claim` backend-attempt observation under the provider-reported (P)
basis; an operator payer must be present on the payable charge. Local
measurements, estimates, advisory/customer perspective, non-provider
boundaries, customer/unknown payers on the payable path, invented authority
labels, and an authority boolean without evidence fail with a typed authority
error. The known `unavailable` authority state remains retainable only as
non-payable partial provider coverage; it cannot authorize a posting. The
store normalizes and rejects these inputs before any head, posting-fence, or
journal mutation. Complete customer/BYOK exclusions and reference-only partial
coverage remain non-payable no-ops; unresolved payer coverage remains partial
rather than becoming operator COGS.

Focused authority GREEN evidence:

```text
go test ./internal/core/billing -run '^TestRefinement43ProviderCostRevision' -count=1
ok

go test ./internal/infra/billingstore -run '^TestRefinement43ProviderCostRevision' -count=1
ok
```

## Legacy V1 provider-cost authority repair

The legacy monetary bridge had a separate authority gap: a local operator-rate
fallback returned a reconciled positive amount with `Authoritative:false`, and
the legacy worker forwarded it to `ApplyProviderCost`. The RED regression
first failed to compile because the typed legacy authority validation seam did
not exist; the durable regression asserts that an untrusted result cannot
reach the provider writer.

The GREEN repair keeps `RateProviderCost`'s non-authoritative result available
as an advisory calculation, but validates authority in the worker and again at
the durable store boundary. The worker uses the existing
`provider_cost_unreconciled` marker and deferred queue retry path. The store
rejects before opening its monetary transaction, so a local/estimated result
cannot create a `provider_call_cogs` journal, selected-cost head, posting
fence, or execution fence. A provider-reported V1 cost remains accepted, and
the legacy worker performs the existing revision cutover claim before resolver
or authority evaluation.

Focused RED/GREEN evidence:

```text
go test ./internal/core/billing -run '^TestRefinement43LegacyProviderCost' -count=1
RED: compile failure before the typed authority seam and worker guard existed.
GREEN: ok

go test ./internal/infra/billingstore -run '^TestRefinement43Legacy' -count=1
GREEN: ok
```

Compatibility impact is intentionally narrow: callers that used an implicit
`Authoritative:false` result for a monetary legacy post must now set authority
only when the result is backed by a provider-reported V1 cost. Existing
authoritative V1 bridge behavior and legacy-only worker composition remain
available. Non-authoritative estimates are never converted into money; they
remain retryable diagnostic work through the existing non-monetary marker seam.

## Runtime single-writer composition guard

The operator review blocker for runtime wiring was RED before this repair:
`buildProcessBillingRuntime` accepted a custom store that implemented
`ProviderCostRevisionStore` and the economic revision result/queue ports but
did not implement `ProviderCostWorkCutoverStore`; with both provider-cost
workers configured, startup could therefore launch an unfenced legacy writer.
`ComposeBilling` likewise accepted that invalid combination.

The focused runtime matrix is GREEN after the guard:

| Configuration | Result |
|---|---|
| Default `billingstore.DurableStore` | composition and startup accepted; durable revision and cutover ports present |
| Legacy-only store and resolver | accepted; legacy worker remains available |
| Pure valuation store without `ProviderCostRevisionStore` | accepted; revision valuation workers start without a money writer |
| Custom revision result/store without cutover | rejected by `ComposeBilling` and runtime startup before process resources are registered |
| Simultaneous legacy + revision configuration with no cutover | rejected with `ErrProviderCostCutoverRequired` |
| Fenced simultaneous configuration | accepted; legacy queue item is cut over before resolver/posting, so no second legacy feed occurs |

RED command:

```text
go test ./internal/infra/runtimebundle -run '^TestRefinement43' -count=1
FAIL: TestRefinement43ComposeBillingRejectsRevisionMoneyWithoutCutover
FAIL: TestRefinement43RuntimeRejectsSimultaneousUnfencedProviderWriters
```

GREEN commands:

```text
go test ./internal/infra/runtimebundle -run '^TestRefinement43' -count=1
ok

go test ./internal/infra/runtimebundle -run 'TestBuildProcessBillingRuntime|TestComposeBilling' -count=1
ok

go vet ./internal/infra/runtimebundle
ok
```

Stop handlers continue to be registered before each worker's `Start`; the
startup guard runs before the terminal sink or any process worker is owned.

## Posting identity and fencing

- The mutable current head is unique on `(store_id, account_id, call_id,
  head_key)` in `billing_provider_cost_heads`.
- A revision source is the bounded identity
  `provider-cost-revision:v1:<sha256>`, derived from account, BillingCallID,
  head key, evidence revision, and input-set hash. The financial operation is
  the existing scoped `provider_call_cogs` operation key, and its immutable
  operation snapshot is written in the same transaction as the journal/head
  transition.
- A higher revision posts `current - previous` using checked integer
  arithmetic. Positive deltas debit `inference_provider_cogs` and credit
  `provider_payable_clearing`; negative corrections reverse those sides. A
  zero delta advances the head and operation marker without a journal row.
- A complete authoritative revision that changes an existing B-leg to a
  customer/BYOK payer is a zero target and reverses the prior operator COGS;
  an initially excluded or partial revision creates no head and no journal.
- Lower revisions are stale no-ops. An exact same-revision identity is a
  replay no-op; a changed valuation, subject, or amount under that identity is
  a typed conflict. Same-revision candidates with different input hashes use
  the deterministic economic-identity ordering: the higher identity advances
  the head and the lower one is stale. Higher revisions advance
  `evidence_revision`, `head_version`, and `fence` using a compare-and-swap
  predicate. PostgreSQL locks the head row while transitioning; SQLite relies
  on its writer transaction plus the CAS.
- Provider COGS uses no customer-account lock, balance update, or customer
  account sequence (`account_sequence` remains NULL). Account snapshots in the
  provider journal are observational only.

### Legacy/revision posting cutover

- The per-head posting fence is unique on `(store_id, account_id, call_id,
  lineage_key)`. A B-leg lineage uses the canonical `BillingCallID:BLegID`
  key; request-scoped provider-charge revisions add a provider-charge suffix
  so independent charge heads remain additive.
- The durable execution fence is unique on `(store_id, account_id, call_id,
  execution_lineage_key)`, where `execution_lineage_key` is always the plain
  B-leg key. It is an amount-free writer gate above the per-head fences: no
  zero-money journal is created merely to claim it. The first committed owner
  is either the legacy B-leg aggregate or a V2 B-leg/provider-charge subject.
  A revision-owned gate suppresses legacy work even when its selected cost is
  partial/unavailable; a later payable revision for the same head may still
  create the selected-cost head and post its exact delta.
- A V2 provider-charge owner permits multiple distinct provider-charge child
  heads, each with its own selected-cost head and signed delta history. A
  legacy aggregate owner suppresses all later provider-charge children because
  their additive relationship cannot be proven without a correction link.
  Thus aggregate and child lineages cannot overlap, while distinct V2 charges
  remain representable.
- A legacy post writes its COGS journal, legacy operation snapshot, fence row,
  execution fence, per-head fence, and processed queue state in one
  transaction with `authority='legacy'`. The first revision adopts that amount
  without a second journal, or posts the signed correction and atomically
  promotes the per-head and execution authority to `revision`.
- A revision-first fence retires pending legacy work before its resolver runs;
  stale resolver failures do not create an unreconciled marker. Both workers
  lock/reload the durable row where supported and rely on unique/CAS predicates
  plus retryable transactions, so a crash rolls back journal/head/fence state
  together and a restart can safely retry. Upgrade recovery also rebuilds the
  execution gate from a surviving provider-charge child fence before invoking
  legacy resolution; the prefix lookup is exact rather than wildcard-based.

## Residual risks and verification limits

- Live PostgreSQL parity was not completed in this environment: the direct
  target did not produce output within the bounded wait and was interrupted.
- `go test -race` could not build because the installed Windows cgo tool exited
  with status 2.
- The revision builder intentionally posts only when complete V2 provider
  observations are present. Reference-only work without an observation
  resolver remains partial/non-payable; allocation-backed non-request costs
  remain owned by the existing allocation selector.
- The phase-level repair now passes the stock runtimebundle billing host-loop
  tests repeatedly. The callback architecture diagnostic was traced to an
  ignored top-level ` internal` duplicate fixture and disappeared after the
  duplicate file was removed.

## Correction and reversal audit linkage

The correction-linkage blocker was RED before the repair: durable provider-cost
journals did not retain an explicit prior-transaction link, and the provider
head/fence stored only the latest identity. A legacy-first adoption could
therefore lose the original transaction after a zero-delta handoff.

The selected semantics are immutable signed-delta adjustments. The first
provider posting starts one stable `CorrectionGroupID`. Every later positive,
negative, payer-change, or cross-payer repost writes both `ReversalOf` and
`CorrectsTransactionID` to the immediately prior posting; the empty incoming
group is inherited from that target by the journal validator. This keeps the
full chain auditable while preserving the original posting and prevents an
exact replay from appending another journal. `OriginalTransactionID` and
`LastTransactionID` are persisted on both provider-cost heads and posting
fences, with legacy-first adoption carrying the legacy journal identity into
the new head.

RED command:

```text
go test -count=1 ./internal/infra/billingstore -run '^TestRefinement43Provider(CostCorrectionLinks|ChargeCorrectionLinks)'
FAIL: durable original/latest identities and correction links were absent;
legacy-first correction had an empty CorrectionGroupID
```

GREEN commands:

```text
go test -count=1 ./internal/infra/billingstore -run '^TestRefinement43Provider(CostCorrectionLinks|ChargeCorrectionLinks)'
ok
go test -count=1 ./internal/infra/billingstore
ok
go test -count=20 -shuffle=on ./internal/infra/billingstore -run '^TestRefinement43Provider(CostCorrectionLinks|ChargeCorrectionLinks)'
ok
make test-db-parity-sqlite
ok
```

The focused durable assertions cover the original posting, second correction,
full reversal, positive repost, provider-charge child heads after restart,
legacy-first adoption followed by correction, rollback/retry without orphan
rows, and exact replay with no extra journal. PostgreSQL DDL uses the matching
`ADD COLUMN IF NOT EXISTS` migration branch; live PostgreSQL parity remains an
environment verification limit recorded above.
