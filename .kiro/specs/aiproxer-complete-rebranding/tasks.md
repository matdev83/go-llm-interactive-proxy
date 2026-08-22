# Implementation Plan

## Execution Rules

This is a **sequential refactor on the critical path**. Do not merge major tasks or skip dependency gates to “save time.” The purpose of this plan is to prevent a repository-wide rename from producing an unbounded red tree.

- A task may begin only when every `_Depends:_` checkpoint is green, except for documented pre-existing failures captured in Task 1.
- Each task owns only its listed naming surface plus tests/fixtures/callers required to keep that surface coherent.
- If a task produces failures outside its expected surface, repair or reduce/revert that task before proceeding.
- Temporary Go import aliases may be used exactly as described for a package wave; duplicate public compatibility packages are forbidden.
- Do not add permanent dual-read/dual-emit support for retired project-owned headers, environment names, config identifiers, or other branding aliases.
- The Legacy Token Set/pattern inventory remains **untracked/out-of-tree** because committing retired spellings would violate the final tree requirement.
- `(P)` means tasks can be delegated in parallel **only after their common dependency is green** and only if they do not touch shared files. Reconcile them through the named merge gate before continuing.

## Tasks

- [ ] 1. Establish a controlled migration baseline

- [ ] 1.1 Capture root repository baseline
  - Run the current root quality, unit-test, and architecture guardrail commands before any rename.
  - Record pre-existing failures separately from rebranding regressions; do not normalize a red baseline into acceptance.
  - Confirm the standard distribution currently builds in the baseline tree.
  - Observable completion: a short implementation log identifies the exact baseline commit and green/failing commands before mutations start.
  - _Requirements: 8.1, 10.1_
  - _Boundary: tests / repository quality_
  - _Depends: none_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/...`_

- [ ] 1.2 Capture nested-module baseline
  - Run the repository's existing all-module checks with the same `GOWORK` behavior used by CI/scripts.
  - Identify connector-support modules separately from connector modules and record their dependency edges.
  - Observable completion: every nested module is classified as clean or as a documented pre-existing exception.
  - _Requirements: 3.4, 8.1_
  - _Boundary: connector modules / tests_
  - _Depends: 1.1_
  - _Validation: platform-appropriate `scripts/check-all-modules.sh` or `scripts/check-all-modules.ps1`_

- [ ] 1.3 Build the untracked Legacy Token Set inventory
  - Generate the source-name patterns from Issue #429 outside the tracked tree.
  - Scan both `git ls-files` paths and textual contents.
  - Classify every meaningful occurrence into: module/import, public package/Go identifier, runtime wire/config, persistence/observability, tooling/CI/release, docs/agent/Kiro/historical, or false-positive/third-party.
  - Flag all suspected durable identifiers for explicit review before editing.
  - Observable completion: every meaningful match has an owner wave and false positives are justified; no pattern manifest is committed.
  - _Requirements: 2.1, 2.2, 2.4, 6.3, 8.2_
  - _Boundary: repository-wide analysis_
  - _Depends: 1.2_
  - _Validation: external inventory reports complete coverage of tracked paths and textual files at the baseline commit_

- [ ] 1.4 Freeze target naming and wave ownership
  - Use the target matrix from `design.md`: `aiproxer`, `github.com/aiproxer/aiproxer`, `aiproxer.com`, `aip`, `pkg/aipapi`, `pkg/aipsdk`, `pkg/aipruntime`, `aipstd`, `X-AIP-*`, `AIP_*`, and `aip_*`.
  - Resolve any discovered project-owned identifier to one target convention before its wave begins; do not invent competing spellings later.
  - Confirm Open Core/Enterprise separation remains out of scope.
  - Observable completion: implementation agents share one target mapping and every inventory category maps to a later task.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 8.7_
  - _Boundary: repository-wide migration control_
  - _Depends: 1.3_
  - _Validation: manual cross-check against Issue #429 and `design.md` target matrix_

- [ ] 2. Migrate the Go module graph without moving public packages

- [ ] 2.1 Cut over the root module namespace as one exact mechanical change
  - Change the root `go.mod` module declaration to `github.com/aiproxer/aiproxer`.
  - Rewrite only root-module import-prefix occurrences belonging to this repository; exclude nested module declarations/requirements/replacements reserved for later tasks.
  - Do not rename `pkg` or `cmd` directories in this task.
  - Use compiler/goimports formatting to catch missed imports rather than broad string substitution over unrelated text.
  - Observable completion: the root module has one target import root and compiles/tests without resolving a source-brand copy of itself.
  - _Requirements: 3.1, 8.3, 8.4, 10.1_
  - _Boundary: root Go module / imports_
  - _Depends: 1.4_
  - _Validation: `go mod tidy && make quality-checks && make test-unit && go test ./internal/archtest/...`_

- [ ] 2.2 Migrate connector-support module namespaces
  - Update every connector-support `module` declaration to the `github.com/aiproxer/aiproxer/...` namespace.
  - Update project-owned root/support `require` and `replace` targets while preserving the existing relative local paths.
  - Update support-module Go imports to the target root.
  - Process one support module at a time and validate it before the next.
  - Observable completion: each support module tidies/tests independently with local replacements and no Legacy Token Set module metadata.
  - _Requirements: 3.2, 3.4, 8.4_
  - _Boundary: connector-support modules_
  - _Depends: 2.1_
  - _Validation: per support module `GOWORK=off go mod tidy && GOWORK=off go test ./...`_

- [ ] 2.3 Migrate connector module batch A (P)
  - Select at most 4–6 connector modules from the inventory with no shared-file edits.
  - Update module declaration, project-owned requirements/replacements, and Go imports to the target namespace.
  - Preserve relative replacement topology.
  - Observable completion: every module in batch A independently tidies/tests under `GOWORK=off`.
  - _Requirements: 3.3, 3.4, 8.4_
  - _Boundary: connector modules_
  - _Depends: 2.2_
  - _Validation: per module `GOWORK=off go mod tidy && GOWORK=off go test ./...`_

- [ ] 2.4 Migrate connector module batch B (P)
  - Select the next at most 4–6 independent connector modules.
  - Apply the same exact module/require/replace/import mapping as batch A.
  - Observable completion: every module in batch B independently tidies/tests under `GOWORK=off`.
  - _Requirements: 3.3, 3.4, 8.4_
  - _Boundary: connector modules_
  - _Depends: 2.2_
  - _Validation: per module `GOWORK=off go mod tidy && GOWORK=off go test ./...`_

- [ ] 2.5 Migrate remaining connector module batches (P)
  - Continue in 4–6-module maximum batches until all connector modules use the target namespace.
  - Keep shared scripts/checkers out of these parallel tasks; they are updated in the later tooling wave.
  - Reduce batch size for connectors with complex local dependencies.
  - Observable completion: every remaining connector module independently tidies/tests under `GOWORK=off`.
  - _Requirements: 3.3, 3.4, 8.4_
  - _Boundary: connector modules_
  - _Depends: 2.2_
  - _Validation: per module `GOWORK=off go mod tidy && GOWORK=off go test ./...`_

- [ ] 2.6 Close the module-graph wave
  - Reconcile connector batches and rerun the repository all-module scripts.
  - Scan all tracked `go.mod`/`go.sum` and Go import strings using the external inventory for module-namespace leftovers.
  - Do not begin public package moves until this checkpoint is green.
  - Observable completion: root, support, and every connector module resolve one target project namespace.
  - _Requirements: 3.5, 8.5, 10.3_
  - _Boundary: complete Go module graph_
  - _Depends: 2.3, 2.4, 2.5_
  - _Validation: `make test-unit` plus platform-appropriate all-module check script_

- [ ] 3. Migrate the canonical public API package to `pkg/aipapi`

- [ ] 3.1 Move the package and establish a compiling target-path bridge inside imports
  - Move the canonical API directory to `pkg/aipapi` using a history-preserving filesystem move.
  - Change package declarations/tests to the target package name where needed.
  - Rewrite consumer import paths to `github.com/aiproxer/aiproxer/pkg/aipapi`.
  - Where necessary, temporarily alias the target import to the pre-wave local selector name so path movement does not require simultaneous symbol-selector edits across the entire repository.
  - Do not create a second public compatibility directory.
  - Observable completion: the root tree compiles with `pkg/aipapi` as the only canonical API package path.
  - _Requirements: 4.1, 4.5, 4.6, 8.4_
  - _Boundary: SDK/public contract_
  - _Depends: 2.6_
  - _Validation: `go test ./pkg/aipapi/... && go test ./internal/core/...`_

- [ ] 3.2 Normalize `aipapi` consumers in public/runtime/core zones
  - Replace temporary pre-wave local selectors with `aipapi` in public packages and `internal/core` consumers.
  - Update compile-time assertions, comments, examples, generated-source inputs, and tests located in those same code zones when they encode the package name.
  - Remove import aliases from completed files.
  - Observable completion: public/runtime/core zones refer to the package locally as `aipapi` and pass focused tests.
  - _Requirements: 4.1, 4.5, 10.1_
  - _Boundary: public packages / core_
  - _Depends: 3.1_
  - _Validation: `go test ./pkg/... ./internal/core/...`_

- [ ] 3.3 Normalize `aipapi` consumers in plugins/composition/infrastructure/testkit
  - Migrate package selectors and relevant same-zone test fixtures in frontend/backend/feature plugins, composition, standard registry, infrastructure, and testkit.
  - Keep functional code unchanged except package naming.
  - Remove aliases from completed files.
  - Observable completion: these zones compile/test with target package naming only.
  - _Requirements: 4.1, 4.5, 10.1_
  - _Boundary: plugins / composition / infrastructure / tests_
  - _Depends: 3.2_
  - _Validation: `go test ./internal/plugins/... ./internal/infra/... ./internal/testkit/... ./internal/standardplugins/...`_

- [ ] 3.4 Normalize `aipapi` consumers in connector-support/connectors
  - Update the local selector/package name within independent modules now that their module namespaces are already target-stable.
  - Validate support modules first, then connector batches; do not combine this work with SDK migration.
  - Observable completion: all independent modules use the `aipapi` target path and local package name.
  - _Requirements: 4.1, 4.5, 8.4_
  - _Boundary: connector modules_
  - _Depends: 3.3_
  - _Validation: platform-appropriate all-module check script_

- [ ] 3.5 Close the `aipapi` wave
  - Remove any remaining temporary aliases for the migrated canonical API package.
  - Confirm no duplicate source package path exists.
  - Run public-contract tests, architecture guardrails, root tests, and all-module checks before starting SDK migration.
  - Observable completion: `pkg/aipapi` is the sole canonical API package and all consumers are green.
  - _Requirements: 4.1, 4.5, 4.6, 8.5, 10.3_
  - _Boundary: SDK/public contract / architecture tests_
  - _Depends: 3.4_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/...` plus all-module check script_

