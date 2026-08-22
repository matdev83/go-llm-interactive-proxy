# Implementation Plan

## Execution Rules

This is a **sequential refactor on the critical path**. Do not merge major tasks or skip dependency gates to “save time.” The purpose of this plan is to prevent a repository-wide rename from producing an unbounded red tree.

- A task may begin only when every `_Depends:_` checkpoint is green, except for documented pre-existing failures captured in Task 1.
- Each task owns only its listed naming surface plus tests/fixtures/callers required to keep that surface coherent.
- If a task produces failures outside its expected surface, repair or reduce/revert that task before proceeding.
- Temporary Go import aliases may be used exactly as described for a package wave; duplicate public compatibility packages are forbidden.
- Do not add permanent dual-read/dual-emit support for retired project-owned headers, environment names, config identifiers, or other branding aliases.
- The Legacy Token Set patterns, scanner implementation, Issue snapshot, and scan evidence remain **untracked/out-of-tree** because committing retired spellings would violate the final tree requirement; their provenance/checksums are frozen in the implementation handoff.
- All migration-only aliases/manifests/helper shims/compatibility bridges must be removed from the final workspace. Only the external scanner inputs/evidence may remain outside the repository during final verification.
- `(P)` means tasks can be delegated in parallel **only after their common dependency is green and an immutable non-overlapping ownership manifest is frozen**. Parallel workers may not choose new modules/paths, edit shared files, or reassign ownership ad hoc. Reconcile them through the named merge gate before continuing.
- The runtime environment-name pass may edit `.github/**` only for project-owned environment-variable identifiers. The later CI pass owns all other project/repository/module/command/artifact/path identities in `.github/**` and ends with a full cross-wave scan.

## Tasks

- [ ] 1. Establish a controlled migration baseline

