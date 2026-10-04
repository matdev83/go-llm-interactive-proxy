# Revision 3 — architecture simplification and conformance factoring

**Status:** normative narrow overlay on revision 2. **Implementation status:** not started. **Financial contract:** unchanged `all-attributable-pessimistic/v1`.

This revision is a design simplification pass. It does not weaken C01-C11, remove any required provider/frontend/modality/transport support, change historical billing semantics, authorize optimistic bounds, or make missing evidence equal zero. It has narrow precedence over the revision-2 packet text only for the topics and task IDs named below. All unmentioned packet requirements, scenarios, file budgets, dependencies, topology gates, and safety invariants remain authoritative.

The complete finite coverage universe remains enumerated before capability filtering. The change is that a native-positive coordinate no longer requires a unique full-stack execution when the same behavior is already independently proven at the frontend edge, backend/profile edge, common financial kernel, and a required real-stack witness. Pairwise sampling is still insufficient and a missing proof chain remains a release blocker.

## R3-A — One atomic prepared-attempt authorization

Revision 2 split final attempt authorization into an allocation/extension transaction and a later grant-consumption transaction. Revision 3 collapses those into one central account transaction after final immutable preparation and local cancellation-handle registration.

The operation is described as `AuthorizePreparedAttempt`; the exact Go symbol may follow existing consumer-owned naming. It SHALL, in one account transaction:

1. validate account/store/call/B-leg/slot identity, active financial epoch/state, immutable prepared digest, model/endpoint/profile, bound vector, contract/material hashes, and process incarnation;
2. claim one matching funded slot, or reserve the full incremental extension for newly required work;
3. append the balanced exposure movements and create the durable dispatch obligation;
4. persist the dispatch as `authorized_unknown` and bind the one provider-dispatch identity;
5. commit any required-extension monetary denial together with the account freeze intent before returning that denial.

A confirmed successful authorization may return one process-local, one-use send capability. A replay/status lookup after commit-ack ambiguity MUST return durable state but MUST NOT mint a new send capability. A crash after authorization retains the attempt as potentially dispatched and recovery MUST NOT blind-resend it.

Local active-work registration still happens before authorization so a concurrently committed account fence can find the execution. Registration is not monetary authority; the transaction's epoch/state check remains authoritative. No database transaction may remain open while provider I/O occurs.

Root envelope admission remains a separate earlier transaction. Local spool-capacity reservation and remote provider send remain separate durability/side-effect boundaries. This revision does not claim distributed atomicity.

### Required proof

- exactly one central authorization transaction per prepared economic attempt on the normal path;
- duplicate/racing authorization yields at most one send capability and one durable dispatch identity;
- stale epoch, changed prepared payload, changed limits, foreign owner, and reused slot all fail before send;
- required-extension denial persists the freeze without consuming sibling commitments;
- commit-ack ambiguity and process death never cause automatic retransmission.

## R3-B — Exhaustive classification with factored conformance evidence

The 220 baseline interface pairs and 901,120 base modality signatures remain a mandatory closed-world inventory. Profile, operation, carrier, and independent delivery/transport expansion also remains mandatory. Enumeration occurs before billing readiness filtering.

Release evidence is now **factored** rather than requiring a distinct end-to-end run for every native-positive coordinate. Each REQUIRED_SUPPORTED coordinate must map to a complete immutable proof chain containing:

- **frontend contract evidence:** real decoder/encoder behavior preserves canonical occurrences, output intent, economic-field receipts, financial errors, and applicable delivery modes;
- **backend/profile contract evidence:** real final encoder/native transport/evidence decoder proves enforceable bounds, native units, overlap semantics, finality, retry behavior, and provider-specific joint capability clauses;
- **common financial-kernel evidence:** independent money oracles and real billing-store tests prove funding, one-time authorization, evidence ingestion, recognition/collection, COGS, debt, freeze, recovery, SQLite/PostgreSQL behavior, and idempotency;
- **required real-stack witness evidence:** the composition of those contracts is exercised through the real runtime at the stable boundaries listed below.

The release gate SHALL require real-stack witnesses for:

1. every baseline frontend/backend pair that has at least one legal REQUIRED_SUPPORTED intersection;
2. every distinct backend profile/API/transport implementation whose behavior is not already the same certified adapter contract;
3. every connector module through its actual module entrypoint, not only the generic connector host ABI;
4. every frontend delivery/carrier family, including asymmetric upstream/downstream streaming modes where supported;
5. targeted cross-boundary mixtures and financial failure schedules, including hidden thinker, large-body/wire, auxiliary/child work, sparse/missing-final usage, mixed media, cancellation, crash-before-ack, long database outage, and same-account freeze.

A pair with no legal positive intersection still needs complete classification plus an independent native limitation predicate and zero-payable-send evidence.

The coordinate certificate SHALL map every expanded coordinate to concrete evidence IDs and digests. Missing frontend evidence, backend evidence, financial-kernel evidence, required witness evidence, or proof composition is a release failure. A coordinate may share evidence with another coordinate only when the frozen contracts prove the relevant behavior is identical.

This is not pairwise test selection. The denominator remains exhaustive and no production frontend×backend translator is introduced.

## R3-C — Hexagonal boundary placement

