# Core package boundaries

This document classifies every top-level `internal/core/*` package so contributors can decide whether new code belongs in core or at an edge (adapter, plugin, composition root, or feature). Nested sub-packages (e.g. `internal/core/securesession/app`) inherit their parent package's classification and are not listed individually.

## Classification scheme

| Classification | Meaning |
| --- | --- |
| use-case orchestration | Coordinates multiple seams to execute a canonical request. |
| policy | Cross-protocol rules that govern routing, capability, or admission. |
| state seam | Continuity, lineage, or session state contracts. |
| canonical contract | Protocol-neutral types and interfaces consumed by adapters. |
| support | Cross-cutting helpers (JSON, safety, config, diagnostics) with no provider awareness. |

## Package classification

| Package | Classification | Reason it belongs in core | Adapter leakage risk |
| --- | --- | --- | --- |
| `runtime` | use-case orchestration | Executes canonical calls; coordinates routing, continuity, secure session, accounting, and stream assembly. | high |
| `routing` | policy | Cross-protocol route selector parsing, alias expansion, candidate resolution, weighted/failover/parallel planning. | medium |
| `execbackend` | canonical contract | Backend execution lifecycle and capability contracts consumed by all backend adapters. | medium |
| `execctx` | canonical contract | Execution context types (views, secure-turn, route prefs) propagated through the request lifecycle. | low |
| `b2bua` | state seam | B2BUA continuity store interface; A-leg/B-leg lineage projection. | medium |
| `continuity` | state seam | Continuity store managers (memory, SQLite, Bun); TTL/max-legs semantics. | medium |
| `lineage` | canonical contract | A-leg/B-leg lineage identifiers and records. | low |
| `leglifecycle` | use-case orchestration | B-leg attempt lifecycle coordinator (register, cancel, end). | medium |
| `affinity` | policy | Session affinity store and missing-identity policy. | low |
| `policy` | policy | Orchestration policy rules including circuit breaker (CandidateHealth). | medium |
| `securesession` | state seam | Secure session management (app, domain, adapters). Begin-turn, resume, denial. | medium |
| `auth` | canonical contract | Auth event dispatcher, session audit policy, remote decider, OS identity contracts. | medium |
| `accessmode` | policy | Access mode definitions (single-user, multi-user) and enforcement gates. | low |
| `admin` | support | Admin HTTP surface contracts within core. | low |
| `http` | support | HTTP middleware helpers (trace, request ID) used by driving adapters. | low |
| `safety` | support | Panic capture and boundary classification for isolated crash handling. | low |
| `capabilities` | canonical contract | Capability negotiation and catalogs; resolver interface for backend caps. | medium |
| `jsonpresence` | support | JSON null-vs-empty round-trip preservation for encoded shapes. | low |
| `diag` | canonical contract | Diagnostics identifiers, handlers (health, attempts, inventory, route trace, pprof), and shared-secret protection. | medium |
| `config` | support | Runtime config types and loading; no provider-specific defaults. | low |
| `stream` | use-case orchestration | Stream pumps, collectors, and event plumbing for the canonical event stream. | medium |
| `streamrecovery` | policy | Stream recovery policy after interruptions (auto-resume, idle timeout, grace period). | medium |
| `hooks` | canonical contract | Hook bus, hook chain configuration, and hook dispatch with panic isolation. | medium |
| `extensions` | use-case orchestration | Extension pipeline stages, immutable runtime snapshots, and SDK facade assembly. | high |
| `auxreq` | canonical contract | Auxiliary request client for executor-runner binding. | low |
| `state` | support | Feature-facing core state (in-memory key-value). | low |
| `traffic` | canonical contract | Traffic observation, capture, and redaction contracts. | low |
| `workspace` | canonical contract | Workspace resolution contracts. | low |
| `modelcatalog` | canonical contract | Core model catalog and vendor resolver contracts. | medium |
| `modelregistry` | canonical contract | Model registry runtime and cache for backend model inventory. | medium |
| `accounting` | canonical contract | Usage accounting price catalog and cost estimation. | low |
| `tokenaccounting` | support | Token counting, preflight, ledger, and observability sub-packages. | medium |
| `controlplane` | state seam | Control-plane validation, status, query, retention, event ledger. | medium |
| `interleavedstate` | support | Interleaved-thinking state tracking for multi-turn reasoning. | low |
| `interleavedthinking` | support | Interleaved-thinking shape configuration and memo store. | low |

## Core admission checklist

Before adding a new `internal/core/*` package or moving code into an existing one, answer:

1. **Is this cross-protocol product policy?** If it only serves one protocol or provider, it belongs in an adapter or plugin.
2. **Does it import or mention provider-specific concepts?** Provider names, SDK types, or wire formats disqualify it from core.
3. **Is it HTTP/operator presentation rather than policy?** HTTP handlers, route mounting, and operator UIs belong in `stdhttp` or `admin`, not core (unless they define canonical query contracts).
4. **Could it be a plugin, feature, or adapter?** If the behavior is optional or provider-specific, it belongs behind a `pkg/lipsdk` facade or in `internal/plugins`.
5. **Does it do durable I/O directly?** Driver details (SQL, file I/O, network) must be isolated in adapter packages; core defines the interface.
6. **Is the interface defined where consumed?** Core defines small interfaces where they are consumed, not where they are implemented.

If any answer is "no" or "unclear," the code likely does not belong in core. Include a short justification in the PR when adding a new core package.