- [ ] 1.1 Capture root repository baseline
  - Run the current root quality, unit-test, and architecture guardrail commands before any rename.
  - Record pre-existing failures separately from rebranding regressions; do not normalize a red baseline into acceptance.
  - Confirm the standard distribution currently builds in the baseline tree.
  - Record the exact baseline commit SHA.
  - Observable completion: a short implementation log identifies the exact baseline commit and green/failing commands before mutations start.
  - _Requirements: 8.1, 10.1_
  - _Boundary: tests / repository quality_
  - _Depends: none_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/...`_

- [ ] 1.2 Capture nested-module and release-producer baseline
  - Run the repository's existing all-module checks with the same `GOWORK` behavior used by CI/scripts.
  - Identify connector-support modules separately from connector modules and record every root, support-to-support, and connector-to-support dependency edge.
  - Inventory build/release/package/container producers that can create artifacts outside the tracked tree, including release tooling and standard executable outputs.
  - Observable completion: every nested module is classified as clean or as a documented pre-existing exception, and every producible artifact family has an identified producer.
  - _Requirements: 2.5, 3.4, 8.1_
  - _Boundary: connector modules / release producers / tests_
  - _Depends: 1.1_
  - _Validation: platform-appropriate `scripts/check-all-modules.sh` or `scripts/check-all-modules.ps1`; baseline standard build/release inspection_

- [ ] 1.3 Freeze a reproducible Legacy Token Set scanner and classified inventory
  - Snapshot the exact Issue #429 body used for this migration. Record its URL, `updated_at`, and SHA-256 of the exact UTF-8 body in the implementation log or CI artifact.
  - Use scanner contract `aiproxer-rebrand-scan/v1`. Keep the scanner implementation out of the tracked repository, compute its SHA-256, and do not change it silently between gates.
  - Deterministically generate the Legacy Token Set pattern file from the frozen issue snapshot plus baseline semantic variants; serialize deterministically and record its SHA-256.
  - Build an out-of-tree generated-artifact manifest from Task 1.2. For each artifact family, record producer command and required probes: filename, archive entry names, textual metadata/manifests, standard executable help/version/build metadata, package/container tags/labels/config, or another explicit probe. Record the manifest SHA-256.
  - Use the canonical scanner invocation from `design.md`: `python3 "$AIP_REBRAND_SCANNER" scan --repo . --patterns "$AIP_REBRAND_PATTERNS" --artifacts "$AIP_REBRAND_ARTIFACT_MANIFEST" --format json`.
  - Scan both `git ls-files` paths and textual contents at the baseline commit; classify every meaningful occurrence into module/import, public package/Go identifier, runtime wire/config, persistence/observability, tooling/CI/release, docs/agent/Kiro/historical, generated-artifact surface, or false-positive/third-party.
  - Flag all suspected durable identifiers for explicit review before editing.
  - Hand off the Issue revision, baseline SHA, scanner contract, scanner SHA, pattern SHA, artifact-manifest SHA, canonical invocation, and inventory result/checksum through the implementation log or CI artifact. Do not commit retired spellings.
  - If Issue #429 changes materially after this freeze, stop and require an explicit owner-approved rebaseline; regenerate affected downstream scan evidence instead of mixing provenance versions.
  - Observable completion: every meaningful match has an owner wave, false positives are justified, the generated artifact set is explicit, and every later agent can reproduce the same scan from the frozen provenance bundle.
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 6.3, 8.2, 8.8_
  - _Boundary: repository-wide analysis / external validation evidence_
  - _Depends: 1.2_
  - _Validation: rerunning the canonical invocation against the same baseline with the same checksummed inputs produces equivalent classified results_

- [ ] 1.4 Freeze target naming and immutable parallel-work ownership
  - Use the target matrix from `design.md`: `aiproxer`, `github.com/aiproxer/aiproxer`, `aiproxer.com`, `aip`, `pkg/aipapi`, `pkg/aipsdk`, `pkg/aipruntime`, `aipstd`, `X-AIP-*`, `AIP_*`, and `aip_*`.
  - Resolve any discovered project-owned identifier to one target convention before its wave begins; do not invent competing spellings later.
  - Freeze an immutable module-to-batch manifest for **every connector module**. Assign each connector exactly once to batch A, batch B, or a numbered later batch, keep each batch at most 4–6 modules unless explicitly reduced for complexity, and record a checksum.
  - Freeze the late documentation/agent path partition: README/docs; active non-Kiro agent automation; active Kiro/repository instructions; historical Kiro/reviews; serial remainder paths. Ensure every parallel path is owned exactly once.
  - Reserve shared/root/support/tooling files for serial tasks rather than parallel connector workers.
  - Confirm Open Core/Enterprise separation remains out of scope.
  - Observable completion: implementation agents share one target mapping and immutable non-overlapping module/path assignments before any parallel work begins.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 8.3, 8.7_
  - _Boundary: repository-wide migration control_
  - _Depends: 1.3_
  - _Validation: ownership manifest proves every connector/path in parallel waves appears exactly once and no shared file is assigned to multiple workers_

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

- [ ] 2.2 Migrate the complete connector-support dependency graph
  - Process connector-support modules in dependency order, one module at a time.
  - Update every connector-support `module` declaration to the `github.com/aiproxer/aiproxer/...` namespace.
  - Update all support-module source imports of the root module and other connector-support modules.
  - Update every project-owned root-module `require`/`replace` edge.
  - Update every inter-support `require`/`replace` edge while preserving intended relative local source topology.
  - Validate each support module independently before moving to the next, then validate the complete support graph.
  - Re-check the frozen connector batch manifest against the now-target-stable support graph; if topology discovery invalidates the manifest, re-freeze/rechecksum it **before** parallel connector work rather than allowing workers to select modules dynamically.
  - Observable completion: every support module and every root/support-to-support dependency edge resolves under the target namespace and independently passes `GOWORK=off` checks.
  - _Requirements: 3.2, 3.4, 8.4_
  - _Boundary: connector-support modules_
  - _Depends: 2.1_
  - _Validation: per support module `GOWORK=off go mod tidy && GOWORK=off go test ./...`, followed by support-graph edge scan using the frozen scanner provenance_

- [ ] 2.3 Migrate frozen connector module batch A (P)
  - Use **only** the immutable batch A module list frozen in Task 1.4/verified in Task 2.2; do not select modules at execution time.
  - Update each assigned module declaration, project-owned requirements/replacements, and Go imports to the target namespace.
  - Preserve relative replacement topology.
  - Do not edit shared support/root/scripts/checkers.
  - Observable completion: every module assigned to batch A independently tidies/tests under `GOWORK=off`, and no module outside batch A was touched.
  - _Requirements: 3.3, 3.4, 8.4_
  - _Boundary: connector modules / frozen batch A_
  - _Depends: 2.2_
  - _Validation: per assigned module `GOWORK=off go mod tidy && GOWORK=off go test ./...` plus ownership-manifest diff check_

- [ ] 2.4 Migrate frozen connector module batch B (P)
  - Use **only** the immutable batch B module list frozen in Task 1.4/verified in Task 2.2.
  - Apply the same exact module/require/replace/import mapping as batch A.
  - Do not edit shared support/root/scripts/checkers.
  - Observable completion: every module assigned to batch B independently tidies/tests under `GOWORK=off`, and no module outside batch B was touched.
  - _Requirements: 3.3, 3.4, 8.4_
  - _Boundary: connector modules / frozen batch B_
  - _Depends: 2.2_
  - _Validation: per assigned module `GOWORK=off go mod tidy && GOWORK=off go test ./...` plus ownership-manifest diff check_

- [ ] 2.5 Migrate frozen connector module batches C+ (P)
  - Use only the remaining **preassigned numbered batches** from the immutable connector ownership manifest; never interpret “remaining” by dynamically scanning what other workers happened to edit.
  - Execute each numbered batch with a maximum of 4–6 modules unless the frozen manifest specifies a smaller complexity-driven batch.
  - Keep shared scripts/checkers/support/root files out of these parallel tasks.
  - Observable completion: every connector assigned to batches C+ independently tidies/tests under `GOWORK=off`, with no overlap with batches A/B or another C+ worker.
  - _Requirements: 3.3, 3.4, 8.4_
  - _Boundary: connector modules / frozen batches C+_
  - _Depends: 2.2_
  - _Validation: per assigned module `GOWORK=off go mod tidy && GOWORK=off go test ./...` plus ownership-manifest diff check_

- [ ] 2.6 Close the module-graph wave
  - Reconcile connector batches and prove every connector from the frozen manifest was covered exactly once.
  - Rerun the repository all-module scripts.
  - Scan all tracked `go.mod`/`go.sum`, Go import strings, and connector-support dependency edges using the frozen scanner provenance for module-namespace leftovers.
  - Do not begin public package moves until this checkpoint is green.
  - Observable completion: root, support, and every connector module resolve one target project namespace and the immutable connector manifest has neither gaps nor overlaps.
  - _Requirements: 3.5, 8.5, 10.3_
  - _Boundary: complete Go module graph_
  - _Depends: 2.3, 2.4, 2.5_
  - _Validation: `make test-unit` plus platform-appropriate all-module check script and manifest coverage check_

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
  - Validate support modules first, then reuse the frozen connector batch ownership; do not combine this work with SDK migration.
  - Observable completion: all independent modules use the `aipapi` target path and local package name.
  - _Requirements: 4.1, 4.5, 8.4_
  - _Boundary: connector modules_
  - _Depends: 3.3_
  - _Validation: platform-appropriate all-module check script_

- [ ] 3.5 Close the `aipapi` wave
  - Remove any remaining temporary aliases for the migrated canonical API package from the workspace.
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
  - Update root test/tool consumers first, then connector-support and connectors using the frozen connector module ownership.
  - Keep this task naming-only; do not refactor test harness architecture.
  - Observable completion: every downstream module compiles against the target SDK path/name.
  - _Requirements: 4.2, 4.5, 8.4_
  - _Boundary: tests / tools / connector modules_
  - _Depends: 4.3_
  - _Validation: `go test ./internal/testkit/... ./tools/...` plus all-module check script_

- [ ] 4.5 Close the `aipsdk` wave
  - Remove remaining SDK migration aliases from the workspace and verify the previous public SDK directory does not exist.
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
  - Use the frozen scanner provenance to confirm module/import/public-package/command categories have no remaining Legacy Token Set matches.
  - Do not begin runtime wire/config renames until this gate is green.
  - Observable completion: all compile-time project namespaces are target-only.
  - _Requirements: 2.1, 2.2, 3.5, 4.1, 4.2, 4.3, 4.4, 8.5_
  - _Boundary: repository compile-time identity_
  - _Depends: 5.3_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/...` plus all-module check script and frozen scanner_

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

