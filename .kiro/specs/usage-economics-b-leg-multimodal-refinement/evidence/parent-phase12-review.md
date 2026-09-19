## Review Verdict
- VERDICT: APPROVED
- TASK: parent-phase-12
- MECHANICAL_RESULTS: PASS fresh focused Phase12 core and storage suites with `-count=5 -shuffle=on`; PASS full affected core/compose/store/economics/metering packages; PASS SQLite parity; PASS focused live PostgreSQL retention/schema-1/envelope/generation/tenant-plan integration with `LIP_REQUIRE_POSTGRES=1`; PASS `go build ./...`, affected-package `go vet` (ordinary and `-tags=integration`), `gofmt`, `git diff --check`, marker scan and high-signal secret scan; known unrelated failures only repository archtest baseline/ratchets and metering journalstore PostgreSQL parity (`value_present` int4 versus boolean); race execution was unavailable because the Windows CGo toolchain failed before tests.
- FINDINGS: NONE. Original findings 1-9 are closed. P12-RR1 and P12-RR2 are closed. R5A/R5B/R5C and Units 1-5 remain closed. No current Phase12-owned correctness, security, architecture, persistence, dialect, or boundary blocker was found.
- REMEDIATION: None required for Tasks 12/12.1-12.4. Tasks 12.1-12.4 may be checked. Phase 13 is authorized. The unrelated baseline failures remain separately tracked and are not Phase12 regressions.
- SUMMARY: Final complete independent re-review after Kiro debug ROUND2 RR1/RR2. Durable generation/canonical replay binding, exact aggregate source binding, comparison/E-Q-P arithmetic, tolerance, selection, immutable retention, schema migration, tenant-scoped latest lookup, and SQLite/PostgreSQL behavior are all supported by fresh source inspection and tests. Phase 12 is approved for handoff to Phase 13.

# Final independent Phase 12 review

## Scope and method

The review covered the shared dirty `feat/b-leg-usage-economics` worktree at
base checkpoint `2301e083797a6d5ab1a3e6a06e44a75de1fbd656`, parent Tasks 12 and
12.1-12.4, and the complete current diff. I read the repository steering,
approved parent requirements/design/research/tasks (including C4, C5 and D5),
the updated `parent-phase12-execution.md`, the prior review evidence, and the
current production, migration and test sources. The review applied the Kiro
review procedure together with the Go audit, architecture, testing,
concurrency, error-handling, quality, security and Cordis guidance.

This reviewer changed only this evidence file. No production or test source,
migration, specification, task checkbox/status, Git state, reset/revert,
commit, rebase, merge, stash, archive or PR operation was performed. The
user's clarification was applied: file count and LOC growth were treated only
as signals; no size-only finding was raised. No concrete duplication,
incohesion, unsafe coupling or speculative abstraction was found in the
reviewed Phase12 additions.

## RR1 closure: durable schema generation and canonical replay

`internal/infra/billingstore/v2_economics_store.go` now uses
`durableReconciliationGeneration` and the single
`bindReconciliationReadGeneration` gate. Get, list and projection-rebuild
paths compare decoded wire generation with the normalized SQL discriminator
before assignment or projection binding. A field-less frozen schema-1 wire is
generation 1; an explicit schema-2 wire is generation 2; unknown values fail
closed; canonical-empty rows are accepted only for legacy generation. This
closes both schema-1-wire/SQL-generation-2 and schema-2-wire/SQL-generation-1
directions, including restart and latest paths.

Replay rows now carry the durable generation and the complete stored
projection identity. `resolveReconciliationReplay` validates the existing
canonical envelope, generation and schema-2 projection before idempotency
comparison. Canonical rows require exact canonical bytes; schema 2 requires a
nonempty matching fingerprint and never uses `result_json` as a replay
fallback. The empty-fingerprint/result-only compatibility path is restricted
to explicitly legacy canonical-empty rows. Schema-1 write bytes, fingerprint
and replay behavior remain byte-frozen and are covered by the SQLite and live
PostgreSQL fixture tests.

The concrete regression tests are in
`internal/infra/billingstore/reconciliation_generation_binding_test.go`,
`reconciliation_envelope_binding_test.go`,
`reconciliation_schema1_compat_test.go`,
`reconciliation_schema1_postgres_test.go` and the retention PostgreSQL suite.
They cover Get/List/retention/latest/restart, both generation mutations,
canonical-empty generation, raw replay poison, projection poison, exact
schema-2 replay, frozen schema-1 bytes/hash/replay and forged schema-1
generation. The live PostgreSQL run passed those cases.

