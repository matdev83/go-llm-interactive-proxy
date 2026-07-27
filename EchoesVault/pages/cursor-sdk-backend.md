---
type: architecture
title: Cursor SDK Backend
description: Experimental Cursor SDK backend — operator install, auth/billing separation, local-only routing, safety defaults, and process-local continuity.
stack: [go, node, typescript]
tags: [backends, cursor, sdk, subprocess, local-agent, experimental]
status: active
---

# Cursor SDK Backend

## Status and Scope

Experimental rollout evidence for `cursorsdk` is complete (spec Task 7 ACCEPT 2026-07-18, post repair-wave): quality gates, honest platform/fake smoke, live summary statuses, and offline ACP-versus-SDK comparison retain both connectors. The backend stays experimental and non-default; measured live/comparative dogfood remains an opt-in **blocked residual** without credentials. It does not deprecate ACP. Operator documentation lives in [docs/cursor-sdk-backend.md](../../docs/cursor-sdk-backend.md); schema example in [config/examples/cursor-sdk-experimental.yaml](../../config/examples/cursor-sdk-experimental.yaml).

The backend is a Go-driven adapter plus a project-owned Node companion. The Node bridge imports exact `@cursor/sdk` 1.0.23; Go owns process supervision, canonical transcript authority, event mapping, cancellation escalation, bridge generations, and final shutdown. Core routing, failover, output commitment, attempt budgets, and B-leg lineage remain unchanged.

## Operator essentials

- Install the packaged bridge manually (`npm ci` in `connectors/cursorsdk/bridge-node`); Go-LIP never installs npm dependencies.
- Node `>=22.13`; exact `@cursor/sdk` 1.0.23; verify with `lip-cursor-sdk-bridge --version` / `doctor`.
- Auth: static SDK API key (`api_key` or bare `CURSOR_API_KEY`); separate from Cursor CLI/Desktop login and billing.
- Local-only registration; rejected under multi-user access.
- Explicit `cursorsdk:…` vs `cursorcliacp:…` routes; no connector-local fallback.
- Defaults: empty `setting_sources`, `sandbox_mode: required` (explicit `off` local-only when needed), `auto_review` off, no custom tools / agent retries / `Agent.resume`.
- MCP: configured JSON/YAML object only; no implicit LIP tool bridge; native tools stay inside the agent.
- Capabilities omitted: canonical tools, parallel tools, vision, documents, structured outputs; max-output fail-closed; reasoning exact.
- Continuity is process-local; rebuild from canonical transcript after restart.

## Verified SDK Contract

- Official runtime: Node 22.13 or newer.
- Published platforms: Linux x64/arm64, macOS x64/arm64, Windows x64. Version 1.0.23 has no Windows arm64 package.
- Structured discovery: `Cursor.models.list({apiKey})` returns model IDs, display names, parameter definitions, and variants.
- Local lifecycle: `Agent.create`, `agent.send`, `run.stream`, `run.wait`, `run.cancel`, and `Symbol.asyncDispose` are the validated APIs.
- `onDelta` provides incremental `text-delta`, `thinking-delta`, and `turn-ended` updates. `turn-ended.usage` is per turn; `RunResult.usage` is cumulative.
- Same-process agent reuse preserves conversation state. `Agent.resume` is deliberately excluded because persisted hidden state can diverge from the canonical branch.

## Bridge Invariants

- Versioned bounded NDJSON RPC over stdin/stdout; stdout is protocol-only and stderr is bounded diagnostics.
- One bridge process per configured backend instance; multiple bounded agents inside it.
- API key is sent in a private request frame after handshake, never argv or child environment.
- SDK local store is adapter-private and in-memory. The SDK's default on-disk SQLite store is not used.
- `settingSources` defaults to empty, auto-review defaults off, custom tools remain disabled, and `enableAgentRetries` is forced false.
- Required sandboxing fails closed. Windows x64 may require explicit local-only `sandbox_mode: off` when the SDK reports sandbox support unavailable.
- No hidden SDK-to-ACP fallback, model retry, `local.force`, or cross-process resume.

## Models and Capabilities

Both Cursor connectors publish canonical `cursor/<native-id>` rows. Registry provenance keeps backend instance and kind distinct; route prefixes are `cursorcliacp` and `cursorsdk`.

