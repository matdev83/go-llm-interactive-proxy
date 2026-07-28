# Reasoning Output Preservation — Release Checklist

Issue [#157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157). Parent spec: [`.kiro/specs/archive/reasoning-output-preservation/`](../.kiro/specs/archive/reasoning-output-preservation/). Follow-up full HTTP E2E spec: [`.kiro/specs/archive/reasoning-preservation-e2e-validation/`](../.kiro/specs/archive/reasoning-preservation-e2e-validation/). OpenAI Responses exact path: [`.kiro/specs/openai-responses-reasoning-preservation/`](../.kiro/specs/openai-responses-reasoning-preservation/). Operator guide: [reasoning-output-preservation.md](reasoning-output-preservation.md).

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
- D12 / default semantics: on standard `lipstd` / `runtimebundle.BuildHost`, an enabled `restore` row is injected when absent; disabled or explicit matching opt-out adds no participants/store/telemetry. Custom roots that skip `EnsureReasoningOutputPreservationInConfig` receive no injection. Installed/enabled ≠ universally active — capture/restore require catalog/rule eligibility; unmatched candidates are inert.
- Hard `reasoning_replay` — no silent dialect downgrade (`reject` excludes; configured `log_skip` continues without restore).
- Builtin catalog is `compatible-auto.v2` (shared `internal/reasoningreplay`); GPT 5.6+ / GPT 6+ / GPT 4.x excluded automatically; explicit rules may opt into GPT 5.6+.
- Examples are **config-validation dogfood** (local-stub); shipped configs document exact injected defaults; in-process behavioral proofs remain `TestPhase5_*`; full HTTP proofs are `TestReasoningPreservationHTTP*` (see suite topology below).
- `lipstd inventory` = generic participant/stage posture; outcome aggregates = `BuildSafeInventory`/telemetry API only.
- OpenAI Responses exact preservation is implemented (allowlisted Opaque, four FE×BE combinations, dialect mismatch reject/`log_skip`, process-local TurnStore). Release-grade claim still needs external gates below — do not mark Linux race / wide soak / live smoke green without evidence.
- Soak / randomized Chat matrix are **not** mandatory PR gates; failure traces must stay content-safe (no reasoning/signature/opaque payloads, ids, or session partition keys).

## Gate commands (record outcomes)

```bash
go run ./cmd/lipstd check-config --config config/config.yaml
# Root config.yaml is a short example; it may fail standard mandatory-backend validation (pre-existing).
# go run ./cmd/lipstd check-config --config config.yaml
go run ./cmd/lipstd check-config --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd routes --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd inventory --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd check-config --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/lipstd routes --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/lipstd inventory --config config/examples/reasoning-preservation-restore.yaml

go test -count=1 -run 'TestPhase5_|TestVisibleThinkerReasoning_' ./internal/core/runtime/ ./internal/plugins/features/reasoningpreservation/ ./internal/plugins/frontends/parity/
go test -count=1 -run 'TestEnsureReasoningOutputPreservationInConfig|TestBuildHost_.*ReasoningPreservation' ./internal/standardplugins/ ./internal/infra/runtimebundle/
go test -count=1 -run TestReasoningPreservationHTTP_DefaultOnInjection ./internal/stdhttp/
go test -count=1 -run 'TestReasoningPreservationHTTP' ./internal/stdhttp/
go test -tags=precommit -count=1 -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/
# Soak is env-gated only (not required for PR green):
# LIP_REASONING_E2E_SEEDS=3 LIP_REASONING_E2E_TURNS=4 LIP_REASONING_E2E_WORKERS=2 make test-reasoning-e2e-soak
make quality-checks
make test-unit
make parity-checks
go test -fuzz=FuzzComputeAnchor$ -fuzztime=30s -run=^$ ./internal/plugins/features/reasoningpreservation/
go test -fuzz=FuzzDecodeConfig$ -fuzztime=30s -run=^$ ./internal/plugins/features/reasoningpreservation/
make test-fuzz   # full suite; do not claim if only targeted fuzz ran
make qa          # quality + unit + lint + vuln (+ integration when DSN set)
```

### Full HTTP E2E suite topology

| Suite | Command / gate | PR mandatory? |
|-------|----------------|---------------|
| Deterministic Chat + Responses FE×BE topology + Anthropic | `go test -run TestReasoningPreservationHTTP ./internal/stdhttp/` | Yes (default tags) |
| Default-on injection / catalog boundary | `go test -run TestReasoningPreservationHTTP_DefaultOnInjection ./internal/stdhttp/` | Yes (default tags; Moonshot, GPT 5.5, GPT 5.6 negative, unmatched, explicit opt-out) |
| Seeded Responses presence smoke | included in default `TestReasoningPreservationHTTP*` / `reasoninge2e.ResponsesSmokeCases` | Yes (default tags; fixed seed, content-safe traces) |
| Seeded Chat matrix 64×20 | `go test -tags=precommit -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/` | Via `make qa` / CI `-tags=precommit`; not default `test-unit`; not lightweight `test-precommit-extra` |
| Soak 1000×100 | `make test-reasoning-e2e-soak` / `.github/workflows/reasoning-e2e-soak-nightly.yml` | **No** (env + nightly/manual only) |
| Live provider smoke | env-gated only | **No** (optional; not required) |


### Recorded evidence (Phase 6 repair, Windows local 2026-07-18)

Historical Phase 6 gate record (pre default-on / catalog-v2 docs slice). Do not treat as proof of later default-injection or shipped-config checks.

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

### Default-on / catalog-v2 evidence (docs/config consistency slice)

| Gate | Result | Notes |
|------|--------|-------|
| `lipstd check-config` `config/config.yaml` | OK | Explicit enabled default row |
| `lipstd check-config` root `config.yaml` | **Pre-existing gap** | Short example documents reasoning defaults; missing mandatory disabled backends (openrouter/…) — not expanded in this feature slice |
| `lipstd check-config` observe/restore examples + dogfood-local-stub | OK | |
| `lipstd inventory` dogfood-local-stub | OK | Feature enabled + attempt_transform / final_stream_observation posture |
| `TestEnsureReasoningOutputPreservationInConfig*` + `TestBuildHost_.*ReasoningPreservation` | OK | standardplugins + runtimebundle |
| `TestBuiltinCatalog*` / `TestModelEligible*` / `TestCatalogVersion*` / `TestCompatibleReplay*` | OK | reasoningpreservation + reasoningreplay + openaicaps |
| `TestReasoningPreservationHTTP_DefaultOnInjection` | OK | Moonshot, GPT 5.5, GPT 5.6 inert, unmatched inert, explicit opt-out |
| `TestPhase5_*` + `TestVisibleThinkerReasoning_*` | OK | |
| `go test -run 'TestDogfood\|TestInventory\|Golden' ./cmd/lipstd/` | OK | |

### OpenAI Responses exact path (local Windows 2026-07-19)

| Gate | Result | Notes |
|------|--------|-------|
| Focused packages (lipapi/feature/FE/BE/mapper/item/reasoninge2e/archtest/stdhttp HTTP) | OK | Exact ingest/encode/restore + topology matrix |
| Cancel/Close aborts unresolved drafts; success-only commit | OK | Feature + sdkStream Close → `AbortReasoningAssembly` |
| Privacy hardening tables | OK | Errors/logs/telemetry content-safe |
| Seeded Responses presence smoke | OK | Fixed-seed `ResponsesSmokeCases` |
| Soak smoke `SEEDS=4 TURNS=2 WORKERS=2` | OK | Env-gated; re-run PASS; **not** wide 1000×100 |
| Fuzz targets ~5s | OK | Canonize / FE decode / BE union / exact observer |
| Focused benches (short) | OK | No hard allocation threshold |
| Fuzz 30s (four targets) | OK | Canonize / FE decode / BE union / exact observer |
| Precommit Chat RandomMatrix | OK | `go test -tags=precommit -run TestReasoningPreservationHTTP_RandomMatrix` |
| TopologyMatrix `-count=3` | OK | Default tags |
| `make quality-checks` | OK | ~35s (gofmt on `pkg/lipapi/reasoning_part_event_test.go`) |
| `make test-unit` | OK | ~61s |
| `make parity-checks` | OK | ~16s |
| `make qa` | OK | ~77s (includes lint 0 issues + govulncheck) |
| Wide soak 1000×100 | **Pending** | Nightly/CI only |
| Linux `-race` | **Pending** | Windows cgo/`make test-race` unavailable — do not claim race-green |
| Live provider smoke | **Not run** | Optional; not required |

### Platform / skip notes

| Gate | Windows local | Linux CI |
|------|---------------|----------|
| `make test-race` | Skipped (no-op) / cgo fail — **do not claim race-green** | Required via `scripts/race-check.sh --strict` |
| Full `make test-fuzz` | Claim only if the full target list completed | Same |
| PostgreSQL authority | Env-gated via `LIP_TEST_POSTGRES_DSN`; when set, integration tests must pass | Env-gated |
| Live provider smoke | Not required for this feature | Optional |

`TestPostgresLedgerStore_recordsRoundTrip` must use unique request/attempt IDs per run (shared DSN tables accumulate rows). Do not wipe external DB; isolate with unique IDs only.

## Manifest hygiene

- [phase-1-red-manifest.md](../.kiro/specs/archive/reasoning-output-preservation/phase-1-red-manifest.md) intentional RED section must list no Phase 1–5 semantic gaps after Phase 6.
- Tasks 5.1–6.4 marked complete in `tasks.md` only when gate evidence matches.
- No stale `*_RED` names claimed as still-failing once fulfilled.

## Changed-file checklist (handoff)

- Operator docs + examples under `docs/` and `config/examples/`.
- Feature/runtime/adapter tests green.
- Fuzz targets registered in `Makefile` / `docs/release-gates.md`.
- EchoesVault page + daily log updated.
- Spec `tasks.md` / `spec.json` status updated (archive only if separately requested).