- [ ] 4. Migrate the extension SDK to `pkg/aipsdk`

- [ ] 4.1 Move the SDK package tree and target all import paths
  - Move the full SDK directory/subpackage tree to `pkg/aipsdk` without changing subpackage responsibilities.
  - Change package declarations/tests where the package name itself is branded.
  - Rewrite imports to the target path and use temporary local aliases only where needed to preserve a green intermediate tree.
  - Do not create a compatibility copy of the previous SDK tree.
  - Observable completion: root compilation resolves the SDK only from `pkg/aipsdk`.
  - _Requirements: 4.2, 4.5, 4.6, 8.4_
  - _Boundary: SDK/public contract_
  - _Depends: 3.5_
  - _Validation: `go test ./pkg/aipsdk/... && go test ./internal/core/...`_

- [ ] 4.2 Normalize SDK consumers in core/runtime/composition/registry
  - Convert temporary local selectors to `aipsdk` in core, runtime composition, registry, standard bundle, and other shared assembly zones.
  - Update compile assertions/tests in the same zones.
  - Remove aliases as each zone becomes green.
  - Observable completion: shared core/composition layers use the target SDK name without legacy selectors.
  - _Requirements: 4.2, 4.5, 10.1_
  - _Boundary: core / composition / registry_
  - _Depends: 4.1_
  - _Validation: `go test ./internal/core/... ./internal/infra/runtimebundle/... ./internal/pluginreg/... ./internal/standardplugins/...`_