Reasoning mapping is exact and catalog-driven:

- use an advertised `reasoning` value directly;
- use an advertised `effort` only through a variant that also enables `thinking=true`;
- boolean-thinking-only models do not advertise canonical reasoning effort;
- never alias `xhigh` with `extra-high`.

First release omits canonical tools, parallel tools, vision, documents, and structured outputs. Cursor-native tools and configured MCP execute inside the agent and are never replayed as frontend tool calls.

## Stream and Lifecycle

Canonical order is response start, message start, incremental text/reasoning, conservative per-turn usage, one terminal event, then EOF. Every event must pass `lipapi.ValidateEventEnvelope`.

Cancellation calls `run.cancel` first. Timeout or an unresponsive bridge escalates to current-generation process-tree termination and reports transport cancellation. Every affected stream fails explicitly; committed output is never replayed.

`execbackend.Backend` gains one optional idempotent close callback. Runtimebundle registers backend closers before inventory startup, disposes partial construction in reverse order, and reuses existing reverse-order shutdown handling.

## Frozen First-Release Bounds

- bridge frame: 16 MiB;
- prompt: 8 MiB;
- MCP config: 256 KiB;
- retained stderr: 8 KiB;
- agents: 1-32, default 32;
- concurrent runs: 1-8, default 8 and no greater than agents;
- start: 1-120 seconds, default 30;
- cancel: 100 milliseconds-30 seconds, default 5;
- shutdown: 1-120 seconds, default 10;
- idle: zero or 1 second-24 hours, default 15 minutes.

## Evidence and live tests

Default Go tests use a fake bridge (no Node/network/account). Platform smoke (`make test-cursor-sdk-platform`) is the fake lifecycle lane (cancel must observe a cancelled terminal). Opt-in Node live (`make test-cursor-sdk-live`) covers real `@cursor/sdk` scenarios without Go process hooks. Opt-in Go→Node live bridge (`make test-cursor-sdk-live-bridge`, build tag `cursorsdk_live_bridge`, `go test -v` JSON) is test-only orchestration over production `Open`/`RunStream`: pin `@cursor/sdk` 1.0.23, discovery/content/cancel/shutdown; hard restart and full-prompt rebootstrap stay honest **blocked** live when active text-only peers could not be held or instrumentation is absent (fake lane only proves those). Ordinary `go test` never selects the tagged entrypoint. Without key/flag scripts exit 0 BLOCKED; opted-in parent summaries may be `status=blocked` with exit 0 when residuals are honest. Cross-OS workflow: `.github/workflows/cursor-sdk-platform.yml`.

Credentialed Windows aggregates (2026-07-19, sanitized): Node probe passed pin/count/deltas/dispose/cancel; Node core discovery/text/safety_off/configured_mcp/cancel/shutdown passed; reasoning skipped; sandbox-required blocked; Node lifecycle hooks blocked; reuse last credentialed observation (before text-only reuse prompt update) timed out and has not been revalidated afterward (not live-passed). Go live-bridge (`go test -v` JSON) parent blocked exit 0 with pin/discovery/content/cancel/shutdown passed; hard restart blocked because active text-only peers could not be held; rebootstrap/MCP/sandbox residuals blocked. Repaired offline defects include official anonymous variants, generation-scoped run isolation, typed process-exit diagnostics, and text-only prompts avoiding upstream pwsh spawn. Experimental rollout remains accepted; measured replacement/dogfood still blocked. Never record model IDs, keys, paths, or prompts.

ACP-versus-SDK comparative dogfood uses a bounded matrix report (`make test-cursor-sdk-comparison-report`; methodology in [docs/cursor-sdk-comparison-report.md](../../docs/cursor-sdk-comparison-report.md)). Offline runs emit synthetic/blocked aggregates only; measured comparative dogfood stays blocked until opted-in credentials and an intentional ACP lane supply safe inputs. Reports retain both connectors and do not switch defaults.

## Related

- [Plugin System](plugin-system.md)
- [Codex App Server Backend](codex-app-server-backend.md)
- [Canonical Contracts](canonical-contracts.md)
- [Streaming Model](streaming-model.md)
- [Routing and Orchestration](routing-orchestration.md)
