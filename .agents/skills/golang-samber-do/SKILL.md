---
name: golang-samber-do
description: "Use and review samber/do/v2 providers, runtime resolution, scopes, lazy/transient lifetimes, overrides, cloning, and shutdown."
---

# samber/do/v2

This skill targets the v2 module path github.com/samber/do/v2. Pin the version in the consuming module and verify the API against that version. Use a manual composition root when the graph is small enough to read directly.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Inspect the pinned v2 source for provider, override, health, shutdown, and clone APIs before asserting a lifecycle guarantee.
- Trace lazy/transient resolution, named services, circular/missing dependencies, concurrent first use, and constructor failure or panic.
- Check scope lookup direction, resource lifetime, instance sharing across clones, dependency shutdown order, context bounds, and close/error ownership.
- Keep injector resolution at composition boundaries; report a service-locator or abstraction issue only when it obscures a concrete dependency or lifetime contract.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Registration and invocation

Providers receive do.Injector and return a value plus an error when construction can fail:

~~~go
package main

import (
    "fmt"
    do "github.com/samber/do/v2"
)

type Config struct{ Address string }
type Server struct{ address string }

func newServer(i do.Injector) (*Server, error) {
    cfg, err := do.Invoke[Config](i)
    if err != nil {
        return nil, fmt.Errorf("resolve config: %w", err)
    }
    return &Server{address: cfg.Address}, nil
}

func main() {
    injector := do.New()
    do.ProvideValue(injector, Config{Address: ":8080"})
    do.Provide(injector, newServer)
    server, err := do.Invoke[*Server](injector)
    _ = server
    _ = err
}
~~~

Provide is lazy unless the selected API says otherwise. Use ProvideTransient when each Invoke should construct a fresh value. Use ProvideNamed/InvokeNamed for multiple registrations of the same type. Eager service helpers are registration options in the library's current API; they are not providers and cannot be passed as if they were provider functions.

Resolution failures, missing services, and circular dependencies are runtime errors. The type parameter improves the result type but does not make the graph compiler-checked.

## Scopes and lifetimes

Create a child with injector.Scope("name", optionalPackageFunctions...). A child can resolve ancestor services; an ancestor cannot resolve child-only services. Register request or tenant resources in a scope that has the same lifetime and shut it down when that lifetime ends.

RootScope.Clone returns a cloned injector for isolated tests. It copies the container graph according to library semantics; it does not clone network connections, files, goroutines, or arbitrary values held by providers. Prefer fresh construction for tests when external resources are involved.

## Lifecycle

Services can implement the library's health-check and shutdown interfaces (including context-aware variants). HealthCheck and Shutdown return the library's reported errors; ShutdownWithContext bounds cleanup. Treat shutdown as an application lifecycle phase and still make individual resources idempotently closable.

Do not store request contexts in services. Hooks can observe registration, invocation, and shutdown, but they should not hide business logic or mutate global state. Add logging/metrics through explicit hooks or an adapter with a documented failure policy.

## Testing

Create a fresh root per test. Register test values or OverrideValue/Override providers before invoking the service. Assert missing, circular, and constructor errors as well as successful resolution. Use injector.Clone only through the current RootScope method when a clone is truly useful, and shut down the test injector with a bounded context.

For production review, inspect provider signatures, lazy versus transient semantics, scope ownership, shutdown ordering, external resource cleanup, and whether direct constructors would be clearer. Check API names in the pinned module before copying examples.