- [ ] 6.2 Replace project-specific environment names with `AIP_*` as one CI-safe identifier-family cutover
  - Update runtime/test environment lookups and active callers in code, scripts, configuration, and tests.
  - Update **only project-owned environment-variable identifiers** inside `.github/**` in the same subwave so CI does not mix the new runtime environment contract with old workflow variable names.
  - Do **not** change repository names, module paths, command paths, artifact names, release identities, or other non-environment branding in `.github/**`; those are exclusively owned by Task 7.2.
  - Preserve provider-owned environment variables unchanged.
  - Update integration/test gating variables and helper constants together.
  - Do not dual-read retired project environment names.
  - Observable completion: active runtime/tests/scripts/workflows use `AIP_*` for project-owned environment configuration, while all non-environment `.github/**` identity changes remain untouched for Task 7.2.
  - _Requirements: 5.2, 5.4, 5.5, 7.2, 10.1_
  - _Boundary: runtime config / tests / scripts / CI environment identifiers only_
  - _Depends: 6.1_
  - _Validation: focused config/testkit tests, `make quality-checks && make test-unit`, and scanner restricted to project-owned environment identifiers across runtime/scripts/`.github/**`_

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
  - Scan runtime wire/config/persistence/observability inventory categories using the frozen scanner provenance.
  - Verify `.github/**` environment identifiers are target-only while explicitly allowing only the non-environment identity categories reserved for Task 7.2 to remain pending.
  - Observable completion: live project-owned contract names are target-only and behavior remains green; CI environment references match the target runtime environment contract.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 6.1, 6.2, 8.5, 10.3_
  - _Boundary: runtime contracts / tests / CI environment identifiers_
  - _Depends: 6.5_
  - _Validation: `make test-unit && make parity-checks` plus all-module check script and frozen runtime-category scan_

