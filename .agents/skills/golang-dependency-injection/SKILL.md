---
name: golang-dependency-injection
description: Go dependency injection and composition: manual constructors first, consumer-owned interfaces, lifecycle ownership, and careful selection of DI libraries.
---

# Dependency injection in Go

Dependency injection is explicit construction of a value's collaborators. It is useful when a component has a real substitution, lifecycle, or configuration boundary. It is not a reason to wrap every concrete type in an interface.

## Default: compose explicitly

Prefer constructors that validate required dependencies and return a concrete type:

~~~go
type Store interface {
    Find(ctx context.Context, id string) (User, error)
}

type Service struct {
    store Store
    clock func() time.Time
}

func NewService(store Store, clock func() time.Time) (*Service, error) {
    if store == nil || clock == nil {
        return nil, errors.New("service dependencies are required")
    }
    return &Service{store: store, clock: clock}, nil
}
~~~

Define a small interface at the consuming package when substitution is needed. Accept a concrete type when its behavior is stable and there is no useful alternative. Return a concrete type unless an interface is intentionally the public boundary. Function values are often the clearest one-operation seam.

Keep dependency construction in the composition root. Pass a request context to operations; never store a context in a long-lived dependency. A singleton is a lifetime decision, not a DI feature. The owner of a connection, worker, or client must also own its close/stop operation. Avoid service locators, mutable globals, hidden init registration, and containers threaded through business functions.

## Choosing a library

- Manual construction: default for small and medium graphs, easiest to debug and compile.
- google/wire: the upstream repository is archived/read-only; do not describe it as a current default. Existing code may still use generated providers, but verify the pinned version and migration plan.
- uber-go/dig: runtime reflection graph; useful for large application composition, but missing providers and type mismatches surface during container construction/invocation.
- uber-go/fx: lifecycle-oriented application framework built around dig; adopt only when its module and lifecycle conventions are worth the coupling.
- samber/do/v2: generic providers, scopes, lazy/transient values, health checks, and shutdown. Its API is runtime-based; dependency graph errors are not compile-time guarantees.

Review the actual module version before writing snippets. Do not compare libraries using unsupported claims about speed, safety, or lifecycle semantics.

## samber/do/v2 essentials

The current v2 provider shape receives do.Injector:

~~~go
import do "github.com/samber/do/v2"

type Config struct{ Addr string }
type Server struct{ cfg Config }

func newServer(i do.Injector) (*Server, error) {
    cfg, err := do.Invoke[Config](i)
    if err != nil {
        return nil, err
    }
    return &Server{cfg: cfg}, nil
}

injector := do.New()
do.ProvideValue(injector, Config{Addr: ":8080"})
do.Provide(injector, newServer)
server, err := do.Invoke[*Server](injector)
~~~

Providers are lazy by default; use ProvideTransient for per-invocation construction and use the current eager/service APIs only after checking their signatures. Use ProvideNamed/InvokeNamed when a type has deliberate multiple instances. A missing or circular dependency is reported at runtime when it is resolved.

A child scope is created from an injector method: child := injector.Scope("request"). The child can see ancestors; registrations in the child do not flow upward. Scope ownership must match the resource lifetime. RootScope.Clone returns a cloned root for tests in v2; it is not a universal deep copy of external resources.

Services implementing the library's health-check or shutdown interfaces can be checked and closed through the injector. Shutdown reports errors; pass a context when the operation needs a bound. Do not assume shutdown ordering beyond the library's documented dependency/lifecycle behavior.

## Testing and review

Manual constructors are usually tested with fakes. For a container, create a fresh root per test, register overrides before invoking the service, assert both resolution and lifecycle errors, and shut it down with a bounded context. Never share a mutable global injector between tests.

For a refactor, map concrete dependencies, globals, init side effects, lifetimes, and callers first. Introduce one seam at a time and keep behavior tests green. Verify race-sensitive lifecycle code with the target platform's race gate where available.
