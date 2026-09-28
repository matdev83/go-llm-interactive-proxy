# Refinement 5.2 execution evidence

Status: implemented and verified for Task 5.2 only.

Scope: provider finalizer, provider statement-line, and provider correction
economics remain appendable after execution closure. Requirements covered:
3.4 and 4.5. This evidence does not claim Task 5.3, a new statement parser,
or a public statement-import endpoint.

## Approved behavior and production call path

The approved statement coverage is a native verified statement-line whose
correlation names exactly one already-closed B-leg. An account-window or
unlinked statement is not allocated across B-legs. The relay now retries such
an outbox item with an explicit error and leaves it pending; it never silently
acknowledges required economic work.

The exercised production path is:

1. `internal/core/runtime/attempt_usage_evidence.go` drains the provider
   adapter's `execbackend.EconomicEvidenceSource`/
   `ProviderEvidenceBuffer` records. `internal/core/runtime/billing_leg.go`
   writes the sealed `CallLegUsageRecord` through the terminal usage sink.
   The provider event's ProviderID, StoreID, provider account, request, and
   charge identifiers are therefore captured at the production seam before
   late evidence is accepted.
2. `internal/core/metering/LateEconomicAppender.AppendLateEconomicEvidence`
   reads only the immutable closed-leg record and exact correction predecessor,
   validates the submitted lineage and trusted provider authority, then calls
   `AppendEconomicObservationWithOutbox`. It has no executor, attempt
   allocator, terminalizer, or replacement-B-leg capability.
3. Stock `runtimebundle.ComposeBilling` wires the real
   `journalstore.NewObservationSinkWithOutbox` and the durable billing store.
   `runtimebundle/observation_economic_bridge.go` claims the observation
   outbox, resolves the correlated B-leg plus verified linked statement lines,
   calls `billing.NewObservationEconomicWorkBuilder`, appends durable provider
   and customer work, and acknowledges only after the work append succeeds.
4. `billing.EconomicRevisionWorker` instances run the real provider and
   customer valuation/reconciliation paths. The provider worker invokes
   `BuildProviderCostRevisionInput` and the durable provider-cost revision
   store; the terminal `ClaimCompleteCall` path performs the independent
   customer settlement.

## RED_PHASE_OUTPUT

The raw terminal transcript was not retained by the session. The following is
an honest reconstruction from the exact terminal output; it is not represented
as a captured transcript.

First-slice RED (the trusted-authority fixture had not yet been repaired):

```text
go test -count=1 -run 'TestRefinement52|TestLateEconomicAppender' ./internal/core/metering ./internal/infra/billingstore ./internal/infra/runtimebundle
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering
--- FAIL: TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable
    ... late provider finalizer append: metering: late economic append rejected: metering: late economic authority mismatch: closed B-leg has no trusted provider evidence
FAIL github.com/.../internal/infra/billingstore
--- FAIL: TestRefinement52RuntimeLateEconomicAppendAfterTerminalClosure
    ... late provider finalizer: metering: late economic append rejected: metering: late economic authority mismatch: submitted provider context has no trusted closed-leg match
FAIL github.com/.../internal/infra/runtimebundle
```

Second-slice RED (before statement-line bridge support):

```text
go test -count=1 -run '^TestRefinement52RuntimeLateEconomicAppendAfterTerminalClosure$' ./internal/infra/runtimebundle
--- FAIL: TestRefinement52RuntimeLateEconomicAppendAfterTerminalClosure
    provider late work omitted verified B-leg-correlated statement "refinement52-runtime-statement": observations=[... finalizer ... correction ... initial provider ...]
FAIL
```

The minimal production change for that RED added typed verified
statement-line linkage to the relay, provider revision input, and valuation
reference selection. Provider COGS reduction still filters statement charges
out; the statement remains an auditable provider valuation reference and does
not become a second COGS leaf.

Third-slice RED (captured focused command output; no separate raw transcript
file was retained):

```text
go test -count=1 -run '^TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably$' ./internal/infra/runtimebundle
# github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle_test [github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle.test]
internal\infra\runtimebundle\refinement5_2_late_economic_integration_test.go:46:11: undefined: openRefinement52ConcurrentBillingStore
internal\infra\runtimebundle\refinement5_2_late_economic_integration_test.go:226:12: undefined: openRefinement52ConcurrentBillingStore
FAIL github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle [build failed]
FAIL
```

The missing symbol was a test-only file-backed durable-store helper. No
production defect was exposed by this RED; adding that helper made the test
compile and the focused command pass.

Fourth-slice RED (captured focused command output from the deterministic
same-revision interleaving, before the convergence fix):

