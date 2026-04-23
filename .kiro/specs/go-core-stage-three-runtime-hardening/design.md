# Design — Go core reimplementation stage three: runtime hardening and instance identity

Spec name: `go-core-stage-three-runtime-hardening`

## Design goals

1. Preserve the current healthy direction without recreating Python-style hidden complexity.
2. Make runtime identity truthful: **adapter kind != configured instance**.
3. Make ownership truthful: **one owner assembles and closes runtime resources**.
4. Make production behavior truthful: **tests may be deterministic; production must not be accidentally deterministic**.
5. Keep the core small and provider-agnostic.

## Status refresh

This design was originally written before most of the stage-three hardening landed.

The current tree already implements these major decisions:

- adapter kind and configured instance identity are distinct in config and registrations,
- route selectors and executor behavior are instance-aware,
- the standard server path is assembled through `internal/infra/runtimebundle`,
- runtime ownership and resource closing are explicit on the standard path,
- production clock/RNG/HTTP-client defaults are injected by the composition root,
- request-correlation middleware is always installed in the standard HTTP server,
- bundled frontends share a common execute-error classifier.

So the purpose of the remaining stage-three work is no longer to create these capabilities from scratch.
The remaining purpose is to:

1. normalize the design text so it matches the implementation that already exists,
2. decide which original targets are still necessary follow-up work,
3. avoid reopening large refactors unless they still buy clear architectural value.

---

## Why stage three is not “build the first server”

The repository already contains:

- `cmd/lipstd`
- `internal/stdhttp`
- mounted bundled frontends
- a runnable HTTP server path

So the next stage should not create “another server”.
It should turn the existing standard distribution into a **correctly owned and scalable** server/runtime.

---

## High-level architecture target

```text
                       +-----------------------------+
                       | cmd/lipstd                  |
                       | explicit standard bundle    |
                       +-------------+---------------+
                                     |
                                     v
                        +-----------------------------+
                        | explicit composition root   |
                        | today:                      |
                        | - pluginreg.Register...     |
                        | - runtimebundle.Build()     |
                        +-------------+---------------+
                                     |
                     +---------------+----------------+
                     |                                |
                     v                                v
           +--------------------+          +----------------------+
           | runtime owner      |          | stdhttp server       |
           | - store            |          | - mux                |
           | - transports       |          | - middleware         |
           | - executor         |          | - readiness/liveness |
           | - observers        |          | - graceful shutdown  |
           | - feature LCs      |          +----------------------+
           +--------------------+
                     |
                     v
           +--------------------+
           | core executor      |
           | - B2BUA            |
           | - routing          |
           | - negotiation      |
           | - hooks            |
           +--------------------+
                     |
                     v
           +--------------------+
           | backend instances  |
           | identified by      |
           | runtime instance   |
           | id, built from     |
           | adapter kind       |
           +--------------------+
```

---

## Core model changes

### 1. Split adapter kind from configured instance identity

#### Status

Implemented in the current tree.

#### Original problem

`id` currently does both of these jobs:

- identifies the bundled adapter/plugin kind
- identifies the configured instance

That prevents multiple instances of the same adapter kind.

#### New model

```text
registration family (frontend/backend/feature)
  ├── adapter kind / factory id   e.g. "openai-responses"
  └── instance id                 e.g. "openai-primary"
```

Current config shape:

```yaml
plugins:
  backends:
    - kind: openai-responses
      id: openai-primary
      enabled: true
      config:
        api_key: ${OPENAI_API_KEY}
    - kind: openai-responses
      id: openai-failover
      enabled: true
      config:
        api_key: ${OPENAI_FAILOVER_KEY}
```

Frontends/features may also use instance identity, even if there is usually one instance.

#### SDK shape

`lipsdk.Registration` now distinguishes:

- `Kind` / existing `PluginKind`
- `FactoryID` (adapter kind)
- `ID` as configured runtime instance identity
- `Enabled`
- opaque `Config`

#### Validation rules

- uniqueness is by `(family, runtime instance id)`
- duplicate `factory_id` is allowed
- mandatory bundle requirements are validated against **factory kind presence rules**, not instance identity collisions

---

## Route selector model

Route selectors must target configured backend instances.

### Status

Implemented in the current tree.

### Old shape

```text
openai-responses:gpt-4o-mini
```

This is adapter-kind shaped.

### Current shape

```text
openai-primary:gpt-4o-mini
openai-primary:gpt-4o-mini|openai-failover:gpt-4o-mini
[weight=3]openai-primary:gpt-4o^[weight=1]openai-failover:gpt-4o
```

