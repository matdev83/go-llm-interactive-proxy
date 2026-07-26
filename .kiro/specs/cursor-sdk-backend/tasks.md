# Implementation Plan

Spec-driven product implementation remains pending. Paths below target the **external**
`connectors/cursorsdk` artifact. Do not create `internal/plugins/backends/cursorsdk`.

- [x] 0. Spec gates (Task 8.3 of backend-connector-plugin-architecture) — COMPLETE when `make kiro-spec-check SPEC=cursor-sdk-backend` passes
  - Keep requirements/design/tasks/file-plan/packaging/AGENTS aligned with external connector architecture.
  - Evidence (2026-07-20T10:42:51+02:00): `make kiro-spec-check SPEC=cursor-sdk-backend` PASS; archtest `TestCursorSDK_*` PASS; product implementation still blocked (`ready_for_implementation: false`).
  - _Validation: `make kiro-spec-check SPEC=cursor-sdk-backend`_

- [ ] 1. Lock the exact SDK and bridge contracts with tests first
- [ ] 1.1 Revalidate the pinned Cursor SDK local-agent contract
  - Isolated probe under `connectors/cursorsdk/bridge-node` for the exact pinned SDK version.
  - Convert validated shapes into sanitized fixtures before production bridge behavior.
  - _Boundary: External connector research_
  - _Depends: none_
  - _Validation: `(cd connectors/cursorsdk/bridge-node && npm test -- --runInBand)`_
  - _Requirements: 2.6, 4.1, 4.6, 4.7, 4.8, 6.3, 6.4, 7.1, 9.5, 12.1, 12.3, 13.6_

- [ ] 1.2 Define the versioned NDJSON bridge protocol
  - Failing Go + TypeScript contract tests for initialize/version, correlation, bounds, unknown mandatory, duplicate terminals.
  - Methods: models/list, agent/create, agent/send, run/cancel, agent/dispose, bridge/health, bridge/shutdown.
  - _Boundary: External connector bridge contract_
  - _Depends: 1.1_
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test ./...) && (cd connectors/cursorsdk/bridge-node && npm test)`_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.7, 6.10, 11.1, 11.2, 12.1, 12.3, 13.2_

- [ ] 1.3 Build deterministic fake-bridge harnesses
  - Fake companion executable for Go tests; mocked SDK runtime for Node tests.
  - Default Go tests require neither Node nor network/account access.
  - _Boundary: Tests_
  - _Depends: 1.2_
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test ./...)`_
  - _Requirements: 2.5, 7.3, 7.4, 7.7, 8.5, 8.9, 8.10, 12.2, 12.3_

- [ ] 2. Connector module skeleton and packaging
- [ ] 2.1 Create `connectors/cursorsdk` module, release.yaml, closed manifest
  - Export `cursorsdk` with static / local_only / per_instance.
  - Declare bridge-node private companion; no root Node deps.
  - Archtests: forbidden root paths; no root require/replace.
  - _Boundary: External connector packaging_
  - _Depends: 1.3_
  - _Validation: `make kiro-spec-check SPEC=cursor-sdk-backend && go test ./internal/archtest -run CursorSDK`_
  - _Requirements: 1.1, 3.4, 3.5, 13.1, 13.4, 13.5, 13.6, 13.7_

- [ ] 2.2 Implement backendplugin service surface
  - Describe/Configure/Execute/ListModels/Close via public ABI; conformance against fake bridge.
  - Secrets via Configure secrets map; never argv.
  - _Boundary: External connector ABI_
  - _Depends: 2.1_
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test ./... -run 'TestDescribe_|TestConfigure_|TestParity_|TestConformance')`_
  - _Requirements: 3.1, 3.2, 3.3, 3.7, 3.8, 13.2, 13.3_

- [ ] 3. Node bridge companion
- [ ] 3.1 Startup, version, models/list
  - _Validation: `(cd connectors/cursorsdk/bridge-node && npm test && npm run typecheck)`_
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 2.6, 4.1, 4.4, 11.1, 11.2, 12.3, 13.6_

- [ ] 3.2 Local agent create/send streaming
  - _Requirements: 5.1, 5.6, 6.2, 6.3, 6.4, 6.5, 6.6, 6.8, 9.1, 9.2, 9.4, 9.5, 9.6, 9.7_

- [ ] 3.3 Cancel, dispose, shutdown, open-handle tests
  - _Requirements: 5.6, 5.7, 7.1, 7.2, 7.7, 8.1, 8.5, 8.7, 9.3, 12.3, 13.9_

- [ ] 3.4 Package companion reproducibly (lockfile, engines, doctor --version)
  - _Requirements: 2.6, 3.5, 3.6, 3.7, 11.1, 11.6, 12.5, 13.6_

- [ ] 4. Go bridge owner, pool, stream
- [ ] 4.1 Bridge process owner (singleflight, handshake, bounded IO, kill tree)
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test -race ./... -run 'Bridge|Process|Kill')`_ (Linux CI)
  - _Requirements: 2.7, 3.5, 3.8, 7.3, 7.4, 7.7, 8.1, 8.5, 8.7, 8.8, 8.9, 8.10, 13.9_

- [ ] 4.2 History coordination and agent pool
  - _Requirements: 5.1–5.8, 8.2, 8.3, 8.4, 8.10, 9.2_

- [ ] 4.3 Managed canonical stream mapping
  - _Requirements: 6.1–6.10, 7.5, 7.6, 10.*, 13.8_

- [ ] 5. Host integration proofs
- [ ] 5.1 Windows/Linux BuildBootstrap e2e with staged plugin + fake bridge companion
  - Real discovery/trust/IPC; no TestLauncher/Fake DialSession; no live Cursor account.
  - _Validation: `go test ./internal/infra/runtimebundle -run TestPhase.*CursorSDK`_
  - _Requirements: 13.3, 12.2, 12.5_

- [ ] 5.2 Absence and mandatory checks
  - Root builds with connector absent; configured-missing fails closed.
  - _Requirements: 1.1, 13.7_

- [ ] 6. Documentation and experimental labeling
  - _Requirements: 1.5, 11.6, 12.7–12.9_
