---
name: golang-solid-principle-review
description: >
  Perform a rigorous multi-pass architectural review of Go code using SOLID principles
  adapted to idiomatic modern Go. Use for pull requests, diffs, packages, or repositories
  when the main objective is to assess cohesion, extensibility, substitutability, interface
  design, and dependency direction without importing Java-style abstractions into Go.
---

# Go SOLID Review

Review Go code through **five separate SOLID passes**, then perform a final validation pass.

The skill is intentionally narrow. Do not turn it into a general bug hunt, style review,
security audit, or performance review unless an issue is directly caused by a SOLID design
problem.

## Core stance

Apply SOLID as architectural reasoning tools, not as mechanical rules.

In idiomatic Go:

- packages are often the primary units of responsibility and dependency direction;
- composition is preferred over inheritance-style hierarchies;
- concrete types are preferred until substitution is actually needed;
- interfaces usually belong near the consumer;
- function values are often better than one-method interfaces;
- generics express compile-time algorithms, not runtime dependency inversion;
- embedding is method promotion and composition, not classical inheritance;
- explicit construction is preferred over service locators, mutable globals, or DI frameworks;
- a small amount of duplication is often better than a premature abstraction.

Never recommend an abstraction merely because it resembles textbook SOLID.

---

# Review protocol

## Pass 0 — Build the design map

Do not report findings yet.

Inspect enough surrounding code to identify:

1. The responsibility of each changed package and major type.
2. The high-level policy code and the low-level implementation details.
3. Public APIs, interfaces, function types, generic constraints, and concrete implementations.
4. Dependency direction across package imports and constructors.
5. State ownership, lifecycle ownership, and concurrency ownership.
6. Existing extension points and likely variation axes demonstrated by the codebase.
7. Callers, implementations, tests, and mocks relevant to changed contracts.

Produce a private design map containing:

- package responsibilities;
- key abstractions and their consumers;
- dependency edges;
- behavioral contracts;
- known extension axes;
- suspicious coupling points.

If the review scope is a diff, distinguish new problems from pre-existing design debt.

---

## Pass 1 — Single Responsibility Principle

### Go interpretation

A component should have one cohesive reason to change.

Evaluate SRP primarily at these levels:

1. package;
2. type;
3. function;
4. goroutine or lifecycle owner.

Do not equate SRP with short functions, tiny files, or many packages.

### Look for

- packages that mix unrelated domain, transport, persistence, orchestration, and presentation concerns;
- vague dumping-ground packages such as `util`, `common`, `helpers`, `manager`, or `service`;
- types that own unrelated state with different lifecycles or synchronization needs;
- functions that combine policy decisions with volatile I/O details;
- business rules duplicated because no component clearly owns the invariant;
- configuration parsing, dependency construction, and business execution mixed together;
- goroutines whose startup, shutdown, cancellation, and state ownership are split across components;
- exported types that expose fields from several unrelated responsibilities;
- packages that must change for unrelated feature categories.

### Do not flag

- a long but cohesive function;
- a package containing several closely related domain types;
- straightforward orchestration code whose purpose is explicitly coordination;
- a file merely because it contains multiple declarations;
- procedural code that becomes less clear when split.

### Valid SRP finding test

Report only when you can state:

1. the distinct responsibilities being mixed;
2. the independent reasons they change;
3. the concrete consequence of mixing them;
4. a smaller, coherent separation boundary.

---

## Pass 2 — Open/Closed Principle

### Go interpretation

Stable policy should support demonstrated variation through composition without requiring
widespread modification.

Go does not require every future behavior to be hidden behind an interface. A `switch` over a
truly closed set can be clearer and safer than an artificial plugin system.

### Prefer these extension mechanisms, in order

1. direct concrete composition;
2. function parameter or function type;
3. small consumer-owned interface;
4. configuration or data-driven dispatch;
5. generic algorithm when behavior is compile-time and type-independent;
6. explicit registry only when runtime registration is a real requirement.

### Look for

- the same branch structure repeated across packages for one evolving concept;
- stable policy importing every concrete provider or backend;
- adding one implementation requiring edits in many unrelated packages;
- orchestration duplicated with only one behavior varying;
- large functions controlled by many independent flags or mode enums;
- concrete clients constructed deep inside policy code;
- extension logic implemented through global `init` registration with hidden order or side effects;
- generic code that still requires runtime type switches for every new type;
- embedding used to simulate an inheritance hierarchy with fragile promoted behavior.

### Do not flag

- a local switch over a closed protocol or finite state machine;
- code that has only one implementation and no demonstrated variation axis;
- direct construction at the application composition root;
- explicit code that is simpler than a speculative abstraction;
- edits required because the business rule itself changed.

### Valid OCP finding test

Report only when:

1. a genuine extension axis is evidenced by multiple implementations, repeated branching,
   requirements, or active growth;
2. stable code is repeatedly modified because of that axis;
3. a concrete Go-native extension seam would materially reduce coupling.

---

## Pass 3 — Liskov Substitution Principle

