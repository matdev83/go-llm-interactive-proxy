# Testing and TDD (Steering)

## Core Testing Invariants

- **TDD by default**: characterize or make the desired invariant fail first, implement the smallest correct change, then refactor.
- **Test behavior at ownership boundaries**: prefer canonical/domain contracts, adapter contracts, and lifecycle invariants over tests that mirror internal call graphs.
- **Every bug fix gets a focused regression**: reproduce the defect with the smallest stable fixture/test that would have caught it.
- **Default tests stay self-contained**: ordinary unit/composed tests use in-memory stores, `httptest`, stubs, fake clocks/IDs, and no external network/service requirement.
- **Concurrency owners prove cleanup**: packages that own goroutines, streams, processes, or async workers need cancellation/cleanup tests and leak/race evidence appropriate to the change.
- **Real canonical types over mock graphs**: do not mock internal implementation chains merely to satisfy coverage.

## Test Architecture

Use layered evidence instead of a Cartesian product:

1. **Unit/domain tests** prove local policy and pure transformations.
2. **Adapter/family contracts** prove each frontend/backend/connector family against canonical semantics.
3. **Core contracts** prove orchestration, routing, commitment, continuity, and lifecycle independently of specific providers.
4. **Architecture/QA ratchets** prove dependency direction, generated-contract currency, bounded change surfaces, and repository hygiene.
5. **Bounded real-stack sentinels** prove a small number of representative end-to-end paths.
6. **Environment/topology tests** prove database, process, or external-service behavior only when that topology matters.

Do not grow frontend×backend matrices simply because another provider/profile was added. A new implementation that passes the relevant family contract should not multiply unrelated test cells.

## Test-Cost and Iteration-Speed Policy

Fast feedback is an architectural constraint.

- Keep the default suite narrow enough for normal development loops.
- Prefer focused package/contract execution over recursively spawning repository-wide test commands from tests.
- Reuse compiled/cached test helpers rather than rebuilding the same executable for each check.
- Prefer fake clocks and deterministic IDs over sleeps/polling.
- Prefer in-memory persistence unless reopen/durability/engine behavior is the subject of the test.
- Do not add expensive tagged/external work to the default unit path.
- Before intentionally increasing test/QA infrastructure cost, run the Windows-authoritative `make test-cost` ratchet and provide evidence. Budget changes require explicit maintainer authorization; they are not a normal escape hatch for regressions.

## Build Tag & Environment Gating Rules

- **Untagged/default tests** must be hermetic and must not require external databases, credentials, or network services.
- **Integration-tagged tests** may require real services/topologies and must state their prerequisites clearly.
- **Precommit/tagged matrices** are for broader certification that is too expensive or environment-sensitive for the inner loop.
- A mandatory gate that explicitly requests an external topology must **fail closed** when the prerequisite is unavailable; an optional ad-hoc integration run may skip when it is not configured.
- Do not infer CI behavior from steering. The `Makefile` and workflow files are authoritative for the current target/job graph.

### Database parity

`internal/testkit/dbparity.DefaultCatalog()` is the authoritative persistence inventory. Steering does not copy the number or names of registered components.

- `make test-db-parity` is the canonical repository-wide dual-engine certification.
- `make test-db-parity-sqlite` proves the SQLite side of the registered contracts.
- `make test-db-parity-postgres-direct` proves the direct PostgreSQL side and fails closed when invoked as a mandatory gate without a usable service.
- Specialized distributed/pooler/migration tests supplement the catalog when topology semantics differ; use the `Makefile` and persistence/release docs for their current names and prerequisites.

## Change-Surface Verification Procedure

Select evidence from the semantics changed, not from habit:

| Change surface | Minimum evidence direction |
| --- | --- |
| Pure domain/canonical logic | focused unit/regression tests |
| Parser/decoder/codec | focused tests + fuzzing where practical |
| Frontend/backend protocol behavior | family contract/conformance tests + focused wire tests |
| Routing/B2BUA/commitment | core runtime/routing tests; prove no post-commit recovery |
| Cancellation/goroutine/stream lifecycle | focused concurrency tests + race evidence where practical |
| Feature/extension-plane behavior | feature tests + SDK/architecture guards; generator check when plane metadata changes |
| Host/generation publication/reload | candidate rollback, publication isolation, retirement/cleanup tests |
| Persistence/schema/migrations | focused store tests + applicable `dbparity` gate/topology proof |
| Connector module | module-local tests + backend-plugin contract/release checks |
| Billing/accounting | domain invariants + persistence/convergence evidence; no stream-path shortcuts |
| Test/QA infrastructure | affected tests + `make test-cost` when cost could change |
| Wide/release-grade change | `make qa` plus domain-specific gates |

Run the smallest complete evidence set that proves the changed invariant. Add wider gates when the blast radius is genuinely wider.

## High-Value Semantic Targets

Prioritize tests for invariants whose failure would invalidate product behavior:

- canonical translation and explicit capability handling;
- protocol-legal streaming/error framing;
- routing planning and pre-output-only recovery;
- at-most-once attempt terminalization and cancellation ownership;
- immutable generation publication/rollback/retirement;
- secure-session and authorization boundaries;
- durable continuity and attempt ordering;
- feature/core ownership seams and no concrete feature leakage into generic runtime;
- secret-safe diagnostics/observability;
- billing exposure/usage/settlement separation;
- database logical parity across supported engines/topologies.

Concrete feature/provider inventories should not be copied into this list.

## Mocking and Fixture Rules

- Prefer small fakes/stubs and `httptest.Server` over general mock frameworks.
- Mock external boundaries, not internal call graphs.
- Use real `pkg/lipapi` canonical types in protocol/core tests.
- Keep fixtures bounded and human-auditable; golden files are appropriate when exact wire/canonical shape is the contract.
- Avoid sleeps as synchronization. Use channels, fake clocks, explicit barriers, or state observation.
- Tests must not depend on execution order or mutable process-global leftovers.

## Failure Triage

When a broad gate fails during a scoped change:

1. reproduce the failure on the change branch;
2. determine whether the touched ownership surface can causally affect it;
3. reproduce on the relevant baseline/main SHA when attribution is uncertain;
4. fix branch-owned regressions before claiming completion;
5. record genuinely unrelated baseline failures without expanding the task into opportunistic cleanup.

Never claim success from a partial command when the requested completion gate is broader.

## Canonical Command Intents

The `Makefile` is authoritative for exact target composition. Common intents are:

- `make quality-checks` — static/architecture/hygiene checks;
- `make test-unit` — default unit/composed tests;
- `make test` — normal comprehensive local verification;
- `make parity-checks` — protocol/contract parity and bounded cross-surface checks;
- `make test-db-parity` — repository-wide persistence parity;
- `make qa` — wide/release-grade verification;
- `make test-cost` — Windows-authoritative test/QA cost comparison.

Do not duplicate current CI job names, workflow predicates, container versions, connector inventories, or the full Make dependency graph in steering.
