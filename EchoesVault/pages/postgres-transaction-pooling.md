---
type: architecture
tags:
  - postgres
  - pooling
  - migrations
  - runtime
---

# PostgreSQL Transaction Pooling

LIP supports PostgreSQL transaction-pooler endpoints for dual-plane usage authority, concurrency leases, and metering journal runtime traffic.

## Connection Roles

- The migration/admin role uses a direct PostgreSQL endpoint and owns DDL.
- The runtime role may use a direct endpoint or a transaction pooler such as PgBouncer transaction mode.
- Runtime SQL must not depend on `search_path`, session GUCs, temporary tables, SQL-level prepared statements, session advisory locks, or connection affinity outside an explicit transaction.

## Configuration Modes

`database.connection_mode`:

- `direct`
- `transaction_pool`

`database.schema_mode`:

- `auto_migrate`
- `verify_only`

Compatibility defaults remain `direct` plus `auto_migrate`. The combination `transaction_pool` plus `auto_migrate` is rejected. Transaction-pooled runtime uses `verify_only`.

When any managed path selects `store: postgres`, `database.max_open_conns` must be greater than zero. Omitted or zero `max_open_conns` is rejected at validation so the process cannot rely on unlimited `database/sql` defaults.

## Pool Ownership

The composition root creates one build-local `db.PoolRegistry`. Pool identity is the sanitized DSN plus pool settings. Authority, concurrency, and metering receive shared non-owning handles. The registry `Close` takes a context: build-failure cleanup uses the startup context, and runtime dispose uses `context.Background()`. The registry closes pools once on normal shutdown and also closes them when a build fails.

Under `auto_migrate`, the first registry-owned store open for a dual-plane DSN runs one capped admin migrate pass (`MaxOpenConns=1`) for every enabled dual-plane component that shares that DSN. Later opens for the same DSN skip admin migrate and only verify/open/claim.

Store adapters expose separate lifecycle operations:

- `Migrate` for admin DDL.
- `VerifySchema` for read-only startup checks.
- `OpenStore` for non-owning runtime construction.
- Legacy constructors retain owning, auto-migrating behavior for compatibility.

A shared runtime lifecycle helper centralizes DSN validation, registry/direct opening, verification, construction, and closer ownership across all three stores.

## Pool Sizing And Saturation

The shared registry pool serves concurrent lease `FOR UPDATE`, usage-authority reserve transactions, and metering journal appends. Size `database.max_open_conns` for peak concurrent dual-plane transactions against that shared key (not for a single store in isolation).

Watch scrape-driven metrics on the registry pools:

- `lip_postgres_pool_in_use_connections` approaching `lip_postgres_pool_max_open_connections` (zero max_open means unlimited in `database/sql`)
- rising `rate(lip_postgres_pool_wait_total[5m])` or wait duration rate under stable load

Admin/DDL opens use a separate short-lived 1-connection handle and must not be counted into runtime sizing.

## Migration Command

`lipstd migrate --components usage-authority,concurrency,metering` reads the admin DSN from `LIP_MIGRATION_POSTGRES_DSN`. Migration dispatch lives in `internal/infra/dbmigrate`; the CLI remains a thin adapter.

## Test And Release Gates

- `LIP_TEST_POSTGRES_ADMIN_DSN` is the preferred direct test bootstrap and cleanup endpoint.
- `LIP_TEST_POSTGRES_DSN` is the runtime endpoint.
- Tests may reuse the runtime DSN for admin setup when it has admin rights.
- The pooled release gate additionally requires `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`; topology is never inferred from the hostname or DSN.
- `make test-postgres-migrations` proves migration and verification.
- `make test-authority-postgres-direct` proves direct runtime contracts.
- `make test-authority-postgres-pooled` proves transaction-pooled runtime contracts at normal parallelism.
- `make test-authority-postgres` aggregates all three.

CI provisions PostgreSQL and PgBouncer in transaction mode separately. Direct and migration DSNs target PostgreSQL; the pooled runtime DSN targets PgBouncer.
