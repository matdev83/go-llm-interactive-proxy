# Task 5.3 Cycle 2B — provider COGS authority in rolling A-leg reports

Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
branch `feat/b-leg-usage-economics`, base HEAD `0120bdf8` plus preserved
C2A1/C2A2 changes. No revert/reset/stash/commit/rebase/merge/Kiro
status/PR operations.

## Design

Cycle 1 counted every scope leg provider-pending. This slice proves
per-leg operator COGS from the trusted revision writer:

- New side-effect-free core evaluator
  `EvaluateALegProviderLeg` in `internal/core/billing/aleg_provider.go`
  owns all provider proof over already-loaded facts. The report shell
  only loads bounded facts, maps DTOs, and accumulates scope totals.
- DTO extension is minimal: `known`/`unknown` leg statuses, per-leg and
  per-contribution `ProviderCost`/`ProviderOperationKey`/
  `ProviderTransactionID`, and scope totals with native-currency
  `KnownSubtotal` plus known/pending/zero/unknown counts. Zero keeps
  the exact `ZeroBasis` (`never_started_not_billable`,
  `rejected_not_payable`, `provider_recorded_zero`,
  `provider_excluded_non_payable`). No generic provider accounting API.
- Nonzero known requires the exact current revision-authority head plus
  its matching immutable journal chain plus posting and execution fence
  agreement. A head alone, a free-form valuation identity alone, or a
  legacy aggregate without revision cutover never suffices (legacy
  aggregates stay pending).
- Corrections net only canonical-rooted immutable signed journals with
  linear latest-to-prior links; every journal is consumed exactly once
  and the chain telescopes from zero to the head amount. Missing links,
  branches, strays, and amount mismatches fail closed; absent snapshots
  stay pending; overflow fails the query.
- Chained deltas carry the root's correction group (the writer clears
  the group on the delta and the generic correction gate rebinds it to
  the target's group before commit — verified against
  `prepareCorrection`, which also rejects second reversals at post
  time). The evaluator requires group equality with the root, which
  additionally covers legacy-adopted cutover chains whose deltas
  inherit the legacy root's group.
- Known zero requires either an evidence-free never-started/rejected
  leg, a recorded zero head with a fully validating reversal chain, or
  a validated revision-authority exclusion fence with no head. Pending
  work (legacy queue, any non-processed state) and pending V2 revision
  state (per head/fence key) always take precedence over zero and over
  older heads.
- Every attributable B-leg is evaluated independently of retail and of
  surfaced/winner selection; provider verdicts never touch retail
  subtotals or the adjustment plane.
- One rollback-only repeatable snapshot; leg enumeration, heads, both
  fence kinds, work states, revision states, and journal-bound
  snapshots all stream in chunk-bounded statements with per-source
  retention caps. No per-call or per-leg statements. Read-only.

## TDD

Core RED: `aleg_provider_test.go` failed to build on undefined
`EvaluateALegProviderLeg` and fact types (plus three fixture defects
fixed honestly: shared-array reversal aliasing, child fence lineage,
and a missing child subject account key). GREEN after
`aleg_provider.go` plus DTO statuses/lineage/totals.

Integrated RED: all 16 new `TestALegReportProvider*` failed against
the unwired shell (pending where authority belongs, known where
pending belongs, missing totals). GREEN after shell wiring with two
genuine behavior fixes from RED evidence:

- A debug test (since removed) proved chained deltas durably carry
  the root's group, not an empty one — the evaluator's group rule
  was corrected to root-group equality, which also models cutover
  chains.
- Chain assembly was restructured to classify structure (roots,
  links, strays) before content proof, so stray lineage fails closed
  even when its snapshot is absent.

Two fixture defects fixed honestly: the durable UNIQUE on
(account, book, source_key) forbids same-source duplication (covered
at core level; integrated uses same-revision competing valuation),
and journal immutability triggers forbid UPDATE drift on books
(conflicting journals are planted raw the way writers reject them).
The bounded proof asserts per-shape IN bounds because the
pre-existing presence query scales with the page union, not the
chunk; provider shapes stay chunk-bounded.

## Coverage

- Production nonzero (rev1 50 + work claim): leg known with exact
  cost/operation/transaction lineage; retail subtotal untouched;
  contributions mirror; work row processed.
- Production correction (rev2 40, delta −10): later query reflects 40
  with both immutable journals retained and head/fence at rev 2/2/2.
- All attempts (failed 3, loser 5, winner 7): every leg known,
  subtotal 15 — never surfaced-only.
- Recorded zero (payer reversal to 0): known_zero with exact basis.
- Exclusion (non-payable fence): pending while work is pending, then
  known_zero excluded after the production claim.
- Never-started: appended leg (pending work) stays pending;
  work-free row with no evidence reports known_zero with exact basis.
- Pending revision: applied+claimed rev1 reports known; an
  in-flight newer revision row keeps the leg pending with no
  finality trigger.
