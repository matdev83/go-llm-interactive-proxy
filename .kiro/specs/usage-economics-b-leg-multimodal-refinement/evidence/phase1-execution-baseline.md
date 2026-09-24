# Phase 1 execution baseline and RED characterization

## Scope and provenance

- Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
- Branch: `feat/b-leg-usage-economics`
- Observed HEAD: `d1847d5e2acdbb873779d94418b6347c92b0ab7d`
- Parent spec: `extensible-usage-economics-reconciliation` (approved, `not_started`; spec baseline `5a8174161a2d4d502ee55692b9c0121ae8c74800`)
- Refinement spec: `usage-economics-b-leg-multimodal-refinement` (approved, `not_started`; spec baseline `bc7e5ce664c51d68bee771d477c53ce5de6265e1`)
- Execution issue: `#620`
- Baseline behavior captured: 2026-09-12 from pristine `d1847d5e`, before the
  Phase 1 repair tests and fixtures were added; the repair files below are
  post-baseline evidence and do not alter that provenance.
- CodeGraph status at start: existing index covered 5,844 files, 95,839 nodes and 383,357 edges; the four untracked Phase 1 files were not indexed, so their symbols were checked directly.
- No production code, monetary posting, external service, Kiro task/status metadata, commit, merge, or PR was changed.

## Task brief and acceptance IDs

This phase is the RED/baseline portion of both approved specs. It does not implement the V2 engine.

Refinement Task 1 is split as follows:

- Refinement 1.1, authority architecture checks: `2.1`, `2.2`, `2.6`, `3.1`, `3.2`, `3.5`, `3.6`.
- Refinement 1.2, retail-selection checks: `2.3`, `4.3`, `5.1`, `5.2`, `5.3`, `5.4`, `5.5`.
- Refinement 1.3, multimodal/continuation fixtures: `1.1`–`1.6`, `3.3`, `3.4`, `4.1`, `4.4`, `4.5`, `6.1`–`6.4`.

The prerequisite parent Phase 1 baseline inventory and characterization are:

- Parent 1.1: `17.1`, `15.1`, `18.3`.
- Parent 1.2: `1.1`, `1.4`, `1.5`, `3.2`, `3.3`, `3.4`, `6.3`, `8.3`, `14.3`, `18.1`, `18.2`.
- Parent 1.3: `10.6`, `11.6`, `17.1`, `17.2`, `18.4`, `18.5`, `18.6`.

## Changed tests and fixture

- `internal/core/billing/usage_economics_refinement_phase1_red_test.go`
  - `TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED`: two accepted B-legs must produce two token line items plus one call-scoped fixed fee (`603`), not the current per-leg result (`606`).
  - `TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED`: attempted work with unavailable provider evidence must remain unknown/unreconciled, with either a typed `ErrUnreconciledCost` or a nil error carrying an explicit unreconciled reason; arbitrary errors are rejected. The current result is `AmountPresent=true, Reconciled=true, Nano=0`.
  - `TestRefinementNeverStartedEvidenceMayRemainKnownZero`: passing characterization that an explicit `never_started` B-leg may remain a known zero.
  - `TestRefinementRetailSelectionMatrixCharacterization`: production `RateCall` characterization for surfaced winner, all accepted retry/loser/winner attempts, rejected/never-started exclusion, and latest accepted interrupted attempt.
  - `TestRefinementProviderCostMatrixCharacterization`: production `RateProviderCost` characterization showing independent authoritative COGS for failed retry, loser, and winner B-legs. V1 has no cost-pass-through selector; that missing API remains a parent-task blocker.
- `pkg/lipsdk/metering/testdata/refinement_phase1_multimodal_vectors.json`
  - Twelve concrete vectors cover image, audio, video, document, mixed/derived, same-A-leg continuation, preterminal revision, and late correction. Input and output directions use native units and retain transform qualifiers and B-leg lineage.
- `pkg/lipsdk/metering/usage_economics_refinement_phase1_red_test.go`
  - `TestRefinementPhase1MultimodalFixtureInventory`: passing inventory-only check; it rejects duplicate/missing vectors and token coercion. It does not certify economics or assert speculative V2 declarations; parent Tasks 2.1–2.5, 3.1–3.4 and 4.1–4.2 own the absent contract.