- [ ] 4.3 Normalize SDK consumers in frontend/backend/feature plugins
  - Migrate plugin imports/selectors without changing plugin ownership or runtime behavior.
  - Update package-local tests and interface assertions together with each plugin group.
  - Observable completion: all in-process plugins use `pkg/aipsdk` and focused tests pass.
  - _Requirements: 4.2, 4.5, 10.1_
  - _Boundary: frontend/backend/feature plugins_
  - _Depends: 4.2_
  - _Validation: `go test ./internal/plugins/...`_

- [ ] 4.4 Normalize SDK consumers in testkit/tools/connector modules
  - Update root test/tool consumers first, then connector-support and connectors in bounded module batches.
  - Keep this task naming-only; do not refactor test harness architecture.
  - Observable completion: every downstream module compiles against the target SDK path/name.
  - _Requirements: 4.2, 4.5, 8.4_
  - _Boundary: tests / tools / connector modules_
  - _Depends: 4.3_
  - _Validation: `go test ./internal/testkit/... ./tools/...` plus all-module check script_

- [ ] 4.5 Close the `aipsdk` wave
  - Remove remaining SDK migration aliases and verify the previous public SDK directory does not exist.
  - Re-run public package, architecture, root, and module gates.
  - Observable completion: `pkg/aipsdk` is the sole extension SDK namespace and the repository is green.
  - _Requirements: 4.2, 4.5, 4.6, 8.5, 10.3_
  - _Boundary: SDK/public contract / architecture tests_
  - _Depends: 4.4_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/...` plus all-module check script_

- [ ] 5. Migrate the public runtime and standard distribution

- [ ] 5.1 Move the public runtime facade to `pkg/aipruntime`
  - Use the same target-import/temporary-local-alias technique as prior public package waves, but only for the runtime facade and its direct consumers.
  - Preserve public runtime semantics and avoid adding commercial/billing fields or unrelated API changes.
  - Remove temporary aliases before closing the task.
  - Observable completion: `pkg/aipruntime` is the sole runtime facade path and root tests pass.
  - _Requirements: 4.3, 4.5, 4.6, 10.1_
  - _Boundary: SDK/public runtime contract_
  - _Depends: 4.5_
  - _Validation: `go test ./pkg/aipruntime/... && make test-unit`_

- [ ] 5.2 Move the standard distribution command to `cmd/aipstd`
  - Move the command directory with history and update root Go references required to build it.
  - Keep composition behavior and startup/security semantics unchanged.
  - Do not yet perform broad documentation/release-file cleanup beyond direct build prerequisites.
  - Observable completion: `go build ./cmd/aipstd` succeeds and no source command directory duplicates it for compatibility.
  - _Requirements: 4.4, 8.4, 10.1_
  - _Boundary: composition root / CLI_
  - _Depends: 5.1_
  - _Validation: `go build ./cmd/aipstd && go test ./cmd/aipstd/...`_

- [ ] 5.3 Converge standard distribution runtime identity
  - Update command help/version/product strings, build-time identifiers, default product-facing service identity, and command-specific tests/fixtures to `aiproxer`/`aipstd` as appropriate.
  - Leave broad release packaging edits to Task 8, but keep local command/build tests coherent now.
  - Observable completion: standard distribution output identifies the target product and command only.
  - _Requirements: 1.1, 4.4, 7.1, 10.1_
  - _Boundary: CLI / composition_
  - _Depends: 5.2_
  - _Validation: `go test ./cmd/aipstd/... && go build ./cmd/aipstd`_

- [ ] 5.4 Close the compile-time package/command migration
  - Run root quality/unit/architecture gates and nested-module checks.
  - Use the external inventory to confirm module/import/public-package/command categories have no remaining Legacy Token Set matches.
  - Do not begin runtime wire/config renames until this gate is green.
  - Observable completion: all compile-time project namespaces are target-only.
  - _Requirements: 2.1, 2.2, 3.5, 4.1, 4.2, 4.3, 4.4, 8.5_
  - _Boundary: repository compile-time identity_
  - _Depends: 5.3_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/...` plus all-module check script_

