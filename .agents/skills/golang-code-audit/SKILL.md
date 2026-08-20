---
name: golang-code-audit
description: Execute an evidence-backed, rigorous code review of Go packages evaluating SOLID principles, architectural cohesion, consumer-driven interfaces, error classification, concurrency safety, and complexity regressions.
---

# Go Architectural & Code Quality Audit Guide

This review framework evaluates Go code against rigorous maintainability standards and idiomatic Go architecture without importing unnecessary OOP baggage.

---

## 1. Idiomatic SOLID Evaluation for Go

| Principle | Go Interpretation | Violations to Flag |
| :--- | :--- | :--- |
| **Single Responsibility** | A package or struct owns one cohesive policy or boundary translation. | "God packages" (`utils`, `handlers`) mixing HTTP parsing, database access, and business rules. |
| **Open / Closed** | Extensible via interfaces and hooks without modifying core orchestration logic. | Core code using `switch` on concrete types or hardcoding provider implementations. |
| **Liskov Substitution** | Interface implementations satisfy the full behavioral and error contract. | Implementations that panic on valid interface methods or ignore context cancellation. |
| **Interface Segregation** | Define small, cohesive interfaces where consumed (1–3 methods). | Wide interfaces exported by producer packages with methods unneeded by consumers. |
| **Dependency Inversion** | Core application logic depends on port interfaces; adapters depend on core contracts. | Core packages importing database drivers, HTTP frameworks, or concrete SDK packages. |

---

## 2. Rigorous Code Review Checklist

### A. Architecture & Dependencies
- [ ] Dependency direction flows inward (Adapters $\rightarrow$ Application Core $\rightarrow$ Domain Models).
- [ ] No circular imports or wide catch-all helper packages.
- [ ] Constructors return concrete types and accept minimal interfaces.

### B. Concurrency & Lifecycles
- [ ] Every spawned goroutine has a bounded lifetime and explicit termination condition.
- [ ] Channels are closed exclusively by senders; mutexes are never copied by value.
- [ ] No `time.Sleep` in tests or business logic used as synchronization primitives.
- [ ] Critical sections under mutex locks are minimal and perform zero I/O operations.

### C. Context & Errors
- [ ] `ctx context.Context` is passed as the first parameter to all I/O functions and never stored in struct fields.
- [ ] Errors are wrapped with `%w` for diagnostic chains or classified into domain types.
- [ ] Sensitive internal errors and database queries are not leaked to public API consumers.

### D. Memory & Performance
- [ ] Known-size slices and maps are preallocated.
- [ ] Sub-slices of large memory buffers are cloned if retained long-term to prevent memory leaks.
- [ ] Object pooling (`sync.Pool`) resets struct state before returning objects.

### E. Test Quality & Verification
- [ ] New features or fixes include table-driven tests covering edge cases and error paths.
- [ ] Tests execute cleanly under `go test -race`.
- [ ] Mock expectations and resources are verified with clean teardown (`t.Cleanup`).
