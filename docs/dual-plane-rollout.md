# Dual-plane rollout

Use this checklist after the dual-plane release gates pass. Replace placeholders
with deployment-specific commands; never put credentials or DSNs in this file.

## Before rollout

1. Back up the target PostgreSQL database with the platform-native snapshot or
   `pg_dump`. Verify the backup can be listed and restored into an isolated
   database before continuing.
2. Run `lipstd migrate --components usage-authority,concurrency,metering` through
   the direct administrative PostgreSQL endpoint.
3. Run the migration command a second time to prove idempotency.
4. Smoke-boot the exact release binary with production configuration and query
   the protected control-plane `/readiness` route.

Schema migrations are forward-only. Rollback means restoring the verified
database backup and deploying the previous binary/configuration together. Do not
attempt ad-hoc down migrations.

## Canary stages

| Stage | Traffic | Minimum observation | Advance when |
|---|---:|---:|---|
| Shadow | 0% enforced | 30 minutes | Metering and readiness remain healthy |
| Canary | 1% | 30 minutes | No authority/lease errors; latency within baseline |
| Ramp | 10% | 60 minutes | Pool and journal metrics remain stable |
| Broad | 50% | 60 minutes | No sustained error or saturation increase |
| Full | 100% | 24 hours | All release indicators remain within thresholds |

Stop and roll back immediately for fail-closed readiness, incorrect charging,
lease resurrection/leakage, repeated settlement failures, or database pool
exhaustion. Pause the ramp for any unexplained metering conflict increase.

## Required observations

- PostgreSQL pool open/in-use/idle counts and acquisition latency.
- Authority admission and settlement latency, denial rate, and provider failures.
- Active, expiring, expired, and released concurrency leases.
- Metering append conflicts, corrections, and journal failures.
- Readiness rows for request authority, attempt authority, concurrency authority,
  and metering journal.
- Request success/error latency compared with the pre-rollout baseline.

For each stage, retain dashboard links, the release commit, start/end times,
operator identity, observed values, and the advance/rollback decision in the
deployment system of record.