### Go interpretation

Every implementation of an interface, function contract, wrapper, adapter, or generic constraint
must preserve the behavior relied on by its consumers.

Implicit interface satisfaction makes behavioral review more important, not less.

### Evaluate the full contract

Check consistency of:

- accepted inputs;
- returned values;
- nil and zero-value behavior;
- error identity and wrapping;
- partial-result behavior;
- mutation and ownership;
- aliasing of slices, maps, buffers, and pointers;
- ordering and determinism;
- idempotency;
- blocking behavior;
- context cancellation and deadlines;
- concurrency safety;
- resource ownership and closure;
- retry semantics;
- side effects;
- performance characteristics explicitly relied upon by callers.

### Look for

- implementations returning incompatible sentinel or wrapped errors;
- wrappers that break `errors.Is` or `errors.As` expectations;
- implementations that unexpectedly retain or mutate caller-owned data;
- mocks that accept behavior production rejects, or reject behavior production accepts;
- implementations with different nil versus empty semantics;
- implementations that ignore context cancellation;
- a supposedly concurrent-safe implementation returning mutable internal state;
- embedded types exposing promoted methods that bypass invariants;
- pointer- and value-receiver method-set mismatches that change interface satisfaction unexpectedly;
- typed nil values stored in interfaces and returned as non-nil;
- adapters that silently change ordering, idempotency, or retry behavior;
- generic constraints that admit types for which the algorithm's real assumptions do not hold.

### Do not flag

- implementation-specific behavior that callers do not rely upon;
- stronger guarantees that do not invalidate consumer assumptions;
- internal differences that remain inside the documented contract;
- different performance where no relevant guarantee or practical dependency exists.

### Valid LSP finding test

Report only when you can identify:

1. the consumer expectation;
2. the substitutable implementations or abstractions involved;
3. the incompatible behavior;
4. a realistic caller-visible failure.

---

## Pass 4 — Interface Segregation Principle

### Go interpretation

A consumer should depend only on the behavior it actually needs.

Interfaces should usually be defined at the point of use. Small interfaces are valuable when they
represent a real behavioral boundary, not when they exist only to satisfy a mocking habit.

Function values may be preferable when only one operation varies.

### Look for

- broad provider-owned interfaces forcing consumers to depend on unrelated methods;
- mocks implementing many unused methods;
- interfaces that change whenever a concrete implementation gains a capability;
- consumers using only a stable subset of a large interface;
- interfaces whose methods belong to different lifecycles or responsibilities;
- functions accepting `any` and then reconstructing an implicit interface with type switches;
- callers depending on concrete type assertions beyond the declared interface;
- generic constraints exposing more operations than the algorithm needs;
- interfaces that mix command and query responsibilities without a cohesive reason;
- constructors returning an interface that hides useful, stable concrete behavior.

### Consider these remedies

- move the interface to the consuming package;
- split interfaces along actual consumer boundaries;
- compose small interfaces where a consumer genuinely needs the combination;
- replace a one-method interface with a named function type;
- accept a concrete type when there is no real substitution requirement;
- narrow a generic constraint to the actual operations used.

### Do not flag

- a broad interface consumed broadly by the same component;
- a one-method interface with genuine semantic value or multiple implementations;
- standard-library interfaces used idiomatically;
- a concrete dependency merely because it is harder to mock;
- a larger interface whose methods are operationally inseparable.

### Valid ISP finding test

Report only when:

1. a specific consumer is forced to depend on methods it does not need;
2. that dependency causes coupling, breakage, awkward mocks, or misuse;
3. a narrower contract corresponds to a real boundary.

---

## Pass 5 — Dependency Inversion Principle

### Go interpretation

High-level policy should not depend directly on volatile low-level details when a stable behavioral
boundary exists.

Dependency inversion in Go is usually achieved through:

- package direction;
- explicit constructors;
- function parameters;
- consumer-owned interfaces;
- function types;
- composition in `main` or another composition root.

Do not introduce a DI container merely to satisfy DIP.

### Look for

- domain or policy packages importing database drivers, HTTP clients, cloud SDKs, CLI frameworks,
  or vendor implementations directly;
- concrete external clients created deep inside business logic;
- hidden dependencies in package-level mutable variables;
- service-locator patterns;
- context values used as dependency containers;
- low-level adapters deciding domain policy;
- constructors that both wire dependencies and perform substantial business work;
- tests requiring real network, filesystem, clock, randomness, or process state because dependencies
  are implicit;
- provider-owned interfaces shaped around implementations instead of consumer needs;
- callbacks used to hide an import cycle without fixing the conceptual dependency cycle;
- packages that cannot be reused because they import application-specific wiring;
- initialization through `init` functions that makes dependency order implicit.

### Prefer

- explicit dependency construction at the application boundary;
- injecting only volatile or behaviorally meaningful dependencies;
- standard-library abstractions such as `io.Reader`, `io.Writer`, `fs.FS`, and `http.RoundTripper`;
- function parameters for clocks, randomness, single operations, or callbacks;
- concrete dependencies for stable, local, deterministic implementation details;
- `internal` packages to enforce dependency direction where appropriate.

