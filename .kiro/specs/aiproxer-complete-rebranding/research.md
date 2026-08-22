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
2. connector-support module declarations/dependencies;
3. connector modules in bounded batches;
4. all-module tidy/check gates.

Connector waves must continue to work with `GOWORK=off` and relative replacements, because that is an existing supported validation mode.

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

### 5. Environment variables and operational identifiers are widespread

Repository searches show project-prefixed environment variables in test infrastructure, persistence, scripts, quality gates, runtime/reload documentation, and CI-oriented commands. Similar project naming can occur in metrics, tracing/service names, user agents, schema identifiers, cache/database names, fixture keys, generated artifacts, and log fields.

**Consequence:** inventory by semantic category before mutation. Rename only identifiers that are project-owned branding. Do not mechanically rewrite coincidental character sequences in unrelated provider names, protocol terms, natural-language words, or third-party identifiers.

### 6. Release and developer tooling hard-code product identity

Release configuration currently embeds the baseline project name, build ID, command path, binary name, and archive identity. Make targets, shell/PowerShell scripts, CI workflows, release checks, agent skills, Kiro steering/templates, and repository rules also contain source-brand references.

**Consequence:** release/tooling changes follow runtime compile-time cutovers. Renaming these first would cause CI and packaging to invoke paths that have not yet moved.

### 7. Documentation and historical project artifacts are part of the requested end state

Issue #429 explicitly includes README files, help text, Markdown, comments, filenames, directories, tests, and all other tracked content. The repository also stores active and archived Kiro artifacts, agent skills, steering documents, reviews, and examples.

**Consequence:** documentation is a late convergence wave, after code names are frozen. Archived material is not exempt from the final tracked-tree scan. Historical technical meaning should be preserved while names and paths are brought to the target identity.

### 8. Repository hosting is a separate external cutover

The desired canonical host path is `github.com/aiproxer/aiproxer`. The currently authenticated GitHub context did not establish that the target organization is available to the acting account. That is a late operational prerequisite, not a reason to block compile-time rebranding work.

GitHub's official repository-transfer documentation states that transfer preserves repository data such as issues, pull requests, stars, and watchers and establishes redirects from the former location. Those platform-managed redirects and immutable Git history are outside the tracked-tree no-legacy condition. The repository must nevertheless update its own canonical links/remotes/configuration to the target identity rather than relying on redirects as a source-level compatibility mechanism.

Reference: GitHub Docs, **Transferring a repository** and **Renaming a repository**.

## Requirements gap analysis

The feature request was clear on target identity and total scope, but brownfield analysis exposed several requirements that must be made explicit to avoid a dangerous implementation.

### Gap A: “everything” needs a precise completion boundary

A literal global interpretation could include immutable Git history, remote issue/PR text, and GitHub-managed redirects, which cannot be rewritten by a source refactor and are not part of the working tree.

**Repair:** define completion over the final **tracked repository paths and textual contents**, generated release metadata produced from those sources, and live project-owned runtime identifiers. Exclude immutable Git object history, external issue/PR discussion history, and platform-managed redirect behavior.

### Gap B: source-name matching cannot be a naïve substring replacement

The issue includes prefix/suffix abbreviation rules, but raw three-character replacement across arbitrary content can corrupt unrelated words and third-party identifiers.

**Repair:** define the Legacy Token Set semantically: source product/repository names and abbreviations from Issue #429, their case/separator variants, and identifiers demonstrably referring to this project. The migration inventory classifies matches before editing. Unrelated lexical coincidences are not in scope.

### Gap C: the multi-module dependency graph requires explicit sequencing

The feature request did not describe connector modules or local replacement edges.

**Repair:** require root -> connector-support -> connector batches -> all-module validation chronology.

### Gap D: public package renames need intermediate green states

Moving every public package and all consumers in one edit would produce thousands of secondary compiler/test failures, exactly the failure mode the implementation must avoid.

**Repair:** require one package-family wave at a time, bounded consumer batches, focused compilation/tests after each batch, and full root validation at package-wave boundaries.