- `internal/core/billing/phase1_v1_compatibility_test.go` and
  `internal/core/billing/testdata/phase1_v1_compatibility.json`
  - Frozen V1 call, B-leg, provider-cost, and journal payloads verify exact
    compact JSON bytes, semantic fingerprints, identity prefixes, schema
    version, typed round trips, and replay rejection after call/attempt
    identity mutation. The fixture contains no raw prompt, authorization, or
    provider payload content.
- `pkg/lipsdk/backendplugin/phase1_v1_sideband_compatibility_test.go` and
  `pkg/lipsdk/backendplugin/testdata/phase1_v1_sideband_compatibility.json`
  - Frozen V1 finalizer request/response and accounting-frame payloads verify
    protojson presence/absence, identity, source/authority/plane, compact
    wire hashes, round trips, and protobuf field numbers. These are ABI
    compatibility fixtures, not a V2 sideband contract.
- `internal/infra/billingstore/phase1_v1_posting_schema_compatibility_test.go`
  and `internal/infra/billingstore/testdata/phase1_v1_posting_schema_compatibility.json`
  - Frozen old posting operation/source/ledger identities, migration IDs,
    stable journal/call/leg columns, indexes, and SQLite/PostgreSQL type
    assumptions. The test checks the actual provider writer and dialect DDL;
    PostgreSQL execution remains covered by the existing integration/parity
    gates when that external service is available.
- `internal/archtest/phase1_usage_economics_guards_test.go`
  - Guards the ordinary public `lipruntime.Options` boundary, provider-named
    branches in economic core zones, duplicate monetary writers/shadow
    posting, explicit version markers in fingerprint sources, and the exact
    69-row dispositioned producer/consumer census. The census guard checks
    paths, anchors, category counts, status, owner, protocol, and parent task;
    category-only roll-ups cannot pass it.
- Deleted `internal/archtest/usage_economics_refinement_phase1_red_test.go`: the prior literal assignment scan was not a behavioral proof. Its ownership boundary is now covered by the runtime behavioral RED below, with the V2 representation prerequisite recorded explicitly.
- `internal/core/runtime/usage_economics_refinement_phase1_test.go`
  - `TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED`: behavioral runtime-boundary RED. It supplies provider finalizer evidence and local estimator stream cost to `billingLegRecord`; the current V1 bridge copies the stream cost into the finalizer record, so independent source observations cannot be preserved in the single `FinalBillingEvidence` slot.
  - `TestRefinementContinuationAfterDoneUsesFreshCallState`: production `Executor.Execute`/`Collect` characterization. It completes one invocation and observes its terminal call closure, resumes the same A-leg through the returned continuation identity, completes again, and verifies distinct BillingCallIDs plus distinct terminal B-leg rows sharing the A-leg and one closure each.

## Producer/consumer inventory and disposition

