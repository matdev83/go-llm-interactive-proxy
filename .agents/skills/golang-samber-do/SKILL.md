---
name: golang-samber-do
description: Use samber/do/v2 for explicit generic dependency injection, scopes, lazy and transient providers, lifecycle checks, cloning, and shutdown.
---

# samber/do/v2

This skill targets the v2 module path github.com/samber/do/v2. Pin the version in the consuming module and verify the API against that version. Use a manual composition root when the graph is small enough to read directly.

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