### Gap E: breaking runtime names must be deliberate, not accidental compatibility

The requested clean break conflicts with the usual migration instinct to dual-read old headers or environment variables indefinitely.

**Repair:** temporary compile-time scaffolding is allowed only inside an implementation wave, but final runtime defaults and accepted project-specific names are target-only unless a non-brand protocol standard independently requires otherwise. No permanent compatibility package, alias namespace, or fallback to Legacy Token Set identifiers may remain.

### Gap F: persistent names may require data-safe migration

A brand string may appear in persisted table names, migration IDs, filesystem paths, cache keys, or stored metadata. Renaming such identifiers without discovery can cause data loss or orphan state.

**Repair:** inventory persistent identifiers before changing them. Where a persisted project-owned branded identifier exists, use a forward migration that preserves data and an explicit rollback/verification path. Do not rename stable semantic identifiers that are not branding.

### Gap G: repository transfer is dependent on external ownership/permissions

The target host operation may not be executable by a coding agent even when code is ready.

**Repair:** model host transfer/rename as a late owner/operator checkpoint with explicit preconditions and post-cutover verification. Earlier implementation waves remain independently executable.

## Design discovery conclusions

### Chosen migration strategy: dependency-ordered strangler of names, not functionality

No new runtime abstraction is warranted. The repository architecture remains intact. Instead, each name surface is migrated behind a checkpoint:

1. establish baseline and classified inventory;
2. change the root/nested Go module namespace;
3. move `aipapi` and migrate its consumers;
4. move `aipsdk` and migrate its consumers;
5. move `aipruntime`, then `aipstd` and standard distribution identity;
6. change runtime wire/config/operational namespaces;
7. change developer/release/CI/agent tooling;
8. rewrite docs, steering, active/archive specs, comments, filenames, and fixtures;
9. perform GitHub host transfer/rename when prerequisites are satisfied;
10. remove migration scaffolding and run zero-legacy plus full quality/release gates.

### Why compile-time namespaces precede runtime contracts

Compiler failures are high-signal and localizable. A module/package wave can be proven green before changing HTTP/env/config behavior. If runtime contract names are changed at the same time, failures from incorrect imports become interleaved with fixture/config failures and obscure root causes.

### Why documentation is last

Earlier implementation waves need current baseline documentation to locate source contracts, and code names can still shift during migration. Rewriting documentation after target paths stabilize avoids repeated edits and stale target references. Documentation may be parallelized only across disjoint directories after the naming freeze.

### Why there is no compatibility layer

The product requirement is an intentional identity break. A compatibility package tree or permanent dual-name parser would preserve exactly the legacy connection the feature aims to remove and would double the public surface. Temporary local aliases are acceptable only as short-lived compiler scaffolding and must be removed in the same package wave or final convergence.

## Validation model

Each implementation subtask must name a focused proof command. Major wave boundaries additionally run the strongest practical repository gate for that surface. The canonical validation toolbox includes:

- `go test` for the affected package/consumer set;
- `go test ./...` / repository unit-test target at package-wave boundaries;
- architecture guardrails after public-package moves;
- all-module check/tidy scripts after module-graph changes and connector batches;
- standard distribution build/smoke tests after command/release changes;
- contract tests for headers/config/frontends when runtime names change;
- full repository test and quality targets at convergence;
- final clean clone/build from `github.com/aiproxer/aiproxer` after host cutover.

The final Legacy Token Set scan must inspect both **tracked path names** and **textual tracked file contents**. The pattern inventory used for this scan is generated or supplied out-of-tree from Issue #429 so the scanner itself does not reintroduce source-brand spellings into the repository.

## Brownfield design-validation verdict

**GO with staged migration.**

The design preserves existing architecture and behavior while isolating naming changes into failure-localizing waves. The principal repair from validation is the explicit root/support/connector module order, followed by one-at-a-time public-package migration and late runtime/release/documentation cutovers. Repository hosting is correctly separated as a late operator checkpoint. No Open Core/Enterprise split, domain redesign, or unrelated package restructuring is required.