- Adversarial (all fail-closed, never known/zero): stray journal,
  head amount mismatch, stale posting fence, foreign execution
  owner, branched chain, book conflict, subject call mismatch —
  each unknown with explicit issues and zeroed leg totals.
- Core unit matrix adds: parser-free pure proof of none/pending/
  known/born-final/recorded/excluded/outcome-zero/legacy-pending,
  child subjects, 35+ adversarial mutations, missing-snapshot
  pending, and telescoping overflow failing the query.
- Cycle 1 suite (`TestALegReport` incl.
  `ProviderNeverStartedStaysPending` via pending work rows) and all
  C2A production tests green unchanged.

## Verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportProvider' ./internal/infra/billingstore/` | RED pre-wiring (16/16), PASS post |
| `go test -count=5 -shuffle=on -run 'TestALegReportProvider\|CostPassThrough'` | PASS |
| `go test -count=1/-count=5 -shuffle=on -run 'TestEvaluateALegProvider' ./internal/core/billing/` | PASS |
| `go test -count=1 -run 'TestALegReport' ./internal/infra/billingstore/` | PASS |
| `go test -count=5 -shuffle=on -run 'TestALegReport' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 -run 'TestRefinement43Provider\|TestSQLiteProviderCost\|TestRefinement43EconomicWorker\|TestSQLiteApplyProviderCost'` (writer revision/fence) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/stdhttp/` | PASS (billingstore 45.8s) |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (69.6s) |
| `go build ./...` | clean |
| `go vet ./internal/core/billing/ ./internal/infra/billingstore/` | clean |
| `gofmt -d` on all touched/new Go files | no output |
| `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain (same
standing platform gate as Cycle 1).

## Residuals

- Legs whose calls never close stay outside the report scope (the
  report enumerates closure-joined legs); their provider evidence
  appears when their calls enter scope. Pre-closure accrual itself is
  untouched.
- Legacy zero-amount aggregates (fence + snapshot, no journal/head)
  report pending until a revision cutover confirms them.
- An exclusion concurrent with newer unheaded revision work for the
  same lineage relies on fence head-key state overlap; unmapped
  future work keys stay a documented gap.
- Headless legs with pending V2 work but no head key yet (never
  processed) cannot join revision state by leg; the legacy work queue
  still pends them.
- No customer settlement/pass-through logic changes, schema/
  migration, HTTP, or writer-semantic changes in this slice.

## C2R1 remediation — complete current-revision authority envelope

The independent Cycle 2 review (`refinement5-3-cycle2-review.md`)
rejected four provider authority gaps; all four are remediated below
with no schema/migration change. The writer change is the minimal
atomic execution-fence advance on applied revisions.

### 1. Execution-fence owner envelope carried and advanced

- Writer (`provider_cost_revision_store.go`): every applied revision
  now advances the existing execution fence to the new current owner
  envelope (authority, kind, head key, revision, input hash,
  fingerprint, operation, transaction) in the same transaction —
  both in the found-head branch and in the cutover/promotion branch
  (applied case only; replays, stale, and ignored exclusions never
  advance). Replay/stale purity is guarded by dedicated tests.
- Report: the shell adapter carries every persisted owner field;
  core requires exact scope, subject kind, head key, evidence
  revision, input hash, fingerprint (bound to the posting fence),
  and current operation/transaction agreement with head and posting
  fence. The fence counter relation is explicit: writer-positive
  and CAS-monotonic per lineage with no cross-counter equality to
  head-local counters (fresh inserts, cutover recoveries, and
  upgrades seed each counter at one independently; a born-revision
  head at fence 1 already coexists with execution fence 2).
- RED: `TestC2R1RevisionAdvancesExistingExecutionFence`,
  `TestC2R1ZeroDeltaRevisionAdvancesExecutionFence`,
  `TestC2R1ExclusionPromotionAdvancesExecutionFence` failed on the
  stale owner envelope; core `stale execution owner *` plus fence
  zero/legacy-authority mutations and integrated
  `StaleExecutionOwnerUnknown` failed as known.

### 2. Current operation anchored; zero-delta representation defined

- The chain terminal must equal head and fence last transaction, and
  the root must equal a non-empty head original transaction
  (promotion-born heads legitimately carry an empty original
  transaction with no chained history to anchor, which the rule
  allows explicitly).
- When the current operation moved without a monetary delta, its
  immutable operation snapshot proves it: keyed by head
  last-operation, fingerprint-bound to the posting fence,
  recomputed integrity, zero sequence, identical balance snapshots.
  The transaction pointer still binds the last monetary journal, so
  no transaction is ever fabricated. Absent snapshots stay pending;
  malformed ones are unresolved.
- RED: integrated same-amount revision plus core diverged-operation
  and root/terminal drift cases; a production same-amount revision
  test asserts exact operation, transaction, root, and status
  lineage with journal count unchanged.

### 3. Exact subject identity at the report seam

