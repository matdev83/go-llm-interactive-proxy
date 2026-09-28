# Refinement 8.2 upstream journalstore JSONB fix — execution

Scope: narrow upstream fix blocking Task 8.2. PostgreSQL stores the observation
economic outbox `payload_json` as JSONB, which normalizes whitespace and key
order; the V2 outbox append/read/decode path compared scanned JSON text
byte-for-byte against canonical Go JSON and falsely returned
`ErrIdentityCollision`. SQLite TEXT never exposed it.

## Root cause

`internal/infra/metering/journalstore/observation_outbox.go` decided replay vs
collision with `existing.PayloadJSON != string(payload)` at three sites:
pre-existing-row check and post-insert verify in `appendObservationOutboxInTx`,
and `decodeObservationOutboxRow`. The post-insert verify had no
`ReplayFingerprint` fallback, so the first finalizer append on PostgreSQL
always falsely collided after JSONB normalization. The same raw-byte shape
(plus an empty-fingerprint bypass) existed in `resolveObservationReplay` in
`internal/infra/metering/journalstore/observation_store.go`.

## Fix

One private validation path, `validateCanonicalObservationReplay`, used by all
four sites on both dialects with no dialect branches:

- decode the stored payload into the canonical `metering.Observation` domain
  type and run its `Canonical()` validation contract;
- fail closed when the stored fingerprint is missing, the stored JSON is
  malformed (`metering.ErrInvalidObservation`), the decoded identity drifts,
  or the recomputed full `Observation.Fingerprint` differs from the durable
  fingerprint column (`ErrIdentityCollision`);
- accept exact semantic replay by full-fingerprint equality and approved
  receipt/lineage-placement replay by `ReplayFingerprint` equality only;
- recompute every compared hash from canonical content; no caller-supplied or
  stored hash is trusted without recomputation; no raw JSON comparison remains
  on these paths.

`decodeObservationOutboxRow` additionally validates the duplicated
`observation_id`/`observation_revision` columns against the decoded payload and
returns classified errors instead of the unclassified mismatch string.

No schema, migration, public API, or billing-authority change.

## RED evidence

- `TestPostgresObservationOutbox_JSONBExactReplayIsIdempotent` (live PG):
  first append failed with `fact identity collision: observation outbox
  identity="obs-pg-jsonb-first" revision=1` before the fix; passes after.
- SQLite normalization simulation (key-order and whitespace variants with
  identical canonical fingerprints): `ListPendingObservationOutbox` failed with
  the unclassified `payload fingerprint mismatch` before the fix; passes after.
- Fail-closed gaps proven before the fix: stored-fingerprint mismatch replay
  returned nil; empty stored fingerprint replay returned nil; row identity
  column drift was accepted by the read path; malformed stored JSON carried no
  error classification.

## Verification

- `go test -count=1 ./internal/infra/metering/journalstore/` — PASS.
- Focused new SQLite tests with `-count=2` — PASS.
- `go test -count=1 ./pkg/lipsdk/metering/ ./internal/core/metering/...` — PASS.
- `make test-db-parity-sqlite` — PASS (journalstore component ok).
- `go test -tags=integration -count=1
  ./internal/infra/metering/journalstore/ -run
  'TestPostgresObservationOutbox_JSONBExactReplayIsIdempotent'`
  (`LIP_REQUIRE_POSTGRES=1`) — PASS on live PostgreSQL.
- Bounded PG integration sweep (`-run 'Postgres'`): all pass except the two
  pre-existing `TestDBParity_PostgresDirect*` failures caused by the
  independent `value_present` int4/boolean baseline drift in the shared
  database (`insert component projection: column "value_present" is of type
  integer but expression is of type boolean`), a separate work order; that path
  (`insertComponent`, DDL) is untouched by this change.
- `go vet ./internal/infra/metering/journalstore/...` — clean.
- `gofmt -l` on changed files, `git diff --check`, placeholder/secret scan —
  clean.

## Remediation (review blockers 1-3)

The review correctly found the same raw-byte/fail-open shape surviving on the
durable fact paths. Remediation extends the same canonical validator:

- Blocker 1: the post-insert concurrent-winner verify in `AppendObservationInTx`
  no longer compares `row.Payload` strings and no longer accepts an empty
  stored fingerprint. It checks the duplicated fact columns, then routes
  through `validateCanonicalObservationReplay`, so a concurrent winner with a
  different representation or receipt metadata replays exactly like the
  pre-insert path.
- Blocker 2: one shared stored-row proof,
  `decodeValidatedStoredObservation`, now serves fact replay, direct reads, and
  rebuild. It decodes the envelope, runs the canonical content contract before
  the store-scope check (invalid content classifies as
  `metering.ErrInvalidObservation`; cross-store content as `ErrQueryOutOfScope`),
  checks duplicated `observation_id`/`revision` columns, and verifies the
  recomputed full fingerprint (missing/mismatch/drift wrap
  `ErrIdentityCollision`). `lookupObservationRow` and the post-insert selects
  now also fetch `observation_id`/`observation_revision` so column drift is
  detectable. `GetObservation`, `ListObservations`, and
  `RebuildObservationProjections` validate every row and return only canonical
  observations; `ReplayFingerprint` remains restricted to approved
  receipt/lineage replay on append paths.
- Blocker 3: `observation_fact_jsonb_test.go` proves concurrent-winner
  receipt/lineage replay, missing/mismatched winner fingerprints, fact
  identity/revision drift, semantically invalid stored observations, and
  Get/List/rebuild corruption, all with `errors.Is` classification. RED was
  observed on every fail-closed case before the fix (drift replay and
  Get/List/rebuild corruption returned nil or unclassified errors).

## Remediation verification

- `go test -count=1 ./internal/infra/metering/journalstore/` — PASS.
- `go test -count=2 ./internal/infra/metering/journalstore/ -run
  'TestObservationFactJSONB|TestObservationOutboxJSONB|TestObservationJSONB'`
  — PASS.
- `go test -count=1 ./pkg/lipsdk/metering/ ./internal/core/metering/...
  ./internal/infra/billingstore/...` — PASS.
- `make test-db-parity-sqlite` — PASS.
- Live PG `TestPostgresObservationOutbox_JSONBExactReplayIsIdempotent`
  (`LIP_REQUIRE_POSTGRES=1`, integration tag) — PASS.
- Bounded PG `-run 'Postgres'` sweep: only the two pre-existing
  `TestDBParity_PostgresDirect*` `value_present` int4/boolean baseline
  failures remain (separate work order; untouched paths).
- `go vet`, `gofmt -l`, `git diff --check`, placeholder/secret scan — clean.

## Residual risks

- The shared PostgreSQL instance still carries the `value_present`
  INTEGER/BOOLEAN drift, so component-bearing observation appends fail there
  for an unrelated reason; the new live-PG test uses an evidence-only
  observation to isolate this fix.
- The post-insert concurrent-winner race window itself is only reachable via
  genuine write concurrency, so its coverage is the shared validator exercised
  through the deterministic replay/read tests plus the live-PG append test;
  no timing-based test was added.
- No claim is made about full Task 8.2 completion; only its upstream blocker
  is fixed.