```text
go test -count=1 -run '^TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay$' ./internal/infra/runtimebundle
--- FAIL: TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay (11.46s)
    refinement5_2_late_economic_integration_test.go:482: timed out waiting for exact provider economic head "b-leg-economic:v1:26649fd60bfd3ba18fe4293aa2f225e48c7396e955b064f94555e9dfe556f8cc" revision 3/hash "2a551ca3befb0d0027088d012a67938a20f2cb900cac03b4d3859d23a964b608": head={Queue: HeadKey: ... EvidenceRevision:0 InputSetHash: ...}: cause=billingstore: get economic valuation head: context deadline exceeded ... pendingOutbox=[] ... provider_call_cogs ... Amount:{Nano:125 Currency:USD}
FAIL
FAIL github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	11.491s
FAIL
```

The run had already completed the statement-only revision-3 work and its
initial 125-nano provider COGS. The later strict-superset work also completed,
but the lexical hash fence retained the partial head, so the exact complete
hash never appeared. The call/head/hash values above are the exact values from
that captured run; generated call identities make them run-specific.

## TDD and focused evidence

`internal/core/metering/late_economic_append_test.go` contains exactly 20
`TestLateEconomicAppender...` tests. They cover accepted finalizer,
statement-line, and correction evidence; exact replay and 16-way concurrent
replay; wrong StoreID/BillingCallID/BLegID/AttemptSeq/ProviderID/account/
request/charge; provenance failures; missing or invalid supersedes; unclosed
or missing closed legs; and sinks without the atomic economic-outbox
capability.

The final focused command passed:

```text
go test -count=20 -shuffle=on -run 'TestRefinement52|TestLateEconomicAppender' ./internal/core/metering ./internal/infra/billingstore ./internal/infra/runtimebundle
ok   	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering	0.194s
ok   	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	50.194s
ok   	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	154.739s
```

The runtime integration was made deterministic by using monotonic evidence
revisions: initial production provider evidence is revision 1, finalizer is 2,
statement is 3, and correction is 4. The test waits for each exact durable
valuation head/input hash before appending the next late observation.

## Durable store proof

`TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable`
uses the production billing terminal append to seal one positive-sequence,
finished B-leg with an immutable fingerprint and a provider-origin observed
authority observation. The metering journal is closed and reopened before the
correction. `LateEconomicAppender` is rebuilt with the reopened durable
journal sink and the same closed-leg reader.

The test proves three immutable observations, exact correction supersession,
two pending outbox identities before correction, same-identity changed-payload
rejection with no extra observation/outbox row, eight concurrent exact
finalizer replays with no duplicate identity, and unchanged closed-leg row,
B-leg, attempt sequence, and fingerprint. It also reduces finalizer plus
correction to one payable effective provider charge of 9 nano-units and builds
one provider revision work item; verified statement references are retained in
the provider plane while statement evidence does not create customer work.

## Authentic stock runtime proof

`TestRefinement52RuntimeLateEconomicAppendAfterTerminalClosure` uses stock
`ComposeBilling`/`BuildHost`, SQLite metering, the real executor, real relay,
real valuation workers, and CustomerInput enabled. The test backend emits a
provider V2 usage event through `ProviderEvidenceBuffer`; the runtime adds the
customer account projection at the observation boundary and seals the resulting
provider authority in the closed leg. No provider account/request/charge value
is self-authorized by the late test input: the late identity is derived from
the sealed authority observation and checked against ProviderID and the closed
record.

After `ClaimCompleteCall` closes the call, the test appends:

- finalizer revision 2, same BillingCallID/BLegID/AttemptSeq and provider
  authority;
- verified statement-importer line revision 3 with native statement-line
  subject and exact B-leg correlation; and
- correction revision 4, with the exact finalizer `ObservationRef` in
  `Supersedes` and a 7-to-9 provider amount correction.

The durable observation outbox is drained by the process-owned relay after
each append. The test does not invoke a work builder as the processing proof;
the builder is used only afterward to independently derive expected immutable
input hashes for read-side assertions. The provider work observed by the
valuation head includes the verified statement-line reference.

The financial and lifecycle assertions are:

- provider valuation heads advance through revisions 1, 2, 3, and 4 with the
  exact expected input-set hash at each stage and complete valuation payloads;
- provider COGS is 125 initial, then finalizer delta +7, then correction delta
  +2; the statement advances the provider valuation head without posting a
  statement COGS leaf; final current provider cost is 134;
- `provider_call_cogs` contains exactly three chained transactions, with each
  correction linked by both `ReversalOf` and `CorrectsTransactionID`, and the
  final transaction contains only the exact +2 delta;