- The shell carries durable `subject_kind`/`subject_id` beside the
  decoded subject JSON; core requires all three to agree (kind
  equality plus recomputed subject identity: B-leg by B-leg,
  provider-charge by charge).
- Ancestry is required, not merely mismatch-checked: exact store,
  account, A-leg, call (either call field), and B-leg; charge
  children additionally need charge/account identity with the exact
  child posting lineage. Missing ancestry is unresolved. `StoreID`
  joined the shared authority scope (retail evaluators ignore it).
- RED: core missing-ancestry/kind-ID drift mutations plus
  integrated missing-A-leg, kind-column drift, child-identity drift,
  and child-drift mutations; the pre-existing foreign-call case
  continues to pass.

### 4. First writer-recorded payable zero

- A first payable zero (zero-net operator charges through the real
  writer: head, both fences, and operation snapshot with no monetary
  journal) reports `known_zero` with the recorded basis only after
  the complete current zero-delta proof above; absent snapshots stay
  pending and malformed ones are unresolved.
- RED: `TestEvaluateALegProviderFirstZeroKnown` (unknown),
  `FirstZeroMissingSnapshotPending` (unknown), and integrated
  `TestALegReportProviderFirstZeroKnown` (unknown).

### C2R1 verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestEvaluateALegProvider\|TestALegReportProvider\|TestC2R1'` (core + store) | PASS |
| `go test -count=5 -shuffle=on` on the same sets | PASS |
| `go test -count=1 -run 'TestALegReport\|CostPassThrough'` | PASS |
| `go test -count=5 -shuffle=on -run 'TestALegReport\|CostPassThrough'` | PASS |
| writer revision/fence suites (`TestRefinement43`, `TestSQLiteProviderCost`, `TestSQLiteApplyProviderCost`, `TestPostJournalTransaction`) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/stdhttp/` | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (68.8s) |
| `go build ./...` | clean |
| `go vet ./internal/core/billing/ ./internal/infra/billingstore/` | clean |
| `gofmt -d` on all touched/new Go files | no output |
| `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

### C2R1 residuals

- Same-revision-different-hash adoption on upgradeDBs with a seeded
  execution fence can report unknown until the next revision heals
  the owner envelope; the writer CAS rules make this transient.
- Cutover chains with a legacy root keep the legacy group binding
  through `prepareCorrection` inheritance (verified against the
  generic correction gate); the evaluator requires group equality
  with the root rather than emptiness.
- Promotion-born heads carry an empty original transaction ID by
  writer construction; root anchoring applies only to non-empty
  original transactions.

## C2R2 remediation — additive multi-child authority plus zero-source hardening

The Cycle 2 re-review (`refinement5-3-cycle2-review.md`, 2026-09-19)
closed the four C2R1 findings on single-head paths with the mechanical
matrix green, and rejected one new Important finding (multiple trusted
provider-charge children collapse to `unknown`) plus one Moderate
zero-snapshot hardening gap. Both are remediated below with no
schema/migration change.

### Chosen authority model

- The shared B-leg execution fence stays the execution-level
  revision/cutover gate. Each provider-charge child's existing
  posting fence plus head plus immutable operation/journal chain is
  its durable per-child owner proof.
- The shared gate names the most recently applied child. The report
  validates its complete owner envelope exactly against the child it
  names and requires that owner child to be present in the evaluated
  set; it never requires the one shared gate to equal every child.
- Every trusted child evaluates independently through the same
  single-head unit proof (subject, head, own posting fence, own
  journal partition by correction group, own snapshots, terminal and
  current-operation anchoring), then the leg aggregates atomically
  with checked summation in deterministic charge order.
- Base B-leg subjects remain a single-subject mode. Aggregate plus
  child heads on one leg fail closed as mixed modes, as do duplicate
  charge identities, over-cap child fanout, and any group outside
  the evaluated head keys (legacy overlap can never double-count).
  The writer itself excludes cross-kind second owners, so mixed
  coexistence is writer-unreachable except by drift.
- Writer: every applied revision — found-head advance, insert-head
  (including second and later children), and applied cutover/
  promotion — advances the shared gate to that revision's owner
  envelope in the same transaction. Replay, stale, ignored, and CAS
  conflicts never mutate it. The one pre-existing test asserting
  first-claim ownership after a second child now asserts
  latest-applied ownership per the flagged defect.

### Coverage

- Production two-child leg (30 + 20 through real
  `ApplyProviderCostRevision`, second child after the first):
  leg known 50 with exact per-child lineage, shared gate on the
  latest child, retail untouched — on SQLite and direct
  PostgreSQL (`TestPostgresProviderTwoChildrenKnown`).
- Per-child corrections (25 after −5; same-amount 20 with
  snapshot-proved current operation), head/fence/operation
  advancement per child, permutation-stable aggregate and lineage
  order, duplicate-rotation unknown, child fanout-cap unknown,
  overflow failing the query.
