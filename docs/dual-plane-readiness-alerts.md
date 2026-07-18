# Dual-plane readiness alerts and performance budgets

Operator recommendations for Phase 7.3 (requirements 12.1–12.9, 13.10). These are OSS technical signals for `EconomicControlReady`, not commercial billing alerts.

## EconomicControlReady

Use `controlplane.EconomicControlReady(report)` / `EvaluateEconomicControlReady` on the control-plane readiness report. Ready means:

- executable generation ready (or disabled when economic control is off)
- required request/attempt/concurrency/usage authorities ready or disabled
- required customer/operator raters ready or disabled
- metering journal ready or disabled
- terminal recovery ready, disabled, or degraded-only for pending post-output work
- protected traffic posture not unavailable

Pending terminal work **degrades** posture but does not block `EconomicControlReady` when `MayServeStrict` remains true.

## Recommended alerts

| Signal | Source | Suggest alert when | Notes |
|--------|--------|--------------------|-------|
| Authority / rating latency | `AuthorityStages` / executor histograms | p99 above rollout budget for 5m | Bound labels to stage/provider ID hashes only |
| Terminal backlog | `lip_terminal_work_backlog` | backlog > 0 for 10m in steady state | Pair with oldest-age |
| Terminal oldest age | `lip_terminal_work_oldest_age_seconds` | age > SLA (e.g. 5m) | Pending post-output recovery |
| Secret-guard quarantine | `SecretGuard` / readiness `secret_guard_quarantine` | quarantine growth | Do not label with content |
| Generation staleness | readiness `executable_generation` / snapshot states | state degraded/unavailable | Refresh keeps prior executable on source-fetch failure |
| Lease uncertainty | readiness `concurrency_authority.lease_sets.uncertain` | uncertain > 0 | Fail-closed renew path |
| Pending lease-set release | readiness `lease_sets.pending_release` | pending_release > 0 for 5m | Terminal work draining release intents |
| Renewal failures | runtime logs / lease renew counters | sustained renew errors | Cancel before unproven expiry |
| Store contention | postgres pool wait / authority stage latency | pool wait or lock wait above budget | Direct migrate vs pooled runtime |

Never use user content, raw prompts, or reversible content as metric labels (design D14).

## Performance budgets (benchstat)

Record baselines with:

```bash
go test -bench='BenchmarkExecutorExecuteAndDrain32Deltas|BenchmarkMemoryFiveSlot|BenchmarkMemoryAppendAndCorrectionReplay|BenchmarkMemoryAcquireSetFiveSlot' -benchmem -count=6 \
  ./internal/core/runtime/ \
  ./internal/infra/concurrencyauthority/leasestore/ \
  ./internal/infra/metering/journalstore/ \
  | tee bench-dual-plane.txt
benchstat bench-dual-plane.txt
```

| Bench | Intent |
|-------|--------|
| `BenchmarkExecutorExecuteAndDrain32Deltas` | Disabled/no-feature overhead baseline |
| `BenchmarkMemoryFiveSlotHundredContenders` | Five-slot contention (legacy per-lease) |
| `BenchmarkMemoryAcquireSetFiveSlotHundredContenders` | Five-slot atomic lease-set contention |
| `BenchmarkMemoryAppendAndCorrectionReplay` | Fact append + correction replay |

Independent principals / hot identities remain covered by usage-authority store contention benches under `internal/infra/usageauthority/authoritystore`.
