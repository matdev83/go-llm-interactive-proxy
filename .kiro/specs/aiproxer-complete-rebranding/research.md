# Research and Brownfield Gap Analysis

## Scope and source of truth

This specification implements Issue #429 as a complete rebranding of the existing product to **aiproxer**. The issue is the authoritative source for the legacy-name set and the target naming scheme. The durable specification intentionally refers to the source names as the **Legacy Token Set** rather than reproducing them: the requested end state is a tracked tree with no remaining source-brand references, so embedding those spellings in the new specification would make the specification itself violate the completion condition.

The target identities are:

- Product, repository, release project, and user-facing executable family: `aiproxer`.
- Canonical GitHub module/repository namespace: `github.com/aiproxer/aiproxer`.
- Project-owned FQDN namespace where a domain-qualified identifier is appropriate: `aiproxer.com`.
- Three-letter project abbreviation: `aip`.
- Public canonical API package: `pkg/aipapi`.
- Public extension/SDK package: `pkg/aipsdk`.
- Public runtime package: `pkg/aipruntime`.
- Standard distribution command and binary: `aipstd`.
- Project-specific HTTP header namespace: `X-AIP-*`.
- Project-specific environment namespace: `AIP_*`.
- Project-specific metric namespace: `aip_*`.

Open-core/Enterprise repository separation is explicitly outside this specification. Existing code remains in its current architectural ownership zones; this work changes identity and names, not commercial packaging boundaries.

## Brownfield classification

**Classification: brownfield, high blast radius.**

The repository is a mature multi-module Go system. Branding is embedded in compile-time namespaces, runtime contracts, release packaging, test infrastructure, documentation, agent instructions, archived specifications, and repository-host identity. A broad search-and-replace would create a large interval in which packages cannot compile, nested modules cannot resolve local dependencies, fixtures disagree with production defaults, and CI/release tooling refers to paths that no longer exist.

The implementation therefore needs a migration graph, not a rename checklist.

## Current-state findings

### 1. Root module identity is a compile-time root dependency

The root `go.mod` declares the current repository namespace as the module path. Internal imports across core, plugins, public packages, tests, tools, and command packages derive from that module identity. The target module path is `github.com/aiproxer/aiproxer`.

**Consequence:** change the root module/import prefix as one coherent wave before renaming public package directories. Mixing the root-module cutover with several package moves would make compiler errors ambiguous and multiply the number of simultaneous failure causes.

### 2. The repository contains independent nested Go modules

Connector-support modules and many connector modules have their own `go.mod` files. They require the root module and, in some cases, shared connector-support modules; local development uses relative `replace` directives. There is no repository `go.work` coordinating them.

**Consequence:** the module-path migration has an internal dependency order:

1. root module and root imports;
2. connector-support module declarations, source imports, root edges, and every inter-support `require`/`replace` edge;
3. connector modules in frozen, non-overlapping bounded batches;
4. all-module tidy/check gates.

Connector waves must continue to work with `GOWORK=off` and relative replacements, because that is an existing supported validation mode. Batch membership must be fixed before parallel execution so two agents cannot claim the same connector or leave one unowned.

### 3. Brand-prefixed public packages are architectural hubs

The public canonical API and extension SDK are imported throughout core, plugins, testkit, tools, connector support, and independent connectors. The public runtime package is also part of the public surface. The standard distribution has a brand-prefixed command directory and binary identity.

The target package/command mapping is:

| Responsibility | Target |
|---|---|
| Canonical protocol-neutral contracts | `pkg/aipapi` |
| Plugin/extension SDK contracts | `pkg/aipsdk` |
| Public runtime facade | `pkg/aipruntime` |
| Standard distribution command | `cmd/aipstd` / `aipstd` |

**Consequence:** move one public package family at a time. During a wave, local import aliases may temporarily preserve pre-wave local identifiers while consumers are migrated in bounded zones. These aliases are migration scaffolding only; no duplicate public compatibility package is permitted, and all aliases carrying Legacy Token Set spellings must be gone before final convergence.

### 4. Project-specific HTTP names are centralized but broadly consumed