- Pending/stale/malformed/conflicting children (missing snapshots,
  in-flight revision state, stray groups, gate-owner mismatch)
  keep the leg pending/unknown with explicit issues and empty
  child lineage: no partial subtotal, unknown never zero.
- Children on failed and loser B-legs contribute independently
  across legs (3 + 5 + 7 = 15) with no surfaced/winner selection.
- Zero-source hardening: the report derives the expected canonical
  revision source from head account/call/key/revision/hash and
  requires it plus account, operation kind, revision-bound
  fingerprint, recomputed integrity, zero sequence, and unchanged
  balances on every current-operation snapshot. Derivation
  equality is proved against the writer function itself
  (`TestAlegProviderRevisionSourceMatchesWriter`); empty, foreign,
  wrong-revision, nonzero-sequence, balance-movement, and
  fingerprint drifts each resolve unknown at core level, with
  fence-drift coverage integrated.

### C2R2 verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestEvaluateALegProvider\|TestAlegProvider\|TestALegReportProvider\|TestC2R1'` (core + store) | PASS |
| `go test -count=5 -shuffle=on` on the same sets | PASS |
| `go test -count=1 -run 'TestALegReport\|CostPassThrough'` | PASS |
| `go test -count=5 -shuffle=on -run 'TestALegReport\|CostPassThrough'` | PASS |
| writer revision/fence suites (`TestRefinement43`, `TestSQLiteProviderCost`, `TestSQLiteApplyProviderCost`, `TestPostJournalTransaction`) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/stdhttp/` | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (70.0s) |
| `go test -tags=integration -count=1 -run 'TestPostgresProviderTwoChildrenKnown'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available |
| `go build ./...` | clean |
| `go vet ./internal/core/billing/ ./internal/infra/billingstore/` | clean |
| `gofmt -d` on all touched/new Go files | no output |
| `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

RED discipline: writer gate-advance, stale owner/subject/root/
terminal/first-zero suites failed pre-fix; multi-child positive
paths are characterization coverage on the new rules (same honest
standard as prior slices), with duplicate/mixed/partial/cap cases
failing closed by construction. Same-revision head-key rotation is
writer-allowed and report-unknown by design, never latest-wins.

### C2R2 residuals

- Fence-only child exclusions without heads do not join revision
  state by leg (no head key exists yet); the legacy work queue
  still pends such legs, and any later payable head joins normally.
- Child-lineage fences without heads are excluded from proof facts
  but still contribute head keys to the revision-state join.
- A same-revision head-key rotation leaves two heads for one
  charge; the report resolves unknown rather than guessing the
  survivor.

## C2R3 follow-up — bounded fact loading plus aggregate known_zero

Two concrete blockers from the final review are remediated below
with no schema/migration change.

### Blocker 1 — deterministic per-identity cap+1 windows

`loadALegProviderHeadsTx` and `loadALegProviderPostingFencesTx`
materialized arbitrary rows per call; the core child cap of eight
only fired after full materialization. Both loaders plus execution
fences and work now stream through deterministic `ROW_NUMBER`
windows (`PARTITION BY` call/subject or call/lineage, ordered by
head key, portable SQLite and PostgreSQL): at most one durable row
per identity is resolvable, so cap+1 rows per identity prove
overflow deterministically. Any overflow unions into the existing
per-call over-cap set, so an over-cap leg resolves unknown with an
explicit `provider_cost_fanout` issue and no partial subtotal.

The design is provably truncation-safe: every ambiguity class
shares a window partition (duplicate heads, duplicate fences,
rotation), so overflow always flags exactly the legs that cannot
be proven; distinct-identity rows load fully and the core decides
them. Legitimate states never exceed one row per identity, so the
windows only ever engage on drift or rotation. Snapshot, journal,
entry, and revision-state loading were already batch/window
bounded and are unchanged apart from the shared over-cap union;
per-call IN lists stay chunk-bounded everywhere.

RED: the rotation-duplicate integration test moved from the core
`customer...unresolved` issue to the loader `provider_cost_fanout`
issue; the direct loader test
(`TestALegProviderHeadLoaderBounded`) proves 3 same-subject heads
retain exactly the first by head key with overflow flagged, plus
a clean single-head load. A debugging detour confirmed the
window SQL returns cap+1 rows while Go retention keeps cap rows
(the temporary debug test was removed).

### Blocker 2 — aggregate known_zero

When every evaluated payable child is proven zero, the leg now
reports `known_zero` with the deterministic `all_children_zero`
basis (per-child exact bases stay visible in the entries),
`ZeroLegs` increments, `KnownLegs` does not, and cost stays an
exact zero. Mixed zero plus nonzero children resolve known with
the correct checked sum. Per-child entries carry the current
operation and last monetary transaction; the aggregate tracks
the shared gate's latest pointers.

RED: core all-zero/mixed tests failed as known-with-zero before
the rule; integrated one-zero, multi-zero, and mixed tests failed
as unknown/pending before the writer paths and aggregation
existed. Production coverage: single reversed-to-zero child,
two reversed-to-zero children, and mixed 25-plus-zero on SQLite;
one-zero, multi-zero, and mixed legs through the real writer on
direct PostgreSQL (`TestPostgresProviderZeroChildren`), plus a
direct PostgreSQL two-child known test and rotation over-cap
test.

### C2R3 verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestEvaluateALegProvider\|TestAlegProvider\|TestALegReportProvider\|TestC2R1'` (core + store) | PASS |
| `go test -count=5 -shuffle=on` on the same sets | PASS |
| `go test -count=1 -run 'TestALegReport\|CostPassThrough'` | PASS |
| `go test -count=5 -shuffle=on -run 'TestALegReport\|CostPassThrough'` | PASS |
| writer revision/fence suites (`TestRefinement43`, `TestSQLiteProviderCost`, `TestSQLiteApplyProviderCost`, `TestPostJournalTransaction`) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/stdhttp/` | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (71.2s) |
| `go test -tags=integration -count=1 -run 'TestPostgresProvider'` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available |
| `go build ./...` | clean |
| `go vet ./internal/core/billing/ ./internal/infra/billingstore/` | clean |
| `gofmt -d` on all touched/new Go files | no output |
| `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

