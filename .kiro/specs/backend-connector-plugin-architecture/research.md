# Current-State Review, Requirements Gap Analysis, Architecture Research, and Design Validation

Generated: 2026-07-19T01:03:15+02:00
Updated after review hardening: 2026-07-19T12:40:34+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `22d6009713ce01fe7c4e1d65b92b8655d27af67d`
- Feature: `backend-connector-plugin-architecture`
- Workflow completed: initialization, requirements generation, mandatory brownfield gap analysis, requirements remediation, design generation, design validation, design correction, task generation, and reviewer hardening
- Change scope: Kiro specification artifacts only
- Implementation readiness: design validated; requirements, design, and tasks remain unapproved in `spec.json`

## Executive Assessment

Go-LIP already has a sound canonical core boundary: `internal/core` consumes `execbackend.Backend`, provider wire mappings live in backend adapters, and routing/failover remain core-owned. The modularity defect is one layer outward. The standard distribution statically imports every concrete backend through `internal/standardplugins/standard_table.go`; the mandatory bundle names optional connectors; the root module owns all bundled dependency graphs; and the generic backend factory exposes internal routing/accounting plus Codex-specific dependencies. The current system is therefore registry-driven static composition, not an independently installable backend plugin system.

The selected direction is a hybrid distribution:

1. OpenAI Responses, OpenAI legacy, Anthropic, Gemini, and Bedrock remain statically linked at the composition root—not in core.
2. Every provider/local-agent backend outside that set becomes an executable process plugin.
3. Closed, versioned manifests are discovered without executing binaries.
4. A versioned gRPC ABI and public authoring kit isolate connectors from `internal/...`.
5. A host anti-corruption layer adapts plugin RPC into the existing internal backend port.
6. Plugin processes start lazily only for enabled backend instances and follow an explicitly declared shared-artifact or per-instance process model.
7. Artifact digest verification is atomically bound to the exact executable bytes launched rather than a rechecked pathname.
8. Configuration and credentials cross only an approved confidential, peer-authenticated local IPC channel.
9. First-party external connectors use independent Go modules or repositories, so their SDKs and language runtimes never enter the root dependency graph.

HashiCorp `go-plugin` over gRPC remains the preferred process substrate, not the public domain contract. Its exact suitability is conditional on Task 1 proving that it can satisfy Go-LIP's closed manifest, exact-byte launch, secure local IPC, lifecycle, and cross-platform requirements. Where it cannot, the same public ABI is hosted by a narrow project-owned process implementation. Go-LIP owns its manifest, backend service, DTOs, security policy, canonical adapter, and conformance suite either way.

## Reviewed Repository Assets

### Steering and workflow

- `AGENTS.md`, `.kiro/AGENTS.md`
- `.kiro/steering/{product,structure,tech,api-standards,routing-and-orchestration,testing}.md`
- `.kiro/rules/{ears-format,gap-analysis,design-principles,design-review}.md`
- `.kiro/settings/templates/specs/{init.json,requirements.md,design.md,tasks.md}`
- `.cursor/skills/golang-hexagonal-architecture/SKILL.md`
- `docs/adr/0001-registry-driven-composition.md`
- `EchoesVault/pages/plugin-system.md`
- `docs/backend-adapter-boundaries.md`

### Registration, config, and runtime

- `internal/pluginreg/reg.go`
- `internal/standardplugins/standard_table.go`
- `pkg/lipsdk/standard_bundle.go`
- `internal/infra/runtimebundle/{bootstrap_plan,build_model}.go`
- `internal/core/config/model.go`
- `internal/core/execbackend/backend.go`

### Connectors and enforcement

- `internal/plugins/backends/*`
- `internal/plugins/backends/acp/*`
- `internal/plugins/backends/{cursorcliacp,geminicliacp,agycliacp}/*`
- `internal/core/codexcatalog/*`
- `internal/archtest/guardrails_test.go`
- `testdata/architecture/hexagonal_migration_baseline.json`
- active `.kiro/specs/cursor-sdk-backend/*`
- archived connector and brownfield specs

## Existing Strengths to Preserve