- CustomerInput remains configured and its durable customer work is claimed by
  the customer worker before late evidence. The independent customer
  settlement is exactly one `customer_call_settlement` for 310, ending at the
  expected 9690 balance. Late provider-only evidence creates no customer work
  identity and no second customer settlement;
- before and after late appends there is exactly one closed B-leg with the same
  fingerprint, B-leg ID, and AttemptSeq, and the executor attempt set has the
  same cardinality; and
- replaying the exact finalizer, statement, and correction through the real
  appender after posting leaves the provider head fingerprint/version/fence,
  provider journal IDs/fingerprints, customer work IDs, and customer balance
  unchanged; a same-identity changed-payload replay is rejected with no
  observation outbox row.

## Statement routing and fail-closed behavior

`TestObservationEconomicRelayRetainsUnlinkedStatementOutbox` appends a valid
native statement-line with no B-leg correlation. The production relay returns
an explicit error, leaves the observation outbox pending, and creates no
provider economic work. A verified statement with a B-leg correlation is
resolved by StoreID, account, A-leg, call, provider account, request, charge,
and exact B-leg checks before it can enter that B-leg's provider revision.
This is the smallest request-scoped bridge and does not introduce Cartesian
account/window allocation.

## Relay lease-recovery proof

`TestObservationEconomicRelayRecoversExpiredLeaseAfterStaleOwner` uses the
real `journalstore.NewObservationSinkWithOutbox` append, durable observation
outbox claim, and durable billing work appender. Relay A claims the eligible
B-leg observation, delegates the real billing work append, and then blocks at a
test-only pre-ack hook. A deterministic clock advances beyond A's lease. Relay
B reclaims the expired row, replays the same durable work identity, and
acknowledges it. A's stale acknowledgement returns
`journalstore.ErrObservationOutboxClaimLost`.

The durable assertions show `status=delivered`, `attempt_count=2`, no pending
observation outbox entries, exactly one provider work identity, and the
original observation still readable. No sleep or wall-clock polling is used in
this proof; channel synchronization and the injected deterministic clock are
the only coordination.

```text
go test -count=20 -shuffle=on -run '^TestObservationEconomicRelayRecoversExpiredLeaseAfterStaleOwner$' ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	35.067s
```

## Concurrent distinct late-revision proof

`TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably` uses
file-backed SQLite billing and metering stores, stock `ComposeBilling` and
`BuildHost`, the real executor/provider evidence buffer, the process-owned
observation relay, both economic valuation workers, and CustomerInput. A real
terminal production path first closes one B-leg and establishes provider
valuation revision 1. The test then starts two concurrent appender calls for
distinct valid evidence: provider finalizer revision 2 and a verified native
statement-line revision 3 carrying the exact B-leg correlation and sealed
provider authority.

These are independent evidence events, not a supersession chain, so the
approved model does not require either sibling to fail stale. Evidence revision
is the maximum durable observation revision and the bridge may coalesce safe
concurrent arrivals; the assertion therefore requires the deterministic final
revision 3 and the exact input-set hash derived from all durable provider
evidence, rather than requiring an intermediate revision-2 queue marker. The
expected work is constructed read-only for the assertion; the production relay
claims the observation outbox and appends the work consumed by workers.

The stock workers serialize the result to a complete provider head at revision
3 with that exact input hash. Provider COGS is 125 initial plus one exact +7
finalizer delta (132 current), with exactly two `provider_call_cogs` journals;
the statement advances provider valuation but contributes no COGS leaf. The
customer queue/work IDs, single `customer_call_settlement`, and customer
balance remain unchanged. The closed B-leg count, fingerprint, B-leg ID, and
AttemptSeq remain unchanged, with no replacement allocation or re-terminalize
path.

The test closes the first stock host and reopens both durable stores through a
new stock host. Exact replay of both late observations leaves the provider
valuation head fingerprint/version/fence/hash, provider-cost head, journal IDs
and fingerprints, customer work/settlement/balance, and closed-leg identity
unchanged.

```text
go test -count=20 -shuffle=on -run '^TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably$' ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	64.962s
```

## Same-revision evidence convergence proof

`EconomicEvidenceSetRelation` in `internal/core/billing/economic_revision.go`
canonicalizes the durable `ObservationRef` set (including payload hash),
rejects conflicting payloads for one observation revision, and classifies the
candidate as equal, a strict superset, a strict subset, or incomparable. The
lexical input hash remains a tie-breaker only for equal sets. The durable
valuation head loads the prior immutable valuation's canonical JSON references
inside its existing transaction; the provider-cost writer uses the same
valuation references plus the incoming provider evidence references. No schema
or migration change was required.