### C2R3 residuals

- Flood-scale drifted subject populations (thousands of distinct
  junk subjects in one call) materialize proportionally to
  distinct identities; verdicts stay fail-closed (any ambiguous
  partition flags the call) but memory follows the drift. This is
  outside the trusted-writer threat model.
- Snapshot/journal/entry/retention caps are unchanged; only
  heads/fences/work gained per-identity windows, reusing the
  existing over-cap verdict path.
- `all_children_zero` is uniform for one or many zero children;
  the single child's recorded/excluded basis stays visible in its
  entry only.

### C2R4 — provider fact-loader boundedness

The remaining review blocker: per-identity loader windows could
not bound totals, so nine distinct children (nine heads, fences,
journals) materialized before the core eight-child cap fired, with
snapshot/revision loads keyed off that uncapped set; and the head
partition omitted B-leg/kind, so the same charge ID on two B-legs
collided into a false overflow.

Loader design now (`internal/infra/billingstore/reports_aleg.go`,
shell only, no core changes):

- SQL head window partitions by exact identity
  (`call_id, subject_kind, subject_id, subject_json`); the stored
  subject JSON carries the owning B-leg, so the same charge ID on
  distinct legs never collides. The Go retention partition in
  `loadALegProviderHeadsTx` mirrors the SQL window exactly at
  string level (no decode cost). Portable `ROW_NUMBER()` on both
  SQLite and PostgreSQL, unchanged mechanics.
- `groupALegProviderHeads` owns the total per-B-leg bound that SQL
  cannot partition portably: heads group by decoded exact
  identity (call, owning B-leg, subject kind, charge); at most
  eight heads per B-leg are examined with the ninth probe row
  proving overflow. Legs beyond the bound, or with a duplicated
  exact identity, mark head-over-cap and keep no rows downstream.
  Undecodable subjects fail the query like any undecodable
  durable fact.
- `retainALegProviderFacts` narrows heads, posting fences
  (lineage match on retained usage-leg keys plus child
  lineages), and journals (call-scoped rows stay) to enumerated
  legs surviving the bound, for calls not already over cap.
  Revision-state keys (`providerHeadKeys`) and snapshot sources
  (`providerSnapshotSources`) derive from the retained maps only,
  so no facts are derived for discarded rows. Fence-only
  exclusions and never-started legs still resolve: retention is
  based on the over-cap set, not on head presence.
- The per-leg loop skips over-cap legs before evaluation:
  unknown with `provider_cost_fanout` (one issue per call, every
  over-cap leg still counts unknown), no partial subtotal or
  children. Evaluation consumes the retained maps, so discarded
  rows cannot fail decoding or pend retained legs.

RED: `TestALegReportProviderChildrenCapUnknown` moved from core
`provider_cost_unresolved` to loader `provider_cost_fanout`, with
a planted pending revision-work row for a discarded head proving
no derived state is consulted (it resolved pending before the
fix); new `TestALegReportProviderSameChargeTwoLegsKnown` failed
as unknown plus fanout under the coarse partition, confirmed RED
on a temporary partition revert alongside the cap test. A debug
detour caught the Go retention partition omitting the B-leg while
the SQL window already distinguished it (temporary debug tests
removed). A GREEN-phase regression caught
`TestALegReportProviderExcludedZero` going pending when
head-gated retention dropped a fence-only exclusion's facts;
retention now keys on the over-cap set.

Production coverage (real writer, SQLite): nine distinct children
fanout with empty children and no pending issue; same charge ID
on winner plus failed legs resolving known with checked 30/20
sums and per-leg lineage; rotation duplicate still fanout;
two-child, corrections, partial-pending, gate-owner, zero,
mixed, and loser-leg suites unchanged. Direct PostgreSQL twins
added (`TestPostgresProviderSameChargeTwoLegsKnown`,
`TestPostgresProviderNineChildrenFanout`).