- [ ] 7. Converge developer tooling, remaining CI identity, and active non-Kiro automation

- [ ] 7.1 Update Make targets and shell/PowerShell tooling
  - Rename project-owned paths, executable names, help strings, temp/artifact names, and checks in `Makefile` and scripts.
  - Preserve the already-migrated `AIP_*` environment mapping from Task 6.2; do not invent a second environment-variable pass.
  - Preserve command semantics and platform parity between shell and PowerShell variants.
  - Update all-module/tidy/change-size/quality scripts to operate on target module/package/command paths.
  - Observable completion: repository quality commands execute without source-brand path/executable dependencies.
  - _Requirements: 7.1, 7.5, 10.1_
  - _Boundary: developer tooling_
  - _Depends: 6.6_
  - _Validation: `make quality-checks && make test-unit` plus all-module check script_

- [ ] 7.2 Complete `.github/**` CI/repository automation identity and close the cross-wave ownership surface
  - Treat Task 6.2's environment-variable substitutions as immutable input; do not re-derive or independently rename them.
  - Migrate all **remaining non-environment** target repository/module/command/artifact/path/release identities in `.github/**` workflows and automation configuration.
  - Preserve triggers, permissions, security controls, job semantics, and platform matrices.
  - Do not rename third-party action/provider identifiers.
  - After the edits, run the frozen Legacy Token Set scanner over **all `.github/**` paths and textual contents**, not merely the identifiers changed in this task. Resolve every genuine project-brand match before proceeding.
  - Observable completion: `.github/**` is fully target-only across environment names from Task 6.2 and all remaining CI identities from this task, with no overlapping ownership ambiguity.
  - _Requirements: 5.2, 7.2, 7.5, 10.1_
  - _Boundary: CI / repository automation, non-environment identity pass + full cross-wave scan_
  - _Depends: 7.1_
  - _Validation: existing local workflow/config checks where available, `make quality-checks`, and full frozen scanner over `.github/**`_

- [ ] 7.3 Update active non-Kiro agent skills, rules, and automation prompts under exact path ownership
  - Own `.agents/skills/**`, `.agents/catalog.json`, `.cursor/**`, `.jules/**`, `.coderabbit.yaml`, and any equivalent **active non-Kiro** agent/rule configuration assigned to this task by the frozen path manifest.
  - Explicitly **exclude** `.agents/reviews/**`, root `AGENTS.md`, and every `.kiro/**` path; those are owned by Tasks 9.2/9.3.
  - Rename project-specific skill/directories when their names are branding.
  - Update agent-facing code paths, command names, architecture examples, and repository identity in owned paths.
  - Preserve the actual architecture guidance; this is not a content redesign.
  - Observable completion: active non-Kiro agent automation uses target package/command/repository instructions, and the diff touches no Task 9.2/9.3-owned path.
  - _Requirements: 2.3, 7.4, 8.3_
  - _Boundary: active non-Kiro agent tooling_
  - _Depends: 7.2_
  - _Validation: frozen scanner limited to the Task 7.3 path partition plus ownership-manifest diff check_