`TestRefinement52DurableSameRevisionEvidenceContainmentFencesHead` covers
exact replay, strict superset advancement, strict subset no-rollback, and
incomparable fail-closed behavior. Incomparable work returns
`ErrEconomicRevisionFence` before transaction commit so its durable work stays
retryable and no head mutation is acknowledged. The stock integration
`TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay` first
relays/processes statement revision 3 alone, then appends the revision-2
provider finalizer. It verifies the complete hash is lexically lower than the
partial hash, yet the durable revision-3 head advances to the complete
reference set and provider COGS moves from 125 to exactly 132 with one +7
delta. Exact replay after the head is complete does not advance either head or
post another journal. The stock host is then closed, both durable stores are
reopened, and the exact statement/finalizer identities are replayed through a
new appender/relay; revision-3 heads, hashes, fences, journals, and 132-nano
COGS remain byte/identity stable. Customer work, the single customer
settlement/balance, the closed B-leg fingerprint/ID/AttemptSeq, and attempts
remain unchanged.

```text
go test -count=1 -run 'TestCompareEconomicEvidenceSets|TestRefinement52DurableSameRevisionEvidenceContainmentFencesHead|^TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay$' ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing	0.022s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	1.783s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	4.780s

go test -count=1 -v -run '^TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay$' ./internal/infra/runtimebundle
    refinement5_2_late_economic_integration_test.go:516: same-revision convergence partial_revision=3 partial_hash=77526ca231cbdc5d1389af84843317dae1857c2d41673f3fef26e8b84e13aa35 complete_revision=3 complete_hash=4a333b227e2517898b3c06946594b8e5858a0547c18718269cb2e015ef257241 complete_hash_lower=true provider_cost_nano=132 provider_cogs_journals=2
--- PASS: TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay (4.13s)
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	4.162s
```

The logged hashes are the exact captured values for that run; the stock call
identity is generated per test invocation, so later repetitions assert the
same relation and final values with different hash bytes.

The durable containment assertion, including the no-partial-valuation check
for incomparable evidence, also passed repeatedly:

```text
go test -count=20 -shuffle=on -run '^TestRefinement52DurableSameRevisionEvidenceContainmentFencesHead$' ./internal/infra/billingstore
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	26.213s
```

## Verification

Final remediation verification for relay lease recovery, concurrent distinct
revisions, and same-revision evidence convergence:

```text
go test -count=20 -shuffle=on -run '^TestObservationEconomicRelayRecoversExpiredLeaseAfterStaleOwner$' ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	35.067s

go test -count=20 -shuffle=on -run '^TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably$' ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	64.962s

go test -count=20 -shuffle=on -run 'TestRefinement52|TestLateEconomicAppender' ./internal/core/metering ./internal/infra/billingstore ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering	0.194s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	50.194s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	154.739s

go test -count=1 ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/... ./internal/infra/billingstore/...
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime	6.689s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime/failclosed	0.015s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering	0.045s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate	0.022s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint	0.017s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/normalize	0.011s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/plane	0.010s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/reconcile	0.019s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay	0.017s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing	0.085s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	32.108s

go test -count=1 ./internal/infra/runtimebundle
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	33.732s

make test-db-parity-sqlite
go run ./internal/testkit/dbparity/cmd sqlite
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	15.941s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore	0.049s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/bunstore	0.436s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview	1.116s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore	0.031s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore	0.025s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/bunstore	0.448s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/storecontract	0.453s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/terminalwork/workstore	0.085s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore	0.458s

go vet ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...
exit 0 (no diagnostics)
```

The explicit post-edit hygiene scan covered all 18 tracked/untracked touched
candidate files, including the new evidence, convergence unit/store test, and
integration-test files. The Go formatting scan covered all 17 touched Go
files:

```text
BOM_SCAN=PASS files=18
TRAILING_WHITESPACE_SCAN=PASS files=18
GOFMT_SCAN=PASS files=17
GIT_DIFF_CHECK=PASS
git diff --cached --name-status
INDEX_SCAN=PASS staged=0
```

The production Go diff inventory is limited to the economic revision contract,
provider-cost/economic head stores, and the existing first-slice production
files in billing, metering, and runtimebundle. The new convergence test and
the adapted same-revision provider-store test are test-only; no unrelated
package or schema/migration file is changed.

Affected trees passed:

```text
go test -count=1 ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/... ./internal/infra/billingstore/...
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime 6.609s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime/failclosed 0.018s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering 0.043s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate 0.022s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint 0.018s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/normalize 0.012s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/plane 0.011s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/reconcile 0.019s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay 0.017s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing 0.072s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore 30.053s

go test -count=1 ./internal/infra/runtimebundle
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle 30.749s
```