- [ ] 6. Migrate live runtime contract namespaces

- [ ] 6.1 Replace project-specific HTTP names with `X-AIP-*`
  - Update centralized SDK header constants/defaults first.
  - Update config resolution, standard HTTP wiring, every frontend decoder/helper, contract/testkit fixtures, scripts that actively send these headers, and tests in the same subwave.
  - Preserve generic standard headers that are not project branding.
  - Do not retain retired project-header names as default aliases or fallback reads.
  - Observable completion: project-specific routing/session/resume/diagnostic and other owned headers use only `X-AIP-*` and protocol tests are green.
  - _Requirements: 5.1, 5.4, 5.5, 8.4, 10.1_
  - _Boundary: SDK/public contract / frontends / config / stdhttp_
  - _Depends: 5.4_
  - _Validation: `go test ./pkg/aipsdk/... ./internal/stdhttp/... ./internal/plugins/frontends/... ./internal/core/config/... ./internal/testkit/contract/...`_

- [ ] 6.2 Replace project-specific environment names with `AIP_*`
  - Update runtime/test environment lookups and all active callers in scripts/workflows/configuration in the same subwave so CI is not left red between tasks.
  - Preserve provider-owned environment variables unchanged.
  - Update integration/test gating variables and helper constants together.
  - Do not dual-read retired project environment names.
  - Observable completion: active code/CI/scripts/tests use `AIP_*` for project-owned environment configuration and relevant tests/checks pass.
  - _Requirements: 5.2, 5.4, 5.5, 7.1, 7.2, 10.1_
  - _Boundary: config / tests / scripts / CI_
  - _Depends: 6.1_
  - _Validation: focused config/testkit tests plus `make quality-checks && make test-unit`_

