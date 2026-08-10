# Design Validation Review

## Review Method

The design was validated as a brownfield architecture change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- structure/testing/architecture steering;
- repository `main` at `294fa587b902fa0989adab8ad0a16f6ab001c33e`;
- canonical `lipapi` usage event, presence, scope and DedupeKey contracts;
- tokenaccounting counter/reconcile/streamusage/ledger paths;
- runtime receive/finalize/cancellation/customer evidence paths;
- metering Fact/journal/aggregate/checkpoint paths;
- static accounting and generic economics.Rater paths;
- request/attempt authority coordinators and fixed-window usage authority;
- token-ledger adapters;
- control-plane usage observer and metering-backed reports;
- backend-plugin AccountingEvidence/FinalizeBilling;
- terminal ownership/terminal-work recovery;
- all final requirements and brownfield gaps.

The review used remediation rounds. Any unresolved split source of truth, duplicated lifecycle owner, overengineered abstraction, persistence coupling, provider/core leakage, terminal ownership conflict, or unowned deletion returned NO-GO.

## Round 1

### Assessment

**Decision: NO-GO**

The initial design correctly sought a canonical economic pipeline but introduced too much new architecture.

### Critical Issue 1: New public EconomicStatement duplicated metering

**Concern:** A public statement DTO between `metering.Fact` and rating/authority/reporting would become another schema to populate and version.  
**Impact:** More mapping code and a third representation of usage rather than convergence.  
**Resolution:** Reuse `metering.Fact` and enrich only the internal reducer Snapshot with stable metadata/provenance needed downstream. No public EconomicStatement.  
**Traceability:** Requirements 3–4, 13; Design Metering Reducer.

### Critical Issue 2: Durable journal was becoming the live transaction coordinator

**Concern:** Forcing live settle through the database would make optional metering mandatory and add persistence latency to terminal paths.  
**Impact:** Operational complexity and a new failure mode in requests that currently run without a durable journal.  
**Resolution:** Request/Attempt hold the same Fact objects in memory and optionally append them to the recorder. The reducer is pure and works on either live facts or durable query facts.  
**Traceability:** Requirement 3.7–3.9; Design Accounting Service, Persistence.

### Critical Issue 3: Generic AccountingManager was a new god object

**Concern:** One manager owning persistence, reduction, rating, authority, reports, terminal recovery and diagnostics would recreate `retryRecvStream` gravity under a new name.  
**Impact:** SRP failure and difficult testing.  
**Resolution:** Use concrete Request/Attempt lifecycle objects. Reducer, Rater, coordinators, UsageAuthority store, terminal-work and read models retain their algorithms.  
**Traceability:** Requirement 6, 13; Design Components.

### Critical Issue 4: Content-fingerprint fallback dedupe preserved the current bug class

**Concern:** Numeric/content fingerprints can collapse separate equal-value charges.  
**Impact:** Under-counting that is extremely hard to diagnose.  
**Resolution:** Stable Fact identity is the only replay identity. Locally generated evidence receives stream-local sequence identity; correction/replacement uses Supersedes.  
**Traceability:** Requirements 3.2, 5.4; Design Identity Rules.

## Round 2

### Assessment

**Decision: NO-GO**

The evidence model became simple, but authority/rating/runtime migration still had alternate paths that could survive indefinitely.

### Critical Issue 1: Built-in UsageAuthority remained a direct runtime fast path

**Concern:** External authorities used coordinators while built-in usage authority could continue direct calls and clamp preview fallback.  
**Impact:** Two admission/settlement lifecycles with different compensation/version rules.  
**Resolution:** Compose built-in usage authority as request/attempt providers and make coordinators the only lifecycle path. Add direct-call retirement gate.  
**Traceability:** Requirement 8; Design Authority Provider Adapters.

### Critical Issue 2: Static price catalog remained a silent fallback

