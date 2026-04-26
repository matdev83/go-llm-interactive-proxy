# Database Persistence Configuration

This project lets operators choose persistence backends directly in `config/config.yaml`.
The settings below control continuity, secure sessions, and managed PostgreSQL pool tuning.

## Backend selection

Both store domains support the same backend names:

- `memory` - in-process, non-durable
- `sqlite` - local durable file-backed storage
- `postgres` - managed durable PostgreSQL storage

### Continuity

Use `continuity.store` to choose the continuity backend.

Relevant fields:

- `continuity.store`
- `continuity.sqlite_path` when `store: sqlite`
- `continuity.postgres_dsn` when `store: postgres`

Behavior:

- `memory` preserves the existing in-memory behavior.
- `sqlite` preserves the existing local durable behavior.
- `postgres` uses the Bun-backed managed durable path.
- Unsupported values fail validation before startup.
- `continuity.postgres_dsn` is required for `postgres` and rejected for other backends.

### Secure sessions

Use `secure_session.store` to choose the secure-session backend.

Relevant fields:

- `secure_session.store`
- `secure_session.sqlite_path` when `store: sqlite`
- `secure_session.postgres_dsn` when `store: postgres`
- `secure_session.token_fingerprint_key` for durable stores
- `secure_session.audit_durability`

Behavior:

- `memory` keeps the existing non-durable secure-session behavior.
- `sqlite` preserves the existing local durable secure-session path.
- `postgres` uses the Bun-backed managed durable path.
- Durable audit is allowed only with `sqlite` or `postgres`.
- `secure_session.postgres_dsn` is required for `postgres` and rejected for other backends.

## Managed PostgreSQL pool tuning

The top-level `database` block applies pool tuning to managed PostgreSQL handles opened by this feature.

Relevant fields:

- `database.max_open_conns`
- `database.max_idle_conns`
- `database.conn_max_lifetime`
- `database.conn_max_idle_time`

Notes:

- Omit fields to use the driver defaults.
- Duration values use Go duration strings such as `30m` or `90s`.
- Invalid or negative values fail validation before startup.

## Example

```yaml
database:
  max_open_conns: 8
  max_idle_conns: 2
  conn_max_lifetime: 30m
  conn_max_idle_time: 2m

continuity:
  store: postgres
  postgres_dsn: "postgres://user:pass@host:5432/continuity?sslmode=require"

secure_session:
  store: postgres
  postgres_dsn: "postgres://user:pass@host:5432/secure_session?sslmode=require"
  token_fingerprint_key: "replace-with-32+byte-secret----------------"
  audit_durability: durable
```

## Validation behavior

- Configuration fails fast for unsupported backends and missing DSNs.
- The proxy does not silently fall back to another backend if the selected durable store cannot open.
- Sample config comments in `config/config.yaml` mirror these fields.
