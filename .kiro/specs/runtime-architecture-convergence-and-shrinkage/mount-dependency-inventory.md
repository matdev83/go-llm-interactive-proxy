# Task 3.1 — stdhttp mount dependency inventory

Deterministic inventory of production HTTP mount helpers and their current
`runtimebundle.Built` / composition dependencies. Desired cohesive groups follow
approved `design.md` names (`StandardHTTPInput` / `HTTP*Input`).

Lifecycle/closer at every mount boundary: **none** (ownership stays in
generation/process composers; mounts must not accept closers).

## Desired composition groups (design § StandardHTTPInput)

| Group field | Type | Concern |
| --- | --- | --- |
| `Core` | `HTTPCoreInput` | execution / routing capability only |
| `Security` | `HTTPSecurityInput` | auth / session / authority surfaces only |
| `Operations` | `HTTPOperationsInput` | metrics / tracing / diagnostics / admin query only |
| `Models` | `HTTPModelInput` | catalog / inventory diagnostic surfaces only |
| `Frontends` | `HTTPFrontendInput` | executor / registry / default-route / frontend configuration only |

Prohibited in every group: `*runtimebundle.Built`, `runtimebundle.RequestPlane`,
resource ledger, generic closer list, `io.Closer`, close/shutdown callbacks,
host/coordinator lifecycle owner, generic dependency getter.

## Mount helpers

| Helper | Source | Current Built / RequestPlane fields read | Desired group(s) + capability | Route(s) | Behavior test(s) | Lifecycle at mount |
| --- | --- | --- | --- | --- | --- | --- |
| `mountMetrics` | `internal/stdhttp/mount_metrics.go` / `mountMetricsInput` | `Built.Metrics` (Registry, HTTP) | `Operations`: Metrics bundle | configured metrics path (default `/metrics`) | `TestRunWithRuntime_metricsEnabledRequiresBuiltMetrics`; `TestStackHTTPHandler_recoveredPanic_combinedMetricsAccessAndSafeBody` | none |
| `mountDiagnostics` | `internal/stdhttp/mount_diagnostics.go` / `mountDiagnosticsInput` | `Built.Store`; `Built.SecretGuardInventory`; also `Exec`, `Reg`, `App`/`Registrations` (non-Built params) | `Operations`: Store, SecretGuardInventory, inventory/route-trace/pprof sinks; `Core`: Executor route-trace attach | health / attempts / inventory / route-trace / pprof (config paths) | `TestStandardMiddlewareMountParity_ComposeRequestPlaneRouteSetAndStack`; diagnostics suites under `internal/stdhttp` | none |
| `mountModelCatalogDiagnostics` | `internal/stdhttp/mount_diagnostics.go` / `diagnosticsMount` | `Built.CatalogRuntime` | `Models`: CatalogRuntime | `model_catalog.diagnostics_path` | `TestModelCatalogDiagnostics_*` | none |
| `mountModelInventoryDiagnostics` | `internal/stdhttp/mount_diagnostics.go` / `diagnosticsMount` | `Built.ModelRegistryRuntime` | `Models`: ModelRegistryRuntime | `model_inventory.diagnostics_path` | `TestModelRegistryStatusHandler_*`; `TestNewStandardHandler_openAIModelsAndModelRegistryDiagMounted` | none |
| `mountSecureSessionDiagnostics` | `internal/stdhttp/mount_securesession.go` / `mountSecureSessionDiagnosticsInput` | `Built.SecureSessionStore` | `Security`: SecureSessionStore | `secure_session.diagnostics_path_prefix` (`GET` base + `/`) | `TestSecureSessionDiagnostics_mount_matchesRunWithRuntimePattern` | none |
| `mountAccountingAdmin` | `internal/stdhttp/mount_admin.go` / `mountAccountingAdminInput` | `Built.TokenAccountingAdmin`; fallback `Built.Executor.AdminCountService` | `Operations`: TokenAccountingAdmin; `Core`: Executor admin-count fallback | `accounting.admin.path` | `TestTokenAccountingAdminMounted*`; `TestTokenAccountingAdminDisabledNotRegistered` | none |
| `mountControlPlaneQuery` | `internal/stdhttp/mount_admin.go` / `controlPlaneQueryMount` | `Built.ControlPlaneQueries`; `Built.ReadinessReport` | `Operations`: ControlPlaneQueries, ReadinessReport | `control_plane.query.path_prefix` (+ `/`) | `TestControlPlaneQuery_MountedWhenEnabledAndProtected`; `TestControlPlaneQuery_NotMountedWhenDisabled` | none |
| `mountAccountingAuthorityQuery` | `internal/stdhttp/mount_admin.go` / `accountingAuthorityQueryMount` | `Built.UsageAuthority`; `Built.ConcurrencyAuthority`; `Built.Executor.ConcurrencyProvider` | `Security`: UsageAuthority, ConcurrencyAuthority; `Core`: ConcurrencyProvider | `accounting.authority.query.path_prefix` (+ `/`, leases) | `TestAccountingAuthorityQuery*` (`authority_mount_test.go`) | none |
| `MountBundledFrontends` | `internal/stdhttp/mount.go` / `MountBundledFrontendsInput` | _(no Built field today)_ — `Exec`, `Reg`, route/plugins/admission/traffic from caller | `Frontends`: Executor, Registry, DefaultRoute, RoutePrefixes, Plugins, DecodeAdmission, TrafficPorts, PreRequestKeepalive | frontend protocol routes (+ A-leg cancel via `mountALegCancel`) | `TestMountBundledFrontends_*`; `TestFrontendMountOptions_*`; `TestBundledFrontends_authRequired_*` | none |
| `MountBundledFrontendsLegacy` | `internal/stdhttp/mount.go` | delegates to `MountBundledFrontends` | `Frontends` (same) | same as bundled frontends | `TestMountBundledFrontends_*` | none |
| `mountALegCancel` | `internal/stdhttp/cancel.go` | _(no Built)_ — `Exec` only | `Frontends` / `Core`: Executor cancel | A-leg cancel prefix | frontend mount suites | none |
| `stackHTTPHandler` | `internal/stdhttp/middleware.go` / `stackHTTPInput` | `Built.HTTPAuthProviders` | `Security`: HTTPAuthProviders; Operations metrics via `HTTPProm` param; tracing from cfg | _(wraps entire mux)_ | `TestStandardMiddlewareMountParity_StackOrderObservables`; `TestMountContract_StackAuthBeforeInnerHandler`; `TestDownstreamServerMiddleware_*`; `TestOuterRecoveryMiddleware_*` | none |