The public SDK centralizes standard inbound header names, while frontend decoders, core config, HTTP wiring, tests, examples, scripts, and documentation consume them. The final defaults must use `X-AIP-*`.

**Consequence:** treat headers as a runtime-contract wave after compile-time package namespaces stabilize. Production constants, config defaults, all protocol frontend tests, examples, and fixtures change together. Issue #429 requests a clean break, so the final implementation must not silently accept the pre-rebrand header namespace as a compatibility default.

### 5. Environment variables and CI references share files but not necessarily ownership

Repository searches show project-prefixed environment variables in test infrastructure, persistence, scripts, quality gates, runtime/reload documentation, and CI-oriented commands. Similar project naming can occur in metrics, tracing/service names, user agents, schema identifiers, cache/database names, fixture keys, generated artifacts, and log fields.

CI workflow files may contain both environment names and unrelated command/module/artifact branding. Splitting those identifier families across migration waves is safe only if ownership is explicit and ordered.

**Consequence:** inventory by semantic category before mutation. Rename only identifiers that are project-owned branding. The runtime environment-name subwave owns only environment-name substitutions, including those references inside `.github/**`; the later CI convergence wave owns the remaining repository/module/command/artifact identities in `.github/**` and must perform a full cross-wave workflow scan. Do not mechanically rewrite coincidental character sequences in unrelated provider names, protocol terms, natural-language words, or third-party identifiers.

### 6. Release and developer tooling hard-code product identity

Release configuration currently embeds the baseline project name, build ID, command path, binary name, and archive identity. Make targets, shell/PowerShell scripts, CI workflows, release checks, agent skills, Kiro steering/templates, and repository rules also contain source-brand references.

**Consequence:** release/tooling changes follow runtime compile-time cutovers. Renaming these first would cause CI and packaging to invoke paths that have not yet moved. Generated release/build/package/container outputs are part of final verification when the repository can produce them; scanning only tracked source is insufficient.

### 7. Documentation and historical project artifacts are part of the requested end state

Issue #429 explicitly includes README files, help text, Markdown, comments, filenames, directories, tests, and all other tracked content. The repository also stores active and archived Kiro artifacts, agent skills, steering documents, reviews, and examples.

**Consequence:** documentation is a late convergence wave, after code names are frozen. Archived material is not exempt from the final tracked-tree scan. Historical technical meaning should be preserved while names and paths are brought to the target identity. Parallel documentation work requires disjoint path ownership: active Kiro/AGENTS material, archived Kiro/review material, and non-Kiro agent automation must not overlap.

### 8. Repository hosting is a separate external cutover

The desired canonical host path is `github.com/aiproxer/aiproxer`. The currently authenticated GitHub context did not establish that the target organization is available to the acting account. That is a late operational prerequisite, not a reason to block compile-time rebranding work.

GitHub's official repository-transfer documentation states that repository contents, issues, pull requests, releases, projects, settings, stars, watchers, webhooks, repository secrets, and deploy keys remain associated with a transferred repository, while packages can require registry-specific handling. Organization-scoped policies and integrations are separate resources and therefore require explicit target-owner verification or recreation rather than an assumption that repository transfer carries them automatically. GitHub also establishes redirects from the former repository location.

**Consequence:** before transfer, classify host configuration into repository-scoped assets expected to remain associated and organization/owner-scoped dependencies that may need rebinding. After transfer, verify both classes explicitly, including rulesets/policies, organization secrets or variables, team/GitHub App access, package/container permissions, and Pages/DNS dependencies where applicable.

Reference: GitHub Docs, **Transferring a repository** and organization ruleset documentation.

### 9. A zero-legacy scan must be reproducible across agents

An out-of-tree pattern source avoids committing retired spellings, but an unspecified scanner lets different agents derive different patterns and produce inconsistent “zero” results.

**Consequence:** the first inventory task freezes a scanner provenance bundle: exact Issue #429 snapshot/revision, a versioned scanner contract, deterministic pattern generation, scanner checksum, pattern-set checksum, artifact-manifest checksum, and canonical invocation. The bundle is handed off through the implementation log or CI artifact and reused unchanged at later gates unless the owner explicitly approves a rebaseline.

