# Routing and Orchestration (Steering)

## Core Ownership Boundaries

Core (`internal/core/`) strictly owns:
- Selector parsing (`internal/core/routing`), model alias expansion, candidate resolution, health/exclusion filtering.
- Attempt sequencing, parallel-race coordination, TTFT budget enforcement (`{ttft_timeout=N}`, `[handicap=N]`).
- A-leg routing-override state (`internal/core/routeoverride`) and turn-level snapshot application; plugins never own override persistence or selector authority.
- B2BUA pre-output recovery policy & lineage tracking (`internal/core/b2bua`).
- Stage evaluation & attempt coordination ([`internal/core/authoritycoord`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/authoritycoord)).
- Control plane projections ([`internal/core/controlplane`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/controlplane)).
- Interleaved reasoning memo stores & sanitization ([`internal/core/interleavedthinking`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/interleavedthinking)).

Plugins supply policy inputs via SDK contracts; plugins **never** own orchestration logic.

---

## Selector Syntax & Capabilities

- **Ordered Failover (`|`)**: Tries candidates left-to-right on pre-output recoverable errors (`model-a | model-b`).
- **Weighted Groups**: Comma-separated weighted branches; the planner picks one eligible arm (session-sticky / retry-aware). Exact grammar stays in the parser.
- **Parallel Races (`!`)**: Races multiple backends concurrently (`model-a ! model-b`).
- **Leg Handicap (`[handicap=N]`)**: Delays start of a parallel leg by N milliseconds (`model-a ! model-b[handicap=200]`).
- **TTFT Budgets (`{ttft_timeout=N}` / `[ttft_timeout=N]`)**: Time-to-first-token budget; satisfied **only** by client-visible canonical output (not keepalive/usage events).
- **First-Request Steering (`[first]`)**: Routes the initial request of a session differently from subsequent turns.
- **Thinker Arm (`[thinker]`)**: Marks an interleaved-thinking branch. Disabled unless `interleaved.enabled`. Cannot combine with `[first]` on the same branch. Override substitution must reuse existing A-leg thinker/memo state, not a second planner.
- **Model Aliases**: Regexp rewrite rules applied before selector parsing. Invalid rules fail startup validation.
- **Syntax Invariants**: Parallel `!` groups CANNOT mix with `^`, weights, or `[first]` in the same arm.

---

## Runtime A-Leg Routing Overrides

Opt-in under `routing.override_admin` (`enabled` defaults false). When set, a protected GET/PUT/DELETE surface under literal `path_prefix` (default `/admin/routing-overrides`) replaces the effective client selector for **later** turns on that A-leg. ServeMux `{id}` wildcards are rejected; the A-leg id is a path suffix under the prefix.

- Latest-wins revisioned state; snapshot once per admitted turn before route planning.
- Never mutate in-flight turns or B-legs. Clear restores current client routing for subsequent turns.
- Full existing selector language (aliases, failover, weights, races, TTFT, affinity, `[first]`, `[thinker]`).
- Persist with continuity (memory and Bun SQLite/PostgreSQL). Generation reload reinterprets the raw selector; it does not rewrite in-flight snapshots.
- Disabling the HTTP surface does not clear persisted overrides. Non-loopback exposure requires the diagnostics shared secret.

---

## Billing Seams (Not Stream Orchestration)

Runtime has exactly two billing touch points: a cheap settled-credit screen before route expansion, then side-effect-free route/quote followed by atomic operational-exposure admission. After admission, terminal ownership appends immutable per-B-leg usage plus one BillingCallID-scoped call closure. Stream handlers must not rate, journal, mutate balance/exposure, or accumulate financial evidence. Failover, races, and no-retry-after-output stay execution concerns.

---

## B2BUA Pre-Output Recovery Rules

1. **Pre-Output Only**: Failover/swallowing is permitted **ONLY BEFORE** client-visible canonical output starts.
2. **Commitment**: The first client-visible content event commits the attempt. No silent failover after commitment.
3. **Lineage Invariant**: Every A-leg (logical client request) and B-leg (backend attempt) MUST be recorded in lineage.
4. **Clean Race Cancellation**: Parallel losing legs MUST be cancelled immediately without goroutine leaks or corrupted lineage.
5. **Leg Attribution**: Each B-leg uses backend-specific identity (`User-Agent`/OpenRouter); A-leg `Server` identity remains proxy-owned ([`docs/proxy-identity.md`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/docs/proxy-identity.md)).

---

## Authority Coordination & Control Plane

- **[`internal/core/authoritycoord`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/authoritycoord)**: `stage_evaluator.go` enforces execution stage budgets and records attempt-stage settle failures.
- **Concurrency & Usage Authorities**: `concurrencyauthority` & `usageauthority` enforce turn limits and token/request quotas per principal/tenant. Production `accounting.authority` YAML must not encode monetary `budget` / `spend_cap` / `money_nano`; money admission is billing authorization.
- **Control Plane Projections**: `controlplane` projects execution facts to ledger stores (`usage_projector.go`), provides metering bridges, and builds readiness reports (`readiness_report.go`). Those rows are not customer-balance truth.

---

## Interleaved Reasoning Preservation

- **Memo Store**: [`internal/core/interleavedthinking`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/interleavedthinking) (`memo.go`, `memo_store.go`) retains structured reasoning blocks across turns and B2BUA failover attempts.
- **Shape & Sanitize**: `shape.go` and `sanitize.go` prevent reasoning duplication/corruption across retries.

---

## Continuity & Durable Store Rules

- **Default In-Memory**: `continuity.store: memory` (single-process mode).
- **Dual SQLite / PostgreSQL Store**: [`internal/core/continuity/bunstore`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/continuity/bunstore) provides durable metadata over Bun ORM (`uptrace/bun`). Secure sessions use the same driver pattern ([`internal/core/securesession/adapters/`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/securesession/adapters)).
- **PgBouncer Pooler Invariants**: Direct admin DSN (`LIP_TEST_POSTGRES_ADMIN_DSN`) for migrations/cleanup; pooled runtime DSN (`LIP_TEST_POSTGRES_DSN`) for runtime DML. **FORBIDDEN**: `SET search_path`, temporary tables, prepared statements, session locks, or `transaction_pool` + `auto_migrate`.
- `internal/plugins/stores/` is reserved for future third-party store plugins. Primary stores stay in `internal/core/`.