- [ ] 6.3 Migrate project-owned config/schema/service/user-agent/IPC identifiers
  - Use the classified inventory to rename only identifiers whose value semantically encodes the project brand.
  - Preserve schema versions, field semantics, protocol/provider names, and compatibility rules unrelated to branding.
  - Update parsers/emitters/tests/fixtures atomically for each identifier family.
  - Observable completion: maintained runtime identifiers use `aiproxer`, `aip`, or `aiproxer.com` according to their namespace convention, with no behavioral drift.
  - _Requirements: 5.3, 5.4, 5.5, 10.1_
  - _Boundary: config / runtime contracts / connectors_
  - _Depends: 6.2_
  - _Validation: affected package tests plus `make test-unit`_

- [ ] 6.4 Migrate metrics, tracing, logging, and diagnostic identity
  - Rename project-owned metric namespace to `aip_*` while preserving metric type, labels, help meaning, and cardinality constraints.
  - Rename project resource/service/tracing/logging/diagnostic branding without weakening redaction/security behavior.
  - Update dashboards/tests/docs inputs that are executable validation dependencies; prose docs remain for Task 9.
  - Observable completion: observability tests/exported names use target identity and no metric semantic changes are introduced.
  - _Requirements: 6.1, 6.2, 10.1_
  - _Boundary: observability / diagnostics_
  - _Depends: 6.3_
  - _Validation: `go test ./internal/infra/metrics/... ./internal/core/diag/...` plus affected observability tests_

- [ ] 6.5 Migrate any durable branded identifiers safely
  - Review every persistence/storage match flagged by Task 1.3 before mutation.
  - For ephemeral or reconstructible identities, rename with explicit rebuild verification.
  - For authoritative persisted names, implement a forward data-preserving migration and rollback/verification procedure; keep this change narrowly scoped and test both supported storage dialects where applicable.
  - Leave semantic/non-brand identifiers unchanged.
  - If no durable branded identifier requires migration, document that evidence and make no persistence churn.
  - Observable completion: no authoritative state can be orphaned by branding changes.
  - _Requirements: 6.3, 6.4, 6.5, 10.1_
  - _Boundary: persistence / infrastructure_
  - _Depends: 6.4_
  - _Validation: affected store/migration tests; SQLite plus configured PostgreSQL parity where relevant_

- [ ] 6.6 Close the runtime-contract wave
  - Run root tests, contract/parity tests appropriate to changed surfaces, and all-module checks.
  - Scan runtime wire/config/persistence/observability inventory categories for leftovers.
  - Observable completion: live project-owned contract names are target-only and behavior remains green.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 6.1, 6.2, 8.5, 10.3_
  - _Boundary: runtime contracts / tests_
  - _Depends: 6.5_
  - _Validation: `make test-unit && make parity-checks` plus all-module check script_

- [ ] 7. Converge developer tooling and active automation

- [ ] 7.1 Update Make targets and shell/PowerShell tooling
  - Rename project-owned paths, variables, executable names, help strings, temp/artifact names, and checks in `Makefile` and scripts.
  - Preserve command semantics and platform parity between shell and PowerShell variants.
  - Update all-module/tidy/change-size/quality scripts to operate on target module/package/command paths.
  - Observable completion: documented repository quality commands execute without source-brand dependencies.
  - _Requirements: 7.1, 7.5, 10.1_
  - _Boundary: developer tooling_
  - _Depends: 6.6_
  - _Validation: `make quality-checks && make test-unit` plus all-module check script_