Registered SQLite database parity passed:

```text
make test-db-parity-sqlite
go run ./internal/testkit/dbparity/cmd sqlite
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore 15.933s
```

No schema or migration files were changed. Direct PostgreSQL parity was run
with `make test-db-parity-postgres-direct` and failed in the pre-existing
metering journal parity check before any Task 5.2 billing-store schema was
exercised:

```text
make test-db-parity-postgres-direct
--- FAIL: TestDBParity_PostgresDirect (0.55s)
    --- FAIL: TestDBParity_PostgresDirect/MigrationAndSchemaParity (0.55s)
        dbparity_postgres_test.go:44:
            Error: Received unexpected error:
                dbparity: table "metering_components": column "value_present" type mismatch: got "integer" ("int4"), want category "boolean"
FAIL github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore
dbparity: test failed for component "metering-journal" package "internal/infra/metering/journalstore" (backend: postgres-direct, exit code: 1)
exit status 1
make: *** [Makefile:165: test-db-parity-postgres-direct] Error 1
```

This is a registered PostgreSQL parity failure outside this slice; SQLite
parity passed below. The changed economic head/provider-cost paths add no
schema or migration delta.

Affected-tree vet passed with no diagnostics:

```text
go vet ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...
```

Targeted architecture checks passed:

```text
go test -count=1 ./internal/archtest -run 'TestDualPlaneEconomicsPublicPackageDAG|TestInternalCoreRuntimeDoesNotImportProviderSDKsOrProtocolPlugins|TestPhase8BillingBoundaryHasNoStreamMonetarySettlement|TestUsageRecordBillingFinalFlowPackagesExist|TestCoreControlPlaneDoesNotImportProviderSDKsOrConcretePlugins'
ok   github.com/matdev83/go-llm-interactive-proxy/internal/archtest 2.564s
```

The broader `TestBillingCoreStaysProviderAndPersistenceFree` architecture
check remains a pre-existing repository failure because its dependency scan
reports `/pkg/lipapi` from the existing billing test tree; no changed
production file imports `pkg/lipapi`. It was not masked as a pass.

The requested race attempt reached the Windows toolchain but failed before
tests during runtime/cgo compilation:

```text
go test -race -count=1 -run '^TestLateEconomicAppender' ./internal/core/metering
runtime/cgo: C:\Users\Mateusz\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.6.windows-amd64\pkg\tool\windows_amd64\cgo.exe: exit status 2
FAIL github.com/matdev83/go-llm-interactive-proxy/internal/core/metering [build failed]
```

This is recorded as a Windows cgo/toolchain limitation, not as a race result.

Latest convergence-slice verification after the production fix:

```text
go test -count=20 -shuffle=on -run 'TestCompareEconomicEvidenceSets|TestRefinement52DurableSameRevisionEvidenceContainmentFencesHead|^TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay$' ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing	0.119s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	31.737s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	93.396s

go test -count=20 -shuffle=on -run 'TestRefinement52|TestLateEconomicAppender' ./internal/core/metering ./internal/infra/billingstore ./internal/infra/runtimebundle
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering	0.218s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	69.332s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	192.138s

go test -count=1 ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/... ./internal/infra/billingstore/...
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime	6.733s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime/failclosed	0.018s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering	0.041s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate	0.024s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint	0.019s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/normalize	0.012s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/plane	0.011s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/reconcile	0.017s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay	0.015s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing	0.068s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	30.319s

go test -count=1 ./internal/infra/runtimebundle
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	30.673s

make test-db-parity-sqlite
go run ./internal/testkit/dbparity/cmd sqlite
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore	15.943s
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore	0.023s
```

The same run also passed `go vet ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...` with no diagnostics and the targeted architecture command with `ok github.com/matdev83/go-llm-interactive-proxy/internal/archtest 2.608s`. The direct PostgreSQL command failed only at the registered metering schema mismatch shown above; no Task 5.2 schema was changed.

## Residual risks

Provider adapters and future statement import adapters still own parsing and
must emit normalized V2 provenance/lineage. Account-window statements remain
pending until a separately owned explicit allocation/reconciliation path
exists. Customer post-closure rebilling remains policy-controlled and is not
inferred from provider-only evidence. Direct PostgreSQL parity is currently
blocked by the registered `metering_components.value_present` int4-versus-
boolean mismatch at `dbparity_postgres_test.go:44`; no migration/schema delta
is present in this slice.
