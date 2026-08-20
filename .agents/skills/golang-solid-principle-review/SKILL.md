---
name: golang-solid-principle-review
description: "Review Go packages, diffs, and APIs through idiomatic SOLID reasoning: cohesion, variation seams, behavioral substitution, consumer interfaces, and dependency direction. Use for architectural reviews focused on cohesion, extensibility, substitutability, interface design, and dependency direction without importing Java-style abstractions into Go."
---

# Go SOLID review

Use SOLID as a reasoning vocabulary, not a scorecard. Review the changed surface and enough callers, implementations, tests, and imports to understand behavior. Do not turn this into a generic style, security, performance, or bug hunt unless the design issue directly causes it.

## Pass 0: design map

Record package responsibilities, policy/implementation boundaries, public contracts, consumers, implementations, constructors, dependency edges, state/lifecycle/concurrency ownership, and demonstrated variation axes. For a diff, separate new regression from existing debt.

## SRP

Ask whether a package, type, function, or lifecycle owner has multiple independent reasons to change. A cohesive orchestrator may call several collaborators; a long function is not automatically an SRP violation. A valid finding states the mixed responsibilities, independent change triggers, caller-visible consequence, and the smallest useful separation.

Watch for policy mixed with transport/persistence, configuration mixed with business execution, unrelated state with different synchronization, and goroutines whose startup/shutdown ownership is split.

## OCP

Look for a real variation axis shown by multiple implementations, repeated branches, or active requirements. Prefer direct composition, function values, a small consumer interface, data-driven dispatch, or a registry only when runtime registration is a real requirement. A switch over a closed protocol/state set can be correct. Do not demand an interface for one implementation or a plugin system for speculative futures.

A valid finding identifies repeated modification caused by the variation and a concrete Go-native seam that reduces it without hiding control flow.

## LSP

Compare implementations and wrappers against the contract callers actually rely on:

- accepted inputs, zero/nil behavior, and result shape;
- errors.Is/As identity and partial results;
- ownership/aliasing and mutation;
- ordering, determinism, idempotency, retries, and blocking;
- context cancellation, deadlines, concurrency safety, and resource closure.

Report a finding only with the consumer expectation, substitutable implementations, incompatible behavior, and realistic failure. Stronger guarantees are not a violation when they preserve assumptions.

## ISP

Interfaces belong near consumers and should contain only needed behavior. Split broad provider interfaces along real consumer/lifecycle boundaries, or replace a one-method interface with a function type. A concrete type is appropriate when substitution is not demonstrated. Mocks with many unused methods, command/query mixes, and callers that immediately type-assert are evidence, not automatic failures.

## DIP

Stable policy should not import volatile implementation details. Use explicit composition and ports where a boundary is real. Generated transport/database/provider code belongs at adapters. Dependency inversion does not mean every package imports an interface or every constructor returns one.

## Findings and severity

For each finding provide file/symbol, evidence, principle, consequence, minimal remediation, and confidence. Use high severity only for a caller-visible contract or architectural boundary failure; medium for repeated coupling with a clear cost; low for a localized improvement. Note uncertainty and pre-existing debt. Review tests and compile behavior before publishing a claim.