1. `pkg/lipapi` is the canonical middle; no pairwise translators are needed.
2. Core owns routing, retry eligibility, output commitment, continuity, secure sessions, and accounting policy.
3. `pluginreg.Registry` is explicit, value-owned, and injected per composition root.
4. `PluginConfig.Config` already preserves connector-private opaque YAML.
5. Model inventory retains backend instance and kind provenance.
6. Backend security profiles already express credential mode and access scope.
7. Architecture tests provide an existing enforcement mechanism.
8. ACP already has reusable JSON-RPC, session, cancellation, subprocess, and mapping behavior.
9. Runtimebundle already owns general closers, although backend lifecycle is incomplete.
10. Connector conformance, stream lifecycle, race, and leak tests are established project practices.

## Current Coupling

1. `standard_table.go` imports and enumerates all concrete backends.
2. `StandardDistributionRequirements` treats many optional kinds as mandatory.
3. `BackendFactory` returns internal `execbackend.Backend`; external modules cannot implement it.
4. `BackendFactoryDeps` includes Codex catalog and OpenCode vendor resolution.
5. `execbackend.Backend` appropriately uses internal candidate/accounting types, making it unsuitable as an external ABI.
6. `internal/core/codexcatalog` exists in core only because two optional connectors share it.
7. Every in-tree connector shares the root `go.mod`; a Node-backed Cursor SDK implementation would turn Node packaging into a root concern.
8. No manifest, compatibility, artifact-trust, discovery, secure local IPC, or plugin-process lifecycle exists.
9. Backend construction does not immediately return generic resource ownership.
10. Current steering and ADR 0001 intentionally describe static-only plugin composition.

## Mandatory Brownfield Requirements Gap Analysis

| ID | Severity | Finding | Classification | Required disposition |
|---|---:|---|---|---|
| G-01 | P0 | Fixed standard table imports every backend. | Missing capability | Split built-ins from discovered external factories and delete optional fixed registrations. |
| G-02 | P0 | Factory seam exposes `internal/...`. | Boundary violation | Add a public versioned ABI and one host adapter. |
| G-03 | P0 | Generic factory dependencies contain Codex/OpenCode concerns. | Boundary violation | Move them to owning connector modules. |
| G-04 | P0 | Optional backend IDs are mandatory bundle requirements. | Constraint | Keep only built-ins mandatory; validate configured external kinds after discovery. |
| G-05 | P0 | No manifest discovery or dynamic factory registration. | Missing capability | Add trusted discovery and generic export registration. |
| G-06 | P0 | Root module owns all connector dependency graphs. | Missing capability | Use independent connector modules/repositories and prohibit root imports/requires/replaces. |
| G-07 | P0 | Cursor SDK would introduce Node under the root connector tree. | Imminent risk | Rebase the active Cursor spec to an external connector artifact. |
| G-08 | P0 | No generic process/instance lifecycle result. | Missing capability | Add idempotent close, rollback, kill, and reap ownership. |
| G-09 | P0 | No closed manifest, atomic executable-identity binding, child-environment policy, or confidential peer-authenticated local configure channel. | Security gap | Reject unknown v1 fields, bind digest verification to exact launched bytes, constrain the environment, require approved OS-specific secure IPC, and prohibit runtime installation. |
| G-10 | P1 | Shared `*http.Client` cannot cross a process boundary. | Constraint | Pass a stable host runtime-policy projection; plugin owns SDK transport. |
| G-11 | P0 | Streaming/cancellation must cross RPC without weakening commitment rules. | Missing capability | Bounded incremental stream, cancellation outcome, terminal, and classified error contracts. |
| G-12 | P1 | Caps, inventory, counting, and billing span the internal seam. | Partial | Cover them as negotiated optional plugin operations. |
| G-13 | P1 | ACP shared code imports internal backend/routing packages. | Partial | Extract dependency-light ACP support; concrete products remain external. |
| G-14 | P1 | Codex catalog is core-owned but optional-connector-specific. | Boundary violation | Move it to the Codex plugin/support module. |
| G-15 | P1 | Static and external owners cannot share a kind. | Migration constraint | Use parity gates and atomic per-kind cutover. |
| G-16 | P1 | CI assumes one root module and a fixed bundle. | Missing capability | Add `GOWORK=off` root proof and dynamically discovered plugin-module matrix. |
| G-17 | P1 | Inspect lacks discovered/process states. | Partial | Add bounded discovered/configured/active/incompatible/failed diagnostics. |
| G-18 | P1 | Steering rejects runtime binary plugins as primary extensions. | Constraint | Add a superseding backend-plugin ADR and steering updates. |
| G-19 | P1 | Discovery can become arbitrary code execution. | Security risk | Discovery is non-executing; closed schema, trust, digest, exact-byte launch, and secure-channel policy precede configuration. |
| G-20 | P1 | One hundred connectors could create a startup process storm. | Scale risk | O(manifests) discovery and lazy process activation. |

