# Parent Phase 13 execution evidence: Task 13.1A

Scope: parent task 13.1 slice 13.1A — normalized statement ingestion domain and
public StatementImporter contracts, with no persistence adapter. New files:

- `pkg/lipsdk/economics/statement_identity.go`
- `pkg/lipsdk/economics/statement_identity_test.go`
- `internal/core/billing/statement_import.go`
- `internal/core/billing/statement_import_test.go`

Vendor invoice parsers, UI, statement matching (13.2), selected-cost heads and
balanced journal adjustments (13.3), economic workers (13.4), SQL/migrations
and dual-dialect persistence (13.1B), report routes and Kiro task-status changes
are untouched. No commit, rebase or PR operation was performed.

Worktree: `go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `3fb2d579`. The worktree contained no
other dirty files before this change and only the four files above are dirty
after it.

## Contract and boundary design

Task 13.1 contract: C5; public `StatementImporter`. Requirements 13.1, 13.6,
16.2, 16.3.

- The public port already exists as `pkg/lipsdk/economics.StatementImporter`
  (`Import(ctx, StatementBatch) (ImportResult, error)`) with the approved C3
  signature; 13.1A does not change it. It is exercised through the frozen
  interface by a test-only bound adapter, proving the domain can satisfy the
  public seam without an internal type in its signature.
- Public immutable identity contracts added on the existing `StatementBatch` /
  `StatementLine` DTOs:
  - `StatementIdentity` (store, provider account, statement, period, revision)
    with `Validate`, deterministic `Key` (`statement:v1:` + SHA-256) and
    `Equal`; `StatementBatch.Identity()`.
  - `StatementLineIdentity` (store, provider account, statement, period, line,
    line revision) with the same methods; `StatementLine.Identity(statement)`.
    The statement envelope revision is deliberately not a line-identity member:
    a later statement revision that restates the same line revision with
    changed content must conflict, while a genuine correction uses a new line
    revision.
  - `StatementBatch.Canonical()` deterministically orders observations and
    lines and deep-copies nested evidence.
  - `StatementBatch.ReplayFingerprints()` returns the statement revision
    fingerprint plus per-line fingerprints. Preimages use each observation's
    `ReplayFingerprint`, so transport receipt timestamps and input order do not
    change economic identity while any changed amount, charge link, outcome or
    unmatched reason does.
- Exact-granularity claims reuse existing public contracts only:
  `metering.Observation` measures/charges with `metering.Decimal` exact values,
  explicit units/currency, statement-line subjects and observation references.
  No parallel quantity or money type was introduced.
- Host-side domain port in `internal/core/billing`:
  - `TrustedStatementScope` (store, tenant, principal, authorized provider
    accounts) is explicit at the import boundary. Validation requires store
    scope plus tenant or provider-account authority; a store-only scope is
    rejected. `AuthorizesProviderAccount`, `AuthorizedProviderAccounts` and
    `Clone` keep the authorization input caller-owned.
  - `StatementImporter` (scope-aware) and `StatementImportService`.
  - `StatementImportLedger` is the consumer-owned persistence port with exact
    `LookupStatementRevision`, batch `LookupStatementLines` and atomic
    `AppendStatementRevision`; the interface documents replay no-op and
    fail-closed conflict semantics. No adapter, SQL or schema is added.
  - `NormalizedStatement` / `NormalizedStatementLine` are canonical,
    self-validating retention values (identity, fingerprint, trusted scope,
    canonical batch, per-line identity/fingerprint) with a detached `Clone`.
- Scope and provenance fail closed: trusted store/tenant/account must match the
  batch and every observation; observations must use statement-line granularity
  and the authenticated provenance tuple (`statement` origin,
  `statement_importer` acquisition, `verified_statement` authority), so
  request/B-leg evidence cannot be relabeled as an imported statement.
- Replay/conflict semantics: an exact statement revision replay reports every
  line as replayed and writes nothing; a retained statement or line identity
  with different content returns `ErrStatementImportConflict` (typed
  `StatementImportConflictError`, conflicting line IDs in `ImportResult.Rejected`)
  with no durable change; a new statement revision may replay an unchanged line
  revision while accepting or rejecting the rest by line identity. Newly
  retained unmatched lines are reported under `Unmatched`, never assigned to a
  guessed B-leg.

## RED evidence (13.1A)

RED 1 — public identity contract compile failure:

```
go test -count=1 -run 'TestStatement|FuzzStatement' ./pkg/lipsdk/economics
FAIL	github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics [build failed]
pkg\lipsdk\economics\statement_identity_test.go:130:26: batch.Identity undefined (type economics.StatementBatch has no field or method Identity)
pkg\lipsdk\economics\statement_identity_test.go:136:25: line.Identity undefined (type economics.StatementLine has no field or method Identity)
pkg\lipsdk\economics\statement_identity_test.go:148:25: batch.Identity undefined (type economics.StatementBatch has no field or method Identity)
pkg\lipsdk\economics\statement_identity_test.go:164:55: undefined: economics.StatementIdentity
... too many errors
```

RED 2 — public behavioral fixture defect (test corrected, contract unchanged):

```
--- FAIL: TestStatementLineIdentityIsScopedToStatementAndLineRevision
    Received unexpected error: economics: statement subject statement ID mismatch
```

RED 3 — domain contract compile failure:

```
go test -count=1 -run 'TestStatementImport|TestStatement' ./internal/core/billing
internal\core\billing\statement_import_test.go:153:36: undefined: TrustedStatementScope
internal\core\billing\statement_import_test.go:201:88: undefined: NormalizedStatement
internal\core\billing\statement_import_test.go:214:12: undefined: StatementImportConflictError
internal\core\billing\statement_import_test.go:234:53: undefined: StatementImportLedger
internal\core\billing\statement_import_test.go:236:18: undefined: NewStatementImportService
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [build failed]
```

RED 4 — domain behavioral failures exposing two real defects:

```
--- FAIL: TestStatementImportScopeFailsClosed/tenant_scoped_import_without_tenant_claim
    expected: "billing: statement import scope mismatch"
    in chain: "billing: invalid statement import: economics: statement line 0
    observation hash/revision does not match included observation"
--- FAIL: TestStatementImportExactReplayIsIdempotent
    Received unexpected error: billing: statement import revision conflict:
    statement "statement:v1:4457891b..."
--- FAIL: TestStatementImportConflictingStatementRevisionFailsClosed
    expected: []string{"line-1"} actual: []string{"line-1", "line-2"}
--- FAIL: TestStatementImportLineConflictAcrossStatementRevisionsFailsClosed
    Expected error with "billing: statement import revision conflict" in chain but got nil
```

Root causes confirmed by RED 4:

1. The domain fixture varied `ObservedAt` with the transport `ReceivedAt`, so an
   exact receipt replay changed semantic observation identity. The fixture was
   corrected to keep `ObservedAt` stable; the production contract correctly
   ignored only receipt metadata.
2. `StatementLineIdentity` initially embedded the full `StatementIdentity`
   including the statement envelope revision. That made a restatement under a
   new statement revision a new line identity instead of a conflict, defeating
   13.2/13.3 immutability. The line identity now excludes the envelope revision;
   a changed restated claim conflicts unless the line revision is bumped.
3. A statement-envelope conflict rejects the whole envelope, so `Rejected`
   reports every line ID rather than only the first changed line; the test and
   contract documentation were aligned with that fail-closed behavior.

## GREEN evidence

- `go test -count=1 -run 'TestStatementImport|TestStatement|FuzzStatement' ./internal/core/billing ./pkg/lipsdk/economics` passed.
- `go test -count=1 ./pkg/lipsdk/economics/... ./internal/core/billing/...` passed.
- `go test -count=1 ./pkg/lipsdk/... ./internal/core/billing/...` passed (no FAIL in the full run).
- `go test -count=5 -shuffle=on ./pkg/lipsdk/economics ./internal/core/billing` passed; focused `-count=5 -shuffle=on` run also passed.
- `go test -fuzz=FuzzStatementBatchReplayFingerprints -fuzztime=15s -run=^$ ./pkg/lipsdk/economics` passed with 769,167 executions and no failing input.
- `go build ./...` passed; `go vet ./internal/core/billing/... ./pkg/lipsdk/economics/...` passed.
- `gofmt -l internal/core/billing pkg/lipsdk/economics` returned nothing; `git diff --check` returned nothing.

19 focused tests pass (13 domain, 5 public plus the fuzz seed):

```
TestStatementIdentityIsDeterministicAndFieldScoped
TestStatementLineIdentityIsScopedToStatementAndLineRevision
TestStatementBatchCanonicalIsDeterministicAndDetached
TestStatementBatchReplayFingerprintsIgnoreReceiptAndInputOrder
TestStatementBatchReplayFingerprintsRejectInvalidBatches
FuzzStatementBatchReplayFingerprints
TestStatementImportAcceptsIndependentNormalizedFixture
TestStatementImportExactReplayIsIdempotent
TestStatementImportConflictingStatementRevisionFailsClosed
TestStatementImportLineConflictAcrossStatementRevisionsFailsClosed
TestStatementImportScopeFailsClosed
TestStatementImportRejectsMalformedAndUnsupportedClaims
TestStatementImportNormalizedRecordSelfValidates
TestStatementImportNormalizedRecordCloneIsDetached
TestStatementImportDeterministicAcrossInputOrder
TestStatementImportRejectsInvalidConstructionAndContext
TestStatementImportSurfacesLedgerFailure
TestStatementImportSatisfiesPublicStatementImporterSeam
TestStatementImportScopeHelpers
```

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Statement/line/revision/account/period identity contracts | Identity/key/validation table tests, statement and line scoping including line-revision and statement-revision behavior |
| Exact-granularity usage/charge claims with currency/unit, refs, exact decimals | Fixtures use `metering.Observation` measures/charges and `metering.Decimal`; invalid decimal, missing charge and observation-linkage cases are rejected |
| Trusted scope explicit at the port; fail closed | Scope table: foreign store, unauthorized account, tenant mismatch, missing tenant claim, mixed tenants, store-only scope, self-inconsistent batch store |
| Deterministic identity/fingerprint for replay vs conflict | Order/receipt-shuffle equality tests and difference-on-amount/reason tests; exact replay and conflicting statement/line fixtures |
| Independent of request/call evidence; no guessed allocation | Acceptance test asserts no A-leg/request/B-leg/billing-call fields on retained statement evidence; unmatched line is retained as unmatched |
| Reject malformed/overbound/duplicated/unsupported/cross-scope | Malformed table: version, revision, empty claim, duplicate line ID, conflicting observation revision, line/observation period and account mismatch, missing charge, invalid decimal, relabeled provider origin, request-scoped granularity, line bound 4096 |
| Public `StatementImporter` seam preserved | Frozen interface compiled against by a test-only bound adapter; no signature change |
| Bounded identities/counts/payloads | Reuses existing `MaxStatementLines`, `MaxRatingObservations`, `metering` identity bounds and observation validation |

## Explicit exclusions

- No persistence adapter, SQL, migrations or `dbparity` registration; the ledger
  port is unimplemented outside tests (13.1B).
- No statement matching, cost-head transition, journal delta, valuation link,
  worker, HTTP route or runtime composition.
- No vendor parser, provider SDK, prompt/raw-body handling or UI.
- No public interface signature change and no change to Phase 12 evidence or
  review files. No Kiro checkbox, spec-status, commit or PR operation.

## Residual risks and skips

- `internal/archtest` targeted run (`Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core`)
  reproduces the three pre-existing failures and no new rule violation:
  `TestPackageTreeBudgetsExact/internal/infra/runtimebundle` (13131 > 12567),
  `TestBillingFinalConvergenceLOCRatchetActive` (pristine `git archive HEAD` of
  `3fb2d579`: 35615 > ceiling 29760; with this change 36074, so the new domain
  file adds 459 measured LOC and widens the already-active failure), and
  `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi`
  dependency; the new files import only `pkg/lipsdk/economics` and
  `pkg/lipsdk/metering`).
- The fuzz target ran 15 seconds, which is a bound on confidence, not a proof;
  the seed corpus still runs in every normal `go test`.
- 13.1B must implement `StatementImportLedger` with atomic SQLite/PostgreSQL
  behavior, including replay no-op and race-safe conflict enforcement, and
  register the store in `dbparity.DefaultCatalog()` if it adds a durable
  component. The domain port contract is documented but not SQL-certified here.
- `make test-unit`, `make test-db-parity` and `make qa` were not run because
  this slice adds no adapter, schema or runtime composition; affected-package
  and public-SDK suites above are the phase verification.

## Task 13.1B: durable dual-dialect statement import ledger and public adapter

Scope: parent task 13.1 slice 13.1B — durable StatementImportLedger for SQLite
and PostgreSQL plus the public import adapter. No 13.2 matching, cost heads,
journal adjustment, posting or worker behavior was added. Worktree
`go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `3fb2d579`. The 13.1A files and all
other existing work are preserved; no commit, rebase or PR operation was made.

New production files:

- `internal/infra/billingstore/20260928000000_billing_statement_import.go`
- `internal/infra/billingstore/statement_import_store.go`
- `internal/core/billing/statement_import_adapter.go`

New test files:

- `internal/infra/billingstore/statement_import_store_test.go`
- `internal/infra/billingstore/statement_import_postgres_test.go` (`integration` tag)
- `internal/core/billing/statement_import_adapter_test.go`

Modified: `internal/infra/billingstore/20260812000000_billing_baseline.go`
(register migration), `internal/infra/billingstore/store.go`
(`RequiredMigrationNames`, probes, index/trigger/table-fragment lists and
PostgreSQL catalog checks), `internal/testkit/dbparity/catalog.go` (two billing
capabilities).

## Persistence, adapter and transaction design

Additive migration `20260928000000` creates two immutable store-scoped tables:

- `billing_statement_revisions`: `(store_id, statement_key)` unique, identity
  columns (provider account, statement, period, revision, tenant, principal),
  `schema_version`, `fingerprint`, `scope_json` (authorized store/tenant/
  principal/provider accounts), `envelope_json` (canonical
  `economics.StatementBatch` including all observation claims/evidence and each
  observation's received metadata), `received_at_unix`. Indexed by
  account/statement/period/revision and by tenant.
- `billing_statement_lines`: `(store_id, line_key)` unique with a foreign key to
  the parent revision, line identity columns (statement, period, line,
  line revision), `outcome`, `charge_item_id`, `observation_id`/
  `observation_revision`, `fingerprint`, `payload_json`. Indexed by parent
  statement and by account/statement/period/outcome for bounded query shapes.

SQLite and PostgreSQL both enforce immutability with update/delete triggers
(four SQLite triggers; `billing_statement_revisions_immutable` and
`billing_statement_lines_immutable` on PostgreSQL). `VerifySchema` now probes
the tables, migration history, indexes, unique constraints, foreign key and
triggers on both dialects.

`DurableStore` implements `billing.StatementImportLedger`:

- `LookupStatementRevision` / `LookupStatementLines` are exact, bounded,
  index-backed key probes. An invalid identity is rejected before SQL; a
  foreign-store identity returns `ErrEconomicsOutOfScope`; a missing identity is
  not an error.
- `AppendStatementRevision` validates the canonical `billing.NormalizedStatement`
  (which re-runs domain scope and provenance checks) and the store identity
  before any write, then runs one local transaction: `INSERT ... ON CONFLICT DO
  NOTHING` for the revision, read-back verification, then the same for every
  line with per-line fingerprint verification. A skipped revision with an equal
  fingerprint is an exact replay no-op including line verification; a different
  fingerprint returns `billing.StatementImportConflictError`. A conflicting line
  returns the typed conflict with the line IDs and rolls back the whole
  transaction, so a multi-line batch is all-or-nothing and a racing conflict
  leaves no mixed revision/line rows. SQLite contention retries are bounded
  (40 attempts, incremental delay) using the existing busy/deadlock classifier.
- `GetStatementRevision` is a self-validating read: it decodes the retained
  scope and envelope, re-normalizes through `billing.NormalizeStatement`, and
  compares every stored identity/scope/fingerprint/envelope projection and line
  fingerprint. Drifted, incomplete or non-canonical rows fail closed with
  `ErrStatementImportMismatch` instead of being returned as evidence.

Public adapter: `billing.BoundStatementImporter` implements the frozen
`economics.StatementImporter` signature and maps the canonical public batch into
`StatementImportService.Import` with a constructor-bound authenticated
`TrustedStatementScope`. The constructor rejects a nil service or an incomplete
scope and stores a detached scope copy. It contains no SQL, provider branch,
global or persistence type. No runtime or `lipstd` composition was wired: the
design places host opt-in import authorization and the protected
`POST /statement-observations` route in Task 16.2, and stock `lipstd` must not
invent accounts or authorization.

Persistence mapping required by the task: normalized statement envelope →
`envelope_json`; independent immutable lines/claims/evidence → line rows plus
the statement-line observations retained in the canonical envelope with no
request/A-leg/B-leg lineage; revision/fingerprint → `statement_key`, `revision`,
`fingerprint`; trusted scope/provenance → `scope_json`, `tenant_id`,
`principal_id`; received metadata → observation `received_at` inside the
envelope plus `received_at_unix`; outcome → line `outcome`.

## RED evidence (13.1B)

RED 1 — adapter contract compile failure:

```
go test -count=1 -run 'TestBoundStatementImporter' ./internal/core/billing
internal\core\billing\statement_import_adapter_test.go:12:39: undefined: BoundStatementImporter
internal\core\billing\statement_import_adapter_test.go:19:16: undefined: NewBoundStatementImporter
... (all call sites)
FAIL	.../internal/core/billing [build failed]
```

RED 2 — durable ledger compile failure:

```
go test -count=1 -run 'TestStatementImportLedger' ./internal/infra/billingstore
statement_import_store_test.go:20:39: cannot use (*DurableStore)(nil) ... does not implement
  billing.StatementImportLedger (missing method AppendStatementRevision)
statement_import_store_test.go:147:27: store.AppendStatementRevision undefined
statement_import_store_test.go:151:35: store.LookupStatementRevision undefined
statement_import_store_test.go:157:25: store.LookupStatementLines undefined
statement_import_store_test.go:207:23: reopened.GetStatementRevision undefined
... too many errors
FAIL	.../internal/infra/billingstore [build failed]
```

RED 3 — first behavioral run after implementation exposed one fixture-semantics
defect and two test-expectation defects:

```
--- FAIL: TestStatementImportLedgerSQLiteReadRejectsIncompleteOrDriftedRows
    Should be zero, but was 1
--- FAIL: TestStatementImportLedgerSQLitePartialBatchRollsBack
    Expected error with "billing: statement import revision conflict" in chain but got nil
--- FAIL: TestStatementImportLedgerSQLiteReplayConflictAndRestart
    Expected error with "billing: statement import revision conflict" in chain but got nil
--- FAIL: TestStatementImportLedgerSQLiteConcurrentConflictsHaveOneWinner
    expected: 1, actual: 2
```

Root cause: a statement-line identity includes the statement id (13.1A), so a
new statement id is a new claim rather than a restatement; conflict fixtures
must reuse the statement id and bump the statement revision. The raw poisoned
row legitimately counted one revision, and the restart counts were corrected to
the accepted second revision. Fixtures/expectations were corrected; the
production contract was not weakened. A local type/function name collision
(`statementImportPayloads`) was fixed during the same compile cycle.

## GREEN evidence

- `go test -count=1 ./internal/infra/billingstore/...` passed (58.4s).
- `go test -count=1 ./internal/infra/billingstore/... ./internal/core/billing/... ./internal/testkit/dbparity/...` passed.
- `go test -count=5 -shuffle=on -run 'TestStatementImportLedger|TestBoundStatementImporter' ./internal/infra/billingstore ./internal/core/billing` passed.
- Live PostgreSQL direct with `LIP_REQUIRE_POSTGRES=1`:
  `go test -tags=integration -count=1 -run 'TestStatementImportLedgerPostgres' ./internal/infra/billingstore`
  passed in 28.5s (`TestStatementImportLedgerPostgresDirect` 14.5s,
  `TestStatementImportLedgerPostgresConcurrentAtomicity` 12.2s), including
  schema/index/trigger catalog checks, replay/conflict/all-or-nothing,
  concurrent identical imports and cross-revision line races, immutability and
  scope isolation on isolated temporary schemas.
- `make test-db-parity-sqlite` passed for all registered components (billing,
  concurrency authority, continuity, conversation view, control-plane ledger,
  metering journal, secure sessions, terminal work, usage authority).
- `go test -count=1 ./internal/qa` passed (dirty-Go-file gate included).
- `go build ./...` and
  `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...`
  passed; `gofmt -l` returned nothing; `git diff --check` returned nothing.
- Architecture subset
  `go test ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reports only the three pre-existing failures: `runtimebundle` package budget
  (13131 > 12567), `TestBillingCoreStaysProviderAndPersistenceFree` (existing
  `pkg/lipapi` dependency), and `TestBillingFinalConvergenceLOCRatchetActive`
  (already active at checkpoint: pristine `3fb2d579` 35615, 13.1A 36074, this
  change 36558, so 13.1B adds 484 measured LOC).
- Race: `go test -race` cannot build on this Windows host (observed
  `runtime/cgo: ... cgo.exe: exit status 2` before test execution), matching the
  previously recorded Windows race-host limitation. Concurrency evidence is
  provided by the SQLite four-writer/racing-conflict tests and the live
  PostgreSQL four-writer and cross-revision race tests.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Consumer-side ledger in billingstore for SQLite and PostgreSQL | `statement_import_store.go`; SQLite package tests plus live isolated-schema PostgreSQL tests |
| Persist envelope, independent immutable lines/claims/evidence, revision/fingerprint, scope/provenance, received metadata, outcome | Revision/line tables with identity, fingerprint, scope JSON, received timestamp, per-line outcome/charge/observation projections and canonical envelope; immutability triggers verified by `VerifySchema` and direct update/delete rejection tests |
| Same revision + fingerprints replay no-op with stable result; changed revision/identity conflict with zero partial insert | Exact replay tests, `GetStatementRevision` equality, all-or-nothing rollback tests, typed `billing.ErrStatementImportConflict` |
| Concurrent identical imports produce one durable effect; racing conflicts leave no mixed state | SQLite and PostgreSQL concurrency tests (4 identical writers; conflicting statement revisions over one line identity) |
| Transactional store/tenant/account/scope validation and multi-line all-or-nothing | Domain scope validation reused through `NormalizedStatement.Validate`, store identity check, rollback tests; adapter scope-boundary and 13.1A scope table |
| Public adapter over the existing `economics.StatementImporter` signature without SQL | `BoundStatementImporter` constructor and public-interface tests, including durable end-to-end import/replay/conflict through the public seam |
| Additive migrations, parity and indexes; dbparity registration | New migration only; `RequiredMigrationNames`, `VerifySchema` SQLite/PostgreSQL checks, two dbparity billing capabilities and passing catalog/parity gates |
| RED first: replay/conflict/restart, rollback, evidence independence, scope rejection, bounds, races, SQLite and live PG | RED output above; bounds/tamper/scope/evidence tests on SQLite and live PostgreSQL |

## Explicit exclusions

- No statement matching (13.2), no cost-head transition, journal adjustment,
  valuation link, rating/posting, or worker (13.3/13.4).
- No HTTP route, query surface, runtime composition or `lipstd` wiring; the
  adapter is constructed by the future host import path (16.2) from
  authenticated context.
- No vendor parser, provider SDK, prompt/raw-body handling, UI, schema change to
  existing billing tables, or change to Phase 12 evidence/review. No Kiro
  checkbox, spec-status, commit or PR operation.

## Residual risks and skips

- PostgreSQL verification ran against a configured remote DSN using isolated
  temporary schemas; the DSN value is intentionally not recorded. No
  transaction-pooler topology test was added because the ledger uses no session
  state, temporary tables or session-pinned prepared statements.
- The billing LOC ratchet was already failing at the checkpoint and this change
  adds 484 measured LOC (pristine `3fb2d579` 35615; 13.1A 36074; now 36558
  against the frozen 29760 ceiling). It is recorded rather than hidden; no
  budget or baseline artifact was edited.
- `-race` is unavailable on this Windows host (cgo build failure); concurrency
  is exercised deterministically by the SQLite and PostgreSQL race tests.
- 13.2 must build matching on the retained line rows; the schema exposes
  account/statement/period/line identity plus charge and observation
  projections but deliberately contains no allocation or posting state.

## Task 13.2: statement matching without guessed per-request allocation

Scope: parent task 13.2 - pure billing-domain statement matching over
normalized imported statements (13.1A) and an explicitly eligible retained
charge/reconciliation evidence set. Exact explicit charge-ID identity or
complete compatible account/period/SKU aggregate coverage only. No persistence
adapter, migration, SQL, vendor parser, UI, posting, cost head, journal delta,
valuation link, worker or runtime composition was added; 13.3 posting and
selected-cost heads are untouched. Worktree
`go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `3fb2d579`. All 13.1A/13.1B files and
other existing work are preserved; no commit, rebase, merge or PR operation was
made, and no Kiro task checkbox or status was changed.

New production files:

- `internal/core/billing/statement_match_contract.go` (372 lines)
- `internal/core/billing/statement_match.go` (603 lines)

New test file:

- `internal/core/billing/statement_match_test.go` (1050 lines, 14 top-level
  tests plus 41 subtests plus one fuzz target with seeds)

## Matcher identity and coverage design

`MatchStatements(statements []NormalizedStatement, evidence []StatementChargeEvidence)
(StatementMatchSet, error)` is a pure, deterministic function. It contains no
SQL, provider SDK, vendor parser, UI, posting, head/journal write or worker
behavior, and it has no context or I/O. Inputs are already-normalized 13.1
statements plus `StatementChargeEvidence` entries: one exact
`metering.ChargeRef`, an explicit eligibility state
(`eligible`/`ineligible`), the retained reconciliation identity, tenant/store/
provider-account/period scope, charge kind, optional `metering.ComponentKey`
SKU, currency, payer and the retained charge's own coverage graph.

Matching rules:

- A statement line whose imported outcome is `unmatched` stays unmatched and
  keeps its declared unmatched reason. No evidence is attached.
- A matched line first attempts explicit charge-ID matching by
  `ChargeItemID`. Exactly one retained candidate must exist; zero candidates
  with a non-aggregate claim is `unmatched`/`charge_not_found`; more than one
  candidate is `conflict`/`cross_revision_ambiguity` because a statement line
  carries no evidence revision and choosing one would be guessing.
- A genuine aggregate claim without an SKU component is an account total and
  stays `unmatched`/`account_scoped_total`. It is never proportionally or
  heuristically split, and no allocation record is invented.
- An aggregate claim with an SKU component matches only complete compatible
  account/period/SKU coverage. The eligible evidence group is the retained
  charges sharing the statement store, provider account, period and exact
  component. Explicit `Covers` references on the statement claim resolve
  exactly; a dangling reference is `partial`/`dangling_coverage_ref`, an
  ineligible member is `partial`/`evidence_ineligible`, and an uncovered
  eligible member is `partial`/`partial_sku_coverage`. Without explicit
  coverage references the complete eligible group is covered. Many charges
  produce one link that retains every covered charge and evidence ID.
- Scope and claim compatibility fails closed as `incomparable` with distinct
  reasons: `store_mismatch`, `tenant_mismatch`, `account_mismatch`,
  `period_mismatch`, `currency_mismatch`, `unit_mismatch`, `sku_mismatch`,
  `granularity_mismatch`. Only an exact match in every checked dimension can
  become `matched`.
- Duplicate/overlapping coverage is resolved over every tentative link in the
  batch. Inclusive retained coverage edges are expanded per link: a charge
  included more than once by one link (duplicate parent+child inclusion), or
  by more than one link (overlapping/duplicate coverage), makes every
  participating link a `conflict`; no arbitrary winner is chosen. A direct
  charge collision reports `overlapping_coverage` and outranks an
  internal/implicit `duplicate_parent_child` reason, deterministically.
- Duplicate exact evidence entries, duplicate statement revisions, more than
  one revision of one statement identity in one batch
  (`StatementMatchAmbiguityError`, `ErrStatementMatchAmbiguous`), malformed
  evidence and count/coverage bound violations all fail closed with
  `ErrStatementMatchInvalid` before any result is produced.

Retention and identity:

- `StatementMatchStatus` (`matched`, `partial`, `incomparable`, `conflict`,
  `unmatched`) and `StatementMatchReason` (19 documented values) are typed,
  closed vocabularies with `IsKnown`/`Validate`; every line keeps its status,
  reason, line identity and declared unmatched reason.
- `StatementCoverageLink` is immutable: statement key/ID/revision, match kind,
  all covered statement line keys/IDs, all covered `metering.ChargeRef`s and
  all covered retained reconciliation/evidence IDs. `Key()` is a deterministic
  `statement-coverage-link:v1:` SHA-256 identity, with `Equal` and a detached
  `Clone`. Links carry no request, A-leg, B-leg, attempt, billing-call or
  allocation field, and a reflection test asserts that schema absence.
- `MatchStatements` sorts statements by identity, lines in canonical 13.1
  order, and links by coverage key, so results are byte-identical across input
  permutations. Indexes (`byRef`, `byChargeItem`, `byComponent`, inclusive
  edges, per-statement observation index) keep lookups bounded and avoid
  quadratic scans in the common path.

## RED evidence (13.2)

RED 1 - fixture contract compile failure (full output):

```
# github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [github.com/matdev83/go-llm-interactive-proxy/internal/core/billing.test]
internal\core\billing\statement_match_test.go:247:62: undefined: StatementChargeEvidence
internal\core\billing\statement_match_test.go:279:11: undefined: StatementEvidenceEligible
internal\core\billing\statement_match_test.go:281:11: undefined: StatementEvidenceIneligible
internal\core\billing\statement_match_test.go:283:9: undefined: StatementChargeEvidence
internal\core\billing\statement_match_test.go:302:83: undefined: StatementChargeEvidence
internal\core\billing\statement_match_test.go:302:108: undefined: StatementMatchSet
internal\core\billing\statement_match_test.go:304:14: undefined: MatchStatements
internal\core\billing\statement_match_test.go:317:16: undefined: StatementChargeEvidence
internal\core\billing\statement_match_test.go:329:19: undefined: StatementMatchStatusMatched
internal\core\billing\statement_match_test.go:330:19: undefined: StatementMatchReasonExplicitCharge
internal\core\billing\statement_match_test.go:330:19: too many errors
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [build failed]
FAIL
```

RED 2 - mutation verification that the fixtures have teeth. Five temporary
production defects were injected one at a time and each was caught by the
focused suite, then the implementation was restored:

- Disabling cross-link conflict resolution made
  `TestStatementMatchRejectsDuplicateParentChildInclusion` and
  `TestStatementMatchRejectsOverlappingAndDuplicateCoverage` fail with
  `expected: "conflict" actual: "matched"`.
- Disabling explicit scope/claim compatibility made every
  `TestStatementMatchScopeAndClaimMismatchesAreIncomparable` subtest fail with
  `expected: "incomparable" actual: "matched"` (and the ineligible case
  `expected: "unmatched"`).
- Removing the account-total guard made the account-scoped fixture fail with
  `expected: "account_scoped_total" actual: "charge_not_found"`.
- Removing duplicate-evidence detection made
  `duplicate_evidence_entries_are_rejected` fail with
  `Expected error with "billing: invalid statement match input" in chain but got nil`.
- Accepting incomplete aggregate coverage made
  `missing_coverage_reference_is_partial` fail with
  `expected: "partial" actual: "matched"`.

RED 3 - a genuine determinism defect found by author review and captured with
a new fixture first. Conflict reason selection originally iterated a Go map,
so a link that had both a direct charge collision and an internal parent+child
duplicate returned `overlapping_coverage` or `duplicate_parent_child`
depending on map order:

```
go test -count=20 -run 'TestStatementMatchRejectsDuplicateParentChildInclusion' ./internal/core/billing
--- FAIL: TestStatementMatchRejectsDuplicateParentChildInclusion (0.01s)
    --- FAIL: .../direct_overlap_outranks_internal_parent-child_inclusion_deterministically
        expected: "overlapping_coverage"
        actual  : "duplicate_parent_child"
```

Root cause: map-iteration-order reason selection in
`resolveStatementMatchConflicts`. Fix: collect `overlapping`/`duplicate` flags
order-independently and give a direct collision explicit priority. The
regression fixture passes 50 consecutive runs
(`go test -count=50 -run 'TestStatementMatchRejectsDuplicateParentChildInclusion|TestStatementMatchRejectsOverlappingAndDuplicateCoverage'`).

## GREEN evidence

- `go test -count=1 -run 'TestStatementMatch|TestStatementCoverageLink' ./internal/core/billing` passed (14 top-level tests, 41 subtests).
- `go test -count=5 -shuffle=on -run 'TestStatementMatch|TestStatementCoverageLink|TestStatementImport|TestStatement|TestBoundStatementImporter' ./internal/core/billing ./pkg/lipsdk/economics` passed.
- `go test -count=5 -shuffle=on ./internal/core/billing` passed (1.65s).
- `go test -count=1 ./internal/core/billing/... ./pkg/lipsdk/economics/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...` passed: billing 0.47s, economics 0.62s, billingstore 62.3s, dbparity 0.62s, dbparity/cmd 4.3s.
- `go test -fuzz=FuzzStatementMatchOrderIndependence -fuzztime=15s -run=^$ ./internal/core/billing` passed with 11,661 executions and no failing input (a prior 20s run passed 17,478 executions); seed permutations run in every normal `go test`.
- `go build ./...`, `go vet ./internal/core/billing/... ./pkg/lipsdk/economics/...`, `gofmt -l internal/core/billing pkg/lipsdk/economics` and `git diff --check` all clean.
- `go test -count=1 ./internal/qa` passed (dirty-Go-file gate included, 6.2s).
- Architecture subset `go test ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'` reports only the three pre-existing failures: `runtimebundle` package budget (13131 > 12567), `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi` dependency) and `TestBillingFinalConvergenceLOCRatchetActive` (pristine `3fb2d579` 35615, 13.1A 36074, 13.1B 36558, this change 37538, so 13.2 adds 980 measured LOC to the already-active failure).
- `-race` remains unavailable on this Windows host (cgo build failure recorded in earlier phases). The matcher is pure, allocates no goroutines and owns no cancellation, so concurrency evidence is not applicable.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Explicit exact charge-ID match only | `TestStatementMatchExplicitChargeEvidence`; single-candidate lookup, exact ref retained in the link |
| Complete compatible aggregate account/period/SKU coverage; many-charge coverage | `TestStatementMatchCompleteAggregateSKUCoverage` with explicit coverage references and complete-evidence-set paths; links retain all three charges and evidence IDs |
| Unmatched lines and account totals preserved, never allocated | `TestStatementMatchRetainsUnmatchedAndAccountScopedLines`; unmatched reason retained, account total stays `account_scoped_total` with no link |
| No timestamp-nearest/proportional/heuristic/guessed allocation | `TestStatementMatchNeverGuessesNearestChargeOrAllocation`; `TestStatementMatchRetainsNoRequestOrAllocationLineage` reflection over every result type |
| Partial aggregate is never matched | `TestStatementMatchPartialAggregateIsNotMatched` (missing reference, ineligible member, dangling reference) |
| Duplicate parent+child inclusion rejected | `TestStatementMatchRejectsDuplicateParentChildInclusion` (single link and separate links) plus direct-overlap priority fixture |
| Overlapping/duplicate coverage rejected | `TestStatementMatchRejectsOverlappingAndDuplicateCoverage`; all participating links become conflict; duplicate evidence/statement revisions fail closed |
| Incompatible account/tenant/store/currency/unit/SKU/period/granularity | `TestStatementMatchScopeAndClaimMismatchesAreIncomparable` table (9 cases with distinct typed reasons) |
| Cross-revision ambiguity rejected | `TestStatementMatchRejectsAmbiguousRevisions`: mixed statement revisions raise `StatementMatchAmbiguityError`; multiple evidence revisions are line conflicts for explicit and aggregate matching |
| Deterministic and order independent | `TestStatementMatchIsDeterministicAcrossInputOrder` (rotations and reversal) and `FuzzStatementMatchOrderIndependence` |
| Boundedness and malformed input fail closed | `TestStatementMatchRejectsMalformedAndOverboundInput` (tamper, 9 malformed evidence cases, evidence bound, statement bound) |
| Typed closed status/reason/kind/state vocabulary | `TestStatementMatchVocabularyIsClosed` |
| Immutable coverage identity retaining all lines and charge/evidence IDs | `TestStatementCoverageLinkIdentityIsStableAndDetached`; `Key`/`Equal`/`Clone` and evidence-ID sensitivity |

