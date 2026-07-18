# Dual-plane migration, rollout, and rollback

Operator guide for Phase 7.4 (requirements 11.1–11.8, 12.3, 13.6). Complements [dual-plane-readiness-alerts.md](dual-plane-readiness-alerts.md) and [release-gates.md](release-gates.md).

## EconomicControlReady (OSS technical posture)

`EconomicControlReady` names whether required economic-control components may serve protected traffic. It is **not** payments, invoicing, tax, wallets, or customer financial journals.

Evaluate with `pkg/lipsdk/controlplane.EconomicControlReady` / `EvaluateEconomicControlReady` on the readiness report. Local memory/SQLite stores report advisory single-process enforcement and never claim distributed strict.

## Local versus distributed posture

| Backing | Enforcement scope | Notes |
|---------|-------------------|-------|
| disabled / absent | disabled | Feature off; existing dogfood path unchanged |
| memory / sqlite | advisory_single_process | Dev/single-node only; not multi-instance strict |
| postgres (strict-capable) | distributed_strict | Required for multi-instance five-slot / authority |

Stop condition: do not enable strict protected traffic until `MayServeStrict` is true and `EconomicControlReady` is true on a postgres-backed deployment.

## Migration ordering

1. Apply admin/direct migrations (`make test-postgres-migrations` / `lipstd migrate --components usage-authority,concurrency,metering`).
2. Verify schema on pooled runtime endpoints (`make test-authority-postgres-pooled` with `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`).
3. Publish executable generations before binding live requests.
4. Enable atomic lease sets after generations are executable.
5. Canary with `EconomicControlReady` + alert budgets before full rollout.

Identity versions are additive. Legacy fact/lease identities remain readable; historical requests without ingress facts are incomplete, not fabricated. Legacy leases migrate as one-member sets.

## Rollback and terminal-work drain

- Disable new admission / unpublish new generations while leaving terminal-work processor running.
- Do not drop schema; rollback is configuration/posture, not destructive migration.
- Pending lease-set release and terminal-work intents continue draining after admission is disabled.
- Provider removal is blocked while pending work references the provider (`CanRemoveProvider`).

## Open-core boundary

Proprietary commercial finance stays outside OSS. The `testdata/enterprise_module` fixture compiles and runs using **public packages only** (`pkg/lipapi`, `pkg/lipsdk/*`, `pkg/lipruntime`). Architecture gate: `go test ./internal/archtest/ -run EnterpriseModule`.

## Non-goals

No web GUI, SSO/SAML/SCIM, payments, invoices, tax, CSP engines, or compression algorithms in this feature.

## Rollout rehearsal commands

```bash
go run ./cmd/lipstd check-config --config config/examples/dogfood-local-stub.yaml
make test-postgres-migrations
make test-authority-postgres-direct
make test-authority-postgres-pooled   # requires pooler DSN attestation
go test ./internal/archtest/ -run EnterpriseModule
go test ./testdata/enterprise_module/   # or via archtest gate (GOWORK=off)
```
