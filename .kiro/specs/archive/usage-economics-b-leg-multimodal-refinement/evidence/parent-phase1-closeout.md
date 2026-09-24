READY_FOR_PHASE1_REVIEW

# Parent Phase 1 closeout

Date: 2026-09-19

Worktree: `go-llm-interactive-proxy-feat-b-leg-usage-economics`

Branch: `feat/b-leg-usage-economics`

Current SHA: `613498711ec9dbdd662c3221987e61fe47a03ceb`

## Scope and provenance

This closeout covers only parent specification Task 1 and its 1.1-1.4
completion criteria. It does not close or certify later parent tasks, release
cutover, or spec archival. No production source was changed for this closeout.

The historical execution baseline and review remain the provenance for the
baseline RED observations:

- `phase1-execution-baseline.md` records the independent APPROVED/VERIFIED
  baseline at observed SHA `d1847d5e2acdbb873779d94418b6347c92b0ab7d`.
- `phase1-review.md` records the prior APPROVED review and explicitly keeps
  repository-wide test, Windows cost, and race gates open.
- `phase1-producer-consumer-census.tsv` is unchanged because the current-tree
  re-inventory found no semantic or source-anchor drift.

## 1.1 Inventory and owner contract

The current-tree mechanical inventory checked every census path and exact
anchor, with no missing paths, missing anchors, duplicate path/anchor pairs,
or disposition gaps:

| Census family | Rows |
| --- | ---: |
| compaction | 2 |
| metering-journal | 4 |
| monetary-rating | 3 |
| monetary-store | 5 |
| monetary-worker | 3 |
| prompt-cache | 6 |
| provider-producer | 16 |
| report | 7 |
| sideband-finalizer | 9 |
| token-contract | 6 |
| token-reducer | 2 |
| token-runtime | 6 |
| **total** | **69** |

The result is 69 validated rows and 69 unique path/anchor pairs. Each row
retains its explicit owner, protocol family, parent-task mapping, and
certified/bridge/unsupported disposition. The exact census guard passed:

```text
go test -count=1 ./internal/archtest -run '^TestPhase1ProducerConsumerCensusIsExactAndDispositioned$'
PASS (0.288s)
```

The complete scoped Phase 1 guard set also passed:

```text
go test -count=1 ./internal/archtest -run '^TestPhase1'
PASS (1.105s)
```

## 1.2 Failure contract and downstream closure

The historical RED evidence remains honest baseline evidence; those tests are
not required to fail on the implemented HEAD. The baseline recorded the
intended failures for fixed-fee multiplication across legs, attempted
missing-provider evidence being reconciled as zero, and finalizer evidence
merging a different stream source. It also retained passing
never-started-known-zero, selection, provider-COGS, and same-A-leg continuation
characterizations.

Current downstream regressions close those historical contracts without
re-running them as required failures:

```text
go test -count=1 ./internal/core/billing/... ./internal/core/metering/...
PASS

go test -count=1 -run '^(TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED|TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED|TestRefinementNeverStartedEvidenceMayRemainKnownZero|TestRefinementRetailSelectionMatrixCharacterization|TestRefinementProviderCostMatrixCharacterization|TestPhase10RetailRatingUsesFrozenSelectionIndependentTariffAndSingleScopeFees|TestRefinement83DefaultRetailSelectsWinnerOnly|TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts|TestRefinement83CostPassThroughSettlesAcceptedProviderCost|TestRefinement83AuthoritiesStaySeparateAndCloneStable)$' ./internal/core/billing
PASS (0.501s)

go test -count=1 -run '^(TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED|TestRefinementContinuationAfterDoneUsesFreshCallState|TestRefinement51ResumableSessionLifecycleAndSettlement|TestRefinement82RuntimeResumeKeepsTerminalOwnership|TestRefinement82RuntimePreterminalCheckpointAdvancesProvider|TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable|TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates|TestRefinement41PreTerminalCheckpointDoesNotMutateMoney|TestRefinement41PreTerminalCheckpointIsDurablyVisible)$' ./internal/core/runtime ./internal/infra/runtimebundle ./internal/infra/billingstore
PASS (core/runtime 0.114s; runtimebundle 7.253s; billingstore 3.557s)
```

The V1 domain validation also passed for billing, backend-plugin, and durable
posting compatibility fixtures:

```text
go test -count=1 -run '^TestPhase1V1' ./internal/core/billing ./pkg/lipsdk/backendplugin ./internal/infra/billingstore
PASS
```

## 1.3 Durability, compatibility, and performance baseline

The frozen representative V1 call, leg, provider-cost, journal, sideband,
and finalizer payload hashes remain in `phase1-execution-baseline.md`. The
recorded hashes are:

```text
call:             5ac40cad1bd42b9eeae6129bd255a2b49d054a417c3c7e858d2c38f545b195b1
leg:              46e59709a6074e46959f106aa3593f833c2c614df5e5c7b4f5b6eb96ba0f0893
provider cost:    758fb8251b7cc1e98136a7554c2dfca1f7a1dc23a87bf9279f083a369a0cb14f
journal:          f3f9436dc71c9ca60295934c5afa098be5661c4be0ecd41196c40456cec39ab0
sideband request: bde09439586d206f97713d12fa128ef8f305ebf81b1f63180793d7a7201e1c04
sideband reply:   f35607baba6cdccbf16ccea782adfab84f9173854ba61b2d75cc4c7c831d2438
accounting frame: 86484beda8263bacef57eb6eaace944ea9b2258a4a9e5505cbae72c7b353575e
```

Compatibility and SQLite evidence is reproducible:

```text
go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./pkg/lipsdk/backendplugin/...
PASS

make test-db-parity-sqlite
PASS
```

The fresh Windows amd64 accounting benchmark used Go 1.26.6 on the same AMD
Ryzen 7 5800X host, `-benchmem -benchtime=100x -count=3`. The three samples
were:

```text
disabled: 169394/130249/136503 ns, 159253/158202/158219 B, 793/780/781 allocs
enabled:  150922/157062/186631 ns, 167377/167516/167488 B, 866/866/866 allocs
```

Disabled runs emitted zero terminal writes; enabled runs emitted one terminal
call, one leg, and one observed append per iteration. These are observations,
not invented performance targets. The historical multi-MiB traffic and decode
measurements, environment, and repetitions remain frozen in
`phase1-execution-baseline.md`.

The required repository-wide measurements were run and are explicitly not
green:

- `make test-unit` failed in the existing `internal/archtest` ratchet/budget
  baseline. The billing, runtime, storage, frontend, and other relevant
  package tests completed; the known archtest failures prevented an overall
  pass.
- `make test-cost TEST_COST_PARALLEL=1` collected the Windows anchor
  measurement (`wall_nanos=117573376000`, `cpu_nanos=772500000000`,
  `packages=330`) but the current-head measurement exited 3 because of the
  same archtest baseline failures and a load-sensitive
  `TestManagerRetire_TwoGenerationsRetireIndependently` failure. An isolated
  `-count=3` retest of that runtimehost test passed. The retained test-cost
  artifacts are under `C:\Users\Mateusz\AppData\Local\Temp\ltc-5eeb0366`.
- PostgreSQL parity and Windows race evidence were not available in this
  closeout and remain pending; the SQLite result above is the only database
  parity gate called green.

## 1.4 Architecture guardrails

The scoped guards pass and cover the requested boundaries: no provider-name
branches in core, no raw-content economic authority, one monetary writer with
no shadow posting, explicit schema/fingerprint version markers, the allowed
public-binding exception, and ordinary non-money `lipruntime.Options`.

The broad architecture and quality commands were run but are not represented
as green:

```text
go test -count=1 ./internal/archtest/...
FAIL: existing connector-overlay, direct-field-copy, request-surface,
content-free-surface, billing-import, attempt-sequence, convergence,
hexagonal, shrinkage, package/file/line-budget, and core/runtimebundle budget
ratchets (19 known baseline failures).

make quality-checks
FAIL: the same architecture baseline plus tracked lint debt and the existing
adhoc-goroutine guard findings in economic worker/observation bridge code.
```

No quality or architecture guard was relaxed, no source-file gate override
was used, and no unrelated source was modified for this closeout. The broad
failures are retained as residual repository certification work rather than
relabelled as passing evidence.

## Completion determination

| Parent item | Determination | Evidence |
| --- | --- | --- |
| 1.1 | COMPLETE | 69/69 exact census rows, current SHA, scoped inventory guard PASS |
| 1.2 | COMPLETE | historical RED contract plus current downstream billing/runtime regressions PASS |
| 1.3 | COMPLETE: baseline captured | frozen hashes, V1 compatibility, SQLite PASS, fresh accounting benchmark; full test-cost remains non-green/pending |
| 1.4 | COMPLETE: scoped guards installed | all `TestPhase1` guards PASS; broad quality/archtest baselines remain non-green |
| **Task 1** | **READY_FOR_PHASE1_REVIEW** | all four scoped completion criteria are evidenced; residual repository gates are listed above |

The parent task file marks only Task 1 and 1.1-1.4 complete. Parent Tasks
12-20 remain unchanged and unchecked. This file does not claim feature GO,
release cutover, or spec archival.

## Verification result

`STATUS: VERIFIED`

`CLAIM_TYPE: TASK`

`CLAIM: Parent extensible-usage-economics-reconciliation Task 1 is ready for
phase review at SHA 613498711ec9dbdd662c3221987e61fe47a03ceb.`

`GAPS: repository-wide architecture/quality and Windows test-cost gates are
not green; PostgreSQL and race measurements remain pending; later parent tasks
remain outside this closeout.`