- [ ] 7.4 Close active tooling and automation
  - Run local quality/unit/module commands through their target-updated entrypoints.
  - Reconfirm `.github/**` and the Task 7.3 active-agent partition are zero-legacy under the frozen scanner.
  - Fix tooling failures before release packaging work begins.
  - Observable completion: active developer/CI helpers and active non-Kiro agent automation reference valid target paths and execute successfully.
  - _Requirements: 7.5, 8.5, 10.3_
  - _Boundary: tooling / CI / active non-Kiro agents_
  - _Depends: 7.3_
  - _Validation: `make quality-checks && make test-unit` plus all-module check script and frozen owned-path scans_

- [ ] 8. Converge release and distribution identity

- [ ] 8.1 Update release project/build/binary/archive configuration
  - Set release project identity to `aiproxer` and standard build/binary identity to `aipstd`.
  - Point release builds to `./cmd/aipstd` and update archive/checksum/package naming that embeds project branding.
  - Preserve supported OS/architecture matrix and build flags.
  - Update the out-of-tree artifact manifest only if an implementation-time producer was missed; any manifest change requires explicit rechecksum and invalidates earlier artifact-scan evidence.
  - Observable completion: release configuration resolves only target paths/IDs and can build the standard binary.
  - _Requirements: 4.4, 7.3, 8.8, 10.1_
  - _Boundary: release packaging_
  - _Depends: 7.4_
  - _Validation: `goreleaser check` when available and `go build ./cmd/aipstd`_

- [ ] 8.2 Update install/package/container/distribution generation inputs
  - Rename project-owned package/container/service/artifact names discovered by the inventory.
  - Update generated-release inputs and executable smoke scripts; prose installation docs remain for Task 9.
  - Preserve deployment/runtime behavior except the identity/name change.
  - Ensure every enabled producer in the frozen artifact manifest can emit the artifact needed for its required final probe.
  - Observable completion: generation inputs produce `aiproxer`/`aipstd` artifact identities only.
  - _Requirements: 2.5, 7.3, 10.1_
  - _Boundary: release / deployment artifacts_
  - _Depends: 8.1_
  - _Validation: repository release/build smoke commands and `go build ./cmd/aipstd`_

- [ ] 8.3 Close the release wave with mandatory generated-artifact probes
  - Produce a local snapshot/release build for every artifact family marked producible in the frozen artifact manifest.
  - Apply every manifest-defined probe: artifact filenames, archive entry names/extracted textual payloads, release/package/container metadata, standard executable filename/help/version/build metadata, and other explicit probes.
  - Run the same frozen Legacy Token Set pattern set across those artifact probes and resolve every genuine project-brand match.
  - Inspect target product/version output for correctness as a positive assertion.
  - Do not proceed to broad docs convergence while release paths or generated artifacts are red.
  - Observable completion: the complete applicable generated artifact set uses only target project/binary identity and has checksummed scan evidence.
  - _Requirements: 2.5, 7.3, 7.5, 8.5, 10.4_
  - _Boundary: release / generated artifacts / tests_
  - _Depends: 8.2_
  - _Validation: release snapshot/check command, manifest-defined artifact probes, frozen artifact scan, `make quality-checks && make test-unit`_

- [ ] 9. Converge documentation, Kiro artifacts, historical tracked text, and paths

- [ ] 9.1 Rewrite README and user/operator/developer documentation (P)
  - Own only `README*`, `docs/**`, and documentation-specific filenames/directories explicitly assigned by the frozen path manifest.
  - Update brand, canonical target repository links, module/package/command paths, HTTP/env examples, install/build commands, diagrams, and prose references.
  - Validate examples against the now-stable target code; do not document compatibility names that no longer exist.
  - Rename owned documentation filenames/directories if their names are part of the Legacy Token Set.
  - Do not edit any Task 9.2/9.3 path.
  - Observable completion: README/docs describe the target system and all code/config examples resolve to real target surfaces.
  - _Requirements: 2.1, 2.2, 2.3, 7.3, 9.5_
  - _Boundary: README/docs partition_
  - _Depends: 8.3_
  - _Validation: doc/link/example checks available in repository, frozen scanner over Task 9.1 partition, ownership-manifest diff check_

