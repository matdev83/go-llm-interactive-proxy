# Kiro Spec-Driven Development - Agent Guidelines

This document provides project-specific guidance for AI agents using the Kiro-style
spec-driven workflow in this Go repository.

## Scope (opt-in)

This guide applies only when the user explicitly opts into Kiro/spec-driven work by:
- invoking a `/kiro:*` command,
- referencing a spec name or path under `.kiro/specs/`, or
- explicitly asking for spec-driven development.

Otherwise follow the normal repo workflow described in the root `AGENTS.md`.

## Core philosophy

- Spec first, code second.
- Keep boundaries explicit and narrow.
- Treat streaming behavior as a first-class contract.
- Preserve the product's distinctive value in the core only where the boundary truly belongs there.
- Push optional behavior behind plugin or hook seams.
- Use TDD for every implementation task.

## Directory structure

```
.kiro/
|- specs/
|  |- {feature-name}/
|  |  |- spec.json
|  |  |- requirements.md
|  |  |- design.md
|  |  |- tasks.md
|  |  `- research.md
|  `- archive/
|- steering/
|  |- product.md
|  |- structure.md
|  |- tech.md
|  `- testing.md
|- AGENTS.md
`- settings/
```

## Steering expectations

Steering files are project memory. They capture patterns and guardrails, not exhaustive lists.

In this repository they must stay aligned with these truths:
- the runtime is a small Go core,
- protocol adapters are plugins,
- routing/failover/B2BUA continuity are core-owned capabilities,
- tool reactors and request/response mutation start as hook seams before feature implementations exist,
- the architecture is streaming-first and capability-driven.

## Spec state hygiene

- Keep `.kiro/specs/` for active specs only.
- Move finished specs to `.kiro/specs/archive/`.
- Keep `spec.json` phase and approvals in sync with the artifact state.

## Workflow phases

### Phase 0: Steering

Use `/kiro:steering` to create or update project memory.
Focus on patterns, boundaries, and invariants that help future specs stay coherent.

### Phase 1: Spec initialization

Use `/kiro:spec-init` to create the feature workspace and metadata.

### Phase 2: Requirements

Use `/kiro:spec-requirements` to generate testable requirements in EARS form.
Requirements should say what the system must do, not how to code it.

Brownfield guidance for this project:
- identify whether the work touches core, frontend plugin, backend plugin, or feature plugin boundaries,
- state whether the change affects canonical contracts or only adapters,
- call out revalidation needs for routing, streaming, or capability negotiation.

### Phase 2.5: Gap analysis

Recommended whenever a change extends existing routing, protocol translation, or session semantics.
Compare requirements against current code and call out viable implementation paths.

### Phase 3: Design

Use `/kiro:spec-design` to turn requirements into architecture and interfaces.

Design must explicitly record:
- what the spec owns,
- what it does not own,
- which packages may change,
- which contracts are stable after this work,
- which downstream specs need revalidation if these contracts change.

For this repository, every design must also answer:
1. Is this behavior core-owned or plugin-owned?
2. Does it require a new canonical concept, or is it provider-specific?
3. Can it preserve streaming-first execution?
4. Can it avoid provider SDK types leaking into the core?
5. Does it preserve the "no retry after first output" invariant?

### Phase 3.5: Design validation

Use `/kiro:validate-design` when the design touches core orchestration, routing, B2BUA continuity,
canonical models, or plugin SDK boundaries.

### Phase 4: Tasks

Use `/kiro:spec-tasks` to create small, testable implementation tasks.

Tasks must:
- map every requirement to implementation work,
- stay within one boundary at a time,
- record `_Boundary:_` and `_Depends:_` annotations when relevant,
- identify required tests and validation commands,
- prefer contract-first steps before plumbing or optimization.

### Phase 5: Implementation

Use `/kiro:spec-impl` to execute tasks through TDD.

Project-specific implementation rules:
- no code edits before requirements and design approval,
- add tests before behavior changes,
- keep core files and packages small,
- avoid speculative abstractions,
- prove changes with tests and focused runtime validation.

## Go-specific design constraints

- Prefer stdlib networking and `log/slog` unless complexity clearly decreases with a dependency.
- Avoid Go's native `plugin` package in v1.
- Use explicit plugin registration in composition roots.
- Keep `pkg/lipapi` and `pkg/lipsdk` stable and minimal.
- Keep `internal/core` free of provider SDK imports.
- Keep non-streaming behavior as a collector over the canonical event stream.
- Do not implement pairwise protocol translators.

## Revalidation triggers

Re-run design validation and integration tests when a spec changes:
- canonical request or event contracts,
- routing selector syntax or semantics,
- capability negotiation rules,
- B2BUA continuity rules,
- frontend or backend plugin registration contracts,
- stream error and cancellation semantics.