## Explicit exclusions

- No persistence, SQL, migration, `dbparity` registration or store adapter;
  matching is pure and the evidence set is supplied by the future caller
  (13.4 worker / host read path).
- No selected-cost head, valuation link, balanced journal delta, posting,
  amount comparison, FX comparability or worker behavior (13.3/13.4). Matching
  performs no arithmetic at all: amount sums and deltas are not this task.
- No vendor invoice parser, provider SDK, prompt/raw-body handling or UI.
- No HTTP route, runtime composition or `lipstd` wiring.
- No public `pkg/lipsdk` signature change, no change to Phase 12 evidence or
  review files, and no Kiro checkbox, spec-status, commit or PR operation.

## Residual risks and skips

- Aggregate completeness is evaluated against the caller-supplied eligible
  evidence set. The contract requires the caller to supply the complete
  retained eligible evidence for the statement store/account/period scope; the
  matcher cannot discover missing retained charges without persistence.
- Matching is identity/coverage only. Whether covered amounts equal the
  statement aggregate, currency conversion and downward corrections remain
  13.3 concerns and are deliberately not evaluated here.
- The billing LOC ratchet was already failing at the checkpoint; this change
  adds 980 measured LOC (pristine `3fb2d579` 35615, 13.1A 36074, 13.1B 36558,
  now 37538 against the frozen 29760 ceiling). It is recorded rather than
  hidden; no budget or baseline artifact was edited.
- The fuzz target ran 15 seconds, which is a confidence bound, not a proof;
  its seed corpus still runs in every normal `go test`.
- `-race` is unavailable on this Windows host (cgo build failure); the matcher
  owns no goroutines, so this is a host limitation rather than an untested
  concurrency surface.

## Task 13.3: selected-cost heads and balanced delta corrections (13.3A)

Scope: parent task 13.3 slice 13.3A - pure selected-cost head transition and
balanced delta-correction domain. No SQL adapter, store transaction, migration,
`dbparity` registration, statement matching, worker queue, customer rebill
policy, runtime composition or HTTP route was added; the durable
`ApplyProviderCostRevision` writer and all 13.1A/13.1B/13.2 files were left
untouched. Worktree `go-llm-interactive-proxy-feat-b-leg-usage-economics`;
branch `feat/b-leg-usage-economics`; checkpoint `3fb2d579`. No commit, rebase,
merge or PR operation was made, and no Kiro task checkbox or status was changed.

New production files:

- `internal/core/billing/selected_cost_head.go` (717 lines) - value contracts.
- `internal/core/billing/selected_cost_head_transition.go` (336 lines) - pure
  transition planner.

New test file:

- `internal/core/billing/selected_cost_head_transition_test.go` (951 lines, 21
  top-level tests plus 31 subtests).

## Transition and accounting design

`PlanSelectedCostHeadTransition(SelectedCostHeadTransitionInput) (SelectedCostHeadTransition, error)`
is a pure, deterministic function. It contains no SQL, context, I/O, provider
SDK or store type, and it never mutates its input.

Input planes:

- `SelectedCostHead` is the current selected/posted valuation pointer: account,
  call, head key, B-leg/provider-charge subject, CAS `Version`, the frozen
  `Selected` valuation (nil until the first posting) and the correction-chain
  transaction references. Version zero and a nil selected valuation are
  required together.
- `SelectedCostHeadExpectation` is the caller's compare-and-swap read: the head
  `Version` and the previously posted `Previous` valuation (its identity,
  revision, selection plane, posted/native amounts and frozen FX basis).
- `SelectedCostValuation` is one frozen Phase 12 selection outcome on a durable
  valuation identity (`ValuationID`, `Revision`, `InputSetHash`). It retains the
  selection status/reason/basis/provenance, the posted-currency exact amount,
  the native exact amount and the explicit `OperatorCostFXBasis` when a frozen
  conversion produced the posted amount. `NewSelectedCostValuation` bridges the
  existing `OperatorCostSelectionResult` without re-rating.

Comparability rule (C5/13.3). Monetary delta is `new selected - previously
posted` only when both valuations have no FX and share the same posted/native
currency, or when both carry the exact same explicit frozen FX basis (same id,
version, direction and exact rate material). In the FX case the delta is
computed in the frozen basis target currency; native amounts stay retained.
Any other combination - 10 USD to 8 EUR without a shared frozen basis, a
changed rate under the same basis identity, a changed basis id, or a basis on
only one side - is `pending`/`incomparable` with reason
`posted_currency_mismatch` or `frozen_fx_basis_mismatch` and performs no
valuation link insertion, journal delta or head transition.

Selection gate. Only `final` and explicitly authorized `known_zero` selected
valuations can post or advance the head. `provisional`, `unknown`, `not
operator payable`, `incomparable` and `conflict` selections stay `pending` with
a distinct typed reason; attempted work without accepted payable evidence is
`missing_attempted_usage` and never becomes a known zero.

Delta and journal rule. The signed delta is computed with exact rational
arithmetic from the posted exact amounts. An upward delta debits
`inference_provider_cogs` and credits `provider_payable_clearing`; a downward
delta reverses those sides with the positive magnitude, so an invalid negative
gross journal amount is never emitted. `BalancedJournalIntent.Validate` proves
two-sided debit/credit balance, matching currency and strictly positive gross
amounts. Exact deltas that cannot be represented as checked integer ledger
nanos fail closed with `ErrSelectedCostHeadLedgerPrecision`; the planner never
rounds. A zero delta is a `no_op`: the head advances to the new selected
revision with an empty (no journal row) intent.

CAS, replay and conflict. A durable head that already carries exactly the new
frozen selected valuation returns `replay`/`already_applied` with a replayed
empty intent and zero effects, even when the caller's expected version is older
(crash retry). A caller version that does not match the durable head returns
`stale`/`stale_head_version`; a same-version expectation whose previous
valuation identity differs returns `conflict`/`head_identity_conflict`; a new
selected revision below the current one returns `stale`/`stale_revision`; the
same revision with a different immutable payload returns
`conflict`/`revision_conflict`. All zero-effect results keep `Link`, `Journal`,
`NextHead` and `Delta` absent and report `HasEffects() == false`.