- [ ] 9.2 Rewrite active Kiro and repository-instruction paths (P)
  - Own exactly: root `AGENTS.md`, `.kiro/AGENTS.md`, `.kiro/steering/**`, `.kiro/rules/**`, `.kiro/settings/**`, `.kiro/templates/**`, and active `.kiro/specs/*` **excluding** `.kiro/specs/archive/**`, plus any additional active Kiro path explicitly assigned by the frozen manifest.
  - Explicitly exclude `.agents/**` and `.kiro/specs/archive/**`.
  - Update target package/command/repository names while preserving requirements/design/task meaning and IDs; this is branding/path repair, not retroactive feature redesign.
  - Rename owned tracked directories/files whose names contain semantic Legacy Token Set identifiers.
  - Observable completion: active Kiro/steering/AGENTS artifacts describe the target architecture consistently without touching historical paths owned by Task 9.3.
  - _Requirements: 2.1, 2.2, 2.3, 7.4, 8.3_
  - _Boundary: active Kiro / repository instructions_
  - _Depends: 8.3_
  - _Validation: frozen scanner over Task 9.2 partition plus ownership-manifest diff check_

- [ ] 9.3 Rewrite archived Kiro and historical review artifacts (P)
  - Own exactly `.kiro/specs/archive/**`, `.agents/reviews/**`, and other historical review/spec paths explicitly assigned by the frozen manifest.
  - Explicitly exclude active Kiro paths from Task 9.2 and active non-Kiro agent paths from Task 7.3.
  - Preserve historical technical substance while translating project names/paths to target terminology.
  - Do not alter immutable Git commit history or external issue/PR discussions.
  - Observable completion: tracked historical partitions contain no semantic Legacy Token Set matches and no parallel worker touched the same path.
  - _Requirements: 2.2, 2.3, 2.6, 8.3_
  - _Boundary: archived Kiro / historical reviews_
  - _Depends: 8.3_
  - _Validation: frozen scanner over Task 9.3 partition plus ownership-manifest diff check_

- [ ] 9.4 Rewrite remaining comments, help, fixtures, goldens, examples, and tracked filenames
  - Reconcile Tasks 9.1–9.3 first and verify their path ownership had no overlap.
  - Handle only the serial remainder paths from the frozen ownership manifest: source comments, help strings, fixture content, golden filenames, examples outside docs, config samples, and miscellaneous tracked paths not already owned.
  - Treat false-positive third-party/unrelated matches according to Task 1.3 classification rather than blindly replacing them.
  - Observable completion: the full tracked path/content scan reports no unresolved project-brand matches except external GitHub host state that is not tracked content.
  - _Requirements: 2.1, 2.2, 2.3, 2.4_
  - _Boundary: serial repository-wide tracked-content/path remainder_
  - _Depends: 9.1, 9.2, 9.3_
  - _Validation: full frozen Legacy Token Set scan over `git ls-files` paths and textual contents plus ownership-manifest coverage check_

- [ ] 9.5 Close the documentation/content wave
  - Run root and module checks again because code comments/examples/generated inputs and filenames may affect builds/tests.
  - Re-run the frozen full tracked-tree scan and resolve every genuine project-brand match before host cutover.
  - Verify every path in the late ownership manifest was handled exactly once or explicitly classified as a false positive.
  - Observable completion: code/tooling/docs/Kiro/history tree is target-only and green before external repository transfer.
  - _Requirements: 2.1, 2.2, 8.5, 10.3, 10.4_
  - _Boundary: repository-wide tracked-tree gate_
  - _Depends: 9.4_
  - _Validation: `make quality-checks && make test-unit` plus all-module check script, full frozen tracked-tree scan, ownership-manifest coverage check_

- [ ] 10. Cut over the canonical GitHub repository location

- [ ] 10.1 Verify target organization/repository prerequisites and classify host configuration
  - Confirm the acting owner can create/transfer into the `aiproxer` organization.
  - Confirm the target repository name `aiproxer` is available and no fork-network/name constraint blocks transfer.
  - Inventory and classify host configuration into:
    - repository-scoped assets expected to remain associated: repository settings, repository webhooks, repository secrets, deploy keys, releases, repository-level rules/configuration, and applicable repository-owned metadata;
    - organization/owner-scoped or external dependencies that may require target-owner recreation/rebinding: organization rulesets/policies, organization secrets/variables, teams/role bindings, GitHub App/install access, package/container permissions, Pages custom-domain/DNS dependencies, and other relevant integrations discovered during preflight.
  - Record the expected post-transfer validation/recreation action for each applicable item. Do not claim an item transfers unless GitHub's current documented behavior supports that expectation.
  - If prerequisites are not met, stop here; do not undo completed code namespaces or stack unrelated changes.
  - Observable completion: owner explicitly records GO for transfer/rename and the host configuration matrix has no unowned item.
  - _Requirements: 9.1, 9.3, 8.3_
  - _Boundary: GitHub repository administration / target-owner dependencies_
  - _Depends: 9.5_
  - _Validation: owner/operator GitHub permission/name preflight plus host configuration matrix review against current GitHub documentation_