## Requirement-to-Asset Map

| Requirement | Existing base | Gap |
|---|---|---|
| 1 Dependency boundaries | Core package zones and archtest | Core is clean; standard binary/root module are not. |
| 2 Public ABI | `lipapi`, `lipsdk`, internal backend port | No out-of-module backend service contract. |
| 3 Discovery | Explicit registry and opaque config | No manifest source or generic host factory. |
| 4 Lazy activation | Enabled-row construction | No process layer or declared shared/per-instance process model; all code is linked. |
| 5 Host/lifecycle | backend port, runtimebundle closers | DTO adaptation and resource ownership missing. |
| 6 Streaming | managed streams and commitment rules | Bounded RPC mapping absent. |
| 7 Security | access profiles and config validation | Closed manifests, exact-byte launch binding, child process policy, and secure local IPC absent. |
| 8 Compatibility | `kind`, `id`, `enabled`, opaque config | Strong base; add generic discovery config. |
| 9 Auxiliary operations | inventory, caps, counter, finalizer | Public negotiated methods absent. |
| 10 Migration | concrete packages and shared helpers | Package/module direction is wrong. |
| 11 Release topology | Go modules and CI | No independent connector artifacts. |
| 12 Evidence | conformance, archtest, race/leak gates | Extend to process, discovery, absence, adversarial local clients, substitution, and scale. |

## Requirements Remediation Performed

The requirements were corrected after gap analysis and review to:

- distinguish core-owned from standard-distribution built-in connectors;
- classify OpenRouter as external while preserving dependency-free generic protocol aliases;
- require the ABI to cover caps, inventory, counting, billing, replay support, and max-output enforcement;
- forbid use of the current internal factory as the external ABI;
- add process/instance lifecycle and rollback ownership;
- declare shared-artifact and per-instance process models without contradiction;
- add a host runtime-policy projection instead of a cross-process HTTP client;
- make discovery non-executing and activation lazy;
- close manifest v1 by rejecting every unknown field and reserve explicit versioned extension mechanisms;
- require digest verification to remain atomically bound to the exact executable bytes launched;
- require confidential expected-peer-authenticated local IPC before configuration or credential delivery;
- keep child environment allowlists non-secret and prohibit runtime installation;
- remove Codex/OpenCode-specific helpers from generic core/factory dependencies;
- make ACP support reusable without keeping concrete ACP products in root composition;
- preserve existing factory kinds and YAML rows;
- require independent modules and `GOWORK=off` root isolation;
- require revalidation of `cursor-sdk-backend`;
- define optionality across compile, install, startup, process, and runtime presence;
- add a one-hundred-manifest scale test plus adversarial manifest, substitution, and unauthorized-peer tests.

## Architecture Options

### A. Build tags or generated blank imports

Retains direct calls, but requires rebuilding Go-LIP whenever the connector set changes, keeps toolchain/dependency coupling, and merely moves the fixed list to build time. Rejected as the primary plugin system.

### B. Go native `plugin` shared objects

Rejected. Official Go documentation warns about limited operating-system support, poor race-detector support, inability to unload, and strict toolchain/build-flag/common-dependency identity. It does not support the project’s Windows requirement or isolate crashes/secrets.

Source: https://pkg.go.dev/plugin

### C. Project-owned executable RPC/process framework

Architecturally viable and retained as fallback, but it would require Go-LIP to implement and secure process negotiation, exact-byte launch, local peer authentication, lifecycle, cleanup, compatibility, encryption, logging, and testing infrastructure.

### D. HashiCorp `go-plugin` gRPC runtime plus Go-LIP ABI and manifests

Selected provisionally. It provides mature local process isolation, protocol versions, gRPC streaming, checksum/TLS hooks, environment controls, and lifecycle APIs. Go-LIP retains control of its domain contract, security policy, manifests, and canonical adaptation. Adoption remains conditional on the Task 1 spike proving every mandatory launch and local-channel control on each supported platform; otherwise Option C is selected without weakening the requirements.