| Boundary | Current producers/consumers | Owner and Phase 1 disposition |
| --- | --- | --- |
| Canonical public metering | `pkg/lipsdk/metering/quantity.go`, `fact.go`, `types.go`, `recorder.go`, `query.go`; `internal/core/metering/{aggregate,checkpoint,plane,reconcile}` | Metering SDK/core. Existing `Fact` has V1 lifecycle/boundary/correlation but `Quantity` is integer-shaped and lacks flow direction. Parent 2.1–2.5 must add V2 DTOs and serializers before refinement multimodal execution. |
| Frontend/customer ingress and egress | `internal/core/runtime/metering_checkpoint.go`, frontend protocol adapters, `internal/plugins/frontends/{openresponses,openairesponses,openaicompat,anthropic,gemini}` | Runtime/frontend owners. Existing checkpoints capture customer scope and token-shaped facts; V2 input/output observations and transform boundaries are parent 3/6 work. |
| B-leg/backend attempt lifecycle | `internal/core/runtime/{billing_call_id,billing_collector,billing_leg,attempt_session,turn_terminal,executor_settlement}.go`; `internal/core/b2bua/store.go` | Core runtime/B2BUA. `BillingCallID` is fresh per invocation; `CallLegUsageRecord` is B-leg keyed. Terminal append is currently the only billing-leg path and finalizer/stream merge is destructive. Parent 5 and refinement 4/5 must extend this boundary. |
| Provider/backend usage producers | Essential adapters under `internal/plugins/backends/{anthropic,openai*,gemini,streampeek}` and protocol packages; OpenAI/OpenResponses/Anthropic/Gemini usage decoders; `connectors/codex`, localstub and reference backends | Adapter/connector owners. Existing producer data is provider/token/sideband shaped. Parent 7/8 and refinement 7 must normalize provider-bound input and provider-origin output without placing provider-shaped objects in core. |
| Sideband and finalizer ABI | `pkg/lipsdk/backendplugin/host/session.go`, `api/backendplugin/v1`, `internal/infra/backendplugins/adapter/backend.go`, connector localstub tests | Backend-plugin ABI owners. `FinalizeBilling` currently returns token/cost event fields only; V2 neutral observation sideband is parent 7/8 work. |
| Prompt-cache and maintenance | `pkg/lipsdk/promptcache`, `internal/core/execbackend/backend.go`, `internal/plugins/backends/streampeek/prepend.go`, `internal/standardplugins/featurehost/keepwarm.go`, compaction feature packages | Runtime/feature owners. Existing prompt-cache and maintenance evidence is sideband/token-shaped; V2 resource observations must preserve scope and source. |
| Billing authority and valuation | `internal/core/billing/{records,call_usage,rating,call_rating,provider_cost,estimate,reconcile,reports}.go`; `RateCall`, `RateProviderCost`, `selectCustomerLegs`, `chargeLeg` | Core billing. `RateProviderCost` currently converts attempted missing evidence to reconciled zero; `chargeLeg` applies fixed charges per selected leg. Parent 2–5 and 9–10 own the V2 authority/retail repairs. |
| Durable metering and billing | `internal/infra/metering/journalstore/*`; `internal/infra/billingstore/{call_leg_usage_store,provider_cost_store,provider_cost_failure}.go`; `internal/infra/billingcompose`, `billingadmission`, `runtimebundle/process_billing.go` | Infra/composition. Existing stores and posting workers are V1 compatibility paths. Parent 4, 13, 14 and 17 own V2 replay, posting and fencing. |
| Reports and public binding | `internal/core/billing/reports.go`, `internal/infra/runtimebundle/process_billing.go`, `internal/infra/billingcompose`, `internal/stdhttp/admin/billing`, public runtime/billing composition | Reporting/binding owners. Current reports consume V1 token/cost records; parent 15/16 and refinement 8 certify V2 projections and no legacy writer drift. |

### Exact owner census (reproducible)

`phase1-producer-consumer-census.tsv` is the mechanical inventory for this
baseline. It has one row for each discovered production owner entry point in
the required token, sideband/finalizer, prompt-cache/compaction, metering
journal, monetary posting/rating, provider-producer, and report categories.
Each row records a path, an exact symbol anchor, protocol family, owning
boundary, and proposed disposition. The broad table above is a readable
roll-up; the TSV is the authoritative enumerated list. It is an inventory,
not evidence that any V2 disposition is implemented.

Run from the repository root to validate the census without building or
mutating the worktree:

```powershell
$file = '.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/phase1-producer-consumer-census.tsv'
$rows = Import-Csv -LiteralPath $file -Delimiter "`t"
$bad = @()
foreach ($row in $rows) {
    if (-not (Test-Path -LiteralPath $row.path)) {
        $bad += "missing path: $($row.path)"
        continue
    }
    $anchor = [regex]::Escape($row.symbol_anchor)
    if (-not (Select-String -LiteralPath $row.path -Pattern $anchor -Quiet)) {
        $bad += "missing anchor: $($row.symbol_anchor) in $($row.path)"
    }
}
if ($bad.Count -gt 0) { $bad; throw 'producer/consumer census validation failed' }
$rows | Group-Object category | Sort-Object Name | ForEach-Object { '{0}={1}' -f $_.Name, $_.Count }
"validated $($rows.Count) exact owner rows"
```

The repository guard adds the frozen cardinality and disposition checks that
the PowerShell path/anchor check cannot provide:

```powershell
go test -count=1 ./internal/archtest -run '^TestPhase1ProducerConsumerCensusIsExactAndDispositioned$'
```

It requires exactly 69 unique `path#symbol_anchor` rows with these category
counts: `compaction=2`, `metering-journal=4`, `monetary-rating=3`,
`monetary-store=5`, `monetary-worker=3`, `prompt-cache=6`,
`provider-producer=16`, `report=7`, `sideband-finalizer=9`,
`token-contract=6`, `token-reducer=2`, and `token-runtime=6`. Every row must
also have a concrete protocol family, owner, explicit status, and `parent`
task disposition. This is an exact, bounded source-anchor inventory; owner
review is still required to establish semantic exhaustiveness.