### C2R4 verification (all exit 0 unless noted)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportProviderSameChargeTwoLegsKnown\|TestALegReportProviderChildrenCapUnknown\|TestALegReportProviderDuplicateChildUnknown\|TestALegReportProviderTwoChildrenKnown'` | PASS |
| `go test -count=5 -shuffle=on` on those plus `TestALegReportProviderExcludedZero`, `TestALegReportProviderRecordedZero` | PASS |
| `go test -count=1 ./internal/infra/billingstore/` (full package) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/stdhttp/` | PASS |
| `go vet ./internal/infra/billingstore/` and `-tags integration` vet | clean |
| `go build ./...` | clean |
| `gofmt -d` on touched test/store files, `git diff --check` | clean |
| `go test -tags=integration -run 'TestPostgresProvider'` (`LIP_REQUIRE_POSTGRES=1`) | NOT RUN here: configured DSN unreachable (`dial tcp ... i/o timeout`); pre-existing PG tests fail identically, so this is environmental. New PG tests compile (vet clean) and must run in CI with live PostgreSQL. |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

### C2R4 residuals

- Transient SQL rows still materialize per identity (cap+1 = 2)
  before the Go per-leg bound drops the excess; row count
  follows distinct subjects per call under the trusted-writer
  model (same standing residual as C2R3). SUPERSEDED by C2R5
  below: SQL now bounds every leg to cap+1 rows before Scan.
- The string-level Go/SQL partition relies on deterministic
  writer JSON marshaling; semantically identical subjects with
  divergent serialization fall through to the decoded exact
  grouping backstop, which still flags them over cap.
- Call-scoped (B-leg-less) journals are retained for all legs of
  the call; per-leg proofs partition them in core as before.

### C2R5 — hard SQL-result bound before Scan

The remaining review blocker: per-identity `ROW_NUMBER` plus Go
grouping still returned unbounded head/fence rows from SQL for
many distinct valid charge IDs. Every provider loader now
enforces a deterministic TOTAL cap+1 = 9 rows per exact
(call, B-leg) scope in SQL, before Scan/materialization, on both
dialects. Core is unchanged.

Query/window stages (`internal/infra/billingstore/reports_aleg.go`,
shell only):

- Each loader computes two windows over the full chunk scope in
  a single pass, filtered together: `identity_rn` (per exact
  identity: heads `(call, kind, id, subject_json)`; fences
  `(call, lineage)`; work `(call, leg key)`) alongside `leg_rn`
  (per exact `(call, B-leg)`), both `ORDER BY` head/leg key for
  determinism. Filters: `identity_rn <= 2` (heads, fences),
  `leg_rn <= 9` (heads, both fence kinds), work `rn <=
  perCallCap+1 AND leg_rn <= 2` (UNIQUE leg key: one legitimate
  row plus probe).
- B-leg extraction in SQL: heads via the durable subject payload
  (`providerHeadBLEGExpr`: SQLite `json_extract(...,
  '$.b_leg_id')`, PostgreSQL `subject_json::jsonb ->>
  'b_leg_id'`); posting/execution fences via the
  `call:b-leg[:provider-charge:id]` lineage
  (`providerLineageBLEGExpr`: portable `substr/length` suffix,
  first-segment split with SQLite `instr` vs PostgreSQL
  `split_part`); work partitions by `usage_leg_key` directly (no
  extraction needed). Kind/identity partitions are preserved, so
  cross-kind/cross-leg same IDs neither collide nor hide
  ambiguity; missing keys coalesce into one bounded pseudo-leg
  that downstream decoding fails closed.
- Explicit probe metadata: the ninth row of a leg (`leg_rn`,
  selected as `LegRn` on every chunk row) proves overflow; Go
  marks the exact leg unknown with `provider_cost_fanout` from
  nine materialized rows (never truncate into known, never a
  partial subtotal). Every loader hard-fails the query if SQL
  ever returns `leg_rn` beyond the bound, so the bound is an
  enforced invariant, not query-text hope. The decoded Go
  grouping stays as a dialect-proof backstop (counts still fail
  the right leg closed if extraction ever degraded).
- Derived revision/snapshot/journal queries consume only the
  C2R4 retained-leg maps (unchanged); journals stay per-call
  capped in SQL plus retained-leg filtered in Go.

Measured returned-row bounds (loader output = Scan output):

- 50 distinct valid writer children, one leg: exactly 9 head
  rows and 9 posting-fence rows (probe present), 1 execution
  fence, 1 work row — SQLite and live direct PostgreSQL.
- 9 distinct children, one leg: 9 rows, fanout, no partials.
- Same charge IDs across two legs plus a B-leg-kind aggregate
  leg: complete loads (2/1/1 heads and fences), all known with
  checked sums; no false cross-leg overflow on either dialect.

RED: `TestALegReportProviderLoaderLegBoundFifty` failed before
the fix with 50 head rows materialized for the flood leg
(`expected: 9, actual: 50`).

Production coverage (real writer): the fifty/four-leg fixture
above on SQLite plus a live direct PostgreSQL twin
(`TestPostgresProviderLoaderLegBoundFifty`); all earlier
suites unchanged (rotation, two-child, corrections,
partial-pending, gate-owner, same-charge, zero/mixed/loser,
excluded, never-started). `known_zero` and every accepted
behavior retained: full package green.

### C2R5 verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportProviderLoaderLegBoundFifty'` (RED before: 50 rows; GREEN after: 9) | PASS |
| `go test -count=5 -shuffle=on` on the bound test plus same-charge/cap/duplicate/two-child/excluded/recorded/never-started | PASS |
| `go test -count=1 ./internal/infra/billingstore/` (full package) | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/stdhttp/` | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run 'TestPostgresProvider'` (`LIP_REQUIRE_POSTGRES=1`, live direct PostgreSQL) | PASS (incl. new fifty twin, 30s) |
| `go build ./...` | clean |
| `go vet ./internal/infra/billingstore/` and `-tags integration` vet | clean |
| `gofmt -d` on touched test/store files, `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

### C2R5 residuals

- Per-call journal cap (16+1) is unchanged: a leg flooding
  journals still marks its call journal-fanout for retail, while
  provider legs resolve from the head bound independently
  (proven by the flood call: provider fanout, no partials).
- PostgreSQL invalid-JSON subjects fail in SQL (cast); SQLite
  defers to the Go decode failure. Both fail the scope query
  closed; writer output is always valid JSON.
- SQLite `json_extract` requires the JSON1 build (present in
  modernc.org/sqlite used here and in CI); the decoded Go
  backstop keeps verdicts fail-closed regardless.
- Pseudo-leg evidence (missing/malformed/foreign B-leg or
  lineage) grouped into bounded partitions that retained-leg
  filtering silently dropped, letting a valid enumerated leg
  stay known. SUPERSEDED by C2R6 below: anomaly markers poison
  fail-closed.

### C2R6 — fail-closed poison for unattributable evidence

The remaining review blocker: JSON-valid malformed/foreign
subjects (`{}`, unknown `b_leg_id`) and malformed/foreign
posting/execution lineages grouped into pseudo-legs that were
silently dropped, so a valid enumerated leg stayed known
despite extra durable provider evidence. C2R5 hard SQL bounds
are untouched. Core is unchanged.

Poison propagation (`internal/infra/billingstore/reports_aleg.go`,
shell only, six chunk-level marker queries before any derived
loads):

- `providerLegEnumerationExists` proves an extracted B-leg names
  an enumerated leg of the exact scope (same call, account,
  A-leg). Evidence unattributable to an enumerated leg can never
  be proven, so its whole call resolves unknown with
  `provider_cost_fanout` and no subtotal/children.
- Call poison (one `DISTINCT call_id` row per call, flood-proof):
  `loadALegProviderHeadAnomaliesTx` (subject B-leg not
  enumerated), posting/execution fence variants (lineage B-leg
  not enumerated), `loadALegProviderWorkAnomaliesTx` (work key
  outside the enumerated `call:b-leg` population, exact key
  match). Call-poisoned calls join `providerOverCap` before
  retain/derivation, so they load no derived facts either.
- Exact-leg poison (`DISTINCT (call, enumerated B-leg)`,
  bounded by the enumerated leg population itself):
  posting fences whose lineage is neither the `call:b-leg` base
  nor base plus `:provider-charge:` with a non-empty charge;
  execution fences outside the bare `call:b-leg` form (the gate
  is always exactly the base key; legacy recovered fences use
  the base form, verified safe). Leg-poisoned legs join the
  `legOverCap` set (renamed from `legHeadOverCap`): retain
  drops them from derivation and the loop marks fanout unknown.
- Attributable heads need no marker: every retained head is
  evaluated, where the core already fails malformed
  kind/id/column units closed per leg. Journals are out of
  scope (C2R5 retained-fact rule stands).

Bounded anomaly detection: every marker is one chunk-level
statement returning at most one row per call (call poison) or
one row per enumerated leg (leg poison). Flooding ten thousand
pseudo-leg identities yields the same single call marker, so
flooding cannot bypass detection or widen any result. No
per-call and no per-leg statements anywhere; portable
`substr/length/||` predicates with the C2R5 dialect B-leg
extractors only.

RED: all eight adversarial report tests failed before the fix
with valid legs staying `known` next to the poison evidence;
after the fix every poisoned scope is unknown with fanout and
no partials while neighbors stay exactly known.

Production coverage (real store, writer-path legs plus raw
drift rows): empty `{}` subject, foreign `b-ghost` head,
malformed `call:b-1:GARBAGE` posting lineage (exact leg,
sibling stays known at 7), foreign `call:b-ghost` posting
lineage, malformed child-suffixed execution lineage (exact
leg), foreign execution lineage, foreign `call:b-ghost` work
key, cross-call transplanted lineage (matched leg poisoned,
sibling known). Eight SQLite tests plus eight live direct
PostgreSQL twins. Hard 50->9 bounds re-proven by the untouched
fifty tests on both dialects.

### C2R6 verification (all exit 0)

| Command | Result |
| --- | --- |
| 8 adversarial report tests (RED before: valid legs `known`; GREEN after: unknown + fanout, neighbors known) | PASS |
| `go test -count=5 -shuffle=on` on those plus bound/same-charge/cap tests | PASS |
| `go test -count=1 ./internal/infra/billingstore/ ./internal/core/billing/ ./internal/stdhttp/` (full packages) | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run 'TestPostgresProvider'` (`LIP_REQUIRE_POSTGRES=1`, live direct PostgreSQL) | PASS (incl. 8 new poison twins, 35s) |
| `go build ./...` | clean |
| `go vet ./internal/infra/billingstore/` and `-tags integration` vet | clean |
| `gofmt -d` on touched test/store files, `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

### C2R6 residuals

- In-flight production calls (provider evidence posted before
  its leg usage row appends) transiently poison the call's
  provider legs to unknown until the leg lands; fail-closed
  safe, resolves known afterwards. All suite fixtures seed legs
  first, so no test covers the transient.
- PostgreSQL invalid-JSON subjects fail the scope query in SQL
  (cast) while SQLite marks per-call poison via the marker;
  both fail closed, consistent with the C2R5 standing split.
- Kind-invalid but B-leg-attributable heads rely on core
  per-unit unknown (evaluated, never dropped); no marker
  duplicates that path, preserving existing issue codes.
- Duplicate top-level b_leg_id keys diverge: SQLite extraction
  reads the first while Go decoding reads the last, so a valid
  key followed by a foreign duplicate evades attribution then
  decodes foreign. SUPERSEDED by C2R7 below.

### C2R7 — duplicate subject-key cardinality poison

Micro-fix, shell only, no accepted semantics touched. The head
call-poison marker gains one bounded predicate: top-level
`b_leg_id` key cardinality other than exactly one poisons all
provider legs of the call before retention.

Exact query behavior
(`internal/infra/billingstore/reports_aleg.go`,
`providerSubjectKeyCountExpr`, wired into
`loadALegProviderHeadAnomaliesTx` as `OR <count> <> 1`):

- SQLite counts top-level pairs with `json_each(subject_json)`
  filtered to `key = 'b_leg_id'`; only top-level members count,
  nested payloads cannot inflate it.
- PostgreSQL counts with `json_each(subject_json::json)` the
  same way: the `json` (not `jsonb`) cast preserves the
  document verbatim and `json_each` emits every top-level pair,
  so duplicate keys are counted, not hidden. The live
  duplicate-order tests prove emission on both dialects,
  including same-value duplicates that no extraction-agreement
  argument could catch.
- Malformed JSON still fails the scope query closed in both
  dialects (cast error in PostgreSQL; loader/extraction
  failure path in SQLite), exactly as before.
- Fences and work need no equivalent: lineages and work keys
  carry no JSON key concept; their markers are unchanged. The
  result stays one `DISTINCT call_id` row per call.

RED: `TestALegReportProviderDuplicateSubjectKeysPoisonsCall`
failed before the fix with duplicate-key legs staying `known`.
GREEN after: valid-then-foreign, foreign-then-valid, two
enumerated legs, and same-value duplicates all resolve unknown
with fanout and no partials on SQLite, with a valid neighbor
call exactly known (subtotal 20, known 4, unknown 8).
`TestPostgresProviderDuplicateSubjectKeysPoisonsCall` proves
the same four shapes live on direct PostgreSQL.

### C2R7 verification (all exit 0)

| Command | Result |
| --- | --- |
| duplicate-key tests (RED before / GREEN after, both dialects) | PASS |
| `go test -count=5 -shuffle=on` on duplicate-key plus all C2R6 adversarial, bound, and zero tests | PASS |
| `go test -count=1 ./internal/infra/billingstore/ ./internal/core/billing/ ./internal/stdhttp/` (full packages) | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$'` | PASS |
| `go test -tags=integration -count=1 -run 'TestPostgresProvider'` (`LIP_REQUIRE_POSTGRES=1`, live) | PASS (incl. duplicate-key twin) |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$'` (`LIP_REQUIRE_POSTGRES=1`, live) | PASS (70s) |
| `go build ./...` | clean |
| `go vet ./internal/infra/billingstore/` and `-tags integration` vet | clean |
| `gofmt -d` on touched test/store files, `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain
(same standing platform gate).

### C2R7 residuals

- Same-value duplicates poison even though both readings agree:
  uniform fail-closed is intentional (drift shape, never
  writer output).
- Non-object subject payloads (arrays, scalars) count zero
  top-level keys and poison; writer subjects are always
  objects, so only drift can hit this.
