# Task 8.3 Final Independent Kiro Re-review

Review target: `feat/b-leg-usage-economics` at `46636c8f698ef4f1b2ba29f85cdd3a00742562e6`.

The current integrated test and execution evidence were inspected directly.
The approved Task 8.3 boundary remains requirements 6.5, 2.3-2.5, and
5.1-5.6: independent all-attributable operator COGS, policy-selected retail,
source-preserving allocations, explicit pass-through, and durable immutable
contribution explanations. Only this review evidence was written by this
review.

## Decisive blocker audit

### COGS included-leg refs and durable authority: closed

The integrated cross-authority test computes production
`AttributeOperatorCOGS`, asserts exact included retry/winner keys and excluded
BYOK/shell keys, then iterates only `cogs.IncludedLegKeys` (lines 648-765 of
`internal/infra/billingstore/refinement83_integrated_economics_test.go`). Each
posting amount is extracted from the immutable operator-payer charge, the
aggregate equals `cogs.KnownSubtotal`, and provider operation keys, journal
source/operation/B-leg/correction identity, currency, and exact debit/credit
entries are asserted. Shell posting fails closed with
`errors.Is(ErrUnreconciledCost)`; BYOK has no operator-payable charge; replay
and conflicting payload use the expected conflict classification.

`expectedRefs` preserves the complete immutable `ObservationRef` tuple. Before
restart, every included ref is compared against `GetCallLegUsage` (lines
861-879) and the matching `CallExplanation` leg (lines 912-940); the shell
explanation explicitly has zero observations (lines 941-942). After restart,
every included ref is compared again through both durable leg readback and the
explanation (lines 1092-1165), while provider operation fingerprints/source
keys and journal identity remain tied to the included set. The full four-leg
durable set includes the failed retry, winner, never-started shell, and BYOK
customer-payer leg.

### Pass-through durable lineage, replay, and journal equality: closed

The pass-through test uses production `RateCall` and
`ApplyCallBillingResult`, then checks the final head's policy, safe bound,
posted amount, provider semantic fingerprint, LUR, valuation, revision, input
hash, and `OriginalTransactionID` settlement linkage (lines 262-318). It
checks the durable customer operation snapshot's call source key and exact
head-bound fingerprint through `CallExplanation` (lines 327-348), exact
adjustment source/operation lineage and delta (lines 362-382), stale behavior,
and `errors.Is(ErrCostPassThroughRevisionConflict)` (lines 384-397). The
unrelated persisted allocation does not enter the 2.00 charge.

The test snapshots all relevant pre-close journal transactions through the
production `JournalTransactions` reader (lines 414-421), reopens the same
store, compares the customer operation/result, and compares the complete
journal set before and after restart. It replays the revision after reopen and
requires the same complete journal set again (lines 425-454). The comparison
helper sorts only to normalize unspecified reader order and checks every
required durable identity/content field: transaction ID, source key,
operation kind, TurnID, B-leg ID, correction group, currency, account
sequence, and every ordered ledger entry and amount (lines 604-650). Excluding
database-assigned `RecordedAt` is explicitly documented as matching the
production semantic identity boundary.

The production reader does not expose a separate pass-through-head report;
the durable customer operation fingerprint is the correct exposed head-bound
authority, so no hidden-reader claim is required.

### Execution evidence accuracy: closed

The execution matrix accurately describes all four durable Call-A legs and
their roles, including failed retry, multimodal winner, never-started shell,
and failed customer-payer BYOK (lines 65-72). It states exact per-leg refs,
pre/post-restart explanation checks, and full journal-transaction equality
(lines 76-81). The pass-through row likewise states exact operation/result,
journal, replay, and restart-set invariants supported by the test (line 80).
The validation section records the final journal-equality remediation and the
changed-file set without stale or overbroad claims.

### Previously approved scope

The retry-inclusive selector still proves the complete frozen policy/reason/
completeness/capability/observation-ref tuple and reselect/rerate equality.
The multimodal direction-qualified rates, separate proxy tariff, one-time
commercial fee, all-attributable COGS, BYOK exclusion, and conserved $16.50
allocation arithmetic/lineage remain unchanged and passing. No A-leg/session
finality dependency or production implementation change was introduced.

## Verification performed

- `go test ./internal/infra/billingstore/ -run '^TestRefinement83Integrated' -count=1 -v`:
  PASS (`5.493s`, all three integrated tests).
- `go test ./internal/infra/billingstore/ -run '^TestRefinement83Integrated' -count=3 -shuffle=on`:
  PASS (`11.367s`).
- `gofmt -l internal/infra/billingstore/refinement83_integrated_economics_test.go`:
  clean.
- `git diff --no-index --check` over the integrated test and execution evidence:
  clean.
- Placeholder and secret-pattern scan over the integrated test and execution
  evidence: clean.
- The preceding review's focused selector/allocation tests, full
  billing+billingstore packages, parity-checks, and vet were green; this final
  remediation changed only certification tests/evidence and the decisive
  integrated repeat above is green.
- Race testing remains the documented Windows toolchain skip; the prior
  `runtime/cgo`/`cgo.exe` failure is not a Task 8.3 functional failure.

## Final decision

VERDICT: APPROVED
BLOCKERS: none
TESTS: Integrated Task 8.3 tests passed once and under `-count=3 -shuffle=on`; gofmt, diff-check, and placeholder/secret scans were clean. Prior focused/full package, parity, and vet gates remain green from the immediately preceding review.
FILES_WRITTEN: .kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement8-3-review.md
RESIDUAL_RISKS: Windows race-detector coverage remains unavailable because of the documented runtime/cgo cgo.exe toolchain failure; no production-code verification is affected.