Observed census result: 69 rows; all paths and anchors validated on
2026-09-12. The TSV intentionally names the current V1 compatibility owner
for every row and records `bridge-v1`, `bridge fixture`, `bridge estimated`,
`pending`, `unsupported`, or `RED` dispositions with parent-task ownership.
Parent Phase 1.1 remains pending until its owner reviews this inventory and
adds any omitted production entry point; this report does not mark that task
complete.

## Phase status at this baseline

| Phase 1 item | Status | Evidence and boundary |
| --- | --- | --- |
| Refinement 1.1 authority/source separation | `RED / BLOCKED ON V2 CONTRACT` | Runtime non-mixing regression fails because V1 `FinalBillingEvidence` has one slot and `mergeStreamCostOntoLeg` copies stream cost into it. The test proves the destructive boundary; it cannot prove preservation of both observations until the parent V2 observation contract exists. |
| Refinement 1.2 B-leg retail/operator selection | `PARTIAL` | Production `RateCall` matrix characterizes surfaced winner, all accepted retry/loser/winner attempts, rejected/never-started exclusion, and latest accepted interrupted attempt. Production `RateProviderCost` characterizes independent COGS for all three attempted rows. Fixed-fee-once remains an intentional RED. V1 has no cost-pass-through selector, so that vector is a dependency blocker rather than a fabricated test. |
| Refinement 1.3 multimodal/continuation | `PARTIAL / BLOCKED ON V2 CONTRACT` | The 12-vector fixture inventory passes and the real `Executor.Execute`/`Collect` continuation path passes for same-A-leg/new-call/new-B-leg lineage. V2 directional measures, durable revisions, preterminal and late-correction execution remain parent-task work. |
| Parent 1.1 exact inventory | `PENDING REVIEW` | The 69-row TSV and validation command are supplied; parent task is not complete until the owner reviews the census. |
| Parent 1.2 red-first characterization | `PARTIAL` | Billing and runtime REDs are preserved with intended failure reasons; fixture and selection/continuation characterizations pass. Full parent acceptance vectors still require V2 DTOs and reducers. |
| Parent 1.3 hashes/performance/cost | `PARTIAL / MEASURED V1 ACCOUNTING BASELINE` | V1 call/leg/provider-cost/journal hashes, finalizer/sideband ABI fixtures, and old posting/dialect schema assumptions pass. Pristine-`d1847d5e` traffic and multi-MiB benchmark observations were captured, and the focused monetary-accounting-specific allocation/terminal-write baseline is recorded below. The bounded Windows `test-cost` run is reproducible but exits 1 on ratchet violations and then exits 3 in `quality-checks`; V2 durable accounting and final release gates remain pending. |

## Frozen V1 compatibility and bounded performance evidence

The checked-in V1 compatibility fixture freezes the current serialization and
identity boundary without asserting V2 behavior:

- `internal/core/billing/testdata/phase1_v1_compatibility.json` records exact
  compact-payload SHA-256 values for call
  `5ac40cad1bd42b9eeae6129bd255a2b49d054a417c3c7e858d2c38f545b195b1`, leg
  `46e59709a6074e46959f106aa3593f833c2c614df5e5c7b4f5b6eb96ba0f0893`,
  provider cost `758fb8251b7cc1e98136a7554c2dfca1f7a1dc23a87bf9279f083a369a0cb14f`,
  and journal `f3f9436dc71c9ca60295934c5afa098be5661c4be0ecd41196c40456cec39ab0`.
  The tests additionally freeze semantic fingerprints, call/B-leg/provider
  source keys, schema version, and replay rejection after identity mutation.
- `pkg/lipsdk/backendplugin/testdata/phase1_v1_sideband_compatibility.json`
  records compact-protojson SHA-256 values for the finalizer request
  `bde09439586d206f97713d12fa128ef8f305ebf81b1f63180793d7a7201e1c04`,
  finalizer response
  `f35607baba6cdccbf16ccea782adfab84f9173854ba61b2d75cc4c7c831d2438`,
  and accounting frame
  `86484beda8263bacef57eb6eaace944ea9b2258a4a9e5505cbae72c7b353575e`.
  The tests freeze presence/absence, finalizer and B-leg identity, source,
  authority, plane, dedupe key, and protobuf field numbers. Hashes are over
  compacted JSON so fixture formatting whitespace is not a wire contract.
