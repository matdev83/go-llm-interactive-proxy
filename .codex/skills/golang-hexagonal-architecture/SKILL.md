---
name: golang-hexagonal-architecture
description: "Go hex architecture: ports/adapters, dependency direction, use cases, repos, transactions."
---

# golang-hexagonal-architecture

## When to use

Use this skill when:

- a Go project has explicitly chosen hexagonal architecture / ports and adapters
- reviewing architectural boundaries in a medium or large Go service
- designing a new bounded context, module, or vertical slice
- refactoring a layered or tightly coupled codebase toward clearer dependency direction
- deciding where use-case orchestration, domain rules, repositories, query paths, and adapters belong

Do not use this skill when:

- the project has not opted into hexagonal architecture
- the work is only about naming or package reshuffling with no boundary question
- the application is a very small CRUD tool where extra layers would add more ceremony than value

## Primary outcome

Keep business rules in the center, technology at the edge, and dependencies pointing inward so the system stays testable, replaceable, and understandable as it grows.

## Mental model

Model the system in four concerns:

- `domain`: business concepts, invariants, entities, value objects, domain services, domain policies
- `app` or `application`: use cases, orchestration, transaction intent, idempotency flow, policy sequencing, coordination across ports
- `adapters`: transport and infrastructure implementations on the edge
- composition root: startup wiring that builds concrete infrastructure and assembles the graph

External systems stay outside the core:

- driving adapters: HTTP, gRPC, CLI, cron, queue consumers
- driven adapters: databases, caches, file storage, SMTP, external APIs, queue publishers

## Non-negotiable rules

1. Dependencies point inward only.
2. Slice by business capability first, not by global technical layer.
3. `domain` and `app` must not import transport, ORM, SQL driver, framework, or vendor SDK packages.
4. Ports are for real substitution boundaries, not for every type or for mocking alone.
5. Ports are defined by the package that consumes them.
6. Use cases coordinate work; adapters translate; domain enforces business rules.
7. Query flows do not need to pretend to be aggregate persistence.
8. Transaction boundaries and side effects must be explicit.
9. Concrete dependencies are created only in the composition root.
10. Prefer boring Go over architecture theater.

## Dependency rule

Allowed dependency direction:

- `cmd/...` or `main` wires the program
- `internal/<context>/app` may depend on `internal/<context>/domain`
- `internal/<context>/adapters/...` may depend on `app` and `domain`
- shared infrastructure under `internal/platform` or similar may be used by adapters and composition code
- `domain` must not depend on transport, database, framework, ORM, SDK, or delivery-layer packages

For server applications, keep most code under `internal/` so the compiler helps enforce boundaries.

## Slice by bounded context first

Prefer package layout that starts with a business capability:

```text
cmd/api/main.go
internal/orders/domain
internal/orders/app
internal/orders/adapters/http
internal/orders/adapters/postgres
internal/orders/adapters/query
internal/billing/domain
internal/billing/app
internal/platform/logging
internal/platform/clock
```

Avoid system-wide horizontal layers like:

```text
internal/domain
internal/app
internal/repositories
internal/http
internal/services
```

Why:

- bounded-context-first layout reduces accidental coupling
- domain language stays local instead of turning into a giant shared model
- import direction is easier to see and enforce

Shared `platform/` packages are for infrastructure helpers such as logging, clocks, IDs, HTTP clients, tracing, and configuration helpers. They are not a dumping ground for business helpers.

## Domain vs application

Put logic in `domain` when it:

- enforces invariants on an entity or value object
- expresses business policy without I/O
- can be tested without a database, network, clock service, or framework
- belongs to domain language rather than a specific use-case workflow

Put logic in `app` when it:

- coordinates multiple ports
- decides ordering of steps across repositories, gateways, and publishers
- chooses transaction scope
- handles idempotency, retries, or compensation flow at the use-case level
- evaluates permissions or workflow rules that depend on loaded state or execution flow

Quick ownership test:

- "Can this rule be pure and stable even if we replace HTTP, Postgres, and Stripe?" -> `domain`
- "Does this logic coordinate collaborators, persistence, or request flow?" -> `app`
- "Is this translating wire or storage details?" -> adapter

Examples:

- "An order cannot be submitted with zero lines" -> `domain`
- "Reserve inventory, persist order, append outbox event" -> `app`
- "Map domain error to HTTP 409" -> driving adapter
- "Translate provider response JSON into domain or app DTOs" -> driven adapter

## Ports

Ports are interfaces for real architectural seams.

Rules:

- define a port in the package that uses it
- outbound ports usually live in `app`
- inbound use-case interfaces are usually unnecessary in Go; let driving adapters depend on concrete app services unless a real consumer boundary needs an interface
- domain ports are rare and only justified when pure domain logic truly depends on a collaborator
- do not define interfaces in adapter packages just because they implement them
- do not create interfaces only for mocks
- keep ports small and use-case-shaped
- prefer domain types, value objects, and application DTOs in port methods
- never expose transport, ORM, SQL, or SDK types through a port

Avoid in ports:

- `*sql.Rows`
- ORM entities or query builders
- `http.Request`, `http.ResponseWriter`
- framework contexts and request models
- queue SDK messages
- vendor API request and response structs

### Repository vs gateway vs query service

Use the right seam for the job:

- repository: persistence of aggregates or domain state owned by the application
- gateway or client: interaction with external systems your app does not own
- query service or query adapter: read-only projections, joins, reporting, search, list views

Repository rules:

- prefer repository per aggregate root or per clear consistency boundary, not per table
- repository methods should reflect domain intent, not generic CRUD theater
- avoid generic repositories like `Repository[T]`
- avoid forcing reporting reads through aggregate repositories
- if the read model is display-oriented, return a read DTO from a query path instead

Bad port:

```go
type UserRepository interface {
    Find(ctx context.Context, q *gorm.DB) (*UserModel, error)
}
```

Better port:

```go
type UserRepository interface {
    ByID(ctx context.Context, id domain.UserID) (domain.User, error)
    Save(ctx context.Context, user domain.User) error
}
```

For read-heavy views:

```go
type UserQueries interface {
    ListActive(ctx context.Context, filter ListUsersFilter) ([]UserListItem, error)
}
```

## Adapters

Adapters are concrete implementations of ports or transport entrypoints.

Rules:

- adapters translate between business-facing contracts and technology-facing APIs
- adapters contain I/O, serialization, retries, timeouts, backoff, and vendor-specific mapping
- adapters do not contain business policy
- adapter packages should usually expose constructors returning concrete types
- composition code assigns concrete adapters to the port interfaces required by use cases

### Driving adapters

Driving adapters initiate application work.

Examples:

- HTTP handlers
- gRPC handlers
- CLI commands
- scheduled jobs
- queue consumers

Their job is thin glue:

1. parse transport input
2. perform transport-level validation
3. call a use case
4. map results and errors to transport output

Driving adapters must not own business rules, persistence policy, or workflow sequencing.

### Driven adapters

Driven adapters implement outbound ports.

Examples:

- Postgres repositories
- Redis caches
- S3 clients
- SMTP clients
- HTTP API clients
- event or outbox writers

Their job is to:

- translate core input into infrastructure calls
- translate infrastructure output into business-facing values
- map known infrastructure conditions into domain or app recognizable errors when justified
- return unexpected failures as wrapped `error`

### Anti-corruption layers

When integrating external systems, keep their language at the edge.

Rules:

- map vendor enums, status codes, request models, and error taxonomies into local concepts in the adapter
- do not let provider names become fake domain concepts
- keep SDK and wire types out of `domain` and `app`
- if the provider model is unstable or overly broad, introduce a narrower app-facing DTO or port contract

Bad:

- `app` code branching on Stripe or AWS status enums
- `domain` types named after a vendor API object

Better:

- adapter maps provider states into local application concepts such as `PaymentAuthorized`, `PaymentDeclined`, or `RequiresCustomerAction`

## Transactions and side effects

Hexagonal architecture gets messy when transaction boundaries are vague. Make them explicit.

Rules:

- the use case in `app` owns transaction intent
- the adapter or composition root owns the concrete transaction mechanism
- never pass `*sql.Tx`, ORM sessions, or driver handles through the core
- do not hide transaction start or commit inside repository methods when the use case spans multiple writes
- if a write and external publication must stay consistent, prefer outbox-style patterns over "save then publish and hope"

Useful port shape:

```go
type Transactor interface {
    WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}
```

When to use it:

- a use case updates multiple repositories atomically
- a use case writes state and appends an outbox record in the same transaction
- the core needs one clear consistency boundary

When not to use it:

- the use case is read-only
- a single repository method already encapsulates one atomic write
- the workflow is intentionally eventually consistent and explicit about it

For database write plus message publish:

- preferred: write domain state and outbox record in one transaction, publish later from an outbox processor
- acceptable only when explicitly okay: publish after commit with known at-least-once or best-effort semantics
- avoid: publishing inside the use case before durable state is committed unless failure semantics are fully intentional

