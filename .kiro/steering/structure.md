# Project Structure (Steering)

## Purpose of This File

This file defines **ownership zones and placement rules**, not a directory inventory. Package names may change and new plugins/connectors may appear without requiring a steering update when they follow the existing architecture.

For current package/file inventory, inspect the repository tree, `docs/architecture.md`, standard contribution registries, manifests, and architecture tests.

## Architectural Zones

### 1. Public contracts — `pkg/`

Public packages expose stable, versionable contracts for external callers and plugins.

- `pkg/lipapi` owns provider-neutral canonical request/event/capability/error semantics.
- `pkg/lipsdk` owns plugin/extension contracts and typed facades.
- `pkg/lipruntime` is a thin public host/reload facade over internal composition.

Rules:

- no provider SDK or internal package types in public contracts;
- keep exported surface minimal;
- add a public abstraction only for a real external/stable seam;
- public runtime options must not become a generic service bag or monetary composition surface.

### 2. Kernel and orchestration — `internal/core/`

Core owns product semantics that remain necessary with optional features disabled: routing, B2BUA lifecycle, commitment/recovery, canonical streaming, continuity/session authority, shared execution policy mechanisms, and narrow domain contracts consumed by infrastructure.

Rules:

- core may depend on public canonical/SDK contracts, never concrete plugins or provider SDKs;
- provider/protocol-specific behavior stays at adapter edges;
- optional UX/policy does not live in core merely because the executor needs to call it;
- new top-level core responsibilities must satisfy the repository's core-ownership admission rules and architecture tests.

The current allowed core package set and justification are executable policy under `internal/archtest/`; do not duplicate that package list here.

### 3. Standard-distribution composition

The standard distribution is assembled explicitly rather than through globals, reflection, or DI containers.

- `internal/pluginreg` owns registry/validation mechanics.
- `internal/standardplugins` owns the concrete built-in contribution set.
- `internal/standardplugins/featurehost` is the only composition layer that knows the concrete standard feature set and owns process/generation feature assembly.
- `internal/featurebundle` owns generic feature-surface merge mechanics.
- `internal/infra/runtimebundle` owns generic Host/process/generation composition, publication, reload, and shutdown.
- `internal/stdhttp` owns the standard HTTP/control surfaces.

Post-refactor invariant:

- `internal/core` and generic `runtimebundle` do not import concrete feature packages;
- generic runtime holds direct typed generation references/ports, not a request-time feature resolver;
- each process feature resource has one constructor path and one physical cleanup owner;
- borrowed generic resources are not closed by feature composition;
- publication-only behavior is registered into the generic generation publication lifecycle rather than executed during candidate compilation.

### 4. Protocol adapters — `internal/plugins/frontends/` and `internal/plugins/backends/`

Frontends and backends are wire/provider adapters around canonical contracts.

- frontends translate client protocols to/from canonical calls/events;
- essential in-process backends translate canonical calls/events to provider protocols;
- reusable compatible-family helpers may share transport/codec mechanics without becoming a second canonical layer.

Provider SDKs and vendor transport types stay inside these adapter boundaries.

### 5. Optional executable backends — `connectors/` and `connector-support/`

Optional integrations that should not widen the root module run as executable connectors over the versioned backend-plugin ABI.

Rules:

- connector modules remain dependency-isolated from the root module;
- discovery is manifest-driven and trust-validated;
- connector-specific dependencies stay in the connector module/support package;
- do not add an optional connector to essential fixed tables simply for convenience.

Current connector inventory is derived from manifests/release metadata, not steering.

### 6. Feature plugins — `internal/plugins/features/`

Features own optional domain behavior, configuration decoding, state/policy, and bundle construction.

Rules:

- features depend on SDK contracts, not core implementation packages;
- feature-owned process/generation resources are composed through `featurehost`;
- the closed typed extension-plane catalog is defined by `pkg/lipsdk/feature` executable metadata;
- adding a normal feature to an existing plane does not require a core branch or steering update;
- a genuinely new platform plane is an SDK/runtime architecture change and requires manifest/generator/contract updates.

### 7. Infrastructure — `internal/infra/`

Infrastructure implements technology-specific driven adapters: databases, connector hosting, HTTP clients, observability, persistence, audit, and similar concerns.

Rules:

- infrastructure implements interfaces/contracts owned by the consuming domain where practical;
- driver handles and vendor types do not leak into core policy;
- persistence behavior must preserve domain semantics across supported engines/topologies;
- infrastructure is not a dumping ground for optional product policy.

### 8. Test and certification surfaces

Reference clients/backends, testkits, architecture guards, QA checks, and contract TCKs are test support rather than production architecture.

Prefer reusable family contracts and architecture ratchets over duplicated end-to-end matrices.

## Intent-to-Zone Decision Table

| Change intent | Default ownership |
| --- | --- |
| New/changed client wire protocol | frontend adapter |
| New essential provider wire implementation | backend adapter + standard contribution |
| New optional provider/tool integration with separate dependencies/runtime | executable connector |
| New compatible vendor on an existing protocol family | provider profile/data first |
| New cross-protocol canonical semantic | `pkg/lipapi` |
| New plugin/host contract | narrow `pkg/lipsdk` seam |
| Routing, commitment, B2BUA, failover semantics | core |
| Optional UX/safety/reasoning/maintenance policy | feature plugin |
| Feature process/generation construction | `internal/standardplugins/featurehost` |
| Generic host/generation lifecycle | `internal/infra/runtimebundle` |
| SQL/driver/telemetry/client implementation | infrastructure adapter |
| Operator/admin HTTP surface | `internal/stdhttp` plus owning domain/infra service |

If the ownership decision is ambiguous, apply the kernel test: **would this behavior still be required with all optional features disabled, and is it provider/protocol neutral?** If not, it usually does not belong in core.

## Dependency-Direction Guardrails

- Core and public contracts never import concrete plugins or provider SDKs.
- Feature plugins do not import core implementations.
- Frontends do not call provider SDKs.
- Backends/connectors do not own frontend framing.
- No pairwise protocol translators.
- No request-time service locator, reflection registry, or generic feature map.
- No native Go `plugin` loading.
- No protocol/provider-specific switch statements in core when an adapter/SDK seam can own the behavior.
- No optional-feature policy hidden in generic infrastructure/composition.

## Package Design Conventions

- Define interfaces where they are consumed; keep them narrow.
- Constructors normally return concrete types unless exposing a stable SDK/plugin contract.
- Prefer function-typed ports or frozen structs when they express the seam more simply than an interface.
- Keep package names short and responsibility-focused; avoid generic `services`, `interfaces`, or `ports` buckets.
- Avoid package churn solely to imitate textbook architecture taxonomy.
- Use compile-time interface assertions for important adapter/plugin contracts.
- Keep process ownership, generation ownership, request ownership, and attempt ownership explicit.

## When Steering Should Change

Update this file when **ownership or dependency rules change** — for example, a new architectural zone, a new allowed dependency direction, or a changed process/generation ownership model.

Do not update it merely because:

- a provider/connector/feature was added or removed;
- a package was renamed within the same zone;
- a registry gained another entry;
- an implementation detail moved between files without changing responsibility.