- `internal/infra/billingstore/testdata/phase1_v1_posting_schema_compatibility.json`
  freezes the current provider COGS journal source/operation/ledger identities,
  migration IDs, usage call/leg and journal identity columns/indexes, and the
  SQLite `INTEGER` versus PostgreSQL `BIGINT` assumptions. Its compatibility
  test checks the actual provider writer and generated dialect DDL without
  requiring a live PostgreSQL service.

The pristine benchmark host was Windows amd64, Go `1.26.6`, AMD Ryzen 7 5800X
8-Core Processor. Existing V1 benchmark targets provide bounded observations
for the request/traffic and large-payload paths:

- `BenchmarkExecutorExecuteAndDrain32Deltas` (five repetitions) had a median
  `365685 ns/op`, approximately `643181 B/op`, and `2409 allocs/op`.
- `BenchmarkExecutor_TrafficDisabled` had a median `138925 ns/op`,
  approximately `161740 B/op`, and `873 allocs/op`; enabled had a median
  `166933 ns/op`, approximately `191694 B/op`, and `943 allocs/op`.
- The bounded `BenchmarkLargePayloadBaseline_Preflight` 5 MiB run (three
  `-benchtime=1x` repetitions) was `23,774,900`–`25,361,400 ns/op`,
  `22,018,912`–`22,018,928 B/op`, and `45`–`46 allocs/op`. DecodeResponses
  was `47,281,400`–`52,888,900 ns/op`, `15,731,408`–`15,733,592 B/op`, and
  `33`–`37 allocs/op`. These short runs are baseline observations, not
  performance thresholds.

The monetary-accounting-specific disabled/enabled benchmark and terminal-write
allocation observations are recorded in the appended section below. The
existing terminal tests establish single-owner behavior but are not a
throughput claim; the focused benchmark is likewise a V1 observation, not a
performance threshold.

## Baseline semantic findings

1. `internal/core/billing/records.go:56` defines `FinalBillingEvidence` with input/output/cache/reasoning/total token quantities and one `Cost`; it cannot represent independent multimodal measures, direction, qualifiers, provider aggregate coverage, or separate source records.
2. `pkg/lipsdk/metering/quantity.go:37` defines `Quantity` as `Component`, `Unit`, `Value int64`, `Present`, and optional schema. Unknown components require a schema, but there is no `FlowDirection`, exact decimal, or neutral `Observation`/`ObservationSink` contract.
3. `internal/core/runtime/billing_leg.go:199` has `mergeStreamCostOntoLeg`; if finalizer cost is absent it copies stream cost into finalizer evidence and may replace authority. This is the intentional architecture RED.
4. `internal/core/billing/provider_cost.go:37` returns a reconciled, present zero whenever provider evidence is not accepted. This conflates an attempted-but-missing observation with a never-started leg and is the intentional authority RED.
5. `internal/core/billing/rating.go:77` iterates selected legs and `chargeLeg` adds fixed components per leg. The retail RED records the required call-scoped fee behavior.
6. `internal/core/runtime/billing_collector.go:58` owns allocated B-legs and freeze state per `BillingCallID`; it is not A-leg finality. `internal/core/runtime/billing_call_id.go:9` allocates a fresh ID for each prepared invocation. Existing behavior therefore supports a separate resumed call, but has no V2 observation/revision sink.
7. `pkg/lipsdk/metering/fact.go:33` defines `Fact`, which already carries V1 perspective, boundary, lifecycle, correlation, source, authority and presence. That is useful compatibility evidence, not proof that the V2 direction/measure contract exists.
8. Existing architecture guards already keep provider SDKs and journal/rating calls out of stream handlers/core billing paths; this phase adds no provider import, store, posting, or parallel accounting engine.

## Baseline and RED commands