- [ ] 10.2 Transfer/rename the repository and recreate/rebind non-transferred dependencies
  - Perform the repository ownership/name operation only after Task 10.1 is GO.
  - Use GitHub's supported transfer/rename mechanism rather than creating a fresh repository copy.
  - Immediately verify that repository-scoped assets expected to remain associated are present enough to continue safely; record discrepancies for repair.
  - Recreate/rebind organization/owner-scoped or external dependencies from the Task 10.1 matrix where the target owner does not automatically provide equivalent configuration.
  - Treat GitHub-managed former-location redirects as external platform behavior, not canonical project identity.
  - Observable completion: GitHub reports `github.com/aiproxer/aiproxer` as canonical and every non-transferred host dependency is either recreated/rebound or explicitly blocking Task 10.3.
  - _Requirements: 9.2, 9.3, 9.4, 9.6_
  - _Boundary: GitHub repository administration / target-owner configuration_
  - _Depends: 10.1_
  - _Validation: canonical repository metadata plus item-by-item host configuration matrix status_

- [ ] 10.3 Verify post-cutover repository, CI, access, and distribution operations
  - Verify repository-scoped assets from the Task 10.1 matrix: branch/ruleset behavior, Actions, repository secrets/environments, webhooks, deploy keys, releases, canonical links, and other applicable repository settings.
  - Verify recreated/rebound target-owner dependencies: organization rulesets/policies, organization secrets/variables, teams/GitHub App access, package/container permissions, Pages/DNS dependencies, and external integrations where applicable.
  - Update maintainer remotes to the canonical target URL.
  - Trigger or observe a representative CI run from the target repository identity.
  - Verify clone/fetch/push and release/package/container access paths that apply.
  - Observable completion: normal contribution and CI/release operations work from the target host, with every applicable host configuration item verified and no maintained dependency on the previous canonical location.
  - _Requirements: 9.3, 9.4, 9.5, 10.5_
  - _Boundary: GitHub / CI / release administration_
  - _Depends: 10.2_
  - _Validation: target-host CI + clone/fetch/push + host configuration matrix + applicable release/package/container verification_

- [ ] 11. Perform final zero-legacy and behavior convergence

- [ ] 11.1 Remove every temporary migration artifact from the workspace
  - Remove all remaining import aliases carrying source-brand identifiers, migration-only manifests copied into the workspace, temporary helper scripts/shims, compatibility-only bridges, duplicate package paths, stale generated files, and transitional comments.
  - Do not satisfy this gate by merely leaving a migration shim untracked; it must be removed from the working workspace so local validation matches a clean clone.
  - Retain only the external checksummed scanner/provenance/pattern/artifact-manifest inputs and implementation evidence needed to run final verification; keep them outside the repository workspace.
  - Confirm no final runtime dual-read/dual-emit of retired project-owned names exists.
  - Observable completion: only target identities remain in maintained implementation paths and no migration-only behavior can affect local builds/tests.
  - _Requirements: 4.5, 4.6, 5.4, 5.5, 10.2_
  - _Boundary: repository/workspace-wide cleanup_
  - _Depends: 10.3_
  - _Validation: clean workspace/status inspection plus frozen focused package/import/runtime-contract scans_

- [ ] 11.2 Run the final frozen source and mandatory generated-artifact zero-legacy scan
  - Reuse the exact scanner/provenance bundle frozen in Task 1.3; verify Issue revision, baseline link, scanner SHA, pattern SHA, artifact-manifest SHA, and scanner contract before running.
  - Scan every path from `git ls-files` and textual contents of all tracked files.
  - Regenerate **every artifact family marked producible** in the frozen artifact manifest and execute every required per-artifact probe, including release archive names/entries/textual payloads, release/package/container metadata, standard executable filename/help/version/build metadata, and other explicit probes.
  - Review apparent matches semantically; only unrelated provider/protocol/third-party/natural-language false positives may remain, and those must not actually encode project identity.
  - Do not add an allowlist for genuine project-brand leftovers just to make the scan pass.
  - Observable completion: zero semantic project-brand matches in tracked paths/textual contents **and** the complete applicable generated artifact set, with checksummed evidence tied to the frozen scanner provenance.
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 2.6, 8.8, 10.4_
  - _Boundary: repository-wide source + generated artifact verification_
  - _Depends: 11.1_
  - _Validation: canonical frozen scanner invocation plus every artifact-manifest producer/probe_