## Context handling

Use `context.Context` explicitly at use-case and adapter boundaries when request lifecycle matters.

Rules:

- `context.Context` is the first parameter of methods that may block, do I/O, or participate in request lifecycle
- pass the same request context from driving adapter to use case to driven adapters
- do not store `context.Context` in structs
- do not invent custom context interfaces
- do not replace request context with `context.Background()` in the middle of a request path
- pure domain functions usually should not accept `context.Context`
- do not use context values for ordinary function parameters

Good use:

- cancellation
- deadlines
- trace propagation
- request-scoped metadata such as correlation ID

Bad use:

- passing optional business parameters
- hiding dependencies in context
- reviving canceled work with a fresh background context inside the core

## Error model

Use normal Go error handling with stable boundaries.

Rules:

- do not use `panic` for routine business or infrastructure failures
- return `error`
- wrap with useful context using `%w`
- classify with `errors.Is` and `errors.As` where callers need branching behavior
- keep business outcomes separate from transport formatting
- `app` and `domain` must not know about HTTP status codes, SQLSTATE details, or gRPC status formatting

Recommended split:

- business rule outcome: domain or app error, or a normal result
- known infrastructure condition: adapter maps it to a stable domain or app recognizable error
- unexpected infrastructure failure: adapter returns wrapped `error`
- transport mapping: driving adapter converts stable errors to HTTP, gRPC, CLI, or message-level responses

Prefer a small set of stable errors that mean something to the core, such as:

- `ErrOrderNotFound`
- `ErrDuplicateRequest`
- `ErrInsufficientInventory`

Avoid exposing raw adapter details as core policy:

- `pgconn.PgError`
- provider SDK error types
- HTTP status branching inside the use case

## Concurrency and background work

Go makes it easy to start goroutines in the wrong place. Do not let concurrency punch holes in the architecture.

Rules:

- do not start goroutines inside `domain`
- keep application use cases sequential by default unless concurrency is part of the workflow benefit
- if `app` uses concurrency, ownership, cancellation, ordering, and error handling must be explicit
- background workers should start from adapters or the composition root, not from entities or random services
- async work that outlives a request should be modeled explicitly, usually via a queue, outbox, or worker component

Smells:

- handler starts a goroutine to "speed up" a business side effect
- domain service launches a goroutine
- use case fires off best-effort work with no ownership, no retry contract, and no shutdown path

## Cross-cutting concerns

Recommended placement:

- authentication: driving adapters or middleware
- authorization: `app` or `domain`, depending on whether it is workflow policy or pure domain policy
- logging: adapters and composition root, not domain
- tracing and metrics: middleware and adapters, propagated via `context.Context`
- transactions: transaction intent in `app`, implementation in adapters or composition root
- retry, timeout, circuit breaker: usually driven adapter policy
- error formatting: driving adapters
- clocks and ID generation: usually app-facing ports implemented in infrastructure, or concrete values passed into domain operations

Important rule:

Cross-cutting concerns must not become excuses for importing frameworks or infrastructure dependencies into the domain.

## Composition root

Never construct concrete dependencies inside use cases.

Wire everything at the composition root:

- `main`
- `cmd/<service>/main.go`
- a dedicated bootstrap or startup package called from `main`

The composition root should:

- load config
- create infrastructure clients
- construct concrete adapters
- assemble use-case services
- register HTTP handlers, gRPC servers, consumers, jobs, and workers

Prefer explicit construction over DI containers, reflection, or hidden registries.

Good:

```go
repo := postgres.NewOrderRepository(pool)
queries := query.NewOrderQueries(pool)
outbox := postgres.NewOutbox(pool)
tx := postgres.NewTransactor(pool)
clock := systemclock.New()
idgen := ulidgen.New()
svc := app.NewCreateOrder(repo, outbox, tx, clock, idgen)
handler := httpadapter.NewCreateOrderHandler(svc)
```

Bad:

- use case creates its own DB client
- repository opens its own connection pool
- adapter reaches into a service locator or global registry

## Suggested package shape

One reasonable default for a Go service:

```text
cmd/api/main.go
internal/orders/domain
internal/orders/app
internal/orders/adapters/http
internal/orders/adapters/postgres
internal/orders/adapters/query
internal/orders/adapters/outbox
internal/billing/domain
internal/billing/app
internal/platform/logging
internal/platform/clock
internal/platform/idgen
```

Notes:

- exact folder names matter less than dependency direction
- package names should remain short and clear
- avoid vague packages like `common`, `helpers`, `shared`, `base`
- package placement should reinforce architectural boundaries, not obscure them
- split by bounded context first, then by layer inside that context

## Worked vertical slice

Use this as the default shape for one write use case.

Application contract:

```go
package app

import (
    "context"
    "fmt"
    "time"

    "example/internal/orders/domain"
)

type OrderRepository interface {
    Save(ctx context.Context, order domain.Order) error
}

type Outbox interface {
    EnqueueOrderCreated(ctx context.Context, event OrderCreatedEvent) error
}

type Transactor interface {
    WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

type Clock interface {
    Now() time.Time
}

type IDGenerator interface {
    NewID() string
}

type CreateOrderCommand struct {
    CustomerID string
    Lines      []CreateOrderLine
}

type CreateOrder struct {
    repo   OrderRepository
    outbox Outbox
    tx     Transactor
    clock  Clock
    ids    IDGenerator
}

func NewCreateOrder(repo OrderRepository, outbox Outbox, tx Transactor, clock Clock, ids IDGenerator) CreateOrder {
    return CreateOrder{
        repo:   repo,
        outbox: outbox,
        tx:     tx,
        clock:  clock,
        ids:    ids,
    }
}

func (uc CreateOrder) Handle(ctx context.Context, cmd CreateOrderCommand) (string, error) {
    order, err := domain.NewOrder(uc.ids.NewID(), cmd.CustomerID, mapLines(cmd.Lines), uc.clock.Now())
    if err != nil {
        return "", fmt.Errorf("create order: %w", err)
    }

    err = uc.tx.WithinTransaction(ctx, func(ctx context.Context) error {
        if err := uc.repo.Save(ctx, order); err != nil {
            return fmt.Errorf("save order: %w", err)
        }
        if err := uc.outbox.EnqueueOrderCreated(ctx, NewOrderCreatedEvent(order)); err != nil {
            return fmt.Errorf("append outbox event: %w", err)
        }
        return nil
    })
    if err != nil {
        return "", err
    }

    return order.ID().String(), nil
}
```

Driving adapter:

```go
package httpadapter

import (
    "encoding/json"
    "net/http"

    "example/internal/orders/app"
)

type CreateOrderHandler struct {
    uc app.CreateOrder
}

func NewCreateOrderHandler(uc app.CreateOrder) CreateOrderHandler {
    return CreateOrderHandler{uc: uc}
}

func (h CreateOrderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    var req createOrderRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid request", http.StatusBadRequest)
        return
    }

    id, err := h.uc.Handle(r.Context(), req.toCommand())
    if err != nil {
        writeError(w, err)
        return
    }

    writeJSON(w, http.StatusCreated, createOrderResponse{ID: id})
}
```

Driven adapter:

```go
package postgres

import (
    "context"
    "fmt"

    "example/internal/orders/domain"
    "github.com/jackc/pgx/v5/pgxpool"
)

type OrderRepository struct {
    pool *pgxpool.Pool
}

func NewOrderRepository(pool *pgxpool.Pool) OrderRepository {
    return OrderRepository{pool: pool}
}

func (r OrderRepository) Save(ctx context.Context, order domain.Order) error {
    _, err := r.pool.Exec(ctx, `insert into orders (id, customer_id) values ($1, $2)`, order.ID(), order.CustomerID())
    if err != nil {
        return fmt.Errorf("insert order: %w", err)
    }
    return nil
}
```

Composition root:

```go
pool := postgres.OpenPool(cfg.Database)
repo := postgres.NewOrderRepository(pool)
outbox := postgres.NewOutbox(pool)
tx := postgres.NewTransactor(pool)
clock := systemclock.New()
ids := ulidgen.New()
uc := app.NewCreateOrder(repo, outbox, tx, clock, ids)
handler := httpadapter.NewCreateOrderHandler(uc)
```

What this example demonstrates:

- `domain` owns order rules
- `app` owns orchestration and transaction intent
- adapters own transport and storage details
- the composition root owns concrete construction
- the use case never sees `*pgxpool.Pool`, `*sql.Tx`, HTTP types, or vendor SDK types

## Testing strategy

Primary test focus:

1. pure domain tests
2. use-case tests with fakes for outbound ports
3. adapter integration tests
4. end-to-end tests for critical flows

Rules:

- prefer fakes over mock-heavy designs
- do not create interfaces only to satisfy mocks
- test domain invariants directly
- test `app` orchestration without a running database where possible
- test adapters against real infrastructure behavior when correctness depends on SQL, schema, protocol, or network details
- test transaction and outbox semantics where consistency matters
- for concurrent flows, make goroutine ownership, cancellation, and shutdown behavior explicit in tests

Good use-case test targets:

- repository called once with the right aggregate
- outbox appended only after successful state transition
- no side effect emitted when validation fails
- domain or app errors propagate without transport leakage

## Architectural review checklist

Check all of the following:

- the project has explicitly opted into hexagonal architecture
- code is sliced by bounded context before technical layer
- dependencies point inward only
- `domain` packages do not import transport, DB, framework, ORM, or SDK concerns
- outbound ports are defined in the consuming core package, usually `app`
- ports are business-shaped rather than technology-shaped
- adapters are concrete implementations that can be replaced without changing core logic
- driving adapters are thin glue only
- use cases own orchestration, transaction intent, and workflow sequencing
- repositories represent persistence of domain state rather than generic CRUD wrappers
- read-heavy or reporting paths use query adapters when that is simpler and clearer
- transaction boundaries are explicit and no DB handle leaks into the core
- provider SDK and wire types are normalized at the edge
- `context.Context` is the first parameter where lifecycle matters and is not stored in structs
- normal Go error handling is used instead of `panic` for routine failures
- concrete construction happens only in the composition root
- tests focus first on domain and use cases, then adapters, then end-to-end coverage

## Common smells and smallest useful refactors

Smell:

- handler branches on business state

Smallest useful refactor:

- move that branch into a use case or domain method and keep the handler focused on input and output mapping

Smell:

- use case imports `database/sql`, `pgx`, `gorm`, or a provider SDK

Smallest useful refactor:

- introduce a consumer-side port in `app`, move infrastructure calls into an adapter, inject the adapter from `main`

Smell:

- repository returns ORM models or SQL rows

Smallest useful refactor:

- add mapping inside the adapter and return domain objects or read DTOs instead

Smell:

- every read goes through a write-oriented repository

Smallest useful refactor:

- introduce a query adapter or query service that returns the exact read model needed

Smell:

- use case constructs concrete dependencies

Smallest useful refactor:

- move construction into the composition root and inject the dependency through the constructor

Smell:

- domain or app branches on vendor enums or SDK types

Smallest useful refactor:

- map those details inside the adapter to stable local concepts before they cross into the core

Smell:

- use case needs one atomic unit across multiple writes and publishes

Smallest useful refactor:

- add a `Transactor` and outbox seam so the consistency boundary becomes explicit

Smell:

- goroutines are started casually in handlers or core services

Smallest useful refactor:

- move background work behind an explicit worker, queue, or outbox processor with a clear owner and shutdown path

## Anti-patterns

Reject these:

- `domain` imports SQL, HTTP, ORM, framework, or SDK packages
- handlers or consumers contain business policy
- business logic decides HTTP status codes or transport responses
- adapters define interfaces only because they implement them
- interfaces exist only for mocking rather than for a real boundary
- repositories or gateways return ORM, transport, or vendor SDK types into the core
- `context.Context` is stored in structs
- use cases instantiate concrete infrastructure clients
- transaction handles leak into `app` or `domain`
- package structure looks clean on disk but dependency direction is violated in code
- everything is forced through repositories, including reporting or search use cases better served by dedicated read models
- provider terms leak into the domain model
- asynchronous work is launched with no ownership or delivery contract

## Heuristics for maintainable large Go applications

Prefer these defaults unless there is a strong reason not to:

- modular monolith before microservices
- explicit boundaries before distributed boundaries
- bounded-context-first package layout
- concrete types from implementors, interfaces at consuming seams
- thin delivery layers
- pure domain where possible
- application layer for orchestration
- dedicated read models when they reduce coupling
- `internal/` boundaries for app-private modules
- explicit wiring in `main` or bootstrap
- outbox or explicit async boundaries over hidden fire-and-forget side effects

## Output expectations when reviewing code with this skill

When reviewing a codebase or diff, identify:

1. boundary violations
2. wrong dependency direction
3. misplaced business logic
4. technology-shaped ports
5. repository misuse and missing query seams
6. adapter translation leaks
7. missing composition-root discipline
8. hidden transaction boundaries
9. concurrency and background work leaks
10. `context.Context` and error handling mistakes

For each issue, explain:

- what rule is violated
- why it harms maintainability, replaceability, or testability
- what package or layer should own the logic instead
- the smallest concrete refactor that moves the code toward the architecture