The following baseline commands ran before edits at HEAD `d1847d5e2acdbb873779d94418b6347c92b0ab7d`:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./internal/core/billing/... ./internal/core/metering/... ./internal/core/runtime/... ./pkg/lipsdk/metering/... ./internal/archtest/...` | 0 | All focused baseline packages passed. |
| `make test-unit` | 0 | Repository unit suite passed. |
| `make test-db-parity-sqlite` | 0 | Catalog-driven SQLite parity passed. |
| `make test-cost` | 1 | The Windows-authoritative ratchet could not measure its temporary anchor: unrelated tests hit `Za mało miejsca na dysku` / `database or disk is full`, including temporary connector builds, SQLite fixture creation and Go linker output. This is an environment/resource failure, not a Phase 1 assertion. The script retained diagnostics at `C:\Users\Mateusz\AppData\Local\Temp\ltc-80d85371`. |

Post-edit focused characterization/RED commands:

| Command | Exit | Intended/observed output |
| --- | ---: | --- |
| `go test -count=1 -run '^TestRefinement' ./internal/core/billing` | 1 | Intentional REDs: `603` expected vs `606` actual; attempted missing evidence reported `AmountPresent=true Reconciled=true Nano=0`. Selection and provider-cost matrix characterizations pass within the same invocation. |
| `go test -count=1 -run '^TestRefinementNeverStarted' ./internal/core/billing` | 0 | Explicit never-started known-zero characterization passes. |
| `go test -count=1 -run '^TestRefinement' ./pkg/lipsdk/metering` | 0 | Inventory-only multimodal fixture passes; no speculative V2 declaration test remains. |
| `go test -count=1 -run '^TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED$' ./internal/core/runtime` | 1 | Behavioral RED: current `mergeStreamCostOntoLeg` copies stream cost into finalizer evidence, losing source independence. |
| `go test -count=1 -run '^TestRefinementContinuationAfterDoneUsesFreshCallState$' ./internal/core/runtime` | 0 | Real `Executor.Execute`/`Collect` twice with terminal call closures: same A-leg, distinct BillingCallIDs, distinct terminal B-legs with positive attempt sequences. |

The bounded repair checks that produced the post-edit rows above were also
run separately to distinguish passing characterizations from intentional REDs:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestRefinementRetailSelectionMatrixCharacterization$' ./internal/core/billing` | 0 | Surfaced-winner, all-potential, rejected/never-started, and interrupted-latest selector cases pass. |
| `go test -count=1 -run '^TestRefinementProviderCostMatrixCharacterization$' ./internal/core/billing` | 0 | Failed retry, loser, and winner authoritative provider costs remain independent and sum to the expected characterization total. |
| `go test -count=1 -run '^TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED$' ./internal/core/billing` | 1 | Intended RED: current V1 returns reconciled/present zero; the assertion accepts only typed `ErrUnreconciledCost` or an explicit unreconciled result. |
| `go test -count=1 -run '^TestRefinementNeverStartedEvidenceMayRemainKnownZero$' ./internal/core/billing` | 0 | Explicit never-started known-zero characterization passes. |
| `go test -count=1 -run '^TestRefinementPhase1MultimodalFixtureInventory$' ./pkg/lipsdk/metering` | 0 | Twelve-vector inventory passes without claiming V2 economics. |

Post-edit V1 compatibility and architecture guards:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestPhase1V1' ./internal/core/billing` | 0 | Call/leg/provider-cost/journal payload hashes, semantic fingerprints, identities, schema markers, typed round trips, and replay mutation checks pass. |
| `go test -count=1 -run '^TestPhase1V1Sideband' ./pkg/lipsdk/backendplugin` | 0 | Finalizer/accounting sideband protojson presence, identity, compact hashes, round trips, field-number, and no-raw-content checks pass. |
| `go test -count=1 -run '^TestPhase1V1PostingIdentityAndDialectSchemaCompatibility$' ./internal/infra/billingstore` | 0 | Old provider posting identity, migration IDs, schema columns/indexes, and SQLite/PostgreSQL DDL type assumptions pass. |
| `go test -count=1 ./internal/archtest -run '^TestPhase1'` | 0 | Public non-money Options, core provider-branch, single-writer, version-marker, and exact 69-row census guards pass. |