**Concern:** A configured external rater could fail and runtime could silently substitute static OSS pricing.  
**Impact:** Money derived under a different commercial policy than the version bound at admission.  
**Resolution:** Static catalog becomes a Rater adapter selected at composition. A selected external rater failure remains unavailable; provider-reported money still wins.  
**Traceability:** Requirement 7; Design Static Rater.

### Critical Issue 3: Token ledger remained independently writable

**Concern:** Runtime could write both metering and legacy token ledger, leaving two durable totals to drift.  
**Impact:** Exactly the source-of-truth ambiguity the spec is meant to remove.  
**Resolution:** Stop direct token-ledger writes; inventory consumers, then delete or derive one-way compatibility rows from metering.  
**Traceability:** Requirement 10; Design Legacy Token Projection.

### Critical Issue 4: Legacy usage observer was being widened into a billing API

**Concern:** Adding presence/correction fields to `usage.Observer` would create another economic contract rather than removing its authority.  
**Impact:** Two reporting input models and larger public SDK.  
**Resolution:** Keep observer best-effort/telemetry. Metering becomes economic reporting source.  
**Traceability:** Requirement 9.4–9.6; Design Reporting.

### Critical Issue 5: Accounting lifecycle was at risk of owning terminal winner selection

**Concern:** Moving cancel/finish/close logic wholesale out of runtime would conflict with no-retry-after-output and existing terminal CAS.  
**Impact:** Competing terminal state machines.  
**Resolution:** Runtime terminal CAS stays authoritative; the winning terminal invokes idempotent Request/Attempt finalization. Terminal-work remains durable recovery.  
**Traceability:** Requirements 1.4, 6.5–6.8; Design System Flows.

### Critical Issue 6: Migration had no forced deletion outcome

**Concern:** The new lifecycle could be layered on top of old streamusage/runtime merge/ledger paths permanently.  
**Impact:** Higher complexity than today.  
**Resolution:** Add explicit deleted concepts, consumer inventories, forbidden-symbol/import gates, >=40% runtime accounting-specific contraction and >=20% legacy economic-surface contraction.  
**Traceability:** Requirement 14; Design Architecture Enforcement.

## Round 3

### Requirements Traceability Review

**Decision: PASS**

- Every numbered requirement maps to a target component/flow and implementation phase.
- All current defect classes identified in gap analysis have an explicit RED test and owning target semantic layer.
- Provider edge, runtime terminal, metering, rating, authority, reporting and financial-accounting boundaries are explicit.
- Optional durability remains optional.
- Historical incomplete data remains explicit rather than rewritten.
- Compatibility debt has consumer inventory and retirement triggers.

### Simplicity / YAGNI Review

**Decision: PASS**

The final design introduces no new external dependency, no generic framework and no new public economic DTO. It reuses:

- `metering.Fact`;
- `metering.Recorder/Querier`;
- `metering/aggregate`;
- `economics.Rater`;
- request/attempt authority coordinators;
- existing usage-authority app/store;
- existing terminal-work.

The only meaningful new application abstraction is concrete Request/Attempt lifecycle ownership inside the existing `internal/core/accounting` namespace.

### SOLID Review

**Single Responsibility — PASS**

- adapter: provider usage semantics;
- metering reducer: fact semantics;
- accounting Request/Attempt: lifecycle sequencing;
- rater: pricing;
- authority provider/store: admission/reservation mutation;
- runtime: execution/terminal ordering;
- control plane: reporting/read projection.

**Open/Closed — PASS**

New token components can use metering quantities; new raters/authorities use existing public ports; provider-specific parsing remains adapters. No central provider switch is added.

**Liskov Substitution — PASS**

Raters and authority providers must satisfy existing validation/lifecycle contracts. Static rater and external rater are selected explicitly rather than runtime mixing behaviors.

**Interface Segregation — PASS**

No broad AccountingManager interface. Concrete lifecycle types consume existing narrow Recorder/Rater/Authority/Counter contracts.

**Dependency Inversion — PASS**

Core accounting depends on public ports/canonical types. SQL, provider SDKs and static-catalog implementation live at edges.

