---
name: kiro-sdd-full-spec
description: Create, revise, validate, and polish a complete Kiro-style / cc-sdd-style software specification from conversation and project context. Use when the user asks to create a Kiro spec, Kiro SDD spec, full spec-driven-development specification, requirements.md/design.md/tasks.md, a spec-only change/PR, or says things like "create a new Kiro spec based on the functionalities discussed above". By default execute the complete continuous workflow: spec init -> EARS requirements -> mandatory brownfield gap analysis and requirements repair -> design discovery/synthesis -> mandatory brownfield design validation and repair -> implementation tasks -> final cross-artifact review. This skill is self-contained and does not require cc-sdd to be installed.
---

# Kiro SDD Full-Spec Orchestrator

## Mission

Produce implementation-ready Kiro-style specification artifacts with strong traceability, explicit architectural boundaries, brownfield compatibility analysis, and executable task planning. Treat specification quality as a correctness problem: discover the project before designing it, repair upstream artifacts when validation exposes defects, and do not paper over ambiguity in a downstream document.

This skill encodes the semantics of the relevant cc-sdd 3.0.2 workflows and adds a stricter continuous brownfield orchestration layer for ChatGPT Web.

## Default behavior

When the user asks to **create a Kiro spec** without naming individual phases, execute the entire workflow in this skill in one continuous run. Do not make the user restate the phase sequence and do not merely tell the user which `/kiro-*` command to run.

Default pipeline:

1. Initialize the specification.
2. Generate and review EARS requirements.
3. If the project is brownfield, perform gap analysis against the actual codebase, repair `requirements.md` when the analysis exposes requirement defects, and re-run the requirements gate.
4. Perform design discovery, synthesis, drafting, and design review.
5. If the project is brownfield, perform an additional architecture-aware design validation, automatically repair local design defects, and revalidate. If validation exposes a requirements defect, repair requirements and propagate the change through gap analysis and design.
6. Generate implementation tasks and run task-plan plus task-graph review.
7. Run a final whole-spec consistency/minimality review and apply final polish. Propagate any upstream change downstream before declaring success.

Do **not** implement production code unless the user explicitly asks for implementation in addition to specification work.

## Continuous mode vs gated mode

**Continuous mode is the default for a full-spec request.** Internal review gates count as approval gates; continue automatically after a phase passes. This is equivalent to deliberate auto-approval after validation, not blind skipping of review.

Only stop between phases when the user explicitly requests a gate-by-gate workflow. A true unresolved product/scope ambiguity may also block finalization when choosing an answer would invent material behavior.

## Source-of-truth priority

Resolve intent and constraints in this order:

1. The user's explicit current instructions.
2. Relevant discussion in the current conversation, especially decisions already made.
3. Existing spec artifacts, issue/PR text, briefs, ADRs, or other project documents explicitly associated with the work.
4. Project steering files, especially `.kiro/steering/product.md`, `tech.md`, and `structure.md`, plus directly relevant custom steering.
5. The actual codebase and tests for current-state facts.
6. External authoritative documentation for dependencies, protocols, standards, or current technical facts when research is needed.
7. The bundled rules in `references/` as the workflow fallback.

Do not ask the user to repeat information that is already available in conversation or project context.

## Project-local cc-sdd customization

This skill must work without cc-sdd being installed. However, if the target project already contains intentional `.kiro` customization, respect it:

- Read project-local `.kiro/steering/` files when present.
- Prefer project-local `.kiro/settings/templates/specs/` templates when they materially differ from the bundled defaults.
- Apply project-local rules that clearly customize Kiro artifacts, provided they do not contradict the user's current instruction.
- Never browse the cc-sdd repository merely to remember what a phase means; the bundled references are sufficient.

## ChatGPT Web execution model

Use the tools available in the current ChatGPT Web session rather than assuming Codex filesystem commands exist.

- For a connected GitHub project, inspect repository files, tests, history, issues, and PR context through the GitHub connector when useful.
- If the user requests repository writes, create/update the canonical spec files on a non-default working branch. Do not mix unrelated code changes into a spec-only change.
- Open a PR only when the user asks for a PR/submission or the request explicitly establishes that delivery mode.
- If repository writes are unavailable, create downloadable artifacts preserving the canonical `.kiro/specs/<feature>/` layout.
- Use web research only when external/current technical facts materially affect design; prefer primary/official sources.
- Subagents are optional optimization. If no subagent primitive is available, perform the same research/review sequentially. Never downgrade the quality gate because subagents are unavailable.

## Brownfield determination

Treat work as **brownfield** when the target repository already contains an implementation that the proposed feature changes, extends, replaces, integrates with, or must preserve. A mature repository with a new module is still brownfield when that module must fit existing architecture.

Treat work as greenfield only when no meaningful existing implementation constrains the design.

When uncertain, prefer brownfield analysis; it is safer than designing against an imagined blank slate.

## Canonical output layout

For feature slug `<feature>` use:

```text
.kiro/specs/<feature>/
├── spec.json
├── requirements.md
├── research.md        # required for brownfield/full discovery; otherwise create when research is meaningful
├── design.md
└── tasks.md
```

Do not create extra permanent validation reports by default. Gap findings and design research belong in `research.md`; review verdicts are workflow state unless the project's local templates explicitly require additional artifacts.

## Required reference loading

Before executing a full spec, read these bundled files:

- `references/01-orchestration.md`
- `references/02-requirements.md`
- `references/03-brownfield-gap.md`
- `references/04-design.md`
- `references/05-design-validation.md`
- `references/06-tasks.md`
- `references/07-final-review.md`
- `references/08-templates.md`

`references/09-provenance.md` is informational and should be read when provenance/version questions arise.

## Artifact language

Use the language explicitly requested by the user. Otherwise use the language of the specification request; if that cannot be determined, use English (`en`). Store the corresponding language code in `spec.json`.

For EARS acceptance criteria, keep the canonical structural keywords (`When`, `If`, `While`, `Where`, and the `shall` construction) recognizable even when the surrounding requirement text is localized, unless a project-local template deliberately specifies another convention.

## Scope discipline

Requirements define **observable contract**. Design defines **technical realization and boundaries**. Tasks define **implementation work**.

Do not leak architecture choices into requirements. Do not use tasks to invent design. Do not use design validation to silently change product scope.

When a downstream phase discovers an upstream problem, move backward, repair the upstream artifact, re-run its gate, then regenerate/reconcile every affected downstream artifact.

## Completion rule

A full-spec request is complete only when:

- required artifacts exist;
- requirements pass the requirements gate;
- brownfield gap analysis has been performed when applicable and requirement defects have been reconciled;
- design passes both the draft design gate and brownfield design validation when applicable;
- tasks pass coverage, executability, dependency, and boundary checks;
- final cross-artifact review passes;
- `spec.json` accurately reflects completion state;
- no material placeholder, contradiction, stale requirement ID, unowned design component, hidden prerequisite, or unpropagated repair remains.

If the final state is implementation-ready, set all three approval groups to generated/approved `true`, set `phase` to `tasks-generated`, and set `ready_for_implementation` to `true`. If a material unresolved blocker remains, do not falsely mark the spec ready.

## Final user-facing report

After completing the workflow, report concisely:

- feature slug and artifact location;
- brownfield/greenfield classification;
- whether requirements gap analysis changed requirements;
- design validation verdict and significant repairs made;
- task count/parallelization summary;
- final GO/NO-GO for implementation readiness;
- repository branch/PR or downloadable artifact location, when applicable.

Do not dump internal review scratch work unless the user asks for it.