Identities. `OperationKey` is
`selected-cost-adjustment:v1:` + SHA-256 over the economic charge identity,
both selected valuation identities, the delta currency or frozen FX basis, and
the adjustment revision (the new selected revision). `Fingerprint` is
`selected-cost-adjustment-fp:v1:` + SHA-256 over the full semantic payload
including exact amounts and delta, so a different payload under the same stable
key is detectable. `SelectedCostValuationLink.Key()` is
`selected-cost-link:v1:` + SHA-256 over the same comparison members. The result
keeps the selection plane (`SelectionStatus`/`SelectionReason`), the comparison
plane (`Comparison`) and the posting plane (`Posting`) separate.

## RED evidence (13.3A)

RED 1 - domain contract compile failure:

```
# github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [github.com/matdev83/go-llm-interactive-proxy/internal/core/billing.test]
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [build failed]
internal\core\billing\selected_cost_head_transition_test.go:53:75: undefined: SelectedCostValuationRef
internal\core\billing\selected_cost_head_transition_test.go:74:50: undefined: SelectedCostValuationRef
internal\core\billing\selected_cost_head_transition_test.go:74:218: undefined: SelectedCostValuation
internal\core\billing\selected_cost_head_transition_test.go:81:53: undefined: SelectedCostValuationRef
internal\core\billing\selected_cost_head_transition_test.go:81:94: undefined: SelectedCostValuation
internal\core\billing\selected_cost_head_transition_test.go:91:67: undefined: SelectedCostValuation
internal\core\billing\selected_cost_head_transition_test.go:91:90: undefined: SelectedCostHead
internal\core\billing\selected_cost_head_transition_test.go:100:50: undefined: SelectedCostHead
internal\core\billing\selected_cost_head_transition_test.go:100:77: undefined: SelectedCostHeadExpectation
internal\core\billing\selected_cost_head_transition_test.go:100:115: undefined: SelectedCostValuation
internal\core\billing\selected_cost_head_transition_test.go:100:115: too many errors
FAIL
```

RED 2 - first implementation attempt exposed one real implementation defect and
one contract clarification, plus two fixture defects that were corrected without
changing the contract:

```
--- FAIL: TestSelectedCostHeadIdentityAndOrderAreStable (0.00s)
    panic: runtime error: invalid memory address or nil pointer dereference
    (debug probe: status="conflict" reason="head_identity_conflict")
--- FAIL: TestSelectedCostHeadSubNanoDeltaFailsClosed
    An error is expected but got nil.
--- FAIL: TestSelectedCostHeadInitialPostingPostsExactSelectedAmount
    expected: "not_evaluated" actual: "comparable"
    expected: "inference_provider_cogs" actual: "provider-cogs-head-13-3"
--- FAIL: TestSelectedCostHeadChangedFrozenFXBasisIsRejected
    ParseDecimal("6.153846153846153846153846153846"): scale 30 exceeds 18
```

Root causes: `sameOperatorCostFX(nil, nil)` is false because it models optional
candidate bases, so identity comparison rejected every previous valuation
without FX - a real defect fixed with an explicit both-nil equality branch in
`SelectedCostValuation.IdentityEqual`; the initial posting reported
`comparable` where the contract requires `not_evaluated` for an absent previous
valuation; and two fixtures were wrong (CorrectionGroupID compared against a
ledger account, oversized decimal scale in one FX fixture). The contract was
unchanged.

RED 3 - mutation verification that the fixtures have teeth. Four temporary
production defects were injected one at a time, each caught by the focused
suite, then restored (restored file SHA-256 equal to the pre-mutation backup):

- Delta sign flip (`prior - next`): `delta = -2/0, want 2/0` and
  `delta = 2/0, want -2/0` in the upward/downward fixtures.
- Dropping the exact frozen FX basis identity check:
  `expected: "pending" actual: "applied"` and
  `expected: "frozen_fx_basis_mismatch" actual: "frozen_fx_basis"`.
- Dropping the downward debit/credit reversal:
  `expected: "provider_payable_clearing" actual: "inference_provider_cogs"`.
- Disabling replay identity detection: `expected: "replay" actual: "stale"`.

## GREEN evidence

- `go test -count=1 -run 'TestSelectedCost' ./internal/core/billing` passed (21
  top-level tests, 31 subtests, 0.077s).
- `go test -count=5 -shuffle=on -run 'TestSelectedCost' ./internal/core/billing`
  passed (0.085s).
- `go test -count=5 -shuffle=on ./internal/core/billing` passed (1.613s).
- `go test -count=1 ./internal/core/billing/... ./pkg/lipsdk/economics/... ./internal/testkit/dbparity/...`
  passed: billing 0.464s, economics 0.618s, dbparity 0.591s, dbparity/cmd
  3.409s.
- `go test -count=1 ./internal/infra/billingstore/...` passed (60.160s); the
  existing durable provider-cost writer is unaffected.
- `go build ./...`, `go vet ./internal/core/billing/...`,
  `gofmt -l internal/core/billing` and `git diff --check` all clean.
- `go test -count=1 ./internal/qa` passed (7.586s, dirty-Go-file gate
  included).
- Architecture subset `go test ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reports only the three pre-existing failures:
  `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi`
  dependency), `runtimebundle` package budget (13131 > 12567) and
  `TestBillingFinalConvergenceLOCRatchetActive` (13.1A 36074, 13.1B 36558,
  13.2 37538, this change 38591, so 13.3A adds 1053 measured LOC to the
  already-active failure against the frozen 29760 ceiling).
- `-race` remains unavailable on this Windows host (cgo build failure recorded
  in earlier phases). The planner is pure, allocates no goroutines and owns no
  cancellation, so concurrency evidence is not applicable.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Clean initial posting in the selected native currency | `TestSelectedCostHeadInitialPostingPostsExactSelectedAmount`; +10 USD delta, version-1 CAS successor |
| 10 USD to 8 USD posts exact -2 USD with reversed debit/credit sides | `TestSelectedCostHeadDownwardCorrectionUsesReversalSides`; both entries positive, `BalancedJournalIntent.Validate` |
| Upward correction posts the positive delta | `TestSelectedCostHeadUpwardCorrectionPostsPositiveDelta` |
| Zero delta is a no-op head advance without a journal row | `TestSelectedCostHeadZeroDeltaAdvancesHeadWithoutJournal` |
| 10 USD to 8 EUR without frozen FX stays pending with no link/delta/head transition | `TestSelectedCostHeadCurrencyMismatchWithoutFrozenFXStaysPending` |
| Same explicit frozen FX basis permits the exact converted delta | `TestSelectedCostHeadSharedFrozenFXComputesExactDelta`; native EUR amounts retained |
| Changed rate, changed basis id or missing basis is rejected | `TestSelectedCostHeadChangedFrozenFXBasisIsRejected` (three cases) |
| Unknown/provisional/missing-attempt/non-operator selections fail closed | `TestSelectedCostHeadMissingAndProvisionalSelectionsFailClosed` (six cases) |
| Authorized known-zero payer correction reverses posted COGS | `TestSelectedCostHeadKnownZeroCorrectionReversesPostedCOGS` |
| Stale CAS version and late revision have zero effects | `TestSelectedCostHeadStaleExpectedVersionHasZeroEffects`, `TestSelectedCostHeadStaleRevisionHasZeroEffects` |
| Same-version identity or revision conflict has zero effects | `TestSelectedCostHeadIdentityConflictHasZeroEffects`, `TestSelectedCostHeadRevisionConflictHasZeroEffects` |
| Exact replay returns the same identity and an empty replayed intent | `TestSelectedCostHeadExactReplayReturnsNoNewIntent` |
| Stable operation key with amount-sensitive fingerprint | `TestSelectedCostHeadFingerprintIsPayloadSensitive` |
| Deterministic identities, detached results, order-independent valuation identity | `TestSelectedCostHeadIdentityAndOrderAreStable` |
| Negative/unbalanced/mismatched journal intents fail closed | `TestSelectedCostHeadJournalIntentRejectsInvalidGrossAmounts` |
| Sub-nano exact delta fails closed instead of rounding | `TestSelectedCostHeadSubNanoDeltaFailsClosed` |
| Malformed head/expectation/valuation/FX input fails closed | `TestSelectedCostHeadRejectsMalformedInput` (eleven cases) |
| Inconsistent Phase 12 selection cannot become a frozen valuation | `TestSelectedCostValuationConstructorRejectsInconsistentSelection` (six cases) |
| Closed typed status/reason/comparison/posting vocabulary | `TestSelectedCostHeadVocabularyIsClosed` |

## Explicit exclusions

- No SQL adapter, store transaction, schema/migration or `dbparity`
  registration; the durable writer still owns `ApplyProviderCostRevision` and
  the next slice (13.3B) must wire this planner into that transaction
  atomically with the head CAS, valuation link and journal delta.
- No statement matching, import ledger change, worker queue, retry/fence
  scheduling, customer rebill policy, report/query route or runtime
  composition.
- No change to any existing production file, Phase 12 evidence/review file, or
  Kiro task/checkbox/status.
- No commit, rebase, merge, push or PR operation.

## Residual risks and skips

- Provisional selections deliberately do not post or advance the head in
  13.3A; provisional accrual, if a later policy requires it, belongs to the
  13.3B/13.4 posting and worker slices.
- The billing LOC ratchet was already failing at the checkpoint; 13.3A adds
  1053 measured LOC (13.2 37538, now 38591 against the frozen 29760 ceiling).
  It is recorded rather than hidden; no budget or baseline artifact was
  edited.
- The planner proves comparability and exact arithmetic but does not validate
  that a frozen FX rate numerically reconciles the native and posted amounts;
  frozen valuation material is authoritative by contract and 13.3B persists it
  as retained rate material.
- `-race` remains unavailable on this Windows host; the planner owns no
  goroutines, so this is a host limitation rather than an untested concurrency
  surface.

## Task 13.3: selected-cost heads and balanced delta corrections (13.3B)

Scope: parent task 13.3 slice 13.3B - transactional selected-cost head CAS plus
the immutable adjustment/valuation-link and balanced journal adapter for SQLite
and PostgreSQL. The pure 13.3A planner is composed unchanged. No worker queue,
economic-policy change, customer rebill, statement matching, runtime
composition, HTTP route or Phase 12 evidence/review edit was made. Worktree
`go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `3fb2d579` with the 13.1-13.3A dirty
files preserved. No commit, rebase, merge or PR operation was made, and no Kiro
task checkbox or status was changed.

New production files:

- `internal/core/billing/selected_cost_adjustment.go` (113 lines) - SQL-free
  input/result/port contracts and normalization.
- `internal/infra/billingstore/20260929000000_billing_selected_cost_adjustments.go`
  (154 lines) - additive dual-dialect migration.
- `internal/infra/billingstore/selected_cost_adjustment_store.go` (574 lines) -
  the durable compare-and-swap adapter.

New test files:

- `internal/core/billing/selected_cost_adjustment_test.go` (86 lines).
- `internal/infra/billingstore/selected_cost_adjustment_store_test.go` (809
  lines, 11 top-level tests plus 8 subtests).
- `internal/infra/billingstore/selected_cost_adjustment_postgres_test.go` (200
  lines, 2 top-level tests plus 4 subtests, `integration` build tag).

Modified production files:

- `internal/infra/billingstore/20260812000000_billing_baseline.go` - register
  the new migration.
- `internal/infra/billingstore/store.go` - `RequiredMigrationNames`,
  `VerifySchema` SQLite fragments/indexes/triggers and PostgreSQL catalog
  checks for the new table and the extended head columns.
- `internal/infra/billingstore/provider_cost_revision_store.go` - the existing
  provider-cost writer now writes the derived exact selected identity into the
  same head row, so both writers keep one coherent head identity; the head
  select/row gained the additive columns.
- `internal/testkit/dbparity/catalog.go` - one `Common` capability
  (`selected-cost-adjustment-cas-link-journal-replay`) and one `PostgresDirect`
  capability (`postgres-direct-selected-cost-adjustment-atomicity`).

## Transaction and schema design

`DurableStore.ApplySelectedCostAdjustment(SelectedCostAdjustmentInput)
(SelectedCostAdjustmentResult, error)` is the driven adapter. In one local
transaction it:

1. begins a transaction, loads the account for its identity/existence snapshot
   without locking or mutating it, and loads the selected-cost head row with
   `FOR UPDATE` on PostgreSQL (SQLite relies on its writer transaction plus the
   version CAS);
2. rebuilds `billing.SelectedCostHead` from the durable row - or from the input
   identity at version zero - and invokes the pure
   `PlanSelectedCostHeadTransition`; the durable head subject must match the
   input subject or the adapter fails closed with a typed conflict;
3. for `applied`/`no_op`: appends the balanced journal delta (if any),
   inserts the immutable adjustment row carrying the valuation link, and
   compares-and-swaps `head_version`/`fence` while writing the exact successor
   identity; any fault rolls the whole transaction back;
4. for `replay`: returns the original stable operation identity from the
   immutable adjustment row (operation key, link key, fingerprint, delta,
   journal transaction); a legacy head with no adjustment row is only a benign
   replay when the caller CAS read exactly matches the durable head;
5. for `pending`/`stale`/`conflict`: returns the typed zero-effect plan and
   rolls the read-only transaction back.

The additive migration reuses the existing `billing_provider_cost_heads` table
and adds the exact 13.3A identity: `valuation_revision`, `selection_status`,
`selection_reason`, `selection_basis`, `selection_provenance`,
`posted_amount_json` (canonical exact amount), `native_amount_json`, `fx_json`
(explicit frozen FX basis and rate material) and `posting_state`. Legacy rows
keep defaults and are read through their integer nanos and a derived
`MonetaryExactAmount`; the reader self-validates that the exact amount
reconciles with the retained nanos. New immutable rows live in
`billing_selected_cost_adjustments` with unique
`(store_id, operation_key)` and `(store_id, link_key)` indexes, a head scope
index, an account foreign key, and SQLite/PostgreSQL immutability triggers. The
down function is a no-op like every existing billing migration, so it destroys
no retained financial facts.

Accounting semantics follow the planner exactly: initial/upward post COGS
debit and payable-clearing credit; a downward 10 USD to 8 USD correction posts
one positive 2 USD debit to `provider_payable_clearing` and credit to
`inference_provider_cogs`; zero delta advances the head and persists the link
with no journal row; 10 USD to 8 EUR without a shared explicit frozen FX basis
persists nothing and does not advance the head; the same frozen basis material
posts the converted delta in the target currency and retains native amounts;
no path locks or updates a customer balance.

## RED evidence (13.3B)

RED 1 - the store contract did not exist; the new tests failed to compile in
both packages exactly on the new boundary:

```
# github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [github.com/matdev83/go-llm-interactive-proxy/internal/core/billing.test]
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [build failed]
internal\core\billing\selected_cost_adjustment_test.go:23:12: undefined: SelectedCostAdjustmentInput
internal\core\billing\selected_cost_adjustment_test.go:41:29: undefined: SelectedCostAdjustmentInput
...
# github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore [github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore.test]
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore [build failed]
internal\infra\billingstore\selected_cost_adjustment_store_test.go:92:160: undefined: billing.SelectedCostAdjustmentInput
internal\infra\billingstore\selected_cost_adjustment_store_test.go:227:24: store.ApplySelectedCostAdjustment undefined (type *DurableStore has no field or method ApplySelectedCostAdjustment)
...
FAIL
```

RED 2 - the first runnable pass exposed three real contract defects that were
fixed before GREEN: normalization did not classify `Expected`/`Selected`
validation failures under `ErrSelectedCostAdjustmentInvalid`; the posting-state
column was written with the domain `applied` spelling while the migration
default used `posted`; and the legacy compatibility fixture left the old
subject in the evidence envelope, so the authority validator correctly
rejected it. The first run also showed that provider journal ordering is
`recorded_at, transaction_id`, so order-sensitive assertions were replaced with
identity lookups by journal transaction id.

## GREEN evidence

- `go test -count=1 -run 'TestSelectedCostAdjustment' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.086s (2 top-level tests), billingstore 3.156s (11 top-level
  tests, 8 subtests).
- `go test -count=5 -shuffle=on -run 'TestSelectedCost' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.091s, billingstore 16.893s.
- `go test -count=5 -shuffle=on ./internal/core/billing` passed (1.844s).
- `go test -count=1 ./internal/core/billing/... ./pkg/lipsdk/economics/... ./internal/testkit/dbparity/...`
  passed: billing 0.421s, economics 0.559s, dbparity 0.473s, dbparity/cmd
  3.591s.
- `go test -count=1 ./internal/infra/billingstore/...` passed (62.369s); the
  existing provider-cost writer and all 13.1-13.3A adapters are unaffected.
- `make test-db-parity-sqlite` passed for all registered components,
  billingstore 16.132s.
- Focused live PostgreSQL: `LIP_REQUIRE_POSTGRES=1 go test -tags=integration
  -count=1 -run 'TestSelectedCostAdjustmentPostgres' ./internal/infra/billingstore`
  passed (29.932s) against the configured live DSN in an isolated schema;
  schema catalog, exact replay after re-open, no-FX mismatch, zero delta,
  downward correction and account balances, all three rollback boundaries,
  stale CAS and concurrent same/different revisions all verified.
- `go build ./...`, `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...`,
  `go vet -tags=integration ./internal/infra/billingstore/...`,
  `gofmt -l internal/core/billing internal/infra/billingstore internal/testkit/dbparity`
  and `git diff --check` all clean.
- `go test -count=1 ./internal/qa` passed (5.315s, dirty-Go-file gate
  included).
- Architecture subset `go test ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reports only the three pre-existing failures:
  `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi`
  dependency), the `runtimebundle` package budget (13131 > 12567) and
  `TestBillingFinalConvergenceLOCRatchetActive` (13.1A 36074, 13.1B 36558,
  13.2 37538, 13.3A 38591, this change 39375, so 13.3B adds 784 measured LOC
  to the already-active failure against the frozen 29760 ceiling).
- `-race` remains unavailable on this Windows host (cgo build failure recorded
  in earlier phases). The adapter owns no goroutines or background work;
  concurrency is exercised by racing store workers in both dialects.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Durable head keeps the exact 13.3A identity (valuation ref/revision/input hash, exact posted/native amount and currency, frozen FX basis, version, posting state) | `TestSelectedCostAdjustmentSQLiteSchemaIsRegistered`, head column asserts in the initial/FX tests, PG `schema catalog` subtest |
| One transaction: load/lock head, pure planner, CAS expected version, immutable adjustment+link, balanced journal delta, head advance | `TestSelectedCostAdjustmentSQLiteFaultInjectionRollsBackEachWriteBoundary` (journal, link, head stages), PG `fault rolls back every write` |
| All-or-nothing at each write boundary, retryable after rollback | same fault tests plus `TestRefinement43ProviderCostRevisionRollsBackAfterCrashAndRetries` unchanged |
| 10 USD to 8 USD posts exactly one -2 economic correction with positive reversed debit/credit legs | `TestSelectedCostAdjustmentSQLiteInitialAndDownwardCorrection`, PG direct |
| 10 USD to 8 EUR without frozen FX posts nothing and does not advance the head | `TestSelectedCostAdjustmentSQLiteNoFrozenFXMismatchPostsNothing`, PG direct |
| Identical frozen FX basis posts the converted delta and retains native amounts; changed rate/basis/one-sided basis stay pending | `TestSelectedCostAdjustmentSQLiteFrozenFXBasis`, head native/FX columns |
| Initial/upward/zero-delta semantics match the planner | `TestSelectedCostAdjustmentSQLiteInitialAndDownwardCorrection` asserts the initial +10, the downward -2 reversal and the upward +4 correction in the original debit/credit orientation; the zero-delta test and the unchanged 13.3A `TestSelectedCostHead*` suite cover the rest |
| Exact replay returns the original stable operation identity with no new effects, including after restart | `TestSelectedCostAdjustmentSQLiteReplayAndRestartReturnsStableIdentity`, PG direct re-open replay |
| Racing same revision/operation produces one effect; racing different revisions obeys CAS with no duplicates | `TestSelectedCostAdjustmentSQLiteRacingSameAndDifferentRevisions`, PG `same operation converges` and `different revisions obey CAS` |
| Stale version/revision and identity conflicts have zero effects | `TestSelectedCostAdjustmentSQLiteStaleAndConflictHaveZeroEffects`, PG stale assert |
| Pending provisional/unknown/non-payable selections persist nothing | `TestSelectedCostAdjustmentSQLitePendingSelectionsPersistNothing` |
| Legacy heads remain readable and correctable | `TestSelectedCostAdjustmentSQLiteReadsLegacyHeads` (both the writer-created and pre-migration blank-identity row) |
| Immutable adjustment/link rows | SQLite/PostgreSQL update/delete trigger asserts in both schema tests |
| Dual-dialect migration registration and schema verification | `RequiredMigrationNames`, `VerifySchema` on both dialects, `dbparity` capabilities |

## Explicit exclusions

- No revision-aware worker queue, retry/fence scheduling, provisional accrual
  policy or backlog reporting (13.4) and no dispute/correction certification
  matrix (13.5).
- No runtime wiring, host/binding change, customer rebill, statement matching
  change, report/query route, HTTP surface or `lipstd` behavior.
- No change to the 13.3A planner, no duplicate ledger, no public `pkg/lipapi` /
  `pkg/lipsdk` signature change, and no Phase 12 evidence/review edit.
- No Kiro task checkbox, spec-status, commit or PR operation.

## Residual risks and skips

- Provisional selections deliberately still do not post or advance the
  selected-cost head; provisional accrual remains a 13.4/worker policy
  decision.
- The adapter assumes the frozen selected valuation is authoritative and does
  not validate that an FX rate numerically reconciles the native and posted
  amounts; the retained frozen rate material is the audit basis.
- A legacy head advanced only by the provider-cost writer has no adjustment/link
  row; replay against it returns a planner-derived identity rather than a
  stored operation, and only when the caller CAS read matches the durable head.
  Both writers now write the derived exact identity columns, so a mixed-writer
  head stays internally coherent.
- Live PostgreSQL coverage needs the configured DSN and a reachable server;
  the focused run used the environment DSN with `LIP_REQUIRE_POSTGRES=1` in an
  isolated schema. `make test-db-parity-postgres-direct` was not run in full
  for this slice; the direct PG evidence is the focused tagged package run.
- The billing LOC ratchet was already failing at the checkpoint; 13.3B adds
  784 measured LOC (13.3A 38591, now 39375 against the frozen 29760 ceiling).
  It is recorded rather than hidden; no budget or baseline artifact was edited.
- `-race` remains unavailable on this Windows host; the adapter owns no
  goroutines, so this is a host limitation rather than an untested concurrency
  surface.

## Task 13.4: revision-aware separated durable economic job queues (13.4A)

Scope: parent task 13.4 slice 13.4A - revision-aware customer/provider/
reconciliation job identity and the durable queue delivery-state seam
(enqueue/upsert exact replay, bounded claim batch, lease/fence, heartbeat,
complete/retry/fail, stale-claim rejection, next-attempt timing, backlog age
and incomplete-evidence exposure). The existing Refinement 4.2
`billing_economic_work` / `billing_economic_revision_work_state` tables and
`EconomicRevisionWork` / `EconomicRevisionIdentity` / claim seams are extended,
not duplicated. No worker loop, orchestration, goroutine, rating/reconciliation
computation, financial head/journal transition, runtime wiring or `lipstd`
composition was added. Worktree `go-llm-interactive-proxy-feat-b-leg-usage-economics`;
branch `feat/b-leg-usage-economics`; checkpoint `3fb2d579` with all 13.1-13.3B
dirty files preserved. No commit, rebase, merge or PR operation was made, and
no Kiro task checkbox or status was changed.

New production files:

- `internal/core/billing/economic_job_queue.go` (370 lines) - job kind,
  dependency, status/reason vocabulary, bounded-text, claimed-work, queue-store
  and backlog-reader contracts.
- `internal/infra/billingstore/20260930000000_billing_economic_job_queue.go`
  (196 lines) - additive dual-dialect queue-state migration.
- `internal/infra/billingstore/economic_job_queue_store.go` (425 lines) -
  bounded batch claim, heartbeat, typed retry, terminal fail, backlog and
  dependency probes.

New test files:

- `internal/core/billing/economic_job_queue_test.go` (219 lines, 5 top-level
  tests).
- `internal/infra/billingstore/economic_job_queue_store_test.go` (369 lines,
  11 top-level tests).
- `internal/infra/billingstore/economic_job_queue_postgres_test.go` (148
  lines, 1 top-level test plus 5 subtests, `integration` build tag).

Modified production files:

- `internal/core/billing/economic_revision.go` - `EconomicRevisionIdentity`
  gains `DependenciesHash`; `Key`/`Less`/`validate` extend the preimage only
  when a dependency hash is present, so every legacy rating identity and
  valuation key is byte-identical. `EconomicRevisionWork` gains `Kind` and
  canonical `Dependencies`; `Normalize` derives/validates the kind, rejects
  rating work with dependencies, requires at least one rating dependency for
  separated reconciliation and rejects a self-referential dependency.
- `internal/infra/billingstore/economic_revision_queue_state.go` - state row
  carries `work_kind`, `dependency_count`, `retry_reason`, `failed_at_unix`;
  claim CAS now fences on the row fence and excludes `failed`; the legacy
  free-text retry path records the closed `unclassified` reason and bounds
  `last_error` to 256 runes.
- `internal/infra/billingstore/economic_revision_store.go` - pending listing
  excludes `failed` and shares one self-validating row decoder; the result
  probe validates a full identity including its dependency hash.
- `internal/infra/billingstore/20260812000000_billing_baseline.go`,
  `internal/infra/billingstore/store.go` - migration registration,
  `RequiredMigrationNames`, SQLite fragments/indexes and PostgreSQL catalog
  checks for the new columns and the extended status contract.
- `internal/testkit/dbparity/catalog.go` - one `Common` capability
  (`revision-job-queue-claim-heartbeat-fail-backlog`) and one `PostgresDirect`
  capability (`postgres-direct-revision-job-queue-fencing`).

## Job identity, dependency and queue design

- `EconomicWorkKind` is a closed vocabulary: `customer_rating` maps only to the
  customer queue; `provider_rating` and `reconciliation` map only to the
  provider queue, so a customer worker can never observe supplier computation.
  A legacy envelope without a kind derives its rating kind from the queue.
- `EconomicJobDependency` is an explicit immutable output reference (rating
  kind, queue, head key, evidence revision, input-set hash). Separated
  reconciliation work is dependency-anchored: it requires 1..8 unique rating
  dependencies, rejects reconciliation chains and self-reference, and its
  dependency set is canonicalized (sorted, exact duplicates collapsed) so
  insertion order cannot change identity. Dependencies may reference customer
  rating outputs while the reconciliation job itself stays on the provider
  queue.
- `EconomicRevisionIdentity` keeps the legacy queue/head/revision/input-hash
  preimage for rating work and appends the canonical dependency hash only for
  dependency-anchored work. A changed dependency set or a corrected rating
  output is therefore a distinct actionable revision; exact replay is one
  durable marker; a stale fence cannot retire, heartbeat, retry or fail a
  reclaimed job.
- `EconomicWorkStatus` (pending/processing/completed/failed) and
  `EconomicWorkReason` (unclassified, transient/rater/reconciler/persistence
  failure, dependency pending, lease expired, permanent failure) are closed,
  validated vocabularies; free-form error text is bounded to 256 runes in
  `last_error` and is never the durable reason.

## Store operation design

- `ClaimEconomicRevisionWorkBatch` selects at most the bounded batch
  (default 32, hard maximum 256) of due jobs for one queue, skips jobs with
  live leases, future `next_attempt_at`, terminal state or missing immutable
  dependency outputs, and leases each remaining job with a monotonically
  increasing fence. Dependency-blocked work stays pending without consuming an
  attempt. Candidate selection is queue-scoped; every read/write touches only
  work markers, delivery state and the immutable valuation probe.
- `HeartbeatEconomicRevisionWork` extends the lease for the current owner and
  fence only; `RetryEconomicRevisionWorkWithReason` returns the job to pending
  with a closed reason and explicit next-attempt time;
  `FailEconomicRevisionWork` terminally retires it with `failed_at_unix`.
  Complete, heartbeat, retry and fail all reject a stale owner/fence with
  `ErrEconomicRevisionClaimLost`.
- `EconomicRevisionQueueBacklog` reports pending/processing/completed/failed
  counts, the immutable work-marker oldest-pending timestamp and age, the
  earliest future next-attempt time and the number of dependency-gated jobs
  whose outputs are not yet durable. The dependency scan is bounded at 512
  non-terminal dependency-bearing jobs per queue.
- `EconomicRevisionDependencyChecks` probes each dependency's immutable
  valuation output directly; `AppendEconomicRevisionResult` (existing) is the
  only way an output becomes visible and is never invoked by the new queue
  operations.
- Migration `20260930000000` is additive: SQLite rebuilds only the mutable
  queue-state table on one reserved connection (widening the status CHECK to
  include `failed`, backfilling the derived rating kind, preserving all
  delivery rows and recreating the due index); PostgreSQL adds the columns and
  replaces the status check with the named
  `billing_economic_revision_work_state_status_contract`. Immutable work
  markers and derived economics are untouched.

## RED evidence (13.4A)

RED 1 - the queue contracts and store operations did not exist; both new test
files failed to compile exactly on the new boundary:

```
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [build failed]
# github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [github.com/matdev83/go-llm-interactive-proxy/internal/core/billing.test]
internal\core\billing\economic_job_queue_test.go:29:101: undefined: EconomicJobDependency
internal\core\billing\economic_job_queue_test.go:36:9: undefined: EconomicJobDependency
internal\core\billing\economic_job_queue_test.go:37:9: undefined: EconomicWorkKindForQueue
internal\core\billing\economic_job_queue_test.go:45:7: work.Kind undefined (type EconomicRevisionWork has no field or method Kind)
internal\core\billing\economic_job_queue_test.go:47:7: work.Dependencies undefined (type EconomicRevisionWork has no field or method Dependencies)
internal\core\billing\economic_job_queue_test.go:53:17: undefined: AllEconomicWorkKinds
... too many errors
# github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore [github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore.test]
internal\infra\billingstore\economic_job_queue_store_test.go:29:68: undefined: billing.EconomicJobDependency
internal\infra\billingstore\economic_job_queue_store_test.go:37:41: unknown field Kind in struct literal of type billing.EconomicRevisionWork
internal\infra\billingstore\economic_job_queue_store_test.go:64:31: store.ClaimEconomicRevisionWorkBatch undefined (type *DurableStore has no field or method ClaimEconomicRevisionWorkBatch)
... too many errors
FAIL
```

RED 2 - the first runnable pass exposed one real production defect, one fixture
defect and one test-build defect:

- `TestEconomicJobQueueSQLiteBacklogAgeAndCounts` failed because the backlog
  oldest-pending basis used the mutable state-row creation timestamp instead of
  the immutable work-marker `created_at`; the query now uses
  `MIN(w.created_at_unix)`, so backlog age cannot be reset by state rewrites.
  The fixture was also corrected to retry the oldest job instead of failing it
  so the intended pending/processing/completed/failed mix is exercised.
- The domain fixture used non-numeric quantity labels where `metering.Decimal`
  parsing is required (`panic: metering: invalid decimal: exponent required`);
  the fixture now carries a separate label and a numeric quantity. The
  production contract was not changed.
- One test used a two-value heartbeat call in a single-value context; the test
  was corrected.

## GREEN evidence

- `go test -count=1 -run 'TestEconomicJob' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.084s (5 top-level tests), billingstore 0.627s (11 top-level
  tests).