### Hexagonal Review

**Decision: PASS**

- Domain fact reduction is pure.
- Accounting is app/use-case orchestration.
- Runtime is a driving orchestrator, not an economic-policy owner.
- Provider/tokenizer/store/rater implementations are driven adapters.
- Runtimebundle remains the explicit composition root.
- Control-plane reporting is a query seam.
- No provider SDK crosses inward.

### Data Integrity Review

**Decision: PASS**

- Identity beats value-based dedupe.
- Explicit zero stays distinct from absence.
- Cumulative/replacement semantics have one reducer.
- Money carries currency/presence/source.
- Provider money and rated money have explicit precedence/provenance.
- Customer and operator stay independent.
- Authority mutation state is not conflated with metering evidence.

### Concurrency / Terminal Review

**Decision: PASS**

- Request/Attempt accounting objects do not own routing or terminal CAS.
- Finalization is idempotent.
- Context is method-scoped; detached contexts are used only at calls that must outlive cancellation.
- No per-request accounting goroutine is introduced.
- Terminal-work carries immutable recovery data.

### Brownfield Compatibility Review

**Decision: PASS**

- Existing provider adapters remain authoritative for wire semantics.
- Final-only usage remains simple.
- Existing Rater/Authority/Metering SDK contracts remain.
- UsageAuthority store algorithm is preserved.
- Non-streaming remains stream collection.
- No post-output retry behavior changes.
- Old reports/token ledger stay only until replacement/consumer proof permits deletion.

### Testing / Provability Review

**Decision: PASS**

The design is unusually testable because its central accounting rule is a pure reducer. Property/metamorphic tests can prove chunking/replay/correction invariants independently of runtime. Request/Attempt lifecycle tests use in-memory facts and small port stubs. Runtime tests focus on terminal ordering, while adapter tests focus on provider normalization. Architecture tests prove ownership and deletion.

### Maintainability / Future Feature Review

**Decision: PASS**

Future changes have predictable homes:

- new provider usage quirk -> backend adapter/connector;
- new billable quantity -> metering Quantity/component schema and raters/rules that choose it;
- new pricing -> Rater implementation;
- new quota/budget authority -> Authority provider;
- new customer/operator report -> reducer-backed query projection;
- financial balance/invoice -> separate enterprise bounded context consuming settled evidence.

No change requires editing `retryRecvStream` merely because an economic component grows.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The final architecture is a convergence/deletion plan rather than a platform rewrite. It makes the newest and strongest existing abstractions authoritative, gives technical accounting one app-level owner, and removes alternate semantic paths from runtime/reporting/token reconciliation. It meets the user's simplicity/modularity/testability priorities and the repository's larger-Go-codebase conventions.

No production implementation is authorized by this review.

## Implementation Gate

Implementation shall begin only after maintainers explicitly set requirements/design/tasks approvals to true and set `ready_for_implementation` to true in `spec.json`. The first implementation phase must be RED characterization and baseline capture.

## Final Artifact Consistency Pass

After task generation, the complete spec bundle was reviewed once more as a unit.

**Decision: PASS**

- All 14 numbered requirements and all acceptance criteria have an explicit implementation or verification owner in `tasks.md`.
- Task dependencies preserve the intended migration order: characterization -> reducer -> lifecycle owner -> authority/rating convergence -> runtime cutover -> read-model migration -> legacy deletion -> architecture certification.
- The design remains below the Kiro template's 1,000-line complexity warning and deliberately reuses the existing metering, economics, authority, terminal-work, and composition contracts rather than adding a parallel framework.
- The production source-of-truth boundaries remain singular: metering facts for measured economic evidence, authority stores/providers for enforceable reservation state, and future financial accounting outside this specification.
- No task authorizes a second permanently mutating shadow accounting path; comparison work is test-only or non-mutating migration evidence.
- `spec.json` remains `ready_for_implementation: false` with requirements/design/tasks unapproved. This PR contains specification artifacts only.