- [ ] 7.2 Update GitHub workflows and repository automation
  - Migrate target repository/module/command/env/artifact identities in `.github` workflows and automation configuration.
  - Preserve triggers, permissions, security controls, job semantics, and platform matrices.
  - Do not rename third-party action/provider identifiers.
  - Observable completion: workflow static checks/repository validation show no broken target paths or source-brand runtime references.
  - _Requirements: 7.2, 10.1_
  - _Boundary: CI / repository automation_
  - _Depends: 7.1_
  - _Validation: existing local workflow/config checks where available plus `make quality-checks`_

- [ ] 7.3 Update repository agent skills, rules, and automation prompts
  - Rename source-brand skills/directories when their names are project-specific.
  - Update AGENT-facing code paths, command names, architecture examples, and repository identity in maintained skills/rules/reviews that remain tracked.
  - Preserve the actual architecture guidance; this is not a content redesign.
  - Observable completion: agents receive target package/command/repository instructions and no maintained skill instructs use of a retired project identity.
  - _Requirements: 2.3, 7.4_
  - _Boundary: agent tooling / repository instructions_
  - _Depends: 7.2_
  - _Validation: external Legacy Token Set scan limited to active agent/rule paths_

- [ ] 7.4 Close active tooling automation
  - Run local quality/unit/module commands through their target-updated entrypoints.
  - Fix tooling failures before release packaging work begins.
  - Observable completion: active developer/CI helpers reference valid target paths and execute successfully.
  - _Requirements: 7.5, 8.5, 10.3_
  - _Boundary: tooling / CI_
  - _Depends: 7.3_
  - _Validation: `make quality-checks && make test-unit` plus all-module check script_

- [ ] 8. Converge release and distribution identity

- [ ] 8.1 Update release project/build/binary/archive configuration
  - Set release project identity to `aiproxer` and standard build/binary identity to `aipstd`.
  - Point release builds to `./cmd/aipstd` and update archive/checksum/package naming that embeds project branding.
  - Preserve supported OS/architecture matrix and build flags.
  - Observable completion: release configuration resolves only target paths/IDs and can build the standard binary.
  - _Requirements: 4.4, 7.3, 10.1_
  - _Boundary: release packaging_
  - _Depends: 7.4_
  - _Validation: `goreleaser check` when available and `go build ./cmd/aipstd`_

- [ ] 8.2 Update install/package/container/distribution generation inputs
  - Rename project-owned package/container/service/artifact names discovered by the inventory.
  - Update generated-release inputs and executable smoke scripts; prose installation docs remain for Task 9.
  - Preserve deployment/runtime behavior except the identity/name change.
  - Observable completion: generated distribution artifacts use `aiproxer`/`aipstd` naming only.
  - _Requirements: 7.3, 10.1_
  - _Boundary: release / deployment artifacts_
  - _Depends: 8.1_
  - _Validation: repository release/build smoke commands and `go build ./cmd/aipstd`_

- [ ] 8.3 Close the release wave
  - Produce a local snapshot/release smoke where repository tooling supports it.
  - Inspect artifact names and embedded product/version output for target identity.
  - Do not proceed to broad docs convergence while release paths are red.
  - Observable completion: build/release artifacts use only the target project/binary identity.
  - _Requirements: 7.3, 7.5, 8.5, 10.3_
  - _Boundary: release / tests_
  - _Depends: 8.2_
  - _Validation: release snapshot/check command plus `make quality-checks && make test-unit`_

- [ ] 9. Converge documentation, Kiro artifacts, historical tracked text, and paths

- [ ] 9.1 Rewrite README and user/operator/developer documentation (P)
  - Update brand, canonical target repository links, module/package/command paths, HTTP/env examples, install/build commands, diagrams, and prose references.
  - Validate examples against the now-stable target code; do not document compatibility names that no longer exist.
  - Rename documentation filenames/directories if their names are part of the Legacy Token Set.
  - Observable completion: README/docs describe the target system and all code/config examples resolve to real target surfaces.
  - _Requirements: 2.1, 2.2, 2.3, 7.3, 9.4_
  - _Boundary: documentation_
  - _Depends: 8.3_
  - _Validation: doc/link/example checks available in repository plus external scan limited to README/docs paths_

