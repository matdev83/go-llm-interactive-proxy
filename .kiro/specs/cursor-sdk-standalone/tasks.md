# Implementation Plan

## Task Format Note
- `(P)` means parallel-safe with peers under the same parent.
- `_Boundary_` names the design component/ownership seam.
- `_Depends_` declares non-obvious cross-group dependencies.
- `_Validation_` names the focused proof command for the task.

- [ ] 1. Extraction baseline and publication prerequisites
- [x] 1.1 Generate exact migration inventory and record baseline SHA
  - Enumerate every active Cursor reference from the implementation baseline, with source hash, destination mapping, category, and planned deletion batch.
  - Record extraction baseline commit, SDK 1.0.23 and Undici 6.28.1 pins, and confirm destination repository name, provisioning, and publication permissions before external writes.
  - Write `.kiro/specs/cursor-sdk-standalone/migration-inventory.json` as migration evidence only.
  - Observable completion is a committed inventory that distinguishes dependency coupling, policy fixtures, and historical documentation.
  - _Requirements: 6.1, 6.6_
  - _Boundary: Public module baseline_
  - _Validation: git diff --check and inventory schema validation_

- [ ] 1.2 Establish independently resolvable root and ACP module baselines
  - Publish/select a real downloadable root semver tag exposing existing public contracts without widening the API.
  - Publish `connector-support/acp` as nested-module tag `connector-support/acp/vX.Y.Z` pinned to that root tag, with sibling replacements removed in the tag content.
  - Verify from a temporary checkout with no adjacent host source that module resolution, download, verify, and build succeed.
  - Observable completion is recorded exact tag versions usable by the standalone plugin with `GOWORK=off`.
  - _Requirements: 2.2, 6.1_
  - _Boundary: Public module baseline_
  - _Validation: GOWORK=off go mod download, go mod verify, and go build ./..._

- [ ] 1.3 Provision standalone plugin repository skeleton without moving behavior
  - Create the external repository layout, module path, license/provenance files, and independent Go plus Node verification workflows.
  - Pin only released dependencies; include no Cursor implementation source yet.
  - Verify clean-checkout module resolution without sibling checkouts or unpublished host packages.
  - Observable completion is an empty but buildable plugin repository that resolves only published contracts.
  - _Requirements: 2.1, 2.5_
  - _Boundary: Standalone connector_
  - _Validation: GOWORK=off go test ./... in an isolated checkout_

- [ ] 2. Standalone connector with released dependencies
- [ ] 2.1 (P) Relocate Go connector onto published contracts
  - Move command, provider adapter, lifecycle, diagnostics, protocol, fixtures, and Go tests while preserving structure beneath the old `connectors/cursorsdk` path.
  - Rewrite only module-relative imports and monorepo assumptions; retain `Service.Describe/Configure`, `ConfiguredInstance` behavior, opaque YAML config, authenticated secrets handling, and existing error mapping.
  - Forbid root `internal/` imports and copied generated ABI files.
  - Observable completion is a standalone module that builds and passes its relocated Go tests against released root and ACP versions.
  - _Requirements: 2.1, 2.2, 4.1, 5.1, 5.3_
  - _Boundary: Standalone connector Go_
  - _Validation: GOWORK=off go test ./... and public-only import audit_

- [ ] 2.2 (P) Relocate SDK bridge with pinned toolchain baseline
  - Move bridge source, production npm lock, SDK fixtures/tests, and bridge scripts with SDK 1.0.23 and Undici 6.28.1 unchanged.
  - Preserve model discovery, text/reasoning/tool/warning/usage semantics, credential redaction, bounded diagnostics, sandbox validation, and readiness/version reporting.
  - Keep all Node tooling inside the plugin repository.
  - Observable completion is a relocated bridge that passes its existing hermetic suite under Node 22.22.3.
  - _Requirements: 2.1, 4.1, 5.3, 5.4, 6.2_
  - _Boundary: Standalone connector bridge_
  - _Validation: npm ci, npm test, and npm run typecheck_