### 10. Clean-clone validation must cover the whole module graph

A representative connector subset is insufficient for final proof because every connector/support module has independent module metadata and local replacement edges.

**Consequence:** the final clean clone must run the same platform-appropriate all-module validation used by CI, with the same `GOWORK` behavior, in addition to root QA, standard-distribution build/release smoke, artifact scanning, and the zero-legacy scan.

## Requirements gap analysis

The feature request was clear on target identity and total scope, but brownfield analysis exposed several requirements that must be made explicit to avoid a dangerous implementation.

### Gap A: “everything” needs a precise completion boundary

A literal global interpretation could include immutable Git history, remote issue/PR text, and GitHub-managed redirects, which cannot be rewritten by a source refactor and are not part of the working tree. Conversely, limiting proof to tracked source would miss generated release/build/package artifacts.

**Repair:** define completion over the final **tracked repository paths and textual contents**, live project-owned runtime identifiers, and the applicable generated artifact set identified by the migration inventory. Exclude immutable Git object history, external issue/PR discussion history, and platform-managed redirect behavior.

### Gap B: source-name matching cannot be a naïve substring replacement

The issue includes prefix/suffix abbreviation rules, but raw three-character replacement across arbitrary content can corrupt unrelated words and third-party identifiers.

**Repair:** define the Legacy Token Set semantically: source product/repository names and abbreviations from Issue #429, their case/separator variants, and identifiers demonstrably referring to this project. The migration inventory classifies matches before editing. Unrelated lexical coincidences are not in scope.

### Gap C: the multi-module dependency graph requires explicit sequencing

The feature request did not describe connector modules or local replacement edges.

**Repair:** require root -> connector-support complete edge migration -> frozen connector batches -> all-module validation chronology.

### Gap D: public package renames need intermediate green states

Moving every public package and all consumers in one edit would produce thousands of secondary compiler/test failures, exactly the failure mode the implementation must avoid.

**Repair:** require one package-family wave at a time, bounded consumer batches, focused compilation/tests after each batch, and full root validation at package-wave boundaries.

### Gap E: breaking runtime names must be deliberate, not accidental compatibility

The requested clean break conflicts with the usual migration instinct to dual-read old headers or environment variables indefinitely.

**Repair:** temporary compile-time scaffolding is allowed only inside an implementation wave, but final runtime defaults and accepted project-specific names are target-only unless a non-brand protocol standard independently requires otherwise. No permanent compatibility package, alias namespace, or fallback to Legacy Token Set identifiers may remain.

### Gap F: persistent names may require data-safe migration

A brand string may appear in persisted table names, migration IDs, filesystem paths, cache keys, or stored metadata. Renaming such identifiers without discovery can cause data loss or orphan state.

**Repair:** inventory persistent identifiers before changing them. Where a persisted project-owned branded identifier exists, use a forward migration that preserves data and an explicit rollback/verification path. Do not rename stable semantic identifiers that are not branding.

### Gap G: repository transfer is dependent on external ownership and organization-scoped configuration

The target host operation may not be executable by a coding agent even when code is ready, and not every target-owner policy/integration is a repository-scoped asset.

**Repair:** model host transfer/rename as a late owner/operator checkpoint with explicit permission/name preconditions, repository-vs-organization configuration classification, recreation/rebinding steps for non-transferred dependencies, and post-cutover verification.

### Gap H: untracked scanner inputs can still be non-reproducible

Keeping source spellings out of Git solves the final-tree self-reference problem but not provenance drift.

**Repair:** freeze and checksum the Issue snapshot, scanner implementation/contract, generated pattern set, and artifact manifest before first use; record one canonical invocation and reuse that exact provenance through final convergence.

### Gap I: parallel batches need immutable ownership

“Take the next modules” or broad directory ownership is ambiguous when several coding agents execute in parallel.

**Repair:** freeze non-overlapping connector batch membership before dispatch and assign exact path partitions to agent/Kiro/documentation workers. Every path/module must be owned exactly once within a parallel wave.

