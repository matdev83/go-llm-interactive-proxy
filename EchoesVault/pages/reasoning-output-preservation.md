---
type: architecture
title: Reasoning Output Preservation
description: Opt-in process-local historical reasoning capture and restore (issue #157).
stack: [go]
tags: [reasoning, features, streaming, privacy, issue-157]
status: active
---

# Reasoning Output Preservation

V1 opt-in feature plugin `reasoning-output-preservation` restores uniquely missing historical assistant reasoning on later turns within the same authoritative secure session. Authoritative operator docs: [docs/reasoning-output-preservation.md](../../docs/reasoning-output-preservation.md). Spec: `.kiro/specs/reasoning-output-preservation/`.

## Ownership

- Canonical reasoning parts and hard `reasoning_replay` capability live in `pkg/lipapi`.
- Generic attempt-transform and final-stream-observer runners live in core/SDK seams.
- Matching, catalog, bounded `TurnStore`, observer, transform, and safe telemetry live in `internal/plugins/features/reasoningpreservation/`.
- Wire dialects and encode legality stay in frontend/backend adapters.
- Standard registration: `internal/standardplugins` factory id `reasoning-output-preservation`.

## Operator posture

- Actions: `observe` (capture only) or `restore` (capture + restore).
- Explicit rules before optional built-in `kimi-moonshot.v1` catalog.
- State is process-local, TTL/turn/byte bounded; restart/rebalance loses artifacts.
- Hard `reasoning_replay`: unsupported dialects never silently downgrade; `reject` excludes, configured `log_skip` continues without restore.
- Privacy: `BuildSafeInventory`/telemetry = fixed aggregate outcomes only; `lipstd inventory` = generic participant/stage posture; never payloads/anchors/partitions.
- Examples (`config/examples/reasoning-preservation-*.yaml`) are local-stub **config-validation dogfood**; behavioral E2E is `TestPhase5_*`.

## Phase status

Phases 1–5 landed contracts, feature store/transform/observer, adapter dialects, and real-bundle runtime proofs (`TestPhase5_*`). Phase 6 added operator docs/examples, fuzz wiring (`FuzzComputeAnchor`, `FuzzDecodeConfig`), conformance/parity pointers, release checklist, and EchoesVault compilation. Gate evidence (2026-07-18 repair): `make qa` OK after ledger unique-ID isolation; targeted fuzz OK; full `make test-fuzz` not claimed; `make test-race` skipped on Windows. Spec `phase` is `implementation-complete` (not archived unless separately requested).
