# Requirements Document

## Introduction

This specification completes the repository-wide identity change defined by Issue #429. The existing product becomes **aiproxer** without changing its functional architecture or splitting Open Core and Enterprise code. Because this is a high-blast-radius brownfield refactor, correctness includes not only the final names but also a staged migration that keeps failures local and makes every wave independently verifiable.

The **Legacy Token Set** means the source product/repository names and project-specific abbreviations defined by Issue #429, together with their case, separator, prefix/suffix, path, filename, package, header, environment, schema, metric, and other semantic variants that refer to this project in the baseline tracked tree. Unrelated third-party identifiers and coincidental character sequences are not members of this set.

## Boundary Context

- **In scope**: product/repository identity; Go module namespaces; public package and command names; source identifiers; project-owned HTTP headers; environment/config/schema/observability identifiers; release artifacts; CI/developer tooling; comments/help; tests/fixtures; README/docs; AGENTS/agent skills; Kiro steering/templates/active/archive artifacts; filenames/directories; canonical links; repository host cutover.
- **Out of scope**: Open Core vs Enterprise separation; feature movement between repositories; licensing/commercial packaging redesign; functional architecture redesign; provider/protocol renaming; unrelated cleanup/refactors.
- **Adjacent expectations**: the later commercial split may build on the `aiproxer` identity, but this specification neither anticipates nor implements that split.
- **Boundary ownership**: repository-wide naming migration across public contracts, core/plugin consumers, composition, config/wiring, tooling, release, tests, and documentation.
- **Revalidation triggers**: public import paths, runtime wire/config names, release identifiers, persistent branded names, GitHub host path, CI/release composition, Legacy Token Set source revision or scanner contract.

## Requirements

### Requirement 1: Canonical aiproxer identity

**Objective:** As a maintainer, I want one canonical target identity, so that every project-owned surface converges on the same brand and namespace.

#### Acceptance Criteria

1. When the rebranding is complete, the product and repository shall identify themselves as `aiproxer` on all user-visible and machine-owned branding surfaces.
2. When a Go module or import path belongs to this repository, the system shall use `github.com/aiproxer/aiproxer` as the canonical root namespace.
3. Where a project-owned fully qualified domain identifier is required, the system shall use the `aiproxer.com` namespace without replacing the canonical GitHub Go module path.
4. Where a three-letter project abbreviation is appropriate, the system shall use `aip` with casing adapted to the identifier convention.
5. The rebranding shall preserve the existing architectural ownership of features and shall not introduce an Open Core/Enterprise code split.

### Requirement 2: Zero legacy branding in maintained source and produced artifacts

**Objective:** As a maintainer, I want maintained source and produced distribution artifacts to contain only the target identity, so that no shipped or maintained surface preserves an obsolete project identity.

#### Acceptance Criteria

1. When final convergence runs, every Git-tracked path name shall contain zero semantic matches from the Legacy Token Set.
2. When final convergence runs, every textual Git-tracked file shall contain zero semantic matches from the Legacy Token Set.
3. Where legacy branding exists in source, tests, comments, help, examples, scripts, workflows, release files, README/docs, AGENTS, agent skills, Kiro steering/templates/specifications, archived project documents, fixtures, or generated-source inputs, the repository shall replace or rename it to the applicable target identity.
4. If a raw character sequence matches a Legacy Token Set pattern but is demonstrably an unrelated provider, protocol, third-party, or natural-language identifier, the migration shall leave that identifier unchanged.
5. When the repository can produce release/build/package/container artifacts, final convergence shall generate the applicable artifact set identified by the migration inventory and shall scan artifact names plus textual/metadata payloads for Legacy Token Set matches; this set shall include release archives and metadata and any other distributable artifact family enabled by the repository at implementation time.
6. The zero-legacy condition shall not require rewriting immutable Git object history, external issue/PR discussion history, or GitHub-managed redirects.

### Requirement 3: Complete Go module-graph migration

**Objective:** As a Go maintainer, I want every module in the repository to share the target namespace, so that root and connector builds resolve consistently.

#### Acceptance Criteria