The routing layer should resolve selectors against configured **instance IDs**.

Diagnostics must report both:

- `instance_id`
- `factory_id`

so operators still know what adapter kind is behind an instance.

---

## Standard bundle composition

### Status

Partially implemented.

The important architectural correction already landed:

- the standard bundle no longer depends on `init()` registration,
- `cmd/lipstd` explicitly installs the bundled factories via `pluginreg.RegisterStandardBundle()`,
- `runtimebundle.Build()` is the explicit owner of standard runtime assembly.

What remains unresolved is whether the repo still needs a new dedicated `internal/standardbundle` package,
or whether the current split across `internal/pluginreg` and `internal/infra/runtimebundle` is the right
steady-state design.

### Original problem

Standard bundle registration is hidden behind global mutable maps and `init()` installation.

That is manageable now, but it is exactly the kind of invisible composition that becomes hard to reason about later.

### Current implemented shape

```text
cmd/lipstd
  -> pluginreg.RegisterStandardBundle()
  -> config.LoadFile()
  -> pluginreg.ValidateBundledFactories()
  -> config.RegistrationsFromConfig()
  -> pluginreg.BuildFeatureHooks()
  -> runtime.New(...)
  -> runtimebundle.Build(...)
  -> stdhttp.RunWithRuntime(...)
```

This already satisfies the most important stage-three goal: the standard path is explicitly assembled,
resource-owned, and no longer depends on import-side effects.

### Remaining design decision

The repo still needs an explicit decision on one question:

- should bundle composition become a value-style registry/bundle object with no package-global registry maps,
  or is the current explicit registration plus `runtimebundle.Build()` split sufficient?

### Original target shape

```text
internal/standardbundle
  - backends.go
  - frontends.go
  - features.go
  - assemble.go
```

If the repo later chooses a dedicated bundle package, it should return an owned assembled runtime similar to:

```go
type Assembled struct {
    App        *runtime.App
    Executor   *runtime.Executor
    Store      b2bua.Store
    Inventory  Inventory
    Closers    []io.Closer
    Lifecycles []plugin.Lifecycle
}
```

or a tighter equivalent.

The important point is not the exact type.
The important point is that bundle composition must stay **explicit and inspectable**, whether that remains
split across `pluginreg` + `runtimebundle` or moves into a dedicated value-style package.

---

## Runtime ownership model

### Status

Implemented in the current tree.

### Original problem

`runtime.App` owns some runtime pieces.
`stdhttp.Run()` still creates others.
Some resources have `Close()` but no owner closes them.

### Current shape

One owner on the standard path owns all open resources.

```text
Assemble
  -> build store
  -> build shared transports/clients
  -> build observers
  -> build hook bus
  -> build executor
  -> build app/runtime owner
  -> pass finished dependencies into stdhttp server
```

### Shutdown ordering

```text
1. mark server shutting down / stop accepting traffic
2. shut down HTTP listener
3. cancel/drain in-flight requests
4. stop feature/plugin lifecycles (reverse order)
5. close store(s), DB handles, transports, observers
```

This is now deterministic and tested on the standard path.

---

## Clock and entropy model

### Status

Implemented in the current tree.

### Original problem

Runtime and frontend encoding can fall back to deterministic values if nothing injects real clock/RNG.

### Current shape

The current tree uses explicit composition-root injection via `runtimebundle.BuildOptions` and executor fields.

Equivalent model:

```go
type RuntimeEnv struct {
    Now func() time.Time
    RNG routing.Rng
}
```

Production standard bundle now:

- inject `time.Now`
- inject non-deterministic RNG / seeded source

Tests now:

- inject deterministic clock and RNG explicitly

### Rule

Determinism must be **opt-in for tests**, not the default behavior of the standard binary.

---

## Transport ownership model

### Status

Implemented in the current tree.

### Original problem

Some backend factories use `http.DefaultClient`.

### Current shape

The standard bundle owns HTTP transport/client construction via `internal/infra/httpclient` and passes the
shared client explicitly into bundled backend builders via internal `pluginreg` machinery.

Suggested package:

```text
internal/infra/httpclient
```

Example responsibilities:

- shared transport defaults
- per-instance timeout config
- optional proxy/TLS knobs
- user-agent wrapping
- tracing / correlation wrapper
- test stub transport creation

Bundled backend builders now receive a client from the assembler rather than reaching for package-global defaults.

---

## Health and observer wiring

### Status

Mostly implemented.

The standard bundle now wires:

- config-enabled circuit-breaker candidate health,
- executor outcome recording into that health source,
- structured `lip.route` observation when logging is enabled.

What remains is policy-depth work, not placeholder removal.

### Original problem

Executor exposes seams for route health and route observers, but standard bundle wiring does not make them real.

### Current implemented shape

The current standard stack is:

```text
runtimebundle.Build
  ├── CandidateHealth = config-enabled circuit breaker or empty health
  ├── RouteObserver   = slog observer or noop observer
  └── RouteTrace      = optional diagnostics buffer owned by stdhttp when configured
```

The executor already consumes interfaces/seams only, and the standard bundle now supplies working implementations.

### Remaining design questions

The remaining design questions are:

- should swallowed pre-output failures count toward opening the breaker, or only surfaced failures?
- should the standard breaker remain pure cooldown-based reopen, or add half-open probing?
- should candidate-health state get a first-class diagnostics/admin endpoint, or are logs and route traces enough?

If diagnostics are expanded, they should be able to expose:

- candidate health
- recent route decisions
- instance inventory
- route exclusion reasons

---

## Continuity retention alignment

### Status

Implemented with an explicit store-specific design.

### Original problem

Retention controls currently read like continuity-level config, but SQLite does not receive them.

### Current stage-three decision

The repo now explicitly chooses store-specific semantics instead of pretending memory and SQLite share the
same retention controls.

#### Memory mode

- keep existing TTL/max-legs semantics
- preserve resource caps

#### SQLite mode

Current behavior:

- SQLite rejects `ttl` / `max_legs` at config validation time until durable pruning is designed and implemented.

This keeps the contract explicit and prevents silently ignored retention settings.

---

## Error taxonomy

### Status

Mostly implemented.

Bundled frontends now share a common execute-error classifier, and request correlation is already always-on.
What remains is deciding whether the shared classifier should become richer than `reject` vs `internal`.

### Original problem

Frontend handlers still own a lot of low-level error mapping logic.

### Current implemented shape

`internal/plugins/frontends/execerr` now provides a shared execute-error classifier used by bundled frontends.
Protocol-specific response codes/types remain in the frontend adapters.

### Remaining target

If the repo decides to expand the shared classifier later, it can add common kinds for failures such as:

- invalid request / reject
- upstream unavailable
- timeout / cancellation
- internal runtime bug
- unsupported capability / downgrade reject

Frontends should continue mapping those shared kinds to protocol-specific shapes.

This keeps frontend code focused on codec behavior.

---

## Request correlation model

### Status

Implemented in the current tree.

Request ID / trace middleware no longer depends on diagnostics endpoints.

Current shape:

- always install request-correlation middleware in standard server
- diagnostics endpoints remain optional
- logs, route observers, and admin views all share the same correlation keys

---

## Package plan

```text
cmd/lipstd/
  main.go

internal/pluginreg/
  register_standard.go
  *_install.go
  reg.go

internal/infra/runtimebundle/
  build.go
  built.go
  bootstrap.go
  route_observer.go

internal/infra/routinghealth/
  config_health.go

internal/core/
  runtime/
  routing/
  hooks/
  continuity/
  policy/
  diag/
  ...

internal/infra/httpclient/
  standard.go

pkg/lipsdk/
  registration.go
  ...
```

`internal/stdhttp` is now primarily a transport/server package, with `RunWithRuntime` serving the already-assembled runtime.

The only open package-plan question is whether the repo should later collapse `pluginreg` + `runtimebundle`
into a dedicated value-style bundle package. That is now a follow-up decision, not a prerequisite for the
hardening already completed.

---

## Migration strategy

Stage-three config migration must be explicit.

Legacy shape:

```yaml
- id: openai-responses
  enabled: true
  config: {}
```

Current explicit multi-instance shape:

```yaml
- kind: openai-responses
  id: openai-primary
  enabled: true
  config: {}
```

Current implemented migration behavior:

- old single-field rows continue using `id` as both factory kind and instance identity when `kind` is omitted
- explicit `kind` + `id` enables multiple configured instances of the same adapter kind

Ambiguity still fails loudly.
The current tree does not silently invent separate instance identifiers for multi-instance configurations.

---

## Architectural guardrails

1. No core package imports bundled plugins.
2. No standard bundle registration through `init()`.
3. No global package-level HTTP clients for backend adapters.
4. No deterministic runtime defaults in the production binary.
5. No config field may imply semantics that only some stores honor unless that limitation is explicit in schema/docs.
6. No core file should grow past ~400 LOC without an ADR-level justification.