## RR2 closure: exact aggregate source binding

`internal/core/billing/reconciliation_validation.go` now passes the retained
quantity and monetary comparisons into aggregate validation. It regenerates
candidate findings using the authoritative
`ReconciliationFindingsFromQuantityComparison` and
`ReconciliationFindingsFromMonetaryComparison` producers. Each retained
finding must match exactly one candidate; no ID-prefix or other heuristic is
used. `reconciliationFindingSourceEqual` compares the complete semantic tuple:
target identity, scope/direction/unit/currency/component/schema/context,
source status/reason, local/provider quality, exact expected/reported values,
canonical source observation references and valuation IDs. Exact rational
amount equality is used, so alternate decimal/rational spellings do not
weaken the binding. Zero or multiple matches fail closed, and an aggregate
with no retained source comparison is rejected.

The evaluated tolerance layer remains deliberately Unit3-owned: its policy
version reference, exact arithmetic, zero-denominator handling, status and
reason are independently rederived, without inventing a frozen policy-rule
snapshot that the approved design does not retain. Complete comparable source
terms require the producer's `ReasonNone`; missing, partial and incomparable
source shapes retain their authoritative labels. The aggregate rows are
rebuilt from retained findings and exact gross/discrepant/net/count/status
values.

Core poison and control coverage is in
`reconciliation_retention_source_binding_test.go` and
`reconciliation_retention_consistency_test.go`; durable SQLite coverage is in
`reconciliation_source_binding_test.go`; live PostgreSQL covers specialized
append, generic schema-2 append and get/list/latest/restart poison. Valid
missing, partial and incomparable controls remain green.

## Original findings 1-9 and prior round closure

1. **Unknown payer selection — closed.**
   `internal/core/billing/operator_cost_selection.go` rejects customer/BYOK,
   unallocated and unknown payer classes before candidate selection; explicit
   rule payer requirements are enforced. An absent payer is allowed only by an
   explicit policy that disables the payer requirement, which is covered policy
   behavior rather than an unknown-payer match.

2. **FX direction, completeness and native amount — closed.**
   Selection validates frozen from/to currency and exact rate identity, keeps
   the native amount, and converts only on the frozen basis. Attempted versus
   never-started/not-billable provenance is preserved, and known zero is not
   synthesized from missing observations.

3. **Tokenizer declaration compatibility — closed.**
   `reconciliation_compare.go` requires compatible tokenizer declarations for
   token units, while native units remain separate. Missing or conflicting
   declarations are incomparable rather than silently matched.

4. **Provider scope/effective measurement context — closed.**
   The comparator includes subject, period/window/reset identity, payer,
   coverage, currency, charge, tokenizer and effective qualifiers. Monetary
   validation re-runs authoritative E/Q/P decomposition and rejects
   valuation context drift.

5. **Deep retention validation and canonical ordering — closed.**
   Units 1-5, RR1 and RR2 now validate quantity cardinality and exact deltas,
   monetary terms, tolerance evaluation, nested scope, aggregate source
   provenance, envelopes, durable generations and canonical permutations.

6. **Bounded canonical exact rationals — closed.**
   Exact amounts and rational bounds use bounded arithmetic; overflow,
   denominator and canonicalization controls are tested without floating-point
   comparison.

7. **Nested references/valuations scoped to the parent — closed.**
   Retained observation, coverage, valuation and component references are
   validated for store/subject scope and exact component identity in core,
   SQLite and PostgreSQL mutation suites.

8. **Latest-retention bounded input/work and index use — closed.**
   Latest lookup requires bounded subject identity, uses subject/schema-leading
   index predicates and deterministic created-at/reconciliation/version/id
   ordering. Tenant refinement has its own additive index and remains bounded.

9. **Durable reconciliation reference for final selection — closed.**
   Final E/Q/P/S selection requires a nonempty durable reconciliation ID,
   nonzero revision and lowercase 64-hex fingerprint. Selection, comparison
   and posting planes remain separate; selection creates no posting/head or
   journal side effect.

R5A is closed: legacy schema-1 canonical bytes and fingerprint are frozen and
read/replay/restart-compatible in SQLite and PostgreSQL, while schema 2 is an
explicitly separate format. R5B is closed: quantity and monetary fields,
status/reason/cause/currency/formula, exact rational amounts, source refs,
valuation IDs and valid missing/partial/incomparable controls are rederived.
R5C is closed: the tenant-bearing additive migration and indexes are registered
for both dialects; high-cardinality tenant isolation and stable latest tie
breaking pass, and live PostgreSQL `EXPLAIN` places tenant in `Index Cond`, not
post-index `Filter`.