Pristine-`d1847d5e` benchmark commands (run in a detached clean reference
worktree, with no feature tests hidden or redacted):

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -run '^$' -bench '^BenchmarkExecutorExecuteAndDrain32Deltas$' -benchmem -count=5 ./internal/core/runtime` | 0 | Five raw observations: `379260`, `366003`, `362797`, `365685`, `358362 ns/op`; `643176`–`643267 B/op`; `2409 allocs/op`. |
| `go test -run '^$' -bench '^BenchmarkExecutor_Traffic(Disabled|Enabled)$' -benchmem -count=5 ./internal/core/runtime` | 0 | Disabled raw `145850/138925/137051/138220/139194 ns/op`, `161722`–`161764 B/op`, `873 allocs/op`; enabled raw `166737/167547/167023/166933/166641 ns/op`, `191613`–`191711 B/op`, `943 allocs/op`. |
| `go test -run '^$' -bench '^BenchmarkLargePayloadBaseline_(Preflight|DecodeResponses)$' -benchmem -benchtime=1x -count=3 ./internal/plugins/frontends/frontendpipe` | 0 | 5 MiB bounded raw observations: Preflight `23870900/25361400/23774900 ns/op`, `22018912`–`22018928 B/op`, `45`–`46 allocs/op`; DecodeResponses `52888900/47281400/47435000 ns/op`, `15731408`–`15733592 B/op`, `33`–`37 allocs/op`. |

The fresh Windows cost command was run against the actual pristine reference
checkout, with serial execution to bound disk pressure:

```powershell
make test-cost TEST_COST_PARALLEL=1 TEST_COST_BASE_SHA=d1847d5e TEST_COST_OUTPUT_ROOT=C:\Users\Mateusz\AppData\Local\Temp\phase1-testcost-d1847-20260912
```

It exited `1`. The repository runner completed `test-unit` measurement but its
report was `passed=false` for the two policy violations
`internal/infra/conversationview` (new package `33.517s`, allowed `8s`) and
`internal/plugins/frontends/frontendpipe` (`40.940s` versus `0.653s` anchor,
`40.287s` delta, allowed `15s`). CPU, process, I/O, and wall aggregate limits
were within the report limits (`anchor` policy commit
`6dbb831885341516117034923f0c3203373aded0`; d1847 pristine head wall
`111.2667224s`, CPU `682.296875s`, 341 packages). The runner then stopped at `quality-checks`
with child exit `3` (`one or more parallel quality guardrails failed`), so no
later cost targets were measured. Artifacts are retained at
`C:\Users\Mateusz\AppData\Local\Temp\phase1-testcost-d1847-20260912`.

The full post-edit unit/parity suites were not rerun in this bounded repair;
they would include the intentional RED tests. The clean baseline results above
are the authoritative pre-edit behavior gates. No test was changed to
manufacture a failure in existing behavior.

## Missing parent prerequisites and dependency schedule

These are concrete prerequisites for later refinement phases; they are not blockers to recording this bounded RED baseline:

1. Parent 2.1–2.5 must add and validate `Decimal`, `ComponentKey`, `Observation`, source/revision references, coverage/aggregate charge contracts, immutable serializers and the `ObservationSink` port. Refinement Tasks 2–6 depend on these symbols.
2. Parent 3.1–3.4 must add provider-edge normalization, independent E/Q/P streams, reducer/revision semantics and TCK fixtures. Until then, multimodal fixture vectors cannot be executed against production DTOs.
3. Parent 4.1–4.4 must add durable observation schemas, append/replay/uniqueness and correction handling. Preterminal and late-correction vectors currently have only fixture coverage, not durable execution.
4. Parent 5.1–5.4 must bind observations to B-leg attempt identity, terminal closure and all-leg operator COGS. Current runtime has V1 call-leg records but no V2 authority envelope.
5. Parent 6.1–6.4 must define customer/provider transform hooks and bounded metadata so provider-bound input and provider-origin output are not conflated.
6. Parent 7.1–7.4 and 8.1–8.5 must extend the neutral connector ABI and migrate/certify Anthropic, OpenAI/OpenResponses, Gemini, Codex and reference producers. No connector implementation belongs in this RED phase.
7. Parent 9.1–10.5 must supply generic exact valuation, policy-selected retail B-leg selection, fixed-fee scope and bounded submission. This is the owner of the retail RED’s eventual GREEN behavior.
8. Parent 11.1–11.3, 13.1–13.5, 14.1–14.3 and 16.1–16.3 must supply allowance gauges, late corrections, admission and report/query projections without stream-time posting.
9. Parent 17.1–17.4 and 18.1–18.2 must establish the migration fence, V1 compatibility/read-only projections and retirement proof. Parent 19–20 then reruns integrated/race/cost/certification gates.

The continuation characterization is already green through the real
`Executor.Execute`/`Collect` path and terminal call-closure sink: fresh
`BillingCallID` and terminal B-leg identity are allocated per invocation while
the A-leg is reused through the returned continuation identity. It must be
retained while later B-leg/V2 work proves durable observation appendability
and revision handling after an earlier call reaches DONE; no A-leg finality
shortcut should be introduced.

## Operational concern

Before the bounded pristine cost run, the host reported `17,444,372,480`
bytes free; after it and reference-worktree cleanup, it reported
`12,944,093,184` bytes free. This run did not fail for disk exhaustion, but it
did stop on the recorded test-unit policy violations and quality child failure.
The earlier disk-exhaustion artifact at
`C:\Users\Mateusz\AppData\Local\Temp\ltc-80d85371` remains retained, as does
the current bounded report directory. The temporary pristine reference
worktree was verified clean and removed; no unrelated worktree or cache was
deleted. Do not rerun an exhaustive default matrix in this bounded pass.

## Monetary accounting-specific V1 allocation and terminal-write baseline

This focused characterization and benchmark were run on 2026-09-12 in the
feature worktree at the observed V1 baseline `d1847d5e2acdbb873779d94418b6347c92b0ab7d`.
The host was Windows amd64, Go `1.26.6`, AMD Ryzen 7 5800X 8-Core Processor,
with benchmark suffix `-16` (GOMAXPROCS 16). Each benchmark mode executed a
fixed 100 iterations per repetition and used three repetitions. The executor
used a real in-memory B2BUA store, route planner, backend open, stream receive,
terminal owner, and `lipapi.Collect` path. Enabled mode additionally wired the
existing test billing identity, credit gate, exposure admission, observer, and
terminal sink stubs. Disabled mode removed the default test sink and left all
billing seams unbound.

The characterization was run first:

```text
go test -count=1 -run '^TestRefinementAccountingBaselineTerminalWrites$' ./internal/core/runtime
exit code: 0
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime	0.091s
```

The bounded benchmark command and exact raw output were:

```powershell
go test -run '^$' -bench '^BenchmarkRefinementAccountingBaseline$' -benchmem -benchtime=100x -count=3 ./internal/core/runtime
```

```text
goos: windows
goarch: amd64
pkg: github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime
cpu: AMD Ryzen 7 5800X 8-Core Processor
BenchmarkRefinementAccountingBaseline/Disabled-16         	     100	    157084 ns/op	         0 terminal-call-appends/op	         0 terminal-leg-appends/op	         0 terminal-observed-legs/op	  150228 B/op	     785 allocs/op
BenchmarkRefinementAccountingBaseline/Disabled-16         	     100	    135233 ns/op	         0 terminal-call-appends/op	         0 terminal-leg-appends/op	         0 terminal-observed-legs/op	  149343 B/op	     773 allocs/op
BenchmarkRefinementAccountingBaseline/Disabled-16         	     100	    140906 ns/op	         0 terminal-call-appends/op	         0 terminal-leg-appends/op	  149312 B/op	     773 allocs/op
BenchmarkRefinementAccountingBaseline/Enabled-16          	     100	    154474 ns/op	         1.000 terminal-call-appends/op	         1.000 terminal-leg-appends/op	         1.000 terminal-observed-legs/op	  158189 B/op	     855 allocs/op
BenchmarkRefinementAccountingBaseline/Enabled-16          	     100	    222516 ns/op	         1.000 terminal-call-appends/op	         1.000 terminal-leg-appends/op	         1.000 terminal-observed-legs/op	  158094 B/op	     855 allocs/op
BenchmarkRefinementAccountingBaseline/Enabled-16          	     100	    209641 ns/op	         1.000 terminal-call-appends/op	         1.000 terminal-leg-appends/op	         1.000 terminal-observed-legs/op	  158132 B/op	     855 allocs/op
PASS
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime	0.197s
exit code: 0
```

The `terminal-*` metrics are incremented only inside the actual observer and
`TerminalUsageSink.AppendLeg`/`AppendCall` callbacks. Across all three
repetitions, enabled execution therefore observed and appended exactly one
B-leg plus one call closure per executed call; disabled execution performed
zero monetary callbacks while still opening and draining the backend. The
allocation and latency values include the current V1 executor, in-memory
B2BUA store, fixed stream, and callback-counter instrumentation. The sink is
an in-memory test stub, so this does not measure durable database I/O,
multi-MiB traffic, V2 observations/revisions, or a production throughput
threshold. Those limitations keep this result a reproducible accounting
allocation/write baseline rather than a claim of final accounting performance.

The optional focused race attempt was not usable on this Windows host:

```text
go test -race -count=1 -run '^TestRefinementAccountingBaselineTerminalWrites$' ./internal/core/runtime
runtime/cgo: C:\Users\Mateusz\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.6.windows-amd64\pkg\tool\windows_amd64\cgo.exe: exit status 2
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime [build failed]
FAIL
exit code: 1
```

No race test executed; this is recorded as a Windows cgo/toolchain build
limitation, not as passing race evidence.