- [ ] 2.3 Wire plugin-local private companion resolution
  - Resolve the packaged default bridge through a direct plugin-local launcher path relative to the installed outer executable.
  - Preserve explicit `bridge_executable` overrides and existing direct-executable validation without shell, npm, global binary, or automatic download behavior.
  - Preserve user-supplied workspace and configuration semantics unchanged.
  - Observable completion is an installed-layout launch that uses the private companion by default while honoring explicit overrides.
  - _Requirements: 3.4, 4.1, 5.4_
  - _Boundary: Standalone connector_
  - _Depends: 2.1, 2.2_
  - _Validation: packaged-layout launch and override regression tests_

- [ ] 3. Private runtime packaging and compatibility evidence
- [ ] 3.1 Implement direct private Node launcher with owned cleanup
  - Build new `cmd/lip-cursor-sdk-bridge` launcher that locates the fixed private Node executable and bridge entrypoint, forwards protocol streams and exit status, and performs no shell/npm/download operations.
  - Establish launcher cleanup before resource escape, release partial acquisitions on startup failure, and supervise the runtime descendant under the existing process-tree policy on both POSIX and Windows paths.
  - Cover startup failure, shutdown during start, descendant termination, late settlement, and repeated close with deterministic tests plus Linux race evidence.
  - Observable completion is a tested launcher that starts, supervises, shuts down, and reaps the private runtime without leaking owned processes.
  - _Requirements: 3.4, 5.1, 5.2_
  - _Boundary: Private runtime package_
  - _Validation: GOWORK=off go test -race ./... for launcher and bridge lifecycle packages_

- [ ] 3.2 Assemble native archives with trustworthy private layout
  - Build production JavaScript, stage only production npm dependencies and SDK-version metadata, include the private Node runtime and license/provenance notices, and emit per-platform archives with checksums.
  - Preserve manifest identity, `local_only`, `per_instance`, `agent_runtime`, static credentials, and existing supported Linux/Windows claims only where native tests pass.
  - Validate checksum coverage for private files, protected install ownership, missing-companion failure, paths containing spaces, and no global Node requirement for the private-runtime variant.
  - Observable completion is installable native archives whose verification script reports exact files, checksums, and runtime metadata.
  - _Requirements: 2.3, 2.4, 3.4, 3.5_
  - _Boundary: Private runtime package_
  - _Depends: 2.3, 3.1_
  - _Validation: scripts/package-plugin and scripts/verify-package on native OS/arch_

- [ ] 3.3 Record packaging evaluation and release compatibility metadata
  - Generate `compatibility.json` from validated release inputs with plugin/build/source identity, exact host/root/ACP/SDK/runtime versions, protocol range, platform evidence, tested host hashes, package verification results, and external-Node requirement flag.
  - Document the SEA versus private-runtime evaluation, including SDK loading, imports, metadata lookup, native assets, sandbox behavior, signatures, and platform limits, without widening the closed host manifest.
  - Publish tested per-OS installation instructions stating explicitly whether system Node is required.
  - Observable completion is a release whose compatibility and packaging decision can be audited without inspecting build logs.
  - _Requirements: 2.4, 3.5, 6.4_
  - _Boundary: Plugin certification_
  - _Depends: 3.2_
  - _Validation: compatibility metadata schema check and installation rehearsal_

- [ ] 4. Plugin certification against public contracts
- [ ] 4.1 Preserve SDK contract, stream, lifecycle, and diagnostics regressions
  - Retain baseline SDK fixtures and cover bridge tests, canonical execution, ordered events with single terminal outcome, session reuse/isolation and resource limits, capability errors, cancellation/shutdown bounds, generation fencing, redaction, health/readiness, and sandbox denial.
  - Keep macOS fake-bridge/lifecycle coverage as development evidence without claiming production Darwin support.
  - Report live provider scenarios separately with credentials; do not hide quota consumption in default verification.
  - Observable completion is a preserved regression suite that fails on semantic, lifecycle, or redaction drift.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 5.1, 5.2, 5.3, 5.4, 5.5, 6.2_
  - _Boundary: Plugin certification_
  - _Depends: 2.3_
  - _Validation: plugin Go tests, bridge tests, and public conformance suite_

- [ ] 4.2 Certify real host install, trust, and optional activation
  - Verify trusted discovery, manifest identity, secure negotiation, inventory listing, canonical execution, explicit capability errors, inactive discovery, and default-deny multi-user behavior against a versioned host artifact without recompiling the host.
  - Verify missing plugin/runtime resources fail explicitly without automatic installation or provider fallback.
  - Observable completion is a certified plugin release installable through the existing mechanism with unchanged host binary.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 4.5, 6.1_
  - _Boundary: Plugin certification_
  - _Depends: 3.3, 4.1_
  - _Validation: real host install, inspect/doctor, negotiation, execution, and access-denial checks_

