# Task 5.3 Cycle 2A2 — trusted pass-through authority in rolling A-leg reports

Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
branch `feat/b-leg-usage-economics`, base HEAD `0120bdf8` plus uncommitted
C2A1 files (preserved untouched in behavior: the C2A1 retention test
still passes unchanged). No revert/reset/stash/commit/rebase/merge/Kiro
status/PR operations.

## Design

Cycle 1 deferred any pass-through evidence to pending. This slice joins
the trusted production writer into the rolling report:

- New side-effect-free core evaluator
  `EvaluateALegPassThroughAuthority` in
  `internal/core/billing/aleg_passthrough.go` owns all pass-through
  financial proof over already-loaded facts (no SQL/I/O). The report
  shell (`reports_aleg.go`) only loads bounded facts and maps DTOs.
- Pass-through stays a distinct customer adjustment plane: validated
  lineage never enters `ALegRetailTotals.KnownSubtotal` and never
  masquerades as inference usage. `CustomerChargeKnown` still describes
  retail settlement only.
- `ALegAdjustmentRef` gains one field, `Validated bool`, so consumers
  distinguish head-admitted lineage (`true`, call may be known) from
  lineage-only evidence (`false`, call stays pending/unknown). The
  transaction ID already carries source identity (the writer sets journal
  ID equal to the canonical source key), so no other DTO growth was
  needed and no generic financial DTO was introduced.
- Composition: retail verdicts other than known return untouched (every
  Cycle 1 behavior bit-for-bit); a retail-known call with no
  pass-through involvement returns unchanged; complete validated
  pass-through authority keeps it known with lineage attached;
  incomplete evidence demotes to pending, conflicting evidence to
  unknown.
- Proof per call validates exact account/A-leg/call/currency, policy
  reference (from the call exposure), head status, posted/bound amounts,
  provider revision/LUR/valuation/input-hash, fence/head-version
  lockstep, settlement-fingerprint binding to the selected settlement
  marker, canonical operation snapshot per journal (recomputed
  integrity, sequence lockstep, full balance agreement), immutable
  journal signed shape (writer pair ledgers, sealed fingerprint), and
  telescoping agreement (marker balance delta plus signed journal deltas
  equals head posted; overflow fails the query).
- Legacy empty-A-leg journals join only through the trusted head: the
  head must name the queried scope exactly and the journal's parsed
  source lineage (account/call/LUR, revision within head continuity,
  current-revision valuation match) plus snapshot/group/shape proof
  must bind. Old journal rows are never mutated or backfilled.
- One rollback-only repeatable snapshot; heads stream per chunk,
  snapshots batch in chunk-bounded source lists, policies load only for
  headed calls; per-source snapshot retention is window-capped so
  ambiguity stays detectable. No writes, no N+1.

## TDD

Core RED: `aleg_passthrough_test.go` failed to build on undefined
`EvaluateALegPassThroughAuthority` / `ParseCostPassThroughAdjustmentSourceKey`
/ fact types. GREEN after `aleg_passthrough.go` plus the strict source-key
parser in `cost_pass_through.go` (one self-inflicted RED fixed honestly:
the adversarial table expected `customer_adjustment_unresolved` for the
book-conflict case, which correctly returns `customer_book_conflict`
by retail-mirroring precedence).

Integrated RED (all 9 new `TestALegReportPassThrough*` failed against
the unwired shell): production positive/negative revisions left the call
pending; provisional-no-journal reported known (Cycle 1: no journals
meant no deferral); adversarial drift stayed pending instead of
unknown. GREEN after shell wiring. Two fixture defects fixed honestly
during RED: the durable UNIQUE on (account, book, source_key) forbids
same-source duplication (adversarial switched to same-revision
competing valuation; same-source stays a core unit case), and dense
revision calls exceed the strict Cycle 1 IN bound through the
pre-existing per-chunk entries query (bounded proof now asserts
per-shape bounds: heads/markers/snapshots/policies ≤ chunk, entries ≤
chunk × capped per-call journal density).

## Coverage

- Production positive revision (+20) then negative correction (−10):
  provisional → pending; rev2 → known with 1 validated ref; rev3 →
  known with 2 validated refs; retail subtotal stays 60 throughout.
- Late revision: query at rev2, append rev3, later query reflects both
  refs with advancing AsOf and no finality/retirement trigger.
- Legacy: pre-C2A1 empty-A-leg journal + final head → known with
  validated lineage; the journal row still carries empty A-leg.
- Adversarial (all fail-closed, never known/zero): foreign head A-leg,
  head amount mismatch, stale future revision, competing revision,
  unrelated legacy journal — each unknown with explicit issues.
- Core unit matrix adds: parser rejections, none/pending/known paths,
  born-final heads, legacy join, 25 adversarial mutations, telescoping
  overflow failing the query.
- Cycle 1 suite (`TestALegReport`, 49+ tests) and C2A1 production
  tests (`CostPassThrough`, incl. retention) all green unchanged.

## Verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportPassThrough' ./internal/infra/billingstore/` | RED pre-wiring (9/9), PASS post |
| `go test -count=1 -run 'TestEvaluateALegPassThrough\|TestParseCostPassThrough' ./internal/core/billing/` | PASS |
| `go test -count=5 -shuffle=on` on both of the above | PASS |
| `go test -count=1 -run 'TestALegReport' ./internal/infra/billingstore/` | PASS |
| `go test -count=5 -shuffle=on -run 'TestALegReport' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 -run 'CostPassThrough' ./internal/infra/billingstore/` (x1 and x5 shuffle) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/stdhttp/` | PASS (billingstore 42.5s) |
| `go test -count=1 -run '^TestDBParity_SQLite$' ./internal/infra/billingstore/` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (71.8s) |
| `go build ./...` | clean |
| `go vet ./internal/core/billing/ ./internal/infra/billingstore/` | clean |
| `gofmt -d` on all touched/new Go files | no output |
| `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain (same
standing platform gate as Cycle 1).

## Residuals

- Pre-fix empty-A-leg journals remain in history with unvalidated
  lineage unless head-joined; nothing is backfilled.
- Mixed-charge settlements (e.g. submission fee deducted from the
  customer charge while the head posts the full charge) cannot
  telescope from the marker delta and stay pending with an explicit
  issue; pure pass-through settlements are exact.
- Provisional heads (provider cost outstanding) report pending even
  though the bound is debited; finality of the provider cost is what
  promotes the call to known.
- No schema/migration, writer-semantic, provider-COGS, HTTP, or
  unrelated changes in this slice.