Primary sources:

- https://github.com/hashicorp/go-plugin
- https://pkg.go.dev/github.com/hashicorp/go-plugin
- https://github.com/hashicorp/go-plugin/releases/tag/v1.8.0

### Module topology

Initial first-party migration uses Go modules in repository subdirectories, with independent module paths/tags and a generated/developer workspace. Root CI and release run with `GOWORK=off`. The ABI also permits a connector to move to a separate repository later.

Sources:

- https://go.dev/ref/mod
- https://go.dev/doc/tutorial/workspaces

## Selected Direction

1. Retain `execbackend.Backend` as the consumer-owned internal port.
2. Add public `backendplugin/v1` contracts and conformance with no internal imports.
3. Add an infrastructure/composition-owned plugin host.
4. Use `go-plugin` v1.8.x gRPC mode only if implementation-time license, API, exact-launch, secure-IPC, and lifecycle revalidation passes; otherwise use a narrow project-owned host.
5. Discover closed, trusted manifests without executing binaries.
6. Register exported backend kinds dynamically.
7. Start processes lazily according to an explicitly declared shared-artifact or per-instance process model.
8. Atomically bind the accepted digest to the exact executable bytes launched using a verified handle or private immutable staging strategy.
9. Require approved confidential peer-authenticated local IPC before sending configuration or credentials.
10. Adapt plugin DTOs/streams in one anti-corruption layer.
11. Preserve core ownership of routing, failover, commitment, continuity, and accounting policy.
12. Keep only five essential backend families statically composed.
13. Move OpenRouter and every current non-essential connector to external modules.
14. Extract dependency-light ACP support and move concrete ACP products out.
15. Move Codex catalog and OpenCode vendor resolution out of generic core/factory dependencies.
16. Prove the path with external local-stub, then migrate by connector family.
17. Preserve current `kind` values and opaque YAML.
18. Revalidate active connector specs before implementation.

## Feasibility and Risk

- Feasibility: high. The canonical port, explicit registry, opaque config, inventory, and composition root are suitable insertion points.
- Effort: XL. This is a platform boundary plus migration of heterogeneous adapters.
- Initial risk: high due executable trust, secure local IPC, stream correctness, lifecycle, compatibility, and broad migration.
- Residual risk: medium only after closed schemas, atomic exact-byte launch, peer-authenticated confidential channels, versioned contracts, process isolation, lazy launch, conformance, and phased cutover.
- Primary complexity: security-sensitive lifecycle and compatibility—not provider mapping algorithms.

## Implementation Research Still Required

Task 1 must revalidate:

1. exact `go-plugin` v1.8.x APIs for versioned plugins, streaming, checksum, TLS/AutoMTLS, Unix sockets, child environment, runners, and Windows cleanup;
2. whether the selected substrate supports Unix peer credentials, macOS `getpeereid` or equivalent, Windows named-pipe DACL and client-token/process verification, or process-generation-bound ephemeral mutual TLS;
3. OS-specific verified executable handle or identity-preserving launch primitives and private digest-addressed staging semantics;
4. unauthorized, wrong-process, and stale-generation local-client rejection;
5. MPL-2.0 obligations;
6. gRPC message/window bounds for event streams;
7. protobuf presence needed for `lipapi` null/omitted semantics;
8. secret-bearing opaque config transport after peer and protocol authentication;
9. cross-platform executable replacement, staged-copy cleanup, upgrade, and rollback;
10. exact current backend seam coverage;
11. shared-artifact versus per-instance process and multi-kind sharing constraints;
12. nested-module tag automation;
13. installer-owned plugin paths per operating system.

A failed probe may replace `go-plugin` with a narrow project-owned gRPC host or mark an unsupported plugin/platform pair, but it does not authorize weaker local transport, pathname-only digest checks, Go native plugins, internal-type leakage, or static optional imports.

## Design Validation Stage

### Critical Issue 1: Initial ABI mirrored internal types

**Concern:** The first design resembled `execbackend.Backend`, including internal candidate/accounting concepts.

**Correction:** The final design uses SDK-owned versioned DTOs and optional service capabilities. Only the host adapter knows both public protocol and internal port. Generic metadata excludes Codex/OpenCode/ACP-product collaborators.