- [ ] 9.2 Rewrite steering, templates, AGENTS, and active Kiro specifications (P)
  - Update `.kiro/steering`, `.kiro/settings/templates`, active `.kiro/specs`, root/.kiro AGENTS, and related maintained project instructions to target package/command/repository names.
  - Preserve requirements/design/task meaning and IDs; this is a branding/path repair, not retroactive feature redesign.
  - Rename any tracked directory/file whose name contains a semantic Legacy Token Set identifier.
  - Observable completion: active Kiro/steering/AGENTS artifacts describe the target architecture consistently.
  - _Requirements: 2.1, 2.2, 2.3, 7.4_
  - _Boundary: Kiro / repository instructions_
  - _Depends: 8.3_
  - _Validation: external scan limited to active Kiro/AGENTS/template paths_

- [ ] 9.3 Rewrite archived Kiro/review/history artifacts that remain tracked (P)
  - Update archived specifications, reviews, matrices, and other historical tracked Markdown/text because Issue #429 explicitly requires repository-wide removal.
  - Preserve historical technical substance while translating project names/paths to target terminology.
  - Do not alter immutable Git commit history or external issue/PR discussions.
  - Observable completion: tracked archives contain no semantic Legacy Token Set matches.
  - _Requirements: 2.2, 2.3, 2.5_
  - _Boundary: archived repository documents_
  - _Depends: 8.3_
  - _Validation: external scan limited to archived Kiro/review/history paths_

- [ ] 9.4 Rewrite remaining comments, help, fixtures, goldens, examples, and tracked filenames
  - Reconcile Tasks 9.1–9.3 first, then handle remaining inventory classes across source comments, help strings, fixture content, golden filenames, examples, config samples, and miscellaneous tracked paths.
  - Treat false-positive third-party/unrelated matches according to Task 1.3 classification rather than blindly replacing them.
  - Observable completion: the external path/content scan reports no unresolved project-brand matches except items requiring the GitHub host cutover itself.
  - _Requirements: 2.1, 2.2, 2.3, 2.4_
  - _Boundary: repository-wide content/path convergence_
  - _Depends: 9.1, 9.2, 9.3_
  - _Validation: full external Legacy Token Set scan over `git ls-files` paths and textual contents_

- [ ] 9.5 Close the documentation/content wave
  - Run root and module checks again because code comments/examples/generated inputs and filenames may affect builds/tests.
  - Re-run the external scan and resolve every remaining tracked-tree project-brand match before host cutover.
  - Observable completion: code/tooling/docs tree is target-only and green before external repository transfer.
  - _Requirements: 2.1, 2.2, 8.5, 10.3, 10.4_
  - _Boundary: repository-wide gate_
  - _Depends: 9.4_
  - _Validation: `make quality-checks && make test-unit` plus all-module check script and full external scan_

- [ ] 10. Cut over the canonical GitHub repository location

- [ ] 10.1 Verify target organization/repository prerequisites
  - Confirm the acting owner can create/transfer into the `aiproxer` organization.
  - Confirm the target repository name `aiproxer` is available and no fork-network/name constraint blocks transfer.
  - Inventory branch/ruleset settings, Actions secrets/environments, webhooks/integrations, release settings, pages/package/container settings, and other owner-controlled configuration that needs post-transfer verification.
  - If prerequisites are not met, stop here; do not undo completed code namespaces or stack unrelated changes.
  - Observable completion: owner explicitly records GO for transfer/rename.
  - _Requirements: 9.1, 8.3_
  - _Boundary: GitHub repository administration_
  - _Depends: 9.5_
  - _Validation: owner/operator GitHub permission and target-name preflight_

- [ ] 10.2 Transfer/rename the repository to `github.com/aiproxer/aiproxer`
  - Perform the repository ownership/name operation only after Task 10.1 is GO.
  - Preserve repository history/issues/PRs/releases/settings through GitHub's supported transfer/rename mechanism rather than creating a fresh repository copy.
  - Treat GitHub-managed former-location redirects as external platform behavior, not canonical project identity.
  - Observable completion: GitHub reports `github.com/aiproxer/aiproxer` as the canonical repository.
  - _Requirements: 9.2, 9.5_
  - _Boundary: GitHub repository administration_
  - _Depends: 10.1_
  - _Validation: repository metadata resolves at `github.com/aiproxer/aiproxer`_

- [ ] 10.3 Verify post-cutover repository operations
  - Verify branch/ruleset behavior, Actions, secrets/environments, webhooks/integrations, releases, canonical links, clone/fetch/push remotes, and package/container settings that apply.
  - Update maintainer remotes to the canonical target URL.
  - Trigger or observe a representative CI run from the target repository identity.
  - Observable completion: normal contribution and CI/release operations work from the target host without maintained references to the previous location.
  - _Requirements: 9.3, 9.4, 10.5_
  - _Boundary: GitHub / CI / release administration_
  - _Depends: 10.2_
  - _Validation: target-host CI + clone/fetch/push verification_

