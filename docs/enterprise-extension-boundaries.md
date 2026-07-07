# Enterprise extension boundaries

This document defines where enterprise features may attach to the OSS core and which integration points are forbidden. It prevents enterprise code from becoming a parallel fork of core.

## Allowed integration points

Enterprise features attach through these stable, documented seams:

| Seam | Package | How enterprise attaches |
| --- | --- | --- |
| Plugin SDK facades | `pkg/lipsdk/*` | Register feature plugins (`FeatureBundle`), backend adapters, or frontend mounts through `pkg/lipsdk` contracts. Enterprise binaries compose their own registry + `runtimebundle.Build`. |
| Composition-root options | `internal/infra/runtimebundle` `BuildOptions` | Inject enterprise auth deciders, event sinks, policy observers, traffic observers, or completion gates via `BuildOptions` grouped sub-structs (`Auth`, `Extensions`, `Policy`, `Diagnostics`, `Testing`). |
| Control-plane ports | `internal/core/controlplane` | Implement control-plane `Store`, `QueryService`, `RetentionController`, or `Recorder` interfaces for enterprise persistence, audit, or search. |
| Hook bus | `internal/core/hooks` | Register enterprise submit/part/tool hooks through the standard hook bus. Prefer `FeatureBundle` over raw hooks for new code. |
| Secure-session authority | `internal/core/securesession/app` | Inject enterprise `Manager`, `Store`, or `GateRecording` implementations for custom session authority. |

## Forbidden integration points

Enterprise code MUST NOT:

- **Edit `runtime.Executor` directly** — add fields, modify `Execute`, or import `internal/core/runtime` to change orchestration behavior. New orchestration concerns belong behind hooks, extensions, or composition-root options.
- **Import deep runtime internals** — `internal/core/runtime`, `internal/core/stream`, `internal/core/leglifecycle`, or `internal/core/lineage` are not extension seams; they are use-case orchestration owned by the OSS core.
- **Fork the composition root** — enterprise binaries should call `runtimebundle.Build` (or `BuildBootstrap`) with enterprise `BuildOptions`, not reimplement assembly.
- **Bypass architecture guardrails** — the hexagonal baseline, line budgets, and import-boundary tests apply to enterprise code equally. Enterprise packages must not import `internal/core` packages beyond the allowed seams above.
- **Introduce global state or hidden registration** — no `init()` registration, package-level registries, `sync.Once` singletons, or reflection-based plugin loading.

## Typical enterprise attachment patterns

| Enterprise feature | Attach via |
| --- | --- |
| Audit/search | Control-plane `Store` + `QueryService` interfaces; traffic observers via `BuildOptions.Extensions.TrafficObservers`. |
| Billing | Token-accounting ledger adapter; `BuildOptions.Extensions.UsageObservers`; `BuildOptions.Policy.PolicyObservers`. |
| SSO / user provisioning | `BuildOptions.Auth.RemoteDecider`; `BuildOptions.Auth.OSIdentity`; `BuildOptions.Auth.AuthEventSink`. |
| Custom routing policy | `BuildOptions.Extensions.RouteHintProviders`; `BuildOptions.Extensions.CompletionGates`; `BuildOptions.Policy.PolicyTimeoutBudgetSource`. |
| Custom backends | Backend plugin via `pkg/lipsdk` factory; register on a custom `*pluginreg.Registry`. |

## Reviewer guidance

When reviewing enterprise-adjacent PRs, check:

- [ ] Does the change use only allowed integration points?
- [ ] Does the change avoid importing deep runtime internals?
- [ ] Does the change keep provider/protocol details out of core?
- [ ] Does the change avoid new global state, init registration, and lazy singletons?
- [ ] Does the change keep canonical streaming as the primary execution path?