- `go test -count=5 -shuffle=on -run 'TestEconomicJob' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.092s, billingstore 2.609s.
- `go test -count=5 -shuffle=on ./internal/core/billing` passed (1.675s).
- `go test -count=1 ./internal/core/billing/... ./internal/testkit/dbparity/...`
  passed: billing 0.406s, dbparity 0.537s, dbparity/cmd 3.467s.
- `go test -count=1 ./internal/infra/billingstore/...` passed (62.477s); all
  Refinement 4.2 and 13.1-13.3B queue/result tests are unaffected.
- `go test -count=1 ./internal/infra/runtimebundle/... ./internal/infra/billingcompose/... ./internal/infra/billingadmission/...`
  passed: runtimebundle 72.326s, billingcompose 0.594s, billingadmission 0.485s.
- `make test-db-parity-sqlite` passed for all registered components
  (billingstore 16.066s).
- Focused live PostgreSQL: `LIP_REQUIRE_POSTGRES=1 go test -tags=integration
  -count=1 -run 'TestEconomicJobQueuePostgres' -v ./internal/infra/billingstore`
  passed (17.600s) against the configured live DSN in an isolated schema:
  schema catalog, bounded batch claim/lease expiry/stale rejection, retry
  reason and next attempt, incomplete-dependency gating and concurrent batch
  claims with one winner per revision (1.740s).
- `go build ./...`, `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...`,
  `go vet -tags=integration ./internal/infra/billingstore/...`,
  `gofmt -l internal/core/billing internal/infra/billingstore internal/testkit/dbparity`
  and `git diff --check` all clean.
- `go test -count=1 ./internal/qa` passed (6.250s, dirty-Go-file gate
  included).
- Architecture subset `go test ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reports only the three pre-existing failures:
  `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi`
  dependency), the `runtimebundle` package budget (13131 > 12567) and
  `TestBillingFinalConvergenceLOCRatchetActive` (13.1A 36074, 13.1B 36558,
  13.2 37538, 13.3A 38591, 13.3B 39375, this change 40265, so 13.4A adds 890
  measured LOC to the already-active failure against the frozen 29760
  ceiling).
- `-race` remains unavailable on this Windows host (cgo build failure recorded
  in earlier phases). This slice adds no goroutine or background work;
  concurrency is exercised by expired-lease fencing on both dialects and by
  two concurrent PostgreSQL batch claims.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Exact revision-aware identity/input hash for customer rating, provider rating and reconciliation work | Domain kind/dependency table tests; identity key changes with dependency set; legacy rating keys unchanged (full billingstore suite) |
| Explicit dependency/evidence revision refs and subject/store/account/call scope | `TestEconomicJobScopeAndDependenciesAreCanonical`; dependency output identity/key/valuation key; subject scope retained through normalization |
| Strict customer vs provider queue separation | `TestEconomicJobQueueSQLiteSeparatesCustomerAndProviderClaims`; kind-queue binding tests; reconciliation owned by the provider queue |
| Reconciliation may depend on immutable outputs without holding customer/account locks | Dependency-gated claim test; `TestEconomicJobQueueSQLiteNeverMutatesAccountsOrHeads` proves no account/journal/valuation/head/balance table is written by enqueue, batch claim, heartbeat, retry, fail, backlog or dependency checks |
| Enqueue/upsert exact replay and changed-revision actionability | `TestEconomicJobQueueSQLiteExactReplayAndChangedRevision` (one marker for replay; changed input hash and later revision are distinct actionable revisions) |
| Bounded claim batch and lease/fence | `TestEconomicJobQueueSQLiteBoundedClaimBatchAndFencing`; hard bound rejects an oversized batch; PostgreSQL concurrent batch claims have one winner per revision |
| Heartbeat/complete/retry/fail with stale-claim rejection | `TestEconomicJobQueueSQLiteHeartbeatExtendsLeaseAndRejectsStale`, `TestEconomicJobQueueSQLiteFailIsTerminalAndBounded`, stale complete/heartbeat/retry/fail asserts in the fencing test |
| Next-attempt timing | `TestEconomicJobQueueSQLiteRetryReasonAndNextAttempt` (future retry hidden, due retry claimable with a newer fence); PostgreSQL retry subtest |
| Bounded retry reason | Closed reason vocabulary; legacy free-text path records `unclassified` and truncates `last_error` to 256 runes; unknown typed reason fails closed |
| Backlog age and incomplete evidence | `TestEconomicJobQueueSQLiteBacklogAgeAndCounts`, `TestEconomicJobQueueSQLiteBacklogNextAttempt`, `TestEconomicJobQueueSQLiteIncompleteDependenciesBlockClaimUntilRated` (missing dependency blocks claim; rating the output satisfies it without a state rewrite) |
| Additive dual-dialect schema, migration registration and parity | `TestEconomicJobQueueSQLiteSchemaIsRegistered`, PostgreSQL `schema catalog` subtest, `RequiredMigrationNames`, `VerifySchema`, dbparity capabilities and passing `make test-db-parity-sqlite` |
| RED first | Compile-failure output above plus the first behavioral failures and their fixes |

## Explicit exclusions

- No worker loop, orchestration, polling, goroutine or runtime composition; no
  `lipstd`/host binding change and no HTTP surface.
- No actual rating or reconciliation computation and no financial transition:
  queue operations never insert a valuation, reconciliation, journal, head,
  exposure or balance row. The existing `AppendEconomicRevisionResult` is
  called only by tests to simulate an already-rated dependency output.
- No result persistence contract for dependency-anchored reconciliation work
  and no dependency-completeness promotion of queue state (13.4B).
- No change to Phase 12 evidence/review files, no Kiro checkbox or spec-status
  change, no commit, rebase, merge or PR operation.

## Residual risks and skips

- The SQLite migration rebuilds only the mutable queue-state table and
  preserves its rows via `INSERT ... SELECT`; a populated pre-migration
  database is not exercised by an automated upgrade test, although immutable
  work markers and derived economics are never touched by the rebuild.
- The backlog incomplete-dependency count is exact within a bounded scan of
  512 non-terminal dependency-bearing jobs per queue; the bound is documented
  in the adapter and is an observability limit, not a correctness limit.
- Dependency-blocked jobs are skipped without recording a retry reason because
  no attempt occurs; incomplete evidence is exposed through the backlog and
  dependency checks instead.
- Reconciliation output persistence, dependency promotion and worker
  orchestration remain 13.4B work; this slice certifies job identity,
  separation and delivery-state safety only.
- The billing LOC ratchet was already failing at the checkpoint; 13.4A adds
  890 measured LOC (13.3B 39375, now 40265 against the frozen 29760 ceiling).
  It is recorded rather than hidden; no budget or baseline artifact was edited.
- `-race` remains unavailable on this Windows host; the new operations own no
  goroutines, so this is a host limitation rather than an untested concurrency
  surface.

## Task 13.4: revision-aware economic worker application orchestration (13.4B)

Scope: parent task 13.4 slice 13.4B - the consumer-owned application runner over
the 13.4A durable queues plus the dependency-output and reconciliation-output
store adapter. One bounded claim batch per explicit queue; dispatch by closed
work kind to injected pure ports; computation outside any database transaction;
idempotent immutable output persistence; exact-fence finalization; stage-aware
failure classification; bounded run summary with backlog age and incomplete
evidence. No runtime scheduler wiring, no background goroutine, no heartbeat
loop, no selected-cost head/journal transition and no provider-specific
branching beyond kind dispatch. Worktree
`go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `3fb2d579` with all 13.1-13.4A dirty
files preserved. No commit, rebase, merge or PR operation was made, and no Kiro
task checkbox or status was changed.

New production files:

- `internal/core/billing/economic_job_runner.go` (484 lines) - dependency
  output/reconciliation store ports, failure stage/disposition vocabulary and
  classifier, run summary, runner config and the bounded `RunOnce`
  orchestration.
- `internal/infra/billingstore/economic_job_runner_store.go` (104 lines) -
  exact dependency output reader and idempotent reconciliation output
  store/probe.

New test files:

- `internal/core/billing/economic_job_runner_test.go` (731 lines, 11 top-level
  tests) - in-memory port fake covering every orchestration behavior.
- `internal/infra/billingstore/economic_job_runner_store_test.go` (204 lines, 3
  top-level tests) - SQLite integration over the real queue/result store.
- `internal/infra/billingstore/economic_job_runner_postgres_test.go` (93 lines,
  1 top-level test plus 3 subtests, `integration` build tag).

Modified production files:

- `internal/testkit/dbparity/catalog.go` - two additional billing capabilities:
  `Common` `revision-job-reconciliation-output-replay` and `PostgresDirect`
  `postgres-direct-revision-reconciliation-output-idempotency`.

No 13.4A queue contract, schema or migration was changed; 13.4B adds only new
files to `internal/core/billing` and `internal/infra/billingstore` plus the
catalog metadata.

## Orchestration, ports and failure design

- `EconomicJobRunnerConfig` requires every port explicitly: `Queue`
  (claim/complete/retry/fail), `Backlog`, `Results` (and its replay probe),
  `Dependencies`, `Reconciliations`, `Rater` and `Reconciler`. `Owner`, `Batch`,
  `Lease`, `RetryBackoff` and `Now` are explicit with bounded defaults (batch
  default 32, hard maximum 256; backoff maximum 24h). The result store must
  implement `EconomicRevisionResultProbe`, so replay-after-interruption cannot
  silently re-rate. The runner owns no goroutine.
- `RunOnce(ctx, queue)` claims at most one bounded batch from the explicit
  queue and processes each immutable job:
  - `customer_rating` and `provider_rating` dispatch to the existing approved
    `PostUsageRater` seam with the job's own immutable basis/input (the two
    rating planes differ by evidence basis, not by provider branching):
    replay probe -> `Rate` -> `normalizeRevisionValuation` ->
    `AppendEconomicRevisionResult` -> complete with the exact fence.
  - `reconciliation` (provider queue only) dispatches to
    `EconomicJobReconciler.ReconcileJob(work, exact dependency outputs)`: replay
    probe -> `LoadEconomicRevisionDependencyOutput` per exact dependency
    revision identity -> pure reconciliation -> `normalizeRevisionReconciliation`
    -> `AppendEconomicRevisionReconciliation` -> complete with the exact fence.
  - Unknown or non-canonical claimed work fails closed terminally.
- Computation always runs after the claim transaction has committed and before
  the result transaction; no database transaction, account or customer lock is
  held while a rater or reconciler executes.
- `LoadEconomicRevisionDependencyOutput` reads `billing_valuations` by the
  dependency's exact valuation key and maps a missing row to
  `ErrEconomicRevisionDependencyOutputMissing`. `AppendEconomicRevisionReconciliation`
  validates the reconciliation against the work identity (ID, revision, subject,
  basis, input hash), maps it onto the existing immutable
  `billing_reconciliations` replay/conflict path, and maps identity conflict to
  `ErrEconomicRevisionConflict`. `HasEconomicRevisionReconciliation` is the
  replay probe. None of these touches a balance, journal, exposure or financial
  head.
- Failure classification is stage-aware and closed: missing dependency output
  retries `dependency_pending`; caller cancellation/deadline retries
  `lease_expired`; deterministic invalid/subject/basis/input/conflict/evidence
  fence (including incomparable evidence sets) terminates `permanent_failure`;
  other probe/dependency/persist/finalize failures retry `persistence_failure`,
  rating failures retry `rater_failure`, reconciliation failures retry
  `reconciler_failure` and remaining stages retry `transient_failure`. Retries
  record a bounded next attempt.
- Cancellation releases the in-flight claim through the bounded detached
  retry path (`lease_expired`); claims not yet started are released the same
  way and counted as `Released`. A claim lost to a newer fence is counted as
  `Superseded`; the immutable output remains and the new owner finishes it.
- `EconomicJobRunSummary` exposes claimed/completed/retried/failed/superseded/
  released counts plus the backlog snapshot, which carries oldest-pending age,
  next-attempt time and incomplete-evidence count.
- Reconciliation dependency promotion is exact and automatic: when the rating
  output is durably appended, the dependency probe in the next bounded claim
  makes the reconciliation job claimable without any promotion job or state
  rewrite. Supplier backlog therefore never blocks customer queue claims or
  execution.

## RED evidence (13.4B)

RED 1 - the orchestration contracts and store adapter did not exist; both new
test files failed to compile exactly on the new boundary:

```
# github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [...billing.test]
internal\core\billing\economic_job_runner_test.go:349:54: undefined: ErrEconomicRevisionDependencyOutputMissing
internal\core\billing\economic_job_runner_test.go:445:12: undefined: EconomicJobDependencyOutput
internal\core\billing\economic_job_runner_test.go:470:102: undefined: EconomicJobReconciler
internal\core\billing\economic_job_runner_test.go:470:125: undefined: EconomicJobRunnerConfig
internal\core\billing\economic_job_runner_test.go:479:17: undefined: NewEconomicJobRunner
... too many errors
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/billing [build failed]
# github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore [...billingstore.test]
internal\infra\billingstore\economic_job_runner_store_test.go:60:18: undefined: billing.EconomicJobDependencyOutput
internal\infra\billingstore\economic_job_runner_store_test.go:74:105: undefined: billing.EconomicJobReconciler
internal\infra\billingstore\economic_job_runner_store_test.go:137:23: store.HasEconomicRevisionReconciliation undefined (type *DurableStore has no field or method HasEconomicRevisionReconciliation)
... too many errors
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore [build failed]
```

RED 2 - the first runnable pass exposed only fixture defects, not production
defects:

- `TestEconomicJobRunnerCustomerProceedsWhileProviderBacklogIncomplete` and the
  SQLite/PG promotion subtests expected `IncompleteDependencies == 1` after a
  provider run that had just rated the dependency output. The expectation was
  wrong: the same run promotes the reconciliation (incomplete evidence is
  observed before the rating output exists). Fixtures now assert the
  before-state as incomplete and the after-state as promoted.
- `TestEconomicJobRunnerProviderRatingPromotesReconciliation` expected one
  completed backlog record after two runs; both rating and reconciliation
  complete, so the expectation is two.
- The PostgreSQL reconciliation-output fixture built the record from an
  un-normalized work, so `InputSetHash` was empty and the store correctly
  rejected it with `billing: economic revision input mismatch`; the fixture now
  normalizes the work first.

## GREEN evidence

- `go test -count=1 -run 'TestEconomicJobRunner' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.079s (11 top-level tests), billingstore 0.242s (3 top-level
  tests).
