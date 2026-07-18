# Reasoning Output Preservation

Opt-in multi-turn reasoning replay for LLM Interactive Proxy ([issue #157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)).

When a client later submits an assistant turn without reasoning that the proxy previously observed on the same authoritative session, this feature can restore uniquely missing reasoning onto a fresh candidate clone before backend open. Spec: [`.kiro/specs/reasoning-output-preservation/`](../.kiro/specs/reasoning-output-preservation/).

## Goals

- Capture bounded reasoning artifacts from final canonical assistant output after response hooks and completion gates.
- Match later assistant history by exact non-reasoning content only.
- Restore only unique `missing` reasoning at recorded positions; never overwrite client-supplied reasoning.
- Keep artifacts session-scoped, TTL/turn/byte bounded, process-local, and absent from ordinary observability.
- Hard `reasoning_replay`: unsupported dialects never silently downgrade. Policy `on_unrepresentable: reject` excludes the candidate; `log_skip` continues without restore (configured skip, not silent success).

## Non-goals (v1)

- Generating, reconstructing, summarizing, or evaluating unobserved reasoning.
- Fuzzy, semantic, embedding, or LLM-assisted matching.
- Cross-session, cross-principal, cross-plugin, or cross-replica restoration.
- Durable/distributed artifact storage.
- Making reasoning visible when a frontend/backend has no legal wire form.
- Retry/failover after first downstream content (existing runtime invariant unchanged).

## Enablement

Feature plugin id: `reasoning-output-preservation` (registered in `internal/standardplugins`).

Disabled by default: omit the features row, or set `enabled: false`. Absent/disabled means no store, no attempt transform, no stream observer, no hashing, and no feature telemetry (D12).

When `enabled: true`, YAML `action` is required: `observe` or `restore`.

### Operator examples (config-validation dogfood)

These YAML files prove `check-config` / `routes` / `inventory` composition with a **local-stub** backend. They do **not** exercise live reasoning capture/restore end-to-end (stub text has no reasoning stream). Behavioral proofs: `TestPhase5_*` in `internal/core/runtime` and `internal/plugins/features/reasoningpreservation`.

| File | Action | Use |
|------|--------|-----|
| [`config/examples/reasoning-preservation-observe.yaml`](../config/examples/reasoning-preservation-observe.yaml) | `observe` | Config-validation: capture-capable wiring |
| [`config/examples/reasoning-preservation-restore.yaml`](../config/examples/reasoning-preservation-restore.yaml) | `restore` | Config-validation: restore-capable wiring |

Validate:

```bash
go run ./cmd/lipstd check-config --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd routes --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd inventory --config config/examples/reasoning-preservation-observe.yaml
go run ./cmd/lipstd check-config --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/lipstd routes --config config/examples/reasoning-preservation-restore.yaml
go run ./cmd/lipstd inventory --config config/examples/reasoning-preservation-restore.yaml
```

**`lipstd inventory`** (enabled): shows the feature instance and generic stage posture — `attempt_transform` / stream-observer participant IDs occupied. It does **not** print outcome aggregates or reasoning payloads.

**Feature `BuildSafeInventory` / telemetry API** (in-process): exposes fixed aggregate outcome counters (`observed`, `restored`, …) and config posture only — never payloads, anchors, signatures, opaque JSON, or session partition keys. When the feature is disabled/absent: no reasoning participants in CLI inventory and empty/disabled safe inventory.

## Configuration

| Field | Meaning |
|-------|---------|
| `action` | `observe` (capture only) or `restore` (capture + restore missing) |
| `use_builtin_catalog` | When true, built-in family/prefix + model keyword entries apply after explicit rules |
| `rules` | Explicit backend instance rules (`id`, `backend`, optional `model_keywords`, required `enabled`) |
| `on_ambiguous` | Must be `log_skip` (non-mutating) |
| `on_unrepresentable` | `reject` (exclude candidate) or `log_skip` |
| `on_state_error` | `reject` or `log_skip` |
| `state.ttl` | Artifact TTL |
| `state.max_turns_per_session` | Max artifacts per authoritative session |
| `state.max_reasoning_bytes_per_turn` | Per-turn reasoning payload bound |
| `state.max_session_bytes` | Per-session total reasoning byte bound |

### Rule / catalog precedence

1. Explicit rule with `model_keywords` matching the candidate model (`enabled: true` / `false`).
2. Explicit backend-only rule (no keywords) for that backend instance id.
3. When `use_builtin_catalog: true`, built-in `kimi-moonshot.v1` family prefixes + model keywords.
4. Otherwise no match → no capture/restore for that candidate.

Operator rules match exact backend instance ids. Built-ins use stable family prefixes plus model keywords and never invent cross-family dialect support.

## Dialect matrix (v1)

| Dialect ID | Typical family | Notes |
|------------|----------------|-------|
| `openai.chat.reasoning_text.v1` | OpenAI-compatible chat / OpenRouter / local OpenAI-chat | Text-style historical reasoning |
| `openai.responses.reasoning_item.v1` | OpenAI Responses | Reasoning item form |
| `anthropic.thinking.v1` | Anthropic Messages | Thinking + optional signature |
| `anthropic.redacted_thinking.v1` | Anthropic Messages | Redacted thinking blocks |

Hard capability: `reasoning_replay`. Every restored dialect must be explicitly supported by the candidate; unsupported dialects follow `on_unrepresentable`. Gemini generateContent is unsupported for positive replay in v1 (explicit unsupported classification).

## Failure policies

| Classification / fault | Behavior |
|------------------------|----------|
| `preserved` | Leave client reasoning unchanged |
| `missing` + unique match | Restore at recorded positions (`restore` action) |
| `conflicting` / `ambiguous` / `unmatched` | Non-mutating (`on_ambiguous` is `log_skip`) |
| Unrepresentable dialect | `reject` → `exclude_candidate` / `log_skip` → continue without restore |
| Store/state errors | `reject` → exclude candidate; `log_skip` → continue without restore |
| Observer/store append failure | Fail-open on committed output; never triggers retry after first content |

## Bounds and privacy

- Artifacts are capped by TTL, turns/session, bytes/turn, and bytes/session.
- Oversize reasoning is discarded (safe `oversize` outcome).
- Feature telemetry / `BuildSafeInventory` expose only fixed outcomes and aggregate counts/bytes: `observed`, `preserved`, `missing`, `restored`, `ambiguous`, `conflicting`, `unmatched`, `unrepresentable`, `state_error`, `evicted`, `oversize`. CLI `lipstd inventory` shows participant posture only (not these aggregates).
- Never log reasoning text, signatures, opaque JSON, anchors, excerpts, or session partition keys.

## Process-local / sticky / restart

- V1 state is process-local memory owned by one feature instance.
- Restoration requires the same process and authoritative secure-session resume.
- Sticky routing helps keep eligible backends, but does not replicate artifacts across replicas.
- Restart, rebalance, or another instance → state miss (no restore) until new observations accumulate.
- Client-supplied session hints cannot access another partition.

## Runtime invariants (operators)

- Restore runs on `CloneCall(baseline)` per candidate before final eligibility/Open.
- Capture commits only on `success_released` for the winning surfaced B-leg.
- Parallel losers, cancellation, EOF/close, and gate-replaced completions do not persist pending artifacts.
- No transparent retry/failover after first downstream content event.

## Migration / compatibility

- Existing deployments with the feature absent remain unchanged.
- Enabling requires an explicit features row + valid `action` and bounds.
- Python-era LIP had no equivalent Go-portable durable store; do not expect cross-process continuity in v1.
- See [feature-migration-map.md](feature-migration-map.md) and [reasoning-output-preservation-release-checklist.md](reasoning-output-preservation-release-checklist.md).

## Full HTTP E2E validation

Follow-up spec: [`.kiro/specs/reasoning-preservation-e2e-validation/`](../.kiro/specs/reasoning-preservation-e2e-validation/) (parent issue [#157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157); parent feature spec [`.kiro/specs/reasoning-output-preservation/`](../.kiro/specs/reasoning-output-preservation/)).

Proofs drive a stateful client transcript and independent backend-request oracle through `runtimebundle.BuildBootstrap` + `stdhttp` + real Chat/Anthropic adapters + protocol emulators (`internal/refbackend`). Failures report seed/mode/turn/structural reason codes only — never reasoning text, signatures, opaque payloads, anchors, or raw bodies.

| Suite | Gate | What it covers |
|-------|------|----------------|
| Deterministic controls | Default `go test` / `make test-unit` | OpenAI Chat disabled/observe/restore paths, tools, conflict/ambiguity, session isolation |
| Anthropic cross-check | Default suite | Signed `thinking` / `redacted_thinking` observe/restore (or observe-only) through real Anthropic adapters |
| Seeded matrix 64×20 | `//go:build precommit` | 16 `random_backend_drop_all` + 16 `always_reason_random_client` + 32 `combined`; 20 HTTP turns/seed; run via `make qa` tagged suite or targeted `go test -tags=precommit -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/` (not lightweight `make test-precommit-extra`) |
| Soak 1000×100 | Env `LIP_REASONING_E2E_SOAK=1` + `make test-reasoning-e2e-soak` | Same full HTTP stack; 250/250/500 mode split; bounded workers; **not** a PR/default gate |

```bash
# Deterministic + Anthropic HTTP E2E (default tags)
go test -count=1 -run 'TestReasoningPreservationHTTP' ./internal/stdhttp/

# Precommit 64×20 matrix
go test -tags=precommit -count=1 -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/

# Opt-in soak (defaults 1000 seeds × 100 turns; override for smoke)
LIP_REASONING_E2E_SEEDS=3 LIP_REASONING_E2E_TURNS=4 LIP_REASONING_E2E_WORKERS=2 make test-reasoning-e2e-soak

# Single-seed soak replay (one command)
LIP_REASONING_E2E_SOAK=1 LIP_REASONING_E2E_MODE=combined LIP_REASONING_E2E_SEED=42 \
  go test -tags=precommit -run TestReasoningPreservationHTTP_Soak -count=1 ./internal/stdhttp/
```

Soak env: `LIP_REASONING_E2E_SOAK=1` (required), `LIP_REASONING_E2E_SEEDS` (default 1000), `LIP_REASONING_E2E_TURNS` (default 100), `LIP_REASONING_E2E_WORKERS` (default 4, max 32), replay pair `LIP_REASONING_E2E_MODE` + `LIP_REASONING_E2E_SEED`. Nightly/manual workflow: `.github/workflows/reasoning-e2e-soak-nightly.yml` (not attached to PR `qa.yml` or `race-fuzz-nightly.yml`).

**OpenAI Chat** is the deep HTTP E2E surface (including reasoning + tool-call replay). **OpenAI Responses** full HTTP E2E is deferred until a stable Responses replay harness exists — do not treat Responses as covered by these suites.

## Release evidence pointers

- Phase 5 runtime/feature proofs: `TestPhase5_*` under `internal/core/runtime` and `internal/plugins/features/reasoningpreservation`.
- Full HTTP E2E: `TestReasoningPreservationHTTP*` under `internal/stdhttp` (matrix/soak require `-tags=precommit`; soak also requires `LIP_REASONING_E2E_SOAK=1`).
- Adapter/parity: Phase 4 dialect encode + `internal/plugins/frontends/parity` VisibleThinker reasoning tests.
- Fuzz: `FuzzComputeAnchor`, `FuzzDecodeConfig` (wired into `make test-fuzz`).
- Architecture: official features must not import `internal/core` (`internal/archtest`).
