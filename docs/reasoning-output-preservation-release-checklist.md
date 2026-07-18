# Reasoning Output Preservation — Release Checklist

Issue [#157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157). Spec: [`.kiro/specs/reasoning-output-preservation/`](../.kiro/specs/reasoning-output-preservation/). Operator guide: [reasoning-output-preservation.md](reasoning-output-preservation.md).

## PR split (when review size warrants)

| PR slice | Scope | Must stay independently green |
|----------|-------|-------------------------------|
| A — Contract/runtime | `pkg/lipapi` reasoning parts/capability; attempt-transform + final-stream-observer runners | unit + focused runtime |
| B — Feature plugin | `internal/plugins/features/reasoningpreservation`, `standardplugins` registration | feature unit + Phase 5 |
| C — Adapters/parity/docs | frontend/backend dialect encode, parity VisibleThinker, examples/docs/checklist | parity + `check-config` examples |

Every PR must link issue #157 and the approved spec path above.

## Review focus

- Process-local / sticky / restart limitation called out in operator docs.
- No retry/failover after first content; observer fail-open never retries.
- Privacy: no payloads/anchors/partitions in logs/metrics/inventory/errors.
- D12: disabled/absent feature adds no participants or behavior change.
- Hard `reasoning_replay` — no silent dialect downgrade (`reject` excludes; configured `log_skip` continues without restore).
- Examples are **config-validation dogfood** (local-stub); behavioral E2E is `TestPhase5_*`.
- `lipstd inventory` = generic participant/stage posture; outcome aggregates = `BuildSafeInventory`/telemetry API only.

## Gate commands (record outcomes)

```bash
go run ./cmd/lipstd check-config --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd routes --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd inventory --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd check-config --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/lipstd routes --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/lipstd inventory --config config/examples/reasoning-preservation-restore.yaml

go test -count=1 -run 'TestPhase5_|TestVisibleThinkerReasoning_' ./internal/core/runtime/ ./internal/plugins/features/reasoningpreservation/ ./internal/plugins/frontends/parity/
make quality-checks
make test-unit
make parity-checks
go test -fuzz=FuzzComputeAnchor$ -fuzztime=30s -run=^$ ./internal/plugins/features/reasoningpreservation/
go test -fuzz=FuzzDecodeConfig$ -fuzztime=30s -run=^$ ./internal/plugins/features/reasoningpreservation/
make test-fuzz   # full suite; do not claim if only targeted fuzz ran
make qa          # quality + unit + lint + vuln (+ integration when DSN set)
```

### Recorded evidence (Phase 6 repair, Windows local 2026-07-18)

| Gate | Result | Notes |
|------|--------|-------|
| lipstd check-config/routes/inventory (both examples) | OK | Participant posture only in CLI |
| `make quality-checks` | OK | |
| `make test-unit` | OK | |
| `make parity-checks` | OK | |
| Targeted `FuzzComputeAnchor` / `FuzzDecodeConfig` (~5–30s) | OK | Wired in Makefile / release-gates |
| Full `make test-fuzz` | **Not claimed** | Only targeted fuzz targets re-run locally |
| `make test-race` | **Skipped** (Windows Makefile no-op) | Linux CI: `scripts/race-check.sh --strict` |
| `make lint` / `govulncheck` | OK | Cleared via Phase 6 repair lint pass |
| `make qa` | **OK** | After unique IDs in `TestPostgresLedgerStore_recordsRoundTrip`; one transient pooled-admin `57P01` cleanup flake re-ran green |

### Platform / skip notes

| Gate | Windows local | Linux CI |
|------|---------------|----------|
| `make test-race` | Skipped (no-op) — **do not claim race-green** | Required via `scripts/race-check.sh --strict` |
| Full `make test-fuzz` | Claim only if the full target list completed | Same |
| PostgreSQL authority | Env-gated via `LIP_TEST_POSTGRES_DSN`; when set, integration tests must pass | Env-gated |
| Live provider smoke | Not required for this feature | Optional |

`TestPostgresLedgerStore_recordsRoundTrip` must use unique request/attempt IDs per run (shared DSN tables accumulate rows). Do not wipe external DB; isolate with unique IDs only.

## Manifest hygiene

- [phase-1-red-manifest.md](../.kiro/specs/reasoning-output-preservation/phase-1-red-manifest.md) intentional RED section must list no Phase 1–5 semantic gaps after Phase 6.
- Tasks 5.1–6.4 marked complete in `tasks.md` only when gate evidence matches.
- No stale `*_RED` names claimed as still-failing once fulfilled.

## Changed-file checklist (handoff)

- Operator docs + examples under `docs/` and `config/examples/`.
- Feature/runtime/adapter tests green.
- Fuzz targets registered in `Makefile` / `docs/release-gates.md`.
- EchoesVault page + daily log updated.
- Spec `tasks.md` / `spec.json` status updated (archive only if separately requested).