1. When the root module namespace is migrated, the root `go.mod` and all root-module imports shall resolve under `github.com/aiproxer/aiproxer` before public package-directory renames begin.
2. When connector-support modules are migrated, each module declaration, source import, root-module requirement/replacement, and every inter-support `require`/`replace` edge shall resolve to the target namespace while preserving the intended relative local source topology.
3. When connector modules are migrated, each module declaration and all project-owned module requirements/replacements shall resolve to the target namespace.
4. While a connector batch is in progress, previously completed module batches shall remain independently buildable/testable with the repository's supported module-check mode, including `GOWORK=off` where used by existing checks.
5. When the module-graph wave completes, all repository module tidy/check gates shall pass without references to the Legacy Token Set.

### Requirement 4: Public package and standard distribution migration

**Objective:** As an SDK and runtime consumer, I want coherent target package and executable names, so that public integration surfaces no longer expose the source brand.

#### Acceptance Criteria

1. When the canonical API package is migrated, its maintained path and package identity shall be `pkg/aipapi`, and all repository consumers shall compile against that identity.
2. When the extension SDK package is migrated, its maintained path and package identity shall be `pkg/aipsdk`, and all repository consumers shall compile against that identity.
3. When the public runtime package is migrated, its maintained path and package identity shall be `pkg/aipruntime`, and all repository consumers shall compile against that identity.
4. When the standard distribution is migrated, its command path and executable/build identity shall be `aipstd`.
5. While a public-package wave is in progress, temporary local import aliases may exist only to keep bounded consumer batches compiling; when that package wave closes, aliases carrying Legacy Token Set identifiers shall be removed.
6. The final public package surface shall not include duplicate compatibility packages or exported aliases whose purpose is to preserve the source-brand namespace.

### Requirement 5: Project-owned runtime contract namespaces

**Objective:** As an operator and client integrator, I want runtime names to use the target identity consistently, so that live configuration and protocol extensions no longer expose the source brand.

#### Acceptance Criteria

1. When project-specific HTTP defaults are migrated, every project-owned custom header shall use the `X-AIP-*` namespace and all frontend/config/test fixtures shall agree with those defaults.
2. When project-specific environment names are migrated, maintained runtime, test, script, and CI environment variables shall use the `AIP_*` namespace.
3. Where a config key, schema identifier, user-agent/product token, service name, IPC identifier, or other project-owned runtime identifier contains source branding, the maintained identifier shall use the applicable `aiproxer`, `aip`, or `aiproxer.com` target form without changing its non-brand semantics.
4. If a request or runtime configuration uses a retired project-owned name after final convergence, the system shall follow the existing unknown/unsupported-input behavior rather than silently treating the retired name as a compatibility alias.
5. The final runtime contract surface shall not dual-read or dual-emit project-specific Legacy Token Set names solely for backward compatibility.

### Requirement 6: Observability and persistent identity safety

**Objective:** As an operator, I want observability and durable state to retain meaning and data while changing brand identity, so that rebranding does not create silent telemetry splits or data loss.

#### Acceptance Criteria

1. Where a project-owned metric name or namespace contains source branding, the maintained metric shall use the `aip_*` namespace while preserving metric meaning, type, labels, and bounded-cardinality rules.
2. Where tracing, logging, service/resource, or diagnostic identity contains source branding, the maintained identity shall use the applicable target form without weakening existing security/redaction constraints.
3. If a persisted table, migration identifier, filesystem location, cache key, stored metadata value, or other durable project-owned identifier contains source branding, the implementation shall identify whether changing it can orphan or lose existing state before applying the rename.
4. When a durable branded identifier must change, the system shall provide a data-preserving forward migration and a verification/rollback procedure appropriate to that storage surface.
5. If a durable identifier is semantic rather than branding, the rebranding shall not rename it merely because it contains a coincidental character sequence.

### Requirement 7: Build, CI, release, and developer-tool convergence

**Objective:** As a contributor and release operator, I want automation to use the same target identity as the code, so that builds and releases remain reproducible after the rename.

#### Acceptance Criteria

1. When command/package paths change, Make targets, shell/PowerShell scripts, module checks, quality gates, and developer helpers shall invoke the target paths and identifiers.
2. When CI/repository automation is migrated, workflows and configuration shall use the target module, command, environment, artifact, and repository identities.
3. When release packaging is migrated, project name, build IDs, binary names, archives, install examples, and generated release metadata shall use `aiproxer`/`aipstd` as appropriate.
4. Where agent instructions, repository skills, rules, or code-review automation contain source branding or paths, those maintained artifacts shall use target terminology and paths.
5. When the tooling/release wave completes, standard local and CI-oriented quality/build commands shall execute without depending on source-brand paths or aliases.