- [ ] 11. Perform final zero-legacy and behavior convergence

- [ ] 11.1 Remove every temporary migration artifact
  - Remove remaining import aliases carrying source-brand identifiers, untracked migration helpers that accidentally entered the tree, compatibility-only shims, duplicate package paths, stale generated files, and transitional comments.
  - Confirm no final runtime dual-read/dual-emit of retired project-owned names exists.
  - Observable completion: only target identities remain in maintained implementation paths and no compatibility tree survives.
  - _Requirements: 4.5, 4.6, 5.4, 5.5, 10.2_
  - _Boundary: repository-wide cleanup_
  - _Depends: 10.3_
  - _Validation: focused package/import/runtime contract searches generated externally from Issue #429_

- [ ] 11.2 Run the final tracked-path and textual-content zero-legacy scan
  - Regenerate/use the out-of-tree Legacy Token Set matcher from Issue #429.
  - Scan every path from `git ls-files` and textual contents of all tracked files.
  - Review apparent matches semantically; only unrelated provider/protocol/third-party/natural-language false positives may remain, and those must not actually encode project identity.
  - Do not add an allowlist for genuine project-brand leftovers just to make the scan pass.
  - Observable completion: zero semantic project-brand matches in tracked paths and textual contents.
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 10.4_
  - _Boundary: repository-wide verification_
  - _Depends: 11.1_
  - _Validation: external full-tree Legacy Token Set scanner reports zero semantic matches_

- [ ] 11.3 Run architecture, public-contract, and root regression gates
  - Run formatting/tidy/vet/architecture checks and the full default root test suite.
  - Verify target public package positive invariants and standard command build.
  - Treat any rebranding-caused failure as a blocker.
  - Observable completion: root repository is green under target namespaces.
  - _Requirements: 8.5, 10.1, 10.3, 10.6_
  - _Boundary: tests / architecture_
  - _Depends: 11.2_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/... && go build ./cmd/aipstd`_

- [ ] 11.4 Run nested-module and parity/integration-oriented gates
  - Run all-module checks after final cleanup.
  - Run repository parity/contract gates using configured external services where applicable; existing environment-gated skips remain valid when dependencies are unavailable.
  - Observable completion: root/support/connectors agree on the target namespace and contract suites pass.
  - _Requirements: 3.5, 8.5, 10.3, 10.6_
  - _Boundary: connector modules / contract tests_
  - _Depends: 11.3_
  - _Validation: all-module check script plus `make parity-checks`_

- [ ] 11.5 Run final QA and clean-clone target-host smoke
  - Run the repository's full QA target.
  - Clone a fresh working copy from `github.com/aiproxer/aiproxer` with no local replacement state carried from the migration workspace.
  - In the clean clone, tidy/check representative root/nested modules, build `./cmd/aipstd`, and run a standard distribution/release/configuration smoke using target names only.
  - Re-run the zero-legacy tracked-tree scan in the clean clone.
  - Observable completion: all final gates are green and the feature can be declared implemented.
  - _Requirements: 9.2, 9.3, 10.3, 10.4, 10.5, 10.6_
  - _Boundary: final release/implementation gate_
  - _Depends: 11.4_
  - _Validation: `make qa`, clean-clone build/test/release smoke, and final external zero-legacy scan_

## Dependency Summary

Critical path:

`1 -> 2.1 -> 2.2 -> (2.3/2.4/2.5) -> 2.6 -> 3 -> 4 -> 5 -> 6 -> 7 -> 8 -> (9.1/9.2/9.3) -> 9.4 -> 9.5 -> 10 -> 11`

Safe parallelism is intentionally narrow:

- Connector batches 2.3–2.5 may run in parallel after connector-support modules are green, provided they touch disjoint modules and no shared script/root file.
- Documentation/Kiro/archive batches 9.1–9.3 may run in parallel only after source/tooling/release naming is frozen.
- Public package waves (`aipapi`, then `aipsdk`, then `aipruntime`) are deliberately sequential.
- Runtime contract subwaves are deliberately sequential because header/env/config/observability fixtures frequently overlap shared config/test infrastructure.

This graph is intentionally conservative: the primary optimization goal is **failure localization and agent reliability**, not minimum wall-clock time.
