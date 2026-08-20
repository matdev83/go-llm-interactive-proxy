---
name: golang-thermonuclear-code-review
description: "Run an unusually strict, evidence-backed Go maintainability review. Use for thermo-nuclear code review, deep code-quality audit, structural simplification, package-boundary analysis, or aggressive detection of complexity regressions."
---

# Go thermonuclear code review

## Mission

- Audit the changed Go surface, callers, implementations, dependencies, and tests.
- Find structural regressions, tangled control flow, weak ownership, leaky boundaries, and needless concepts.
- Seek code-judo: delete branches, states, wrappers, layers, and duplication by using the existing design better.
- Require evidence. Preserve behavior. Prefer the highest-leverage remedy; allow wider restructuring when it demonstrably deletes more concepts, branches, or ownership ambiguity.
- Be direct and demanding; do not soften material maintainability failures into optional polish.
- Reject cosmetic churn, speculative abstractions, arbitrary size limits, and taste-only findings.

## Evidence pass

1. Record branch, worktree status, review base, and exact diff; preserve unrelated changes.
2. Exclude generated code unless its source contract or generated behavior changed.
3. Trace changed symbols through direct callers, implementations, imports, tests, and wire boundaries.
4. Map package responsibility, dependency direction, variation seams, state ownership, lifecycle ownership, and public contracts.
5. Label every concern as introduced, worsened, pre-existing, uncertain, or generated-only.

## Attack checklist

### Structure

- Delete incidental concepts before extracting or relocating them.
- Flag feature checks scattered through shared paths, repeated condition chains, flag-driven modes, partial-state protocols, duplicated helpers, identity wrappers, and abstraction layers that do not reduce reader state.
- Keep policy separate from transport, storage, provider, generated, and composition details.
- Keep cohesive behavior together; split only independent change reasons or ownership boundaries.
- Keep interfaces at consumers, minimal, and justified by demonstrated substitution or testing; prefer concrete types or function values otherwise.
- Keep construction and registration explicit; reject reflection, globals, hidden side effects, and speculative plugin machinery.
- Reframe state models so branches disappear; turn exceptions into the default flow; prefer typed models or explicit dispatch over flag combinations.
- Require cohesion or decomposition justification when a change materially enlarges an overloaded file or package; treat size as evidence, never proof.

### Contracts

- Preserve exported API, method sets, zero-value usability, mutation/aliasing, ordering, idempotency, retry, blocking, and serialization behavior.
- Preserve nil-versus-empty collections and absent-versus-explicit JSON fields.
- Trace errors end to end; preserve wrapping, sentinel/type identity, partial results, and wire mapping through `errors.Is`/`errors.As`.
- Require `context.Context` at I/O boundaries; verify propagation, cancellation, deadlines, and no storage in long-lived structs.
- Reject silent capability loss, fallback that hides an invariant, and adapter semantics leaking into core policy.

### Lifecycle and concurrency

- Identify the owner of every goroutine, channel close, lock, timer, transaction, stream, body, file, socket, and cancellation function.
- Trace success, failure, cancellation, retry, shutdown, and partial-initialization paths.
- Check goroutine exit, bounded work, send/close ordering, lock order/scope, cleanup coverage, `defer` order, atomicity, and race/deadlock/leak risk.
- Do not reorder cleanup, locking, channel, transaction, or error paths without mechanical equivalence.
- Do not prescribe parallelism unless it improves the demonstrated problem while preserving ordering, bounds, cancellation, ownership, and error semantics.

## Proof

- Run the narrowest focused tests that exercise the implicated path; prove selectors are non-vacuous.
- Build/test affected packages and callers; use repository lint/vet gates when relevant.
- Use `-race` for concurrency claims where supported; use benchmarks/profiles only for performance claims.
- Inspect contract fixtures and assertions for API, JSON, errors, cancellation, streaming, and cleanup findings.
- Record skipped checks, unrelated failures, and residual assumptions; passing adjacent tests is not proof.

## Output

- Return findings first; order structural regressions, missed high-leverage simplifications, contract/lifecycle risks, then local maintainability issues; break ties by confidence.
- Use this header for every finding:

`severity | confidence | file:line/symbol | introduced/worsened/pre-existing`

- `Evidence`: diff plus caller, test, contract, or runtime proof.
- `Consequence`: concrete failure mode or maintenance cost.
- `Remediation`: highest-leverage behavior-preserving structural change; invariant to preserve.
- `Verification`: completed and required checks.
- Do not approve merely because behavior is correct or tests pass.
- Treat demonstrated structural regressions and clear opportunities to delete substantial incidental complexity as presumptive blockers; require explicit justification to waive.
- Block other contract, ownership, lifecycle, dependency, or material complexity regressions only when demonstrated.
- If no finding survives verification, say so; list inspected surface and checks.