- [ ] 11.3 Run architecture, public-contract, and root regression gates
  - Run formatting/tidy/vet/architecture checks and the full default root test suite.
  - Verify target public package positive invariants and standard command build.
  - Treat any rebranding-caused failure as a blocker.
  - Observable completion: root repository is green under target namespaces.
  - _Requirements: 8.5, 10.1, 10.3, 10.6_
  - _Boundary: tests / architecture_
  - _Depends: 11.2_
  - _Validation: `make quality-checks && make test-unit && go test ./internal/archtest/... && go build ./cmd/aipstd`_

- [ ] 11.4 Run complete nested-module and parity/integration-oriented gates
  - Run the full platform-appropriate all-module validation after final cleanup using the same `GOWORK` behavior as CI/scripts.
  - Run repository parity/contract gates using configured external services where applicable; existing environment-gated skips remain valid when dependencies are unavailable.
  - Observable completion: root/support/every connector agree on the target namespace and contract suites pass.
  - _Requirements: 3.5, 8.5, 10.3, 10.6_
  - _Boundary: complete connector/support module graph / contract tests_
  - _Depends: 11.3_
  - _Validation: platform-appropriate full all-module check script plus `make parity-checks`_

- [ ] 11.5 Run final QA and full clean-clone target-host convergence
  - Run the repository's full QA target in the migration workspace.
  - Clone a fresh working copy from `github.com/aiproxer/aiproxer` with no local replacement state or migration helper carried from the migration workspace.
  - In the clean clone, run the **same platform-appropriate full all-module validation script with CI-equivalent `GOWORK` behavior**, covering root, every connector-support module, and every connector module. Do not substitute representative nested modules.
  - Build/test `./cmd/aipstd` and run the standard distribution/release/configuration smoke using target names only.
  - Regenerate the mandatory artifact set from the frozen manifest in the clean clone and repeat all artifact probes.
  - Re-run the same frozen source/artifact Legacy Token Set scan in the clean clone and verify provenance/checksums match Task 11.2.
  - Observable completion: QA, the entire module graph, target-host clean build/release smoke, generated artifacts, and zero-legacy scans are green; the feature can be declared implemented.
  - _Requirements: 2.5, 3.5, 8.8, 9.2, 9.3, 10.3, 10.4, 10.5, 10.6_
  - _Boundary: final release/implementation gate_
  - _Depends: 11.4_
  - _Validation: `make qa`, platform-appropriate full all-module check script with CI-equivalent `GOWORK`, clean-clone build/test/release smoke, artifact-manifest probes, and final canonical frozen scanner invocation_

## Dependency Summary

Critical path:

`1 -> 2.1 -> 2.2 -> (2.3/2.4/2.5) -> 2.6 -> 3 -> 4 -> 5 -> 6 -> 7 -> 8 -> (9.1/9.2/9.3) -> 9.4 -> 9.5 -> 10 -> 11`

Safe parallelism is intentionally narrow:

- Connector batches 2.3–2.5 may run in parallel only from the immutable module lists frozen before dispatch; each connector is owned exactly once and shared root/support/scripts are reserved for serial tasks.
- Documentation/Kiro/history batches 9.1–9.3 may run in parallel only after source/tooling/release naming is frozen and only under the exact disjoint path partitions frozen in Task 1.4.
- Public package waves (`aipapi`, then `aipsdk`, then `aipruntime`) are deliberately sequential.
- Runtime contract subwaves are deliberately sequential because header/env/config/observability fixtures frequently overlap shared config/test infrastructure.
- `.github/**` receives two **sequential identifier-family passes**, not parallel edits: Task 6.2 owns environment names only; Task 7.2 owns all remaining project identities and closes with a full cross-wave scan.

This graph is intentionally conservative: the primary optimization goal is **failure localization, reproducibility, and agent reliability**, not minimum wall-clock time.
