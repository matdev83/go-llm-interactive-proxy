# Enterprise extension boundaries

This document defines where enterprise features may attach to the OSS core and which integration points are forbidden. It prevents enterprise code from becoming a parallel fork of core.

## Allowed integration points

Enterprise features attach through these stable, documented seams:

| Seam | Package | How enterprise attaches |
| --- | --- | --- |
| Plugin SDK facades | `pkg/lipsdk/*` | Register feature plugins (`FeatureBundle`), backend adapters, or frontend mounts through `pkg/lipsdk` contracts. Enterprise binaries compose via public `pkg/lipruntime.Build` with `lipruntime.Options`. |
| Composition-root options | `pkg/lipruntime.Options` / `runtimebundle.ProductionOptions` | Prefer canonical registration fields on public `lipruntime.Options`: `RequestRegistrations`, `AttemptRegistrations`, `ConcurrencyRegistration`, and `RaterRegistrations` (plus observers/metering). Internal distributions use registration-only `runtimebundle.ProductionOptions` via `BuildHostInput.Production`. Deprecated parallel provider/rater fields (`RequestProviders`, `AttemptProviders`, `ConcurrencyProvider`, `Rater`, `ProviderDescriptors`) remain current major source compatibility only and are quarantined in `pkg/lipruntime`; see [lipruntime-options-migration.md](lipruntime-options-migration.md). Grouped `BuildOptions` sub-structs remain for focused internal composition helpers, not as a `BuildHost` parameter. |
| Control-plane ports | `internal/core/controlplane` | Implement control-plane `Store`, `QueryService`, `RetentionController`, or `Recorder` interfaces for enterprise persistence, audit, or search. |
| Hook bus | `internal/core/hooks` | Register enterprise submit/part/tool hooks through the standard hook bus. Prefer `FeatureBundle` over raw hooks for new code. |
| Secure-session authority | `internal/core/securesession/app` | Inject enterprise `Manager`, `Store`, or `GateRecording` implementations for custom session authority. |

## Forbidden integration points

Enterprise code MUST NOT:

- **Edit `runtime.Executor` directly** — add fields, modify `Execute`, or import `internal/core/runtime` to change orchestration behavior. New orchestration concerns belong behind hooks, extensions, or composition-root options.
- **Import deep runtime internals** — `internal/core/runtime`, `internal/core/stream`, `internal/core/leglifecycle`, or `internal/core/lineage` are not extension seams; they are use-case orchestration owned by the OSS core.
- **Fork the composition root** — enterprise binaries should call public `pkg/lipruntime.Build` with `lipruntime.Options` (observers, metering, canonical `RequestRegistrations` / `AttemptRegistrations` / `ConcurrencyRegistration` / `RaterRegistrations`), not reimplement assembly. Do not grow the legacy provider/rater fields; migrate using [lipruntime-options-migration.md](lipruntime-options-migration.md). Internal distributions that already import `runtimebundle` compose via `runtimebundle.BuildHost` with registration-only `BuildHostInput` / `ProductionOptions` — not `BuildOptions`, which is a separate composition bag and is not accepted by `BuildHost`.
- **Bypass architecture guardrails** — the hexagonal baseline, line budgets, and import-boundary tests apply to enterprise code equally. Enterprise packages must not import `internal/core` packages beyond the allowed seams above.
- **Introduce global state or hidden registration** — no `init()` registration, package-level registries, `sync.Once` singletons, or reflection-based plugin loading.

## Typical enterprise attachment patterns

| Enterprise feature | Attach via |
| --- | --- |
| Audit/search | Control-plane `Store` + `QueryService` interfaces; traffic observers via `lipruntime.Options.TrafficObservers` (or `BuildHostInput.Production.TrafficObservers`). |
| Billing | Token-accounting ledger adapter; `lipruntime.Options.UsageObservers` / `PolicyObservers`. |
| SSO / user provisioning | Internal composition helpers using `runtimebundle.BuildOptions.Auth` (`RemoteDecider`, `OSIdentity`, `AuthEventSink`) — not currently a `lipruntime.Options` field and not a `BuildHost` parameter. |
| Custom routing policy | Internal `BuildOptions.Extensions` / `BuildOptions.Policy` helpers where those fields are wired; prefer FeatureBundle hooks for new code. |
| Custom backends | Backend plugin via `pkg/lipsdk` factory; register on a custom `*pluginreg.Registry` before public/internal Build. |

## Reviewer guidance

When reviewing enterprise-adjacent PRs, check:

- [ ] Does the change use only allowed integration points?
- [ ] Does the change avoid importing deep runtime internals?
- [ ] Does the change keep provider/protocol details out of core?
- [ ] Does the change avoid new global state, init registration, and lazy singletons?
- [ ] Does the change keep canonical streaming as the primary execution path?