- `go test -count=5 -shuffle=on -run 'TestEconomicJobRunner' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.111s, billingstore 0.950s.
- `go test -count=5 -shuffle=on -run 'TestEconomicJob' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.120s, billingstore 3.411s (13.4A and 13.4B together).
- `go test -count=5 -shuffle=on ./internal/core/billing` passed (1.596s).
- `go test -count=1 ./internal/core/billing/... ./internal/testkit/dbparity/...`
  passed: billing 0.596s, dbparity 0.522s, dbparity/cmd 4.173s.
- `go test -count=1 ./internal/infra/billingstore/...` passed (64.391s); all
  13.1-13.4A queue/result tests are unaffected.
- `go test -count=1 ./internal/infra/runtimebundle/... ./internal/infra/billingcompose/... ./internal/infra/billingadmission/...`
  passed: runtimebundle 36.743s, billingcompose 0.559s, billingadmission 0.443s.
- `make test-db-parity-sqlite` passed for all registered components
  (billingstore 16.060s).
- Focused live PostgreSQL: `LIP_REQUIRE_POSTGRES=1 go test -tags=integration
  -count=1 -run 'TestEconomicJobRunnerPostgres' -v ./internal/infra/billingstore`
  passed (14.977s) against the configured live DSN in an isolated schema:
  exact dependency output loading, idempotent reconciliation replay/conflict
  and rating-then-reconciliation promotion.
- `go build ./...`, `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...`,
  `go vet -tags=integration ./internal/infra/billingstore/...`,
  `gofmt -l internal/core/billing internal/infra/billingstore internal/testkit/dbparity`
  and `git diff --check` all clean.
- `go test -count=1 ./internal/qa` passed (6.164s, dirty-Go-file gate
  included).
- Architecture subset `go test ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reports only the three pre-existing failures:
  `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi`
  dependency), the `runtimebundle` package budget (13131 > 12567) and
  `TestBillingFinalConvergenceLOCRatchetActive` (13.1A 36074, 13.1B 36558,
  13.2 37538, 13.3A 38591, 13.3B 39375, 13.4A 40265, this change 40897, so
  13.4B adds 632 measured LOC to the already-active failure against the frozen
  29760 ceiling).
- `-race` remains unavailable on this Windows host (cgo build failure recorded
  in earlier phases). The runner owns no goroutine and holds no lock across
  computation; concurrency safety is provided by the 13.4A claim fences
  exercised here through stale-fence and supersession tests.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Consumer-owned bounded batch per explicit queue, dispatch by closed work kind to injected ports | `TestEconomicJobRunnerBoundedBatch`, `TestEconomicJobRunnerCustomerProceedsWhileProviderBacklogIncomplete`, SQLite/PG runner tests; customer/provider rating use the approved `PostUsageRater` basis seam, reconciliation its own port |
| Load exact immutable dependency outputs; compute outside any DB transaction; persist immutable output idempotently; finalize with exact fence | `TestEconomicJobRunnerProviderRatingPromotesReconciliation` exact valuation assert, `LoadEconomicRevisionDependencyOutput` missing/exact store test, `TestEconomicJobRunnerStaleFenceCompletionIsSuperseded` |
| Interruption after output before complete replays to the same output and finishes once | `TestEconomicJobRunnerInterruptionAfterOutputBeforeCompleteReplaysOnce`, `TestEconomicJobRunnerDuplicateWorkerReplayFinishesOnce`, SQLite interruption test (rater called once, one valuation) |
| Reconciliation is provider-queue work that waits on exact dependency outputs; supplier backlog never blocks customer claims | `TestEconomicJobRunnerCustomerProceedsWhileProviderBacklogIncomplete`, `TestEconomicJobRunnerMissingDependencyRetriesWithDependencyPending`, SQLite/PG promotion tests with `Backlog.IncompleteDependencies` before/after |
| Failure classification: deterministic invalid/incomparable terminal; transient bounded retry | `TestEconomicJobRunnerTransientRetryVersusTerminalFailure`, `TestEconomicJobRunnerFailureVocabularyIsClosed` (dependency_pending, lease_expired, permanent_failure, rater/reconciler/persistence/transient mapping) |
| Cancellation releases via lease expiry/retry path; no leaked goroutines | `TestEconomicJobRunnerCancellationReleasesClaim` (retry reason `lease_expired`, no output); the runner starts no goroutine |
| No selected-cost head/journal transition inside the worker | `TestEconomicJobRunnerSQLiteNeverMutatesFinancialTables` (accounts, journals, provider cost heads, selected-cost adjustments, unit balances, exposures unchanged; only pure valuations/reconciliations added) |
| Bounded run summary with backlog age and incomplete evidence | Summary/backlog asserts across runner tests (`Backlog.OldestPendingAge`, `NextAttemptAt`, `IncompleteDependencies`) |
| Dual-dialect store behavior and parity | SQLite runner tests, live PG runner test, dbparity capabilities and passing `make test-db-parity-sqlite` |
| RED first | Compile-failure output above plus the first behavioral failures and their fixture corrections |

## Explicit exclusions

- No runtime scheduler, host/`runtimebundle` wiring, polling loop, ticker or
  goroutine; construction is explicit and the caller owns invocation.
- No selected-cost head CAS, balanced journal delta, account/customer balance,
  exposure or settlement mutation; those remain the separate 13.3 adapter
  invocation after explicit output selection.
- No provider-specific branching beyond closed kind dispatch and no new
  provider SDK dependency.
- No 13.4A queue contract, schema or migration change and no 13.1-13.3 file
  change.
- No change to Phase 12 evidence/review files, no Kiro checkbox or spec-status
  change, no commit, rebase, merge or PR operation.

## Residual risks and skips

- The runner does not heartbeat during a long computation (no background
  goroutine by design). An overrun is recovered by lease expiry with a newer
  fence; the replay probe then finishes the durable output without
  recomputation.
- Dependency promotion is automatic through the exact-output probe; there is no
  separate promotion job or state rewrite to audit.
- `RunOnce` returns batch-level failures, cancellation and release faults;
  per-item retry/terminal outcomes are durable queue state and summary counts.
  A terminal per-item failure is therefore visible in the summary rather than
  as a returned error.
- Reconciliation work persists its reconciliation record only; it does not
  advance a valuation head. Any later monetary interpretation remains the
  separate selected-cost adapter's decision.
- The billing LOC ratchet was already failing at the checkpoint; 13.4B adds 632
  measured LOC (13.4A 40265, now 40897 against the frozen 29760 ceiling). It is
  recorded rather than hidden; no budget or baseline artifact was edited.
- `-race` remains unavailable on this Windows host; the runner owns no
  goroutines, so this is a host limitation rather than an untested concurrency
  surface.

## Task 13.5: correction and dispute-like recovery certification

Scope: parent task 13.5 - end-to-end certification of correction and
dispute-like recovery. Late provider evidence after call/customer closure,
corrected statement quantities and aggregate statement adjustments flowing
through normalized import/match/selection to one idempotent delta, duplicate
imports/replays/racing corrections, unmatched statement lines and many-charge
aggregate coverage, a native-currency mismatch with no frozen FX, the default
customer no-rebill policy and the explicit versioned provisional
pass-through adjustment. Worktree
`go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `3fb2d579` with all 13.1-13.4 dirty
files preserved. No commit, rebase, merge or PR operation was made, and no Kiro
task checkbox or status was changed. No Phase 12 evidence/review file was
edited.

This slice is pure certification: **no production file was changed**. The
existing 13.1 durable statement import ledger and public `StatementImporter`,
the 13.2 pure matcher, the 13.3 selected-cost CAS adapter and the earlier
explicit customer cost-pass-through seam (`CostPassThroughSettlement` /
`ApplyCostPassThroughRevision` with the frozen `AllowLateAdjustment` policy and
the head-backed signed delta) already implement every required behavior. No
demonstrated gap required a minimal production fix and no speculative policy
seam, invoicing platform, vendor parser or UI was added.

New test files only:

- `internal/core/billing/correction_recovery_certification_test.go` (222 lines,
  3 top-level tests) - domain composition of 13.1 import, 13.2 matching and
  13.3 selected-cost planning.
- `internal/infra/billingstore/correction_recovery_certification_test.go` (534
  lines, 4 top-level tests plus 3 subtests) - durable SQLite end-to-end
  recovery scenarios.
- `internal/infra/billingstore/correction_recovery_postgres_test.go` (211
  lines, 1 top-level test plus 4 subtests, `integration` build tag) - direct
  PostgreSQL transaction behavior.

## Policy and scenario design (reused contracts)

- Default no-rebill: an independent-retail customer settlement is the only
  customer monetary path (`ApplyCallBillingResult`, `customer_call_settlement`
  journal). Provider COGS corrections go exclusively through
  `ApplySelectedCostAdjustment`, which posts only provider journals against the
  `billing_provider_cost_heads` row and never reads or writes the customer
  balance, version or settlement journal. A late supplier adjustment against a
  customer call that never elected cost pass-through fails closed with
  `ErrCostPassThroughHeadNotFound` instead of inventing a rebill.
- Explicit provisional pass-through: the frozen customer policy must carry
  `MissingCost = provisional` and `AllowLateAdjustment = true`; the settlement
  posts the approved bound and retains the original transaction identity. A
  later authoritative provider cost emits exactly one bounded `Posting` whose
  delta is `authoritative - posted`, inside `SafeBound` and in the head
  currency. The original immutable settlement journal is never updated; the
  adjustment journal references the original transaction as its correction
  group. Exact revision replay returns `Replayed` with no new journal, link or
  balance effect; an oversized amount fails `ErrCostPassThroughBoundExceeded`
  and a non-comparable currency fails `ErrCostPassThroughCurrencyMismatch`.
- Aggregate correction chain: a corrected statement revision uses a new line
  revision; the durable ledger retains revision 1 and accepts revision 2,
  duplicate imports report every line as `Replayed`, and the matcher yields one
  `aggregate_sku` coverage link retaining all three covered charges and their
  retained reconciliation evidence IDs. The corrected aggregate amount becomes
  one `new - posted` selected-cost delta. An aggregate claim without an SKU
  component stays `account_scoped_total` with no link; no per-request
  allocation is created.
- No-FX incomparability: `10 USD -> 8 EUR` without a shared frozen basis stays
  `pending` / `posted_currency_mismatch` / `incomparable` in both the pure
  planner and the durable adapter, with zero journal, link and head effects;
  the prior COGS posting, the head and the customer settlement remain.

## Pre-change checks and RED notes (pure certification)

Pre-change focused suites were green before any 13.5 file existed:

```
go test -count=1 -run 'TestStatementImport|TestStatementMatch|TestSelectedCost|CostPassThrough' ./internal/core/billing ./internal/infra/billingstore
ok  github.com/matdev83/go-llm-interactive-proxy/internal/core/billing  0.174s
ok  github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore  4.038s
```

Three test-side failures were found and fixed while building the fixtures; no
production defect was demonstrated and no production line changed:

- RED 1 (domain expectation): the first replay fixture asked the pure planner
  to replay a correction against a head that had not yet advanced. The fixture
  now presents the durable successor head (`version 2`) exactly as a crash
  retry does; the planner correctly returned `applied` before and `replay`
  after.
- RED 2 (durable fixture identity): the first direct-PostgreSQL run shared one
  call identity across subtests of one store and `AppendCallUsage` correctly
  rejected the second record with `billing: replay fingerprint conflict`
  because `usage_call_records` is keyed by call identity. Each subtest now uses
  its own call identity; the production replay guard is the evidence that the
  fixture was wrong, not the store.