**Traceability:** 2.1-2.8, 5.1-5.2, 9.1-9.8.

### Critical Issue 2: Discovery trust was incomplete

**Concern:** Directory scanning plus handshake cookies did not establish artifact trust, path containment, minimal environment, secure peer binding, or secret transport.

**Correction:** The final design restricts discovery to configured/installer-owned directories, closes manifest v1, binds SHA-256 verification atomically to the exact executable bytes launched, avoids CWD/PATH search, launches without a shell, constrains the environment, requires an approved confidential expected-peer-authenticated local channel, delivers secrets only after peer and protocol negotiation, bounds logs, and prohibits runtime installation. Cookies are explicitly not security controls.

**Traceability:** 3.1-3.8, 4.1-4.8, 7.1-7.10, 12.2.

### Critical Issue 3: Migration compatibility was underspecified

**Concern:** Removing static registration could break existing YAML, cause kind collisions, omit full-bundle packaging, and let Cursor SDK be implemented in the root tree.

**Correction:** The design preserves factory kinds and opaque YAML, uses atomic per-kind cutover after parity gates, installs optional artifacts in a curated full bundle, keeps minimal installations valid, and blocks downstream connector implementation until spec revalidation.

**Traceability:** 8.1-8.8, 10.1-10.9, 11.1-11.10, 12.5-12.9.

## Reviewer Hardening Cross-Check

Four unresolved CodeRabbit findings were independently cross-checked against the requirements and selected process architecture. All four were accepted as technically correct and worthwhile:

1. **Unknown manifest fields**: valid. A security-sensitive installation manifest cannot silently accept unrecognized metadata. Manifest v1 is now closed, and future growth requires an explicit schema version or standardized versioned extension block.
2. **Digest/launch TOCTOU**: valid. Rehashing a pathname immediately before process creation does not guarantee the launched bytes are the verified bytes. The design now requires a verified-handle or private immutable staging strategy and fails closed where no binding can be proved.
3. **Local configure channel**: valid with refinement. Confidentiality and peer authentication are mandatory, but TLS is not the only acceptable mechanism. Approved profiles use Unix-domain-socket peer credentials, Windows named-pipe ACL/token verification, or ephemeral mutual TLS for loopback fallback. Cookie-only and plaintext channels are prohibited.
4. **Process cardinality contradiction**: valid. The requirements now distinguish explicitly declared shared-artifact processes from per-instance processes; neither is implied by a universal at-most-one rule.

The hardening changes were propagated through requirements, design, implementation tasks, cross-platform tests, threat modeling, downstream Cursor SDK revalidation, and final evidence gates.

## Validation Checklist

- Complete requirements/design/task traceability.
- Core imports neither connectors nor plugin runtime.
- Five essential families only in the built-in backend bundle.
- OpenRouter external; generic compatible codecs remain provider-neutral.
- Public versioned ABI with no internal/provider types.
- Automated non-executing discovery with no fixed optional list.
- Closed manifest v1 with explicit versioned evolution only.
- Lazy configuration-driven activation honoring shared-artifact or per-instance process declarations.
- Exact executable bytes are atomically bound to the accepted digest.
- Approved confidential peer-authenticated local IPC precedes configuration and credential delivery.
- Incremental bounded ordered streaming and one terminal.
- No plugin-owned retry/failover or hidden replay after output.
- Composition-owned close, rollback, invalidation, and later-operation restart.
- Trusted paths, minimal non-secret environment, no runtime install.
- Complete caps/inventory/accounting method coverage.
- All current non-essential connectors and ACP/Codex helpers included in migration.
- Root module isolation and independent connector releases.
- Cursor SDK spec explicitly revalidated.
- Fake executable, conformance, unauthorized-peer, substitution, race/leak, fuzz, absence, scale, and cross-platform gates.
- ADR/steering and operator/author documentation included.

## Final Validation Verdict

**PASS after reviewer hardening.**

The design is suitable for task generation. It adds a Go-canonical process-isolated extension boundary without moving provider policy into core, covers the full backend seam, treats discovery, exact executable identity, and local IPC as security boundaries, preserves existing configuration identities, and defines a staged migration that proves dependency isolation before deleting the fixed optional connector table.
