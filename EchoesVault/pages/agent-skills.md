---
type: reference
title: Agent Skill Loading System
description: Skill-to-task mapping for repo agents, including hexagonal architecture enforcement and local skill inventory.
stack: [go]
tags: [agents, skills, opencode, hexagonal]
status: active
---

# Agent Skill Loading System

Defined in `AGENTS.md` Skill Loading. Maps work categories to golang-specific skills that agents load before executing. Repo steering overrides generic skill defaults.

## Skill-to-Task Mapping

| Task Category | Skills to Load |
|---|---|
| Architecture, package boundaries, feature design | `golang-hexagonal-architecture`, `golang-design-patterns` |
| Constructors, lifecycle, interfaces, facades | `golang-dependency-injection` or `golang-structs-interfaces` |
| Tests, conformance, regressions | `golang-testing` (+ `golang-stretchr-testify` for testify code) |
| Streaming, concurrency, cancellation | `golang-concurrency`, `golang-context` |
| Error handling | `golang-error-handling` |
| Security review, hardening | `golang-security` |
| Observability (metrics, tracing, logging) | `golang-observability` |
| Database, persistence | `golang-database` |
| CLI commands, flags, config | `golang-cli` |
| Performance, optimization | `golang-performance` |
| Linting, style | `golang-lint`, `golang-code-style` |
| Dependency management | `golang-dependency-management` |
| Documentation | `golang-documentation` |
| Troubleshooting, debugging | `golang-troubleshooting` |
| Simplification / refactor-only | `go-simplify` (skill directory: `.opencode/skills/golang-simplify/`) |
| gRPC / protobuf | `golang-grpc` |
| Naming conventions | `golang-naming` |
| Project layout | `golang-project-layout` |
| Safety (nil, races, zero values, lifecycle) | `golang-safety` |
| Data structures, generics | `golang-data-structures` |
| Popular libraries guidance | `golang-popular-libraries` |
| Modernization, idioms | `golang-modernize` |
| Continuous integration | `golang-continuous-integration` |
| Benchmarking, profiling | `golang-benchmark` |

## Local Skill Inventory (`.opencode/skills/`)

49 local skill directories are present:
- **32 Go skills:** 31 `golang-*` directories plus `golang-simplify/`, invoked by repo rules as `go-simplify` for refactor-only work.
- **14 `kiro-*` skills:** spec discovery, specs, implementation, review, validation, verification, steering, and debugging.
- **3 `echoes-*` skills:** vault daily log append, page create/update, and page search.

Additional globally available skills may exist outside `.opencode/skills/`; do not document them as project-local unless their files are present in this repository.

## Hexagonal Architecture Skill (`golang-hexagonal-architecture`)

Must be loaded for any work touching:
- Architecture design or package boundary decisions
- New feature design, especially in `internal/core/`, `pkg/lipapi/`, `pkg/lipsdk/`
- Plugin contract changes (frontends, backends, features)
- Routing, orchestration, or continuity semantics
- Dependency direction (`internal/core` may depend on `pkg/lipapi` and `pkg/lipsdk`; it must not depend on concrete plugins)

Key hexagonal rules enforced by this skill:
- Domain/policy center (`pkg/lipapi` + `internal/core/`) must not import adapters
- Driving adapters (frontends) decode into canonical requests; driven adapters (backends) emit canonical events
- Composition roots wire everything explicitly
- Interfaces where consumed, not in central `ports/` packages
- No provider SDK types outside adapter boundaries