### Do not flag

- stable concrete dependencies with no meaningful substitution need;
- direct use of standard-library functionality;
- direct construction in `main` or a dedicated wiring package;
- simple pure helpers;
- package-level immutable constants or stateless functions;
- lack of an interface where no abstraction boundary exists.

### Valid DIP finding test

Report only when you can show:

1. which code represents high-level policy;
2. which volatile detail it depends on;
3. how the dependency obstructs testing, replacement, reuse, or independent evolution;
4. the narrowest explicit dependency boundary that fixes it.

---

# Pass 6 — Cross-principle validation

Review all provisional findings before producing the answer.

For every finding:

1. Re-open the relevant code and verify the exact evidence.
2. Confirm that the issue is caused or materially worsened by the reviewed change.
3. Identify the primary SOLID principle. Mention secondary principles only when useful.
4. Remove duplicates across passes.
5. Reject purely hypothetical extension concerns.
6. Reject suggestions that add more abstraction than the problem requires.
7. Reject findings based only on naming, file length, method count, or personal style.
8. Confirm that the proposed remedy remains idiomatic Go.
9. Prefer the smallest safe correction over architectural rewriting.
10. Limit the final result to material findings.

If a proposed fix introduces an interface, verify all of the following:

- there is a real substitution boundary;
- the consumer can own the interface;
- the interface contains only required behavior;
- a function type would not be simpler;
- the abstraction does not exist only for tests;
- the abstraction does not leak implementation-specific types.

If a proposed fix splits a package or type, verify that the new boundary has independent cohesion,
ownership, and reasons to change.

---

# Severity model

Use only these severities:

## Critical

The design issue can directly cause severe production failure, data loss, security compromise,
permanent incompatibility, or an unbounded operational failure.

## High

The design issue creates a likely correctness failure, major contract violation, severe coupling,
or unsafe lifecycle/concurrency behavior.

## Medium

The design issue materially increases change risk, makes an active extension axis brittle, or
creates a substantial testing and maintenance burden.

## Low

The issue is real and evidenced but has limited current impact. Use sparingly.

Do not report speculative cleanup as Low.

---

# Output format

Start with findings. Order them by severity, then by confidence.

For each finding use exactly this structure:

## [Severity] Concise finding title

- **Principle:** `SRP`, `OCP`, `LSP`, `ISP`, or `DIP`
- **Location:** precise file and line range, symbol, package, or dependency edge
- **Evidence:** what the code concretely does
- **Why it matters:** the current or realistic failure mode
- **Go-specific analysis:** why this is a SOLID problem in Go rather than a generic style preference
- **Recommended correction:** the smallest idiomatic design change
- **Confidence:** `high`, `medium`, or `low`

After findings, include:

## SOLID review ledger

| Principle | Result | One-sentence assessment |
|---|---|---|
| SRP | Healthy / Concern / Violation / Insufficient evidence | ... |
| OCP | Healthy / Concern / Violation / Insufficient evidence | ... |
| LSP | Healthy / Concern / Violation / Insufficient evidence | ... |
| ISP | Healthy / Concern / Violation / Insufficient evidence | ... |
| DIP | Healthy / Concern / Violation / Insufficient evidence | ... |

Then include:

## Overall design assessment

Summarize:

- the dominant architectural strength;
- the dominant architectural risk;
- whether the change should be accepted, revised, or redesigned;
- the highest-value next action.

If no material violations exist, state:

> No material SOLID violations found in the reviewed scope.

Then provide the ledger and a brief overall assessment. Do not invent findings to fill the report.

---

# Review constraints

- Do not review all five principles simultaneously. Complete each pass separately.
- Do not use SOLID acronyms as substitutes for reasoning.
- Do not recommend inheritance-style base types.
- Do not recommend interfaces for every concrete dependency.
- Do not recommend package fragmentation without a cohesive boundary.
- Do not recommend plugin systems for hypothetical future needs.
- Do not treat generics as a replacement for runtime polymorphism.
- Do not treat interfaces as a replacement for generics.
- Do not use mocking convenience as the sole reason for an abstraction.
- Do not penalize explicit code merely for containing a switch or duplication.
- Do not propose a DI framework unless the repository already uses one and the finding concerns its
  incorrect application.
- Do not widen the review into unrelated style, formatting, or micro-optimization comments.
- Do not praise routine code. Spend output budget on material design information.

---

# Optional delegation mode

When subagents are available, delegate exactly one principle to each subagent:

1. SRP reviewer;
2. OCP reviewer;
3. LSP reviewer;
4. ISP reviewer;
5. DIP reviewer.

Provide every subagent with the same design map and review scope.

Each subagent must return only provisional findings with evidence. The coordinating agent must then
perform Pass 6 itself, verify the code, remove duplicates, and reject over-engineered recommendations.

Do not publish unverified subagent findings.