## Composition roots (consume mounts)

Composition roots are inventoried separately from mount helpers. Task 3.2
requires mounts and the focused composer to stop accepting broad bags; it does
**not** require every composition root to accept `StandardHTTPInput` yet.

| Helper | Source | Current broad input | Desired after focused seam | Migration status | Behavior test(s) | Lifecycle at mount |
| --- | --- | --- | --- | --- | --- | --- |
| `prepareStandardHandler` | `internal/stdhttp/handler.go` | `*runtimebundle.Built` (+ reads Closers, Executor, EffectiveDefaultRoute, PluginRegistry, DecodeAdmission, RuntimeSnapshot, RoutePrefixes, ModelRegistryRuntime) | `StandardHTTPInput` groups; closers owned by caller/generation | **strict Task 3.2 target** — focused composer/preparer; must accept `StandardHTTPInput` and no lifecycle owner | `TestNewStandardHandler_*`; mount parity | closers owned **above** mount boundary |
| `NewStandardHandler` | `internal/stdhttp/handler.go` | `*runtimebundle.Built` | project to `StandardHTTPInput` then invoke focused composer; mounts never see `Built` after 3.2 | **transitional legacy adapter** — broad source `*Built` input allowed until Phase 4 caller migration/deletion; not a Task 3.2 strict mount-signature failure | same | closers owned above |
| `ComposeRequestPlane` | `internal/stdhttp/request_plane.go` | `runtimebundle.RequestPlane` → `requestPlaneAsBuilt` | project directly to focused groups / `StandardHTTPInput`; mounts never see `RequestPlane`/`Built` after 3.2 | **transitional generation adapter** — source `RequestPlane` allowed until Task 3.5; `requestPlaneAsBuilt` remains explicitly scheduled for Task 3.5 (Task 3.2 may delete it naturally); not a Task 3.1 BuiltDependency mount-signature failure | `TestComposeRequestPlane_RouteConflictRejects`; `TestComposeRequestPlane_ManagementRoutesNotMounted`; `TestStandardMiddlewareMountParity_*` | none at mount; generation owns resources |

Task 3.2 live architecture gates fail only on strict surfaces (mount helpers +
`prepareStandardHandler`). Scanner detection of transitional adapter bags remains
for capability proofs; Task 3.5 / Phase 4 own final adapter source-signature
prohibition and deletion.

## Middleware order (outer → inner; frozen)

`DownstreamServerMiddleware` → `outerRecoveryMiddleware` → optional OTel HTTP → optional Prometheus HTTP → `TraceMiddleware(RequestIDMiddleware)` → access log → `RecoveryMiddleware` → transport `auth.Middleware` → route mux.

Evidence: `TestStandardMiddlewareMountParity_StackOrderObservables`, `TestMountContract_StackAuthBeforeInnerHandler`, `TestMountContract_NilOptionalCapabilitiesSkipMounts`.
