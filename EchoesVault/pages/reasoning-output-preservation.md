---
type: architecture
title: Reasoning Output Preservation
description: Opt-in process-local historical reasoning capture/restore (issue #157) plus full HTTP E2E validation suite topology.
stack: [go]
tags: [reasoning, features, streaming, privacy, issue-157, e2e]
status: active
---

# Reasoning Output Preservation

V1 opt-in feature plugin `reasoning-output-preservation` restores uniquely missing historical assistant reasoning on later turns within the same authoritative secure session. Authoritative operator docs: [docs/reasoning-output-preservation.md](../../docs/reasoning-output-preservation.md). Parent spec: `.kiro/specs/reasoning-output-preservation/`. Follow-up full HTTP E2E validation: `.kiro/specs/reasoning-preservation-e2e-validation/`.

## Ownership

- Canonical reasoning parts and hard `reasoning_replay` capability live in `pkg/lipapi`.
- Generic attempt-transform and final-stream-observer runners live in core/SDK seams.
- Matching, catalog, bounded `TurnStore`, observer, transform, and safe telemetry live in `internal/plugins/features/reasoningpreservation/`.
- Wire dialects and encode legality stay in frontend/backend adapters.
- Standard registration: `internal/standardplugins` factory id `reasoning-output-preservation`.
- Full HTTP E2E harness lives in `internal/stdhttp` + `internal/testkit/reasoninge2e` + protocol emulators under `internal/refbackend` (not core).

## Operator posture

- Actions: `observe` (capture only) or `restore` (capture + restore).
- Explicit rules before optional built-in `kimi-moonshot.v1` catalog.
- State is process-local, TTL/turn/byte bounded; restart/rebalance loses artifacts.
- Hard `reasoning_replay`: unsupported dialects never silently downgrade; `reject` excludes, configured `log_skip` continues without restore.
- Privacy: `BuildSafeInventory`/telemetry = fixed aggregate outcomes only; `lipstd inventory` = generic participant/stage posture; never payloads/anchors/partitions.
- Examples (`config/examples/reasoning-preservation-*.yaml`) are local-stub **config-validation dogfood**; in-process behavioral proofs are `TestPhase5_*`; full HTTP proofs are `TestReasoningPreservationHTTP*`.

## Full HTTP E2E suite topology

| Suite | Placement |
|---|---|
| Deterministic OpenAI Chat controls + Anthropic signed/redacted thinking cross-check | Default `go test` / `make test-unit` |
| Seeded 64×20 matrix (16/16/32) | `//go:build precommit`; `make qa` / targeted matrix command (not lightweight `test-precommit-extra`) |
| Env-gated 1000×100 soak (250/250/500) | `LIP_REASONING_E2E_SOAK=1` + `make test-reasoning-e2e-soak`; nightly `.github/workflows/reasoning-e2e-soak-nightly.yml` (not a PR gate) |
| OpenAI Responses HTTP E2E | Deferred until a stable Responses replay harness exists |

Failure output is content-safe: seed/mode/turn/structural reason codes only. Deep Chat coverage includes reasoning+tool replay; soak supports single-seed replay via `LIP_REASONING_E2E_MODE` + `LIP_REASONING_E2E_SEED`.

## Phase status

Parent feature phases 1–6 landed contracts, adapters, Phase 5 runtime proofs, docs/fuzz/checklist. Follow-up `reasoning-preservation-e2e-validation` adds standard-stack HTTP E2E, precommit matrix, and opt-in soak/Make/workflow; independent review and full QA gate evidence remain before claiming that follow-up complete.