### Gap J: final convergence must remove migration shims from the workspace

Allowing a migration helper to remain merely because it is untracked can make local validation differ from a clean clone.

**Repair:** remove migration-only aliases, manifests, helper shims, duplicate package paths, and compatibility bridges from the workspace before final proof. Only external scanner inputs/evidence may remain outside the repository for final verification.

## Design discovery conclusions

### Chosen migration strategy: dependency-ordered strangler of names, not functionality

No new runtime abstraction is warranted. The repository architecture remains intact. Instead, each name surface is migrated behind a checkpoint:

1. establish baseline, deterministic scanner provenance, classified inventory, artifact manifest, and immutable parallel-work partitions;
2. change the root/nested Go module namespace;
3. move `aipapi` and migrate its consumers;
4. move `aipsdk` and migrate its consumers;
5. move `aipruntime`, then `aipstd` and standard distribution identity;
6. change runtime wire/config/operational namespaces, including the environment-name slice of CI workflow files;
7. change developer/release/remaining CI/agent tooling with disjoint path/identifier ownership;
8. rewrite docs, steering, active/archive specs, comments, filenames, and fixtures using explicit non-overlapping partitions;
9. perform GitHub host transfer/rename when prerequisites are satisfied and recreate/rebind target-owner dependencies as needed;
10. remove migration scaffolding and run tracked-tree, generated-artifact, full module, quality, release, and clean-clone gates.

### Why compile-time namespaces precede runtime contracts

Compiler failures are high-signal and localizable. A module/package wave can be proven green before changing HTTP/env/config behavior. If runtime contract names are changed at the same time, failures from incorrect imports become interleaved with fixture/config failures and obscure root causes.

### Why documentation is last

Earlier implementation waves need current baseline documentation to locate source contracts, and code names can still shift during migration. Rewriting documentation after target paths stabilize avoids repeated edits and stale target references. Documentation may be parallelized only across explicitly disjoint paths after the naming freeze.

### Why there is no compatibility layer

The product requirement is an intentional identity break. A compatibility package tree or permanent dual-name parser would preserve exactly the legacy connection the feature aims to remove and would double the public surface. Temporary local aliases are acceptable only as short-lived compiler scaffolding and must be removed in the same package wave or final convergence. No migration-only helper may survive merely by remaining untracked.

## Validation model

Each implementation subtask must name a focused proof command. Major wave boundaries additionally run the strongest practical repository gate for that surface. The canonical validation toolbox includes:

- `go test` for the affected package/consumer set;
- `go test ./...` / repository unit-test target at package-wave boundaries;
- architecture guardrails after public-package moves;
- all-module check/tidy scripts after module-graph changes and connector batches;
- standard distribution build/smoke tests after command/release changes;
- contract tests for headers/config/frontends when runtime names change;
- cross-wave `.github/**` scan after the final CI edits;
- generated artifact creation and scanning for every artifact family enabled by the repository at implementation time;
- full repository test and quality targets at convergence;
- final clean clone from `github.com/aiproxer/aiproxer`, followed by the platform-appropriate all-module check with CI-equivalent `GOWORK` behavior, standard distribution/release smoke, artifact scan, and zero-legacy scan.

The Legacy Token Set scanner must inspect **tracked path names**, **textual tracked file contents**, and the **mandatory generated artifact set**. Its Issue snapshot, scanner contract/implementation, pattern set, and artifact manifest are frozen and checksummed out-of-tree at Task 1.3; later gates reuse those exact inputs so the scanner itself does not reintroduce source-brand spellings or drift between agents.

## Brownfield design-validation verdict

**GO with staged migration.**

The design preserves existing architecture and behavior while isolating naming changes into failure-localizing waves. Review-driven repairs tighten generated-artifact proof, complete connector-support edge migration, make scanner provenance reproducible, freeze connector batch membership, make CI and agent/Kiro ownership explicit, strengthen repository-transfer settings handling, require workspace cleanup, and run the full module graph in the clean clone. No Open Core/Enterprise split, domain redesign, or unrelated package restructuring is required.
