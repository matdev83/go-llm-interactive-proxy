---
type: architecture
title: Reasoning Output Preservation
description: Standard-distribution default-on, catalog-gated historical reasoning capture/restore (issue #157) including OpenAI Responses exact opaque replay and full HTTP E2E suite topology.
stack: [go]
tags: [reasoning, features, streaming, privacy, issue-157, e2e, openai-responses]
status: active
---

# Reasoning Output Preservation

V1 feature plugin `reasoning-output-preservation` restores uniquely missing historical assistant reasoning on later turns within the same authoritative secure session. Standard `lipstd` / `runtimebundle.BuildHost` injects an enabled `restore` default row when absent (`use_builtin_catalog: true`, `compatible-auto.v2`, reject/log_skip policies, 24h / 16 turns / 64KiB / 256KiB bounds); explicit matching `enabled: false` (or omitted enabled) opts out with no participants (D12). **Installed/enabled ≠ universally active:** capture and restore require ResolveMatch eligibility (builtin catalog or explicit enabled rule); unmatched candidates leave the feature inactive for that request (no store read/append, no event parse/buffer, no restore, no outcome telemetry). Not a `StandardDistributionRequirements` entry — custom roots are not injected. Authoritative operator docs: [docs/reasoning-output-preservation.md](../../docs/reasoning-output-preservation.md). Parent spec: `.kiro/specs/reasoning-output-preservation/`. Chat/Anthropic HTTP E2E follow-up: `.kiro/specs/reasoning-preservation-e2e-validation/`. OpenAI Responses exact path: `.kiro/specs/openai-responses-reasoning-preservation/`.

## Ownership

- Canonical reasoning parts and hard `reasoning_replay` capability live in `pkg/lipapi` (`EventReasoningPart` + `Collected.ReasoningParts` for terminal exact items; Chat/Anthropic carriers remain distinct).
- Generic attempt-transform and final-stream-observer runners live in core/SDK seams.
- Matching, catalog, bounded process-local `TurnStore`, observer, transform, and safe telemetry live in `internal/plugins/features/reasoningpreservation/`.
- Shared automatic model/prefix matcher: `internal/reasoningreplay` (`compatible-auto.v2`), consumed by the feature catalog and `internal/plugins/backends/openaicaps` compatible replay eligibility. Automatic set: DeepSeek, Kimi/Moonshot (`moonshot` / `moonshotai`), GLM, MiMo, Qwen, HY3, MiniMax, GPT 5.x through 5.5; GPT 5.6+ / GPT 6+ / GPT 4.x excluded automatically (dot/dash/underscore numeric minors share the ceiling; named suffixes like `gpt-5-mini` stay eligible; boundary-aware; explicit rules retain contains semantics and may opt into GPT 5.6+).
- Exact OpenAI Responses reasoning-item Opaque schema (allowlisted fields; absent/null/empty/value presence): `internal/plugins/protocols/openairesponsesitem` (FE and BE share; no FE→BE imports).
- Streaming assembly/mapper: `internal/plugins/backends/protocols/openairesponsestream` (+ openairesponses / openaicompat adapters). Cancel/Close aborts unresolved drafts.
- Wire dialects and encode legality stay in frontend/backend adapters; Responses BE exact replay has no summary/text fallback.
- Standard registration: `internal/standardplugins` factory id `reasoning-output-preservation`.
- Full HTTP E2E harness lives in `internal/stdhttp` + `internal/testkit/reasoninge2e` + protocol emulators under `internal/refbackend` / `internal/refclient` (not core).

## Operator posture

- Actions: `observe` (capture only) or `restore` (capture + restore). Standard default inject uses `restore`.
- Explicit rules before optional built-in `compatible-auto.v2` automatic families; unmatched models leave the feature inactive for that request (no store read/append, no event parse/buffer, no restore, no outcome telemetry). Explicit rules keep contains keyword semantics and may opt into GPT 5.6+. Automatic family tokens use left/non-letter-right boundaries (shared `internal/reasoningreplay`); letter-continuation incidentals stay inactive.
- State is process-local, TTL/turn/byte bounded; restart/rebalance loses artifacts.
- Hard `reasoning_replay`: unsupported dialects never silently downgrade or convert; `reject` excludes, configured `log_skip` continues without restore.
- Privacy: `BuildSafeInventory`/telemetry = fixed aggregate outcomes only; `lipstd inventory` = generic participant/stage posture; never payloads/anchors/partitions/ids/summary/content/encrypted values.
- Examples (`config/examples/reasoning-preservation-*.yaml`) are local-stub **config-validation dogfood**; shipped `config/config.yaml` / root `config.yaml` document the standard defaults; in-process behavioral proofs are `TestPhase5_*`; full HTTP proofs are `TestReasoningPreservationHTTP*` (including default-on injection and Responses FE×BE topology).

## OpenAI Responses exact semantics

- Dialect `openai.responses.reasoning_item.v1`: complete allowlisted item Opaque only.
- Four combinations: Chat/Chat, Responses/Responses, Chat FE/Responses BE, Responses FE/Chat BE.
- Backends stream; frontend nonstream collects the canonical stream.
- Parallel losers, cancellation, EOF/error, and gate-replaced completions do not commit pending exact artifacts.

## Full HTTP E2E suite topology

| Suite | Placement |
|---|---|
| Deterministic OpenAI Chat + Responses FE×BE topology + Anthropic signed/redacted thinking | Default `go test` / `make test-unit` |
| Default-on injection / catalog boundary (Moonshot, GPT 5.5, GPT 5.6 negative, unmatched, explicit opt-out) | Default suite (`TestReasoningPreservationHTTP_DefaultOnInjection`) |
| Seeded Responses presence smoke (`reasoninge2e.ResponsesSmokeCases`) | Default suite (fixed seed, content-safe traces) |
| Seeded Chat 64×20 matrix (16/16/32) | `//go:build precommit`; `make qa` / targeted matrix command (not lightweight `test-precommit-extra`) |
| Env-gated 1000×100 soak (250/250/500) | `LIP_REASONING_E2E_SOAK=1` + `make test-reasoning-e2e-soak`; nightly `.github/workflows/reasoning-e2e-soak-nightly.yml` (not a PR gate) |
| Live provider smoke | Optional env-gated only; not required for local green |

Failure output is content-safe: seed/mode/turn/structural reason codes only. Deep Chat coverage includes reasoning+tool replay; soak supports single-seed replay via `LIP_REASONING_E2E_MODE` + `LIP_REASONING_E2E_SEED`.

## Phase status

Parent feature phases 1–6 and Chat/Anthropic HTTP E2E landed. Spec `openai-responses-reasoning-preservation` has local implementation + hardening evidenced (exact carrier, neutral schema package, BE ingest/cancel, feature capture/restore, FE fidelity, ref harness, FE×BE HTTP matrix, four targeted fuzz runs at 30s each, a 4×2×2 soak smoke, and passing quality/unit/parity/QA gates). **Pending external release gates:** Linux `-race`, the wide 1000×100 soak, and optional live-provider validation. Do not claim race-green, wide-soak, or live-provider validation until evidenced. `implementation_complete` remains false until Requirement 10 gates pass.