## Requirement, architecture and boundary coverage

The C4 comparison is pure and multimodal: complete context identity is
required before matching, exact signed/absolute deltas are separated from
missing/partial/incomparable outcomes, and quantity and monetary evidence do
not collapse into one unit. C4 E/Q/P decomposition uses exact `big.Rat`
arithmetic for Q-E, P-Q and P-E; missing terms are absent, not zero, and
suspected pricing is limited to the appropriate pricing residual. Alternative
valuations and immutable source references remain attached.

C5 remains outside Phase12: no statement/adjustment posting or head mutation
was added. The comparison, selection and posting planes are distinct. D5
retention is additive, immutable and canonical: stored full results are
validated before projection repair/replay, revisions and fingerprints are
durable, and old schema-1 bytes are preserved.

The core packages contain no provider SDK or persistence imports. Interfaces
are consumer-shaped; SQL and dialect branching remain in infrastructure. No
aliasing, nil/zero conflation, SQL fanout, scope leak, false complete/matched/
known-zero result or unbounded latest query was found. The Cordis check found
no new lifecycle, ownership, generation or cleanup edge in this synchronous
retention/selection slice.

## Migration and dialect evidence

`20260926000000_billing_reconciliation_retention.go` adds the schema
discriminator and subject-leading retention index. The additive
`20260927000000_billing_reconciliation_retention_tenant_index.go` adds the
tenant-bearing index for SQLite and PostgreSQL. Billingstore schema
registration, logical migration parity, SQLite parity and direct live
PostgreSQL tests all pass. The tenant plan assertions require the tenant
predicate in PostgreSQL `Index Cond`, reject a sequential scan/filter-only
plan, and pass with high-cardinality decoys and shared subject IDs.

## Fresh mechanical verification

The following checks were run in this final review against the shared current
worktree:

- `go test -count=5 -shuffle=on ./internal/core/billing -run 'Test(ReconciliationRetention|ReconciliationCompare|MonetaryDiscrepancy|ReconciliationTolerance|ReconciliationAggregate|OperatorCostSelection)'` — PASS.
- `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'Test(ReconciliationRetention|ReconciliationSchema1|LatestReconciliationRetention|ReconciliationEnvelopeBinding|ReconciliationGenerationBinding)'` — PASS.
- All affected package tests — PASS: `./internal/core/billing/...`,
  `./internal/infra/billingcompose/...`,
  `./internal/infra/billingstore/...`, `./pkg/lipsdk/economics/...` and
  `./pkg/lipsdk/metering/...`.
- `make test-db-parity-sqlite` — PASS for all registered components,
  including billingstore.
- With `$env:LIP_REQUIRE_POSTGRES='1'`, live integration tests for retention,
  schema-1 fixture/replay, envelope/generation binding and latest tenant plan
  — PASS. Billingstore PostgreSQL direct retention includes all Unit1-5
  poisons, both RR1 generation directions, replay poison, RR2 unbound source
  binding, schema-1 forged-generation fail-closed, tenant isolation and plan
  proof.
- `go build ./...` — PASS. `go vet` for affected packages, both ordinary and
  `-tags=integration` — PASS. `gofmt -l` over dirty Phase12 Go files produced
  no output; `git diff --check` — PASS.
- Marker scan for TODO/FIXME/HACK/XXX and high-signal secret scan over Phase12
  production files — PASS. The WSL `check-staged-secrets.sh` wrapper was not
  counted because that environment could not resolve this Windows worktree's
  `.git` file.
- `go test -race` was attempted for core/compose but the Windows CGo toolchain
  failed before package tests (`cgo.exe` exit status 2); this is an environment
  limitation, not a Phase12 test failure.

Known unrelated baselines are separated from this verdict: `internal/archtest`
continues to fail its pre-existing connector overlay, runtimebundle,
request-attempt-state, billing fixture, complexity and LOC ratchets; raw file
or LOC growth was not used as a Phase12 finding. Full
`make test-db-parity-postgres-direct` reaches billingstore successfully and
fails only at the pre-existing metering journalstore `value_present` int4 vs
boolean mismatch. No new Phase12 architecture or dialect failure was found.

## Authorization

All original findings 1-9, R5A/R5B/R5C findings, and final-round blockers
P12-RR1/P12-RR2 are closed with concrete source and SQLite/PostgreSQL evidence.
There is no current Phase12-owned blocker or unresolved spec conflict.
Tasks 12.1-12.4 may be checked, and Phase 13 is explicitly authorized.