### Requirement 8: Staged migration safety and failure localization

**Objective:** As an implementation agent, I want a strict chronological migration plan with green checkpoints, so that a repository-wide rename does not turn into thousands of simultaneous unrelated failures.

#### Acceptance Criteria

1. Before any namespace mutation begins, the implementation shall record a clean or explicitly understood baseline for the repository's relevant quality, unit-test, architecture, and module checks.
2. Before edits begin, the implementation shall classify Legacy Token Set occurrences by module/import, public package, runtime contract, persistence/observability, tooling/release, and documentation/historical surface.
3. While implementation proceeds, a task shall not begin if its declared dependency checkpoint is red for a newly introduced failure.
4. When a bounded migration batch completes, the implementation shall run its focused compile/test/check command before starting the next batch.
5. When a major migration wave completes, all gates designated for that wave shall pass before the next wave begins.
6. If a migration batch produces a failure set too broad to attribute to that batch, the implementer shall reduce/revert the batch rather than stack additional rename waves on top of unresolved failures.
7. The implementation shall not combine unrelated architecture cleanup or feature changes with the rebranding solely to make the rename convenient.
8. Before the first Legacy Token Set scan, the implementation shall freeze and record the exact Issue #429 source revision, scanner contract/version, pattern-set checksum, scanner checksum, artifact-manifest checksum, and canonical invocation; all later migration gates shall use that same provenance unless an explicitly approved rebaseline invalidates and regenerates the downstream scan evidence.

### Requirement 9: Canonical repository-host cutover

**Objective:** As a repository owner, I want the canonical GitHub location to match the new module identity, so that source, documentation, releases, and external consumers converge on one repository address.

#### Acceptance Criteria

1. Before host cutover, the implementation shall verify that the acting owner has permission to create/transfer into the `aiproxer` organization and that the target `aiproxer` repository name does not conflict with an existing repository or fork-network constraint.
2. When the codebase and repository-owned automation are target-ready, the repository shall be transferred/renamed so its canonical location is `github.com/aiproxer/aiproxer`.
3. Before transfer, the implementation shall classify relevant repository-host configuration into repository-scoped assets expected to remain associated with the repository and organization/owner-scoped dependencies that may require recreation or rebinding at the target owner.
4. When host cutover completes, repository-scoped assets—including applicable repository settings, webhooks, repository secrets, deploy keys, releases, and repository-level rules/configuration—shall be verified in the target location, while organization/owner-scoped dependencies such as organization rulesets, organization secrets/variables, team/app access, package/container permissions, Pages/DNS dependencies, and other target-owner policy shall be recreated or rebound where required and then verified.
5. When host cutover completes, contributor documentation and maintained remote/clone/install examples shall reference only the target canonical location.
6. GitHub-managed redirects from the former location may continue as platform behavior, but the maintained repository shall not rely on them as its canonical source identity.

### Requirement 10: Functional preservation and final implementation gate

**Objective:** As a project owner, I want rebranding to change identity rather than behavior, so that users receive the same proxy capabilities under the new name.

#### Acceptance Criteria

1. While names are migrated, routing, streaming, capability negotiation, frontend/backend behavior, secure sessions, billing/accounting semantics, persistence semantics, plugin boundaries, and connector behavior shall remain functionally unchanged except where the name itself is an external contract being intentionally replaced.
2. When final convergence begins, all temporary migration aliases, manifests, helper shims, duplicate package paths, compatibility-only target/source bridges, and other migration-only workspace artifacts shall be removed from the working tree and workspace; only the external scanner inputs/evidence required for final verification may remain outside the repository.
3. When final convergence runs, architecture guardrails, root tests, all-module checks, contract/parity checks, and repository quality gates shall pass under the target namespaces.
4. When final convergence runs, the frozen Legacy Token Set scanner shall report zero project-brand matches across tracked paths/textual contents and the mandatory generated artifact set.
5. After the GitHub host cutover, a clean clone from `github.com/aiproxer/aiproxer` shall run the platform-appropriate full all-module validation with the same `GOWORK` behavior as CI, build/test the standard distribution, and complete a release/configuration smoke path using only target identities.
6. If any final verification gate fails because of the rebranding, the implementation shall remain incomplete and the feature shall not be declared migrated.