Pure financial policy stays in `internal/core/billing`. Infrastructure and provider adapters supply immutable facts.

In particular, catalog/model lookup, snapshot loading, and external metadata acquisition remain adapter/infrastructure concerns, while the rules for catalog-first reservation maximum, explicit fallback eligibility, transmitted-cap validation, and typed financial rejection belong in the billing core. Do not create a new generic application-service layer solely for this move; use the smallest existing consumer-owned port that exposes the immutable catalog facts required by the core policy.

Provider SDK/wire types remain at the edge. SQL/Bun remains in `internal/infra/billingstore`. Public `pkg/lipsdk` additions remain limited to capabilities external hosts/connectors genuinely need.

## R3-D — CORDIS-v4 lifetime and teardown clarification

Financial retention and runtime-generation lifetime are different concerns. An unresolved durable financial obligation SHALL retain the exact financial materials and recovery identity it needs, but SHALL NOT by itself pin an obsolete `GenerationRuntime` or duplicate an existing lifecycle owner.

| Resource/effect | Owner | Quiesce/retention rule | Required close order |
|---|---|---|---|
| prepared request/body handle | existing request/attempt owner | retain through authorization/send/capture only as required by the existing secure body contract | release after send/capture dependents finish; denied/no-send paths release deterministically |
| active account cancellation registration | existing runtime request/attempt owner indexed by the process-owned financial-control service | register before final authorization; unregister idempotently after execution ownership ends | quiesce launches, initiate cancellations, then drop registrations |
| account fence poller/watchdog | process owner | no generation ownership; stop admitting/authorizing before shutdown | cancel/join before closing its store/control dependencies |
| spool/economic/recovery workers | existing `ProcessServices` process lifetime | accepted durable work survives generation retirement; shutdown stops producers and preserves pending rows rather than waiting forever for an empty queue | stop producers, join workers, then close spool/store/transports they may still use |
| frozen tariff/schema/capability material | durable financial store/reference lifetime | retain while referenced by unresolved obligations regardless of reload | garbage-collect only after reference-safe financial resolution |
| provider recovery operation | existing process-owned backend/recovery capability or bounded operation lease | obtain only when a pending obligation needs it; do not retain a whole old generation just because a row is unresolved | release lease before lower-level process/backend supervisor closes |

No new generic effect runtime, DI container, resource graph, or reconciliation framework is authorized.

## R3-E — Task overrides

The revision-2 execution archive remains the immutable base packet set. Before executing any task, read this file after `START-HERE.md`. For the following tasks, these overrides have precedence over conflicting packet wording:

| Task | Revision-3 override |
|---|---|
| 1.3 | Binding V2 exposes one prepared-attempt authorization capability rather than separate public allocate/extend and consume-dispatch ports. Replay/status APIs never mint send authority. |
| 2.1 | Put catalog-first cap/fallback decision policy in `internal/core/billing`; infrastructure supplies immutable catalog facts/snapshots. |
| 2.5 | A one-use send capability is produced only by a confirmed atomic prepared-attempt authorization and is not durable/remintable. |
| 3.5 | Implement slot claim or extension, obligation creation, epoch check, and transition to `authorized_unknown` in one account transaction. |
| 4.1-4.6 | All paid launch paths use prepare → register → atomic authorize → send; no second central grant-consumption transaction exists. |
| 7.2 | Register active work before atomic prepared-attempt authorization, not before a separate grant-consumption phase. |
| 9.7 | Family and cross-interface integration uses edge contract suites plus required real-stack witnesses; representative sentinels remain supplemental. |
| 10.2 | Final release verifies complete coordinate-to-proof mapping and required witnesses, not one full-stack call per positive coordinate. |
| 11.1/11.5 | Preserve complete closed-world inventory and joint-capability predicates; emit stable contract identities usable by proof composition. |
| 15.1 | Build the exhaustive classifier/proof composer and required witness runner; do not execute every positive coordinate through the entire stack solely to repeat proven edge/kernel behavior. |
| 15.4 | Mutation gates must detect missing proof components, missing pair/profile/connector witnesses, stale digests, all-deny behavior, and denominator filtering. |
| 15.5 | Release closes only when every coordinate has a valid composed proof or justified negative and every mandatory real-stack witness/topology gate has executed. |

## R3-F — Simplification acceptance

Revision 3 is successful only if implementation preserves the original financial behavior while reducing architecture/test indirection:

- normal prepared attempts use one central final-authorization transaction rather than two;
- no new generic owner, broker, DI container, effect graph, or frontend×backend production translator is introduced;
- every expanded coordinate remains accounted for in the release certificate;
- adding a frontend requires frontend-contract evidence plus affected real-stack witnesses, not a cross-product of duplicated backend financial tests;
- adding a backend/profile requires backend/profile-contract evidence plus affected real-stack witnesses, not duplicated frontend decoder tests;
- connector additions still execute their real entrypoint and cannot opt themselves out with a billing-ready flag;
- unresolved financial rows survive reload without retaining obsolete runtime generations solely for accounting;
- performance claims are reported from measured gates; no unmeasured throughput promise is introduced.

The revision changes implementation shape and evidence composition only. It does not relax affordability, durability, all-attributable charging, native-unit preservation, complete support obligations, or failure containment.
