---
name: golang-architecture
description: Design, structure, and review Go codebases using hexagonal architecture (ports and adapters), consumer-driven interfaces, functional options, explicit dependency injection, and standard package layouts.
---

# Go Architecture & Design Patterns Guide

Go emphasizes simplicity, explicit dependencies, small interfaces defined at the point of consumption, and clean separation of domain policy from external protocols and infrastructure.

---

## 1. Hexagonal Architecture (Ports & Adapters)

Hexagonal architecture organizes a system by dependency direction rather than directory templates. Domain policy sits at the center; protocols, external APIs, databases, and third-party SDKs sit at the outer edges.

```
+----------------------------------------------------------------+
| Inbound Adapters (HTTP / gRPC / CLI / Workers)                  |
|          |                                                     |
|          v                                                     |
|    +-----------------------------------------------------+     |
|    | Application Core / Use Cases                        |     |
|    | - Business invariants & validation                  |     |
|    | - Orchestration & workflow policy                   |     |
|    | - Port Interface Definitions (Outbound contracts)   |     |
|    +-----------------------------------------------------+     |
|          ^                                                     |
|          |                                                     |
| Outbound Adapters (Database / gRPC Client / External APIs)     |
+----------------------------------------------------------------+
```

### Core Dependency Rules
1. **Core Independence**: The core package must never import database drivers, HTTP/gRPC framework types, or vendor SDKs.
2. **Ports Owned by the Consumer**: Interfaces (ports) are declared in the package that consumes them, not in the package implementing them.
3. **No Pairwise Protocol Translation**: Normalize external wire formats to canonical internal domain models at the edge adapter boundary.

~~~go
// Core domain / port definition (internal/core/auth)
package auth

type UserRepository interface {
    FindByID(ctx context.Context, id string) (*User, error)
    Save(ctx context.Context, u *User) error
}

type Service struct {
    repo UserRepository
    now  func() time.Time
}

func NewService(repo UserRepository, opts ...Option) *Service {
    s := &Service{repo: repo, now: time.Now}
    for _, opt := range opts {
        opt(s)
    }
    return s
}
~~~

---

## 2. Structs & Interfaces

### "Accept Interfaces, Return Concrete Types"
- **Constructors return concrete structs** (e.g., `*Service`, `*Client`), giving consumers the full capabilities of the implementation.
- **Functions accept interfaces** representing the minimal behavior required for that operation.

~~~go
// GOOD: Minimal consumer interface
type Renderer interface {
    Render(w io.Writer, data any) error
}

func SendEmail(w io.Writer, r Renderer, payload any) error {
    return r.Render(w, payload)
}

// AVOID: Accepting large concrete structs or bloated multi-method interfaces
~~~

### Interface Design Guidelines
- Keep interfaces small (1–3 methods). Compose larger behaviors by embedding smaller interfaces (`io.ReadCloser`).
- Design for zero values: Make structs usable in their default zero state whenever possible (e.g., `sync.Mutex`, `bytes.Buffer`).

---

## 3. Idiomatic Construction & Options Pattern

### Functional Options Pattern
Use functional options for flexible, backwards-compatible struct configuration:

~~~go
type Server struct {
    addr    string
    timeout time.Duration
    logger  *slog.Logger
}

type Option func(*Server)

func WithTimeout(t time.Duration) Option {
    return func(s *Server) {
        if t > 0 {
            s.timeout = t
        }
    }
}

func WithLogger(l *slog.Logger) Option {
    return func(s *Server) {
        if l != nil {
            s.logger = l
        }
    }
}

func NewServer(addr string, opts ...Option) *Server {
    s := &Server{
        addr:    addr,
        timeout: 30 * time.Second, // safe default
        logger:  slog.Default(),
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}
~~~

---

## 4. Explicit Dependency Injection & Composition Root

Go favors **explicit wiring** over magic reflection frameworks or runtime DI containers.

### Composition Root
Wire all dependencies explicitly in the application entrypoint (`cmd/app/main.go` or a dedicated runtime bundle factory):

~~~go
// cmd/lipstd/main.go (Composition Root)
func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    cfg := config.LoadFromEnv()
    dbPool := database.NewPool(cfg.DatabaseURL)
    defer dbPool.Close()

    userRepo := postgres.NewUserRepository(dbPool)
    authService := auth.NewService(userRepo)
    httpHandler := httphandler.New(authService)

    srv := server.New(cfg.ListenAddr, httpHandler)
    if err := srv.Run(ctx); err != nil {
        log.Fatalf("server terminated: %v", err)
    }
}
~~~

---

## 5. Standard Project Layout

Organize repositories cleanly according to Go standard practices:

```
├── cmd/               # Main application entrypoints (thin main binaries)
│   └── lipstd/
│       └── main.go
├── pkg/               # Public API / SDK contracts consumable by external projects
│   ├── lipapi/        # Canonical request/response contracts
│   └── lipsdk/        # Plugin interfaces and registration types
├── internal/          # Private implementation packages (Go compiler enforced)
│   ├── core/          # Business logic, orchestration, and port contracts
│   ├── plugins/       # Concrete frontend and backend adapters
│   ├── infra/         # Storage, HTTP servers, logging infrastructure
│   └── qa/            # Integration and architecture hygiene tests
├── docs/              # Architecture Decision Records (ADRs) and design specs
└── scripts/           # Automation and build validation scripts
```

### Package Boundary Rules
- **Avoid package circular dependencies**: If package A needs package B and vice versa, split common interface contracts into a separate package or define the interface at consumer site.
- **Never expose `internal/` packages**: Packages under `internal/` cannot be imported outside the parent module.
- **Keep `pkg/` minimal**: Only export types in `pkg/` that external consumers genuinely need to depend on.