- RED 3 (statement-line encoding): an account total was first encoded as an
  imported unmatched outcome, which the 13.1 ledger correctly reports under
  `Unmatched`. The fixture now encodes it as a genuine aggregate claim without
  an SKU component, which imports as a claim and is reported
  `account_scoped_total` by the 13.2 matcher.

## GREEN evidence

- `go test -count=1 -v -run 'TestCorrectionRecovery' ./internal/core/billing ./internal/infra/billingstore`
  passed: core `ok` 0.347s (3 top-level tests), billingstore `ok` 1.939s (4
  top-level tests plus 3 subtests). Every expected PASS line is present:
  `TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta`,
  `TestCorrectionRecoveryCertifyDuplicateImportAndConflictingRestatementAreInert`,
  `TestCorrectionRecoveryCertifyUnmatchedAccountTotalIsNeverAllocated`,
  `TestCorrectionRecoverySQLiteLateProviderCorrectionAfterClosureDoesNotRebillCustomer`,
  `TestCorrectionRecoverySQLiteNoFrozenFXCorrectionRetainsIncurredEconomics`,
  `TestCorrectionRecoverySQLiteCorrectedAggregateStatementAdjustsOnce`,
  `TestCorrectionRecoverySQLiteProvisionalPassThroughAdjustmentIsBounded`
  (independent retail / explicit provisional / pending policy).
- `go test -count=5 -shuffle=on -run 'TestCorrectionRecovery' ./internal/core/billing ./internal/infra/billingstore`
  passed: core 0.130s, billingstore 9.115s.
- `go test -count=5 -shuffle=on ./internal/core/billing` passed (2.314s).
- `go test -count=1 ./internal/core/billing/... ./internal/testkit/dbparity/...`
  passed: billing 0.695s, dbparity 0.750s, dbparity/cmd 4.133s.
- `go test -count=1 ./internal/infra/billingstore/...` passed (68.280s); the
  unchanged 13.1-13.4 suites and the earlier Phase 10 pass-through suites are
  unaffected.
- `make test-db-parity-sqlite` passed for all registered components
  (billingstore 16.172s); no catalog/schema change was needed.
- Focused live PostgreSQL: `LIP_REQUIRE_POSTGRES=1 go test -tags=integration
  -count=1 -v -run 'TestCorrectionRecoveryPostgresDirect' ./internal/infra/billingstore`
  passed (17.24s) against the configured live DSN in an isolated schema:
  late correction after closure without customer rebill, no-FX pending with
  retained head/economics, four racing identical corrections with one applied
  effect and three replays, and provisional pass-through with one bounded
  adjustment plus exact replay.
- `go build ./...`, `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...`,
  `go vet -tags=integration ./internal/infra/billingstore/...`,
  `gofmt -l internal/core/billing internal/infra/billingstore` and
  `git diff --check` all clean.
- `go test -count=1 ./internal/qa` passed (6.239s, dirty-Go-file gate
  included; 43 dirty Go files).
- Architecture subset `go test -count=1 ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reports only the three pre-existing failures:
  `TestBillingCoreStaysProviderAndPersistenceFree` (pre-existing `pkg/lipapi`
  dependency), the `runtimebundle` package budget (13131 > 12567) and
  `TestBillingFinalConvergenceLOCRatchetActive` (13.4B 40897, now 40899
  against the frozen 29760 ceiling). This slice adds only `_test.go` files and
  no production file, so the already-active ratchet failure is unaffected by
  the certification change.
- `-race` remains unavailable on this Windows host. Concurrency is exercised
  by four concurrent SQLite workers and four concurrent direct-PostgreSQL
  workers for the same correction revision plus the existing head CAS/fence.

## Acceptance coverage

| Required behavior | Evidence |
|---|---|
| Late provider evidence after call/customer closure updates immutable COGS history through 13.3 without reopening/rebilling the customer by default | `TestCorrectionRecoverySQLiteLateProviderCorrectionAfterClosureDoesNotRebillCustomer` (settle 25, exposure closed, then +10 and -2 COGS: customer balance/version and the single settlement journal unchanged; head advances to 8); PG `late correction after closure ...` |
| Corrected quantities flow through normalized import/match/selection to one idempotent delta | `TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta` (statement rev 2 / line rev 2, matched 3-charge aggregate, exact `-2` delta, one balanced journal, coverage link changed, exact replay zero-effect) |
| Aggregate statement adjustment posts once; duplicate import/replay inert | `TestCorrectionRecoverySQLiteCorrectedAggregateStatementAdjustsOnce` (public `StatementImportService` over the durable ledger; duplicate import replayed; one link retaining three charges and their reconciliation IDs; 4 racing corrections = 1 applied + 3 replayed, 2 journals/rows) |
| Conflicting restatement fails closed with no durable change | `TestCorrectionRecoveryCertifyDuplicateImportAndConflictingRestatementAreInert` (`ErrStatementImportConflict`, ledger append count unchanged) |
| Unmatched statement lines/account totals preserved; many-charge aggregate coverage; no guessed per-request allocation | `TestCorrectionRecoveryCertifyUnmatchedAccountTotalIsNeverAllocated`; durable aggregate test asserts `account_scoped_total`, empty link key and one 3-charge link with no per-charge split |
| 10 USD -> 8 EUR without frozen FX stays pending/incomparable: no journal/link/head advance, incurred COGS and customer settlement not erased | `TestCorrectionRecoverySQLiteNoFrozenFXCorrectionRetainsIncurredEconomics` (head still 10 USD at version 1, one COGS journal, one customer journal, balance 975); PG no-FX subtest |
| Customer no-rebill default for independent retail offers | `TestCorrectionRecoverySQLiteProvisionalPassThroughAdjustmentIsBounded/independent_retail_has_no_late-rebill_head` (`ErrCostPassThroughHeadNotFound`, balance/version/journal unchanged) |
| Explicit versioned provisional pass-through adjustment emits one bounded customer intent and never rewrites the original settlement | same test `explicit_provisional_pass-through_posts_one_bounded_adjustment` (100 provisional -> authoritative 80 -> one `-20` posting; journal `UPDATE` rejected by the immutability trigger; correction references the original settlement; bound/currency fail closed) |
| Pending pass-through policy cannot late-adjust | same test `pending_policy_ignores_a_late_adjustment` (`Ignored`, balance unchanged) |
| Interruption/replay produces one effect | SQLite file-store close/reopen returns the original operation and transaction identity with zero new effects; PG exact replay; existing 13.3B/13.4B interruption tests unchanged |
| Dual-dialect transaction behavior where it matters | durable SQLite suite plus direct live PostgreSQL; `make test-db-parity-sqlite` passing |

## Explicit exclusions

- No production file change, no new policy seam and no schema/migration or
  `dbparity` catalog change; every asserted behavior is provided by the
  existing 13.1-13.4 contracts.
- No dispute-management workflow, invoice generation, tax engine, vendor
  invoice parser or UI (13.6); no runtime/`lipstd` wiring or HTTP route.
- No provider SDK dependency, no provider-name branching and no synthetic
  B-leg.
- No Kiro checkbox or spec-status change, no commit, rebase, merge or PR, and
  no Phase 12 evidence/review edit.

## Residual risks and skips

- The certification reuses the existing cost-pass-through seam as the durable
  customer adjustment intent. A future full dispute/invoicing product remains
  out of scope; this slice only certifies that the frozen policy gates a
  bounded, idempotent adjustment and that the default offer never rebills.
- Frozen FX material is authoritative by contract; the certification does not
  add numerical FX reconciliation (unchanged from 13.3A/B).
- Direct-PostgreSQL evidence is the focused tagged run in an isolated schema
  against the configured live DSN; `make test-db-parity-postgres-direct` was
  not run in full for this slice, consistent with the 13.3B/13.4B evidence.
- `-race` remains unavailable on this Windows host; this slice adds no
  goroutine or background work beyond test-owned workers.
- The billing LOC ratchet was already failing at the checkpoint; this slice
  adds only test files (measured 40899 versus the 13.4B 40897). It is recorded
  rather than hidden; no budget or baseline artifact was edited.

## Task 13.1 tenant-scope remediation (review blocker)

Scope: focused Phase13 review remediation only for Task 13.1 tenant-scope
defect (Requirements 13.1, 16.3). Worktree
`go-llm-interactive-proxy-feat-b-leg-usage-economics`, branch
`feat/b-leg-usage-economics`. Shared dirty worktree preserved; no
reset/revert/overwrite, commit/rebase/merge, Kiro tasks/status change, PR, or
review-evidence edit. `parent-phase13-review.md` untouched. Validation-only
fix; no schema/migration/new abstraction.

RED_PHASE_OUTPUT (strict RED first, domain/service import must fail
`ErrStatementImportScopeMismatch` before ledger append):

```
go test -count=1 -run 'TestStatementImportForeignLineTenantIsScopeMismatchRed' ./internal/core/billing -v
=== RUN   TestStatementImportForeignLineTenantIsScopeMismatchRed/matched
    statement_import_tenant_scope_red_test.go:33:
        Error: Expected error with "billing: statement import scope mismatch" in chain but got nil.
=== RUN   TestStatementImportForeignLineTenantIsScopeMismatchRed/unmatched
    statement_import_tenant_scope_red_test.go:33:
        Error: Expected error with "billing: statement import scope mismatch" in chain but got nil.
--- FAIL: TestStatementImportForeignLineTenantIsScopeMismatchRed (0.00s)
    --- FAIL: TestStatementImportForeignLineTenantIsScopeMismatchRed/matched (0.00s)
    --- FAIL: TestStatementImportForeignLineTenantIsScopeMismatchRed/unmatched (0.00s)
```

A trusted tenant-1 batch with one tenant-2 line (matched or unmatched)
imported successfully with nil error, confirming the blocker:
`validateStatementImportScope` recorded only batch subject and observation
tenants and never inspected `batch.Lines`.

Fix (`internal/core/billing/statement_import.go:258-310`,
`validateStatementImportScope` only):

- Every line subject is validated against trusted scope and batch statement
  identity, including unmatched lines: `StoreID` must equal trusted
  `StoreID`, `ProviderAccountKey` must be authorized by
  `TrustedStatementScope.AuthorizesProviderAccount`, and when
  `TrustedStatementScope.TenantID != ""` the line `TenantID` must exactly
  equal the trusted tenant (empty and foreign both reject with
  `ErrStatementImportScopeMismatch`).
- All line tenants join the single-tenant mixing check, so a tenant-1 batch
  with any tenant-2 line fails as mixed scope.
- Tenant-scoped imports additionally require the envelope-level claim
  (`batch.Subject` plus observation subject/correlation) to carry the trusted
  tenant, so a line-only tenant claim cannot authorize the import.
- Empty tenant remains allowed only in provider-account mode
  (`TrustedStatementScope.TenantID == ""`): there the per-line exact check
  and envelope-tenant requirement are skipped and only the single-tenant
  mixing rule applies, preserving the existing provider-account authority
  contract without weakening it.
- Store/account line checks are redundant with the existing canonical
  linkage (`economics.StatementBatch.Validate`) but return the typed scope
  error if ever reached; statement/period/line-ID linkage errors keep their
  existing `ErrStatementImportInvalid` type.

Files:

- `internal/core/billing/statement_import.go` (validation only, no new type)
- `internal/core/billing/statement_import_tenant_scope_red_test.go` (new:
  matched + unmatched foreign-tenant line must fail scope mismatch with zero
  ledger rows)
- `internal/infra/billingstore/statement_import_tenant_scope_test.go` (new:
  SQLite multi-line batch with one foreign line rejects atomically, zero
  statement/line rows, valid replay green)
- `internal/infra/billingstore/statement_import_postgres_test.go` (appended:
  `TestStatementImportLedgerPostgresForeignLineTenantRejectsAtomically`,
  matched + unmatched, live PG atomic reject + valid green)

Commands/results:

- `go test -count=1 -run 'TestStatementImportForeignLineTenantIsScopeMismatchRed' ./internal/core/billing -v` now PASS (matched + unmatched).
- `go test -count=5 -shuffle=on -run 'TestStatementImport|TestBoundStatementImporter|TestStatementMatch|TestSelectedCost|TestEconomicJob|TestCorrectionRecovery' ./internal/core/billing ./internal/infra/billingstore` PASS (core 0.562s, billingstore 26.180s).
- `go test -count=1 ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/testkit/dbparity/...` PASS (economics 0.024s, billing 0.407s, billingstore 70.683s, dbparity 0.540s, dbparity/cmd 3.080s).
- `make test-db-parity-sqlite` PASS (billingstore 15.986s, all components).
- Live PG `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run 'TestStatementImportLedgerPostgres' ./internal/infra/billingstore -v` PASS (53.372s: Direct 13.69s, ConcurrentAtomicity 13.20s, ForeignLineTenantRejectsAtomically 26.47s with matched 13.75s + unmatched 12.72s).
- Live PG `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run 'TestCorrectionRecoveryPostgresDirect' ./internal/infra/billingstore -v` PASS (17.00s).
- `go build ./...` PASS; `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./pkg/lipsdk/economics/...` PASS; `go vet -tags=integration ./internal/infra/billingstore/...` PASS; `gofmt -l internal/core/billing internal/infra/billingstore` empty; `git diff --check` clean; `go test -count=1 ./internal/qa` PASS (5.160s).

Evidence:

- SQLite `TestStatementImportLedgerSQLiteForeignLineTenantRejectsAtomically/matched|unmatched` PASS: foreign import returns `ErrStatementImportScopeMismatch`, `statementImportRevisionCount == 0`, `statementImportLineRows == 0`, then exact valid batch imports with `Accepted [line-1]`, `Unmatched [line-2]`, 1 revision + 2 lines.
- Live PG `TestStatementImportLedgerPostgresForeignLineTenantRejectsAtomically/matched|unmatched` PASS with the same atomic-zero plus valid-green assertions on isolated schemas.
- Existing `TestStatementImportScopeFailsClosed`, `TestStatementImportAcceptsIndependentNormalizedFixture`, SQLite replay/conflict/rollback and PG direct/concurrent suites remain green, proving exact valid replay is unaffected.

Residuals:

- No economics linkage type change: `pkg/lipsdk/economics` still enforces structural store/account/statement/period linkage with `ErrStatementImportInvalid`; tenant authorization stays in the trusted domain scope check where caller authority exists.
- `-race` not run (Windows cgo limitation, pre-existing); concurrency covered by SQLite/PG race suites.
- Architecture/QA gates: `go test ./internal/qa` passes; archtest subset not rerun for this validation-only slice beyond QA (review baseline already records the three pre-existing failures).