- [ ] 5. Host decoupling and Node-independence guard
- [ ] 5.1 Add host no-Node verification and active-reference regression
  - Add a no-Node lane that runs root build, unit/quality/comprehensive checks, minimal packaging, CLI startup/help, and non-Cursor runtime smoke where Node and npm are unavailable and fails if either tool is invoked.
  - Add a narrow QA regression over active build/workflow/module inventories for Cursor source paths and Node setup/install commands, excluding historical specs and generic examples.
  - Observable completion is a host check that proves standard development works without the Cursor toolchain.
  - _Requirements: 1.1, 1.2, 1.3, 6.3_
  - _Boundary: Host independence guard_
  - _Validation: node-independence lane and internal/qa/node_independence_contract_test.go_

- [ ] 5.2 Retire Cursor-specific host coupling after external certification
  - Remove family census entry, dedicated workflow/Dependabot/cache-lane coupling, Cursor Makefile/script targets and source-specific exceptions, Cursor examples/config assumptions, active documentation npm instructions, first-party inventory entries (`backend_prefix_inventory`, `multi_user_backend_policy`, `standard_bundle_posture`, `expected_inventory`, `catalog_population`, `backend_multi_user_policy`), and the `discovered_factories` absence guard.
  - Replace presence-based architecture tests with external-boundary/absence regressions while preserving generic trust, lifecycle, default-deny, and contract coverage; swap the Cursor-named authority fixture for a generic external-plugin fixture where practical and leave `cursorcliacp` references untouched.
  - Inspect required CI checks/rulesets first and replace the host-relevant invariant before retiring or unrequiring bridge checks.
  - Observable completion is a host tree with no active Cursor build/CI dependency and no blocked required job.
  - _Requirements: 1.4, 6.3_
  - _Boundary: Host independence guard_
  - _Depends: 4.2, 5.1_
  - _Validation: make quality-checks, affected architecture/QA tests, and required-check inspection_

- [ ] 6. Bounded cutover and migration verification
- [ ] 6.1 Transfer test ownership and complete first bounded removal
  - Transfer all 59 Cursor Go test files and bridge verification ownership to certified external CI while retaining a completely buildable in-tree implementation fallback.
  - Remove in-tree Cursor-only workflow/QA expectations made obsolete by the transfer without weakening generic architecture rules.
  - Keep this change within the 100-Go-file gate and preserve generic contract coverage needed by the remaining implementation.
  - Observable completion is external ownership of Cursor tests with a still-buildable host fallback and passing host gates.
  - _Requirements: 1.4, 6.1, 6.2_
  - _Boundary: Host independence guard_
  - _Depends: 4.2, 5.2_
  - _Validation: go test ./... scope, architecture/QA tests, and changed-file count audit_

- [ ] 6.2 Remove remaining implementation and finalize host guards
  - Delete the remaining 66 Cursor Go files, module/release metadata, implementation assets, active enumerations, and host inventory/absence-guard updates within the remaining file budget, splitting further if host guard changes exceed budget.
  - Replace active in-tree build/installation instructions with the standalone plugin location while retaining historical archived specifications.
  - Observable completion is a host tree with no active Cursor source and final external-boundary regressions passing.
  - _Requirements: 1.4, 6.2, 6.6_
  - _Boundary: Host independence guard_
  - _Depends: 6.1_
  - _Validation: go test ./..., make test, git diff --check, and changed-file count audit_

- [ ] 6.3 Rehearse migration, rollback, and final host certification
  - Rehearse installation with preserved factory/config continuity, explicit executable-path adjustments, upgrade to the new artifact, and rollback to a compatible prior artifact without implicit SDK downgrade.
  - Run final no-Node host certification, installed external plugin compatibility, and documentation checks.
  - Observable completion is a verified operator path from the in-tree integration to the standalone release and back to a prior compatible release.
  - _Requirements: 6.3, 6.4, 6.5, 6.6_
  - _Boundary: Plugin certification_
  - _Depends: 6.2_
  - _Validation: migration/rollback rehearsal and final no-Node plus installed-plugin gates_
