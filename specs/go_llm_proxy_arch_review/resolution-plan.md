# Resolution plan — go-llm-interactive-proxy architecture de-slopification roadmap

Generated: 2026-07-07

## Planning principles

This plan assumes **no behavior change** unless explicitly called out. The goal is to reduce future maintenance cost while preserving the current product behavior and architectural strengths.

Guiding constraints:

1. Preserve canonical `pkg/lipapi` and `pkg/lipsdk` contracts.
2. Preserve explicit composition; do not introduce DI containers, reflection registries, global mutable registries, or `init()` registration.
3. Preserve streaming-first behavior and no-retry-after-output semantics.
4. Prefer same-package file/function extraction before adding new packages.
5. Add architecture tests before large refactors where practical.
6. Keep refactors small enough to review and bisect.
7. Do not rename architecture for aesthetics; change package boundaries only when ownership becomes clearer.

---

## Phase 0 — Baseline and safety rails

**Goal:** Make architectural mass visible before refactoring.

**Risk:** Low

**Expected duration:** Small/medium

### Task 0.1 — Generate architecture metrics report

Create a script or Go test helper that outputs:

- non-test Go lines by package;
- non-test Go lines by file;
- direct import count per package;
- top internal fan-out packages;
- top internal fan-in packages;
- exported symbol count for `pkg/lipapi` and `pkg/lipsdk`;
- current hexagonal baseline classifications.

Suggested path:

```text
internal/archtest/report_test.go          // tests/metrics helpers
scripts/arch-report.go or scripts/arch-report.ps1
```

Acceptance criteria:

- `make arch-report` or equivalent produces deterministic Markdown/JSON output.
- Report does not fail CI initially.
- Output highlights `runtimebundle`, `pluginreg`, `stdhttp`, and `runtime.Executor` hotspots.

### Task 0.2 — Add critical-file warning budgets

Add advisory warnings or tests for high-risk files:

- `internal/core/runtime/executor.go`
- `internal/infra/runtimebundle/build.go`
- `internal/infra/runtimebundle/options.go`
- `internal/stdhttp/server.go`
- `internal/pluginreg/standard_table.go`

Acceptance criteria:

- Budgets are initially warnings or generous limits.
- Any future budget increase must include a short rationale.
- The check is not noisy enough to block useful development immediately.

### Task 0.3 — Convert `extract` baseline entries into actionable backlog

The hexagonal baseline already marks `runtimebundle` and `internal/core/extensions` as `extract` and `pluginreg` as `exception`.

Add fields or a companion Markdown file with:

- owner/area;
- next intended extraction;
- retirement target;
- blocking dependencies;
- status.

Acceptance criteria:

- Every `extract` or `exception` classification has a linked task or plan item.
- The baseline stops being only a snapshot and becomes a controlled migration register.

---

## Phase 1 — Low-risk cleanup and canonical path simplification

**Goal:** Remove obvious transitional indirection and reduce reading cost before deeper refactors.

**Risk:** Low/medium

### Task 1.1 — Make `runtimebundle.Build + stdhttp.RunWithRuntime` the canonical path

`stdhttp.Run` already documents that composition roots should normally build once and then call `RunWithRuntime`. Formalize that.

Implementation options:

- Keep `Run` but mark it compatibility/convenience only.
- Move `Run` to a test/helper path if production no longer uses it.
- Remove `Run` if it has no meaningful call sites and this is acceptable before v1.

Acceptance criteria:

- `cmd/lipstd` uses one explicit build path.
- Tests use the same path or clearly named test helpers.
- No duplicate runtime assembly pass exists in production startup.

### Task 1.2 — Remove or deprecate `stdhttp.BuildExecutor`

`internal/stdhttp/wire.go` is a thin wrapper around `runtimebundle.BuildExecutor`.

Steps:

1. Search all call sites.
2. Replace internal call sites with `runtimebundle.BuildExecutor` or preferably `runtimebundle.Build`.
3. Delete wrapper if unused, or mark deprecated if a stable internal compatibility seam is needed.

Acceptance criteria:

- One fewer redundant wiring API.
- No change in runtime behavior.

### Task 1.3 — Split `stdhttp/server.go` by concern without changing package

Move functions/blocks into focused files:

```text
internal/stdhttp/server.go
internal/stdhttp/handler.go
internal/stdhttp/middleware.go
internal/stdhttp/mount_diagnostics.go
internal/stdhttp/mount_metrics.go
internal/stdhttp/mount_admin.go
internal/stdhttp/mount_securesession.go
internal/stdhttp/mount_frontends.go
```

Do not create subpackages unless import cycles or clear ownership reasons appear.

Acceptance criteria:

- Same exported API.
- Same middleware order.
- Same mounted routes.
- Same shutdown/closer behavior.
- `server.go` becomes primarily listener lifecycle and `RunWithRuntime`.

### Task 1.4 — Add route/middleware preservation tests if missing

Before or during `stdhttp` extraction, add tests that assert:

- diagnostics require shared-secret protection where expected;
- metrics path behavior unchanged;
- frontend routes are mounted;
- middleware order preserves panic recovery, auth, trace/request ID, access logs, metrics/tracing.

Acceptance criteria:

- Refactor has test coverage for route/middleware behavior.
- No route accidentally becomes public/unprotected.

---

## Phase 2 — Runtimebundle decomposition

**Goal:** Keep `runtimebundle.Build` as a stable facade but make internals composable and inspectable.

**Risk:** Medium/high because startup wiring is broad.

### Task 2.1 — Introduce explicit build context/result structs

Create internal structs such as:

```go
type buildContext struct {
    Cfg *config.Config
    Log *slog.Logger
    Opts normalizedBuildOptions
    Parent context.Context
    Closers closerStack
}

type observabilityRuntime struct { ... }
type securityRuntime struct { ... }
type modelRuntime struct { ... }
type backendRuntime struct { ... }
type persistenceRuntime struct { ... }
type extensionRuntime struct { ... }
type executorRuntime struct { ... }
```

Acceptance criteria:

- Closer handling remains LIFO and error-safe.
- Startup error paths still dispose resources.
- Existing tests continue to pass.

### Task 2.2 — Extract observability and HTTP client construction

Move metrics bundle creation, upstream HTTP tuning, metrics wrapping, and outbound tracing wrapping into one build unit.

Suggested function:

```go
func buildObservabilityRuntime(ctx buildContext) (observabilityRuntime, error)
```

Acceptance criteria:

- All HTTP-client wrapping remains behaviorally identical.
- Tracing and Prometheus behavior unchanged.
- Build body shrinks.

### Task 2.3 — Extract auth/security runtime construction

Move auth event dispatcher, session audit policy, HTTP auth providers, backend security profile validation, OS identity, remote decider, and auth renderers into a focused security/auth build unit.

Acceptance criteria:

- Auth startup failures preserve current error messages as much as practical.
- Local/noop and remote auth behavior unchanged.
- Backend local-only access-scope checks remain centralized.

### Task 2.4 — Extract model catalog/registry runtime

Move model catalog startup, backend inventory integration, model registry runtime, refresh loop, and registry closers into a model runtime builder.

Acceptance criteria:

- Model inventory refresh behavior unchanged.
- Cache behavior unchanged.
- Closers registered correctly.

### Task 2.5 — Extract persistence runtime

Move continuity store opening, secure-session store construction, control-plane store wrapping, and B2BUA wrapping into persistence/security-specific units.

Acceptance criteria:

- SQLite/Postgres/memory behavior unchanged.
- TTL/max-leg semantics unchanged.
- Secure-session diagnostics store remains available to `Built`.

### Task 2.6 — Extract extension runtime snapshot building

Move `buildRuntimeSnapshot` and option merging into a dedicated extension runtime builder.

Acceptance criteria:

- Snapshot remains immutable per build generation.
- Feature-bundle surfaces are cloned/merged as before.
- Traffic, usage, policy observer composition unchanged.

### Task 2.7 — Extract executor construction

Move the `runtime.Executor` field assembly into one focused function that accepts already-built sub-runtimes.

Acceptance criteria:

- Executor receives the same fields/values.
- Error paths still dispose resources.
- `Build` reads as orchestration over build units.

### Task 2.8 — Normalize `BuildOptions`

Add grouped options and migrate internal code to use normalized groups.

Possible staged strategy:

1. Define grouped options.
2. Add `normalizeBuildOptions(old BuildOptions) normalizedBuildOptions`.
3. Keep old fields for compatibility.
4. Migrate call sites gradually.
5. Remove old fields before v1 if feasible.

Acceptance criteria:

- Internal build units consume grouped options.
- Tests clarify production vs testing overrides.
- No behavior change.

---

## Phase 3 — Split registry from standard distribution bundle

**Goal:** Make `pluginreg` a true registry package and move standard bundle ownership elsewhere.

**Risk:** Medium/high because standard plugin registration touches many adapters.

### Task 3.1 — Define target package ownership

Choose final package names. Suggested options:

Option A:

```text
internal/pluginreg
internal/standardplugins
internal/featurebundle
```

Option B:

```text
internal/pluginreg
internal/pluginreg/standard
internal/pluginreg/featuremerge
```

Option A is cleaner conceptually. Option B is less disruptive but keeps everything visually under pluginreg.

Recommended: **Option A** if no import-cycle problems appear.

Acceptance criteria:

- Architecture docs updated with the chosen ownership.
- `pluginreg` is explicitly described as registry only.

### Task 3.2 — Move standard bundle tables out of `pluginreg`

Move `StandardBundle`, `StandardBackendBundle`, standard frontend/backend/feature registration entries, and concrete plugin imports into a standard distribution package.

Acceptance criteria:

- Concrete plugin imports no longer live in the narrow registry package.
- Standard bundle install still happens explicitly from `cmd/lipstd` or standard composition root.
- Architecture tests updated to allow concrete plugin imports only in the standard bundle package.

### Task 3.3 — Move frontend mount functions closer to frontend ownership

Move `mountOpenAIResponses`, `mountOpenAILegacy`, `mountAnthropic`, and `mountGemini` into frontend packages or the new standard bundle package.

Preferred medium approach:

- Each frontend package exposes `Mount(mux, opts)`.
- Standard bundle registers `frontopenairesponses.Mount`, etc.

Acceptance criteria:

- Mount paths are protocol-owned.
- Repeated handler field wiring is reduced.
- No generic abstraction unless it clearly reduces code.

### Task 3.4 — Move feature-bundle merge/migration helpers

Move `FeatureFactoryFromHooks` and hook-to-bundle compatibility logic out of registry.

Acceptance criteria:

- Registry package does not import `internal/core/hooks` solely for migration wrapping.
- New feature plugins use native `FeatureBundle` factories.
- Hook bridge has an explicit retirement target.

### Task 3.5 — Narrow or retire `pluginreg` exception baseline

Update `testdata/architecture/hexagonal_migration_baseline.json` after the split.

Acceptance criteria:

- `pluginreg` classification moves from `exception` to `aligned` or at least has fewer allowed `internal/core` imports.
- Retirement trigger is either satisfied or narrowed.
- Tests enforce the new boundary.

---

## Phase 4 — Executor collaborator extraction

**Goal:** Keep the executor API stable while reducing internal complexity.

**Risk:** High because runtime behavior is subtle.

### Task 4.1 — Add characterization tests around `Execute`

Before extraction, ensure tests cover:

- call validation failure;
- nil context behavior;
- secure-session required behavior;
- selector alias resolution;
- model-only selector defaulting;
- unresolved model-only failure;
- pre-output failover;
- no retry after output begins;
- B-leg lifecycle registration/cancellation;
- route preference behavior;
- affinity identity behavior;
- stream recovery behavior;
- accounting preflight/ledger failure behavior;
- interleaved-thinking wrapper selection.

Acceptance criteria:

- Tests lock behavior before refactor.
- Refactor can be reviewed as mechanical extraction.

### Task 4.2 — Group executor fields

Introduce internal grouped structs:

```go
type RoutingRuntime struct { ... }
type SecurityRuntime struct { ... }
type AccountingRuntime struct { ... }
type ObservabilityRuntime struct { ... }
type ExtensionRuntime struct { ... }
type InterleavedRuntime struct { ... }
```

Migrate `Executor` fields gradually. Compatibility can be preserved by keeping exported fields temporarily if tests or packages set them directly.

Acceptance criteria:

- Field count in `Executor` decreases or is grouped semantically.
- Runtimebundle executor construction becomes clearer.
- Tests setting fields are migrated to builders/helpers.

### Task 4.3 — Extract request preparation

Create a request preparation collaborator that handles:

- call validation;
- runtime snapshot attachment;
- secure-session readiness and begin-turn path;
- submit hooks;
- A-leg preparation;
- context enrichment.

Acceptance criteria:

- `Execute` delegates preparation to a named component.
- Existing error wrapping remains stable enough for tests/operators.

### Task 4.4 — Extract route planning setup

Create a route planning setup component that handles:

- selector aliasing;
- parse/default backend;
- attempt budget;
- TTFT budget;
- session routing state;
- excluded set;
- request-size estimate;
- affinity key;
- route preferences;
- interleaved state loading if it belongs to planning.

Acceptance criteria:

- Route setup can be unit-tested without backend opens.
- `Execute` loop starts with a compact planning state object.

### Task 4.5 — Extract attempt opening

Move planning/opening attempt logic behind a collaborator that receives planning state and returns an opened stream or a continue/failure decision.

Acceptance criteria:

- Capability negotiation and context-limit behavior unchanged.
- B-leg lifecycle behavior unchanged.
- Parallel/race/failover behavior unchanged.

### Task 4.6 — Extract stream assembly

Move retry stream creation and wrapper decisions into a stream assembler:

- retry stream fields;
- accounting tracker;
- recovery policy;
- secure-turn propagation;
- interleaved hidden/visible wrapping.

Acceptance criteria:

- `Execute` ends by delegating to stream assembly.
- Stream behavior unchanged.
- New assembler tests cover wrapper selection and stream state.

### Task 4.7 — Add executor file budget

After extraction, set a reasonable file budget for `executor.go` and maybe sub-files.

Acceptance criteria:

- New code cannot casually re-bloat `executor.go`.
- Budget changes require rationale.

---

## Phase 5 — Core boundary tightening

**Goal:** Prevent `internal/core` from becoming a bucket for everything cross-cutting.

**Risk:** Medium

### Task 5.1 — Classify existing core packages

Create a short table in `docs/architecture.md` or a new `docs/core-boundaries.md`:

| Package | Classification | Reason it belongs in core | Adapter leakage risk |
| --- | --- | --- | --- |
| `runtime` | use-case orchestration | executes canonical calls | high |
| `routing` | policy | cross-protocol route semantics | medium |
| `b2bua` | policy/state seam | continuity semantics | medium |
| `diag` | canonical diagnostics contract/support | operator views | medium |
| ... | ... | ... | ... |

Acceptance criteria:

- New contributors have clear guidance.
- Ambiguous packages are identified.

### Task 5.2 — Add core admission checklist

Add to PR template or architecture docs:

- Is this cross-protocol product policy?
- Does it import or mention provider-specific concepts?
- Is it HTTP/operator presentation rather than policy?
- Could it be a plugin/feature/adapter?
- Does it do durable I/O directly?
- Is the interface defined where consumed?

Acceptance criteria:

- Architecture review becomes repeatable.
- Core additions include a short justification.

### Task 5.3 — Add package-doc rule for new core packages

Architecture test can require `doc.go` or package comment for new `internal/core/*` packages, at least for non-trivial ones.

Acceptance criteria:

- New core package explains its boundary.
- Package purpose is reviewable without reading all code.

---

## Phase 6 — Feature extension platform cleanup

**Goal:** Finish migration from hook-era concepts to explicit feature bundle seams.

**Risk:** Medium

### Task 6.1 — Inventory hook-only feature factories

List all feature factories still using hook-only adapters.

Acceptance criteria:

- A Markdown checklist exists.
- Each item has migrate/keep/drop decision.

### Task 6.2 — Convert bundled features to native `FeatureBundle`

For each reference/noop feature, return `FeatureBundle` directly where practical.

Acceptance criteria:

- `FeatureFactoryFromHooks` usage drops.
- Tests confirm feature registration and behavior unchanged.

### Task 6.3 — Retire or quarantine legacy bridge

Move bridge to a compatibility package or delete when no longer needed.

Acceptance criteria:

- Registry package no longer imports core hooks for legacy bridging.
- Architecture baseline narrows accordingly.

---

## Phase 7 — Governance and long-term maintenance

**Goal:** Keep architecture from regressing as features and enterprise overlays grow.

**Risk:** Low

### Task 7.1 — Add architecture PR checklist

Suggested checklist:

```md
- [ ] Does this change keep provider/protocol details out of core?
- [ ] Does this change avoid new global state, init registration, and lazy singleton registries?
- [ ] Does this change keep canonical streaming as the primary execution path?
- [ ] Does this change add a new core package? If yes, why does it belong in core?
- [ ] Does this change widen public contracts? If yes, is it versionable and minimal?
- [ ] Does this change increase architecture budgets? If yes, is the reason documented?
```

Acceptance criteria:

- Checklist exists in PR template or contributor docs.
- Reviewers use it for broad changes.

### Task 7.2 — Add architecture drift report to CI artifacts

CI can publish arch metrics without failing initially.

Acceptance criteria:

- A contributor can see file/package growth over time.
- Hotspots become visible before they become painful.

### Task 7.3 — Define enterprise overlay boundary

Given your stated open-core direction, define where enterprise features attach:

- OSS core emits events or exposes stable extension seams.
- Enterprise package/binary registers features through `pkg/lipsdk` and composition root options.
- Enterprise should not import deep runtime internals unless a deliberate internal extension boundary exists.
- Audit/search/billing/SSO/user provisioning should attach via SDK seams, control-plane ports, or standard composition options, not by editing `runtime.Executor` directly.

Acceptance criteria:

- A document such as `docs/enterprise-extension-boundaries.md` exists.
- It lists allowed and forbidden integration points.
- It prevents enterprise code from becoming a parallel fork of core.

---

## Suggested implementation order

### First pull request group — very low risk

1. Add architecture metrics report.
2. Add critical-file warning budgets.
3. Split `stdhttp/server.go` by file/concern without changing APIs.
4. Deprecate/remove `stdhttp.BuildExecutor` if feasible.

### Second pull request group — composition cleanup

1. Normalize `runtimebundle.BuildOptions` internally.
2. Extract observability/auth/model/persistence/extension/executor build units.
3. Add tests for resource cleanup and startup error paths.

### Third pull request group — registry split

1. Create standard bundle package.
2. Move `standard_table.go` and installer code.
3. Move feature bridge out of registry.
4. Narrow hexagonal baseline exception.

### Fourth pull request group — executor internals

1. Add characterization tests.
2. Group executor fields.
3. Extract request preparation.
4. Extract route planning setup.
5. Extract attempt opening.
6. Extract stream assembly.
7. Add executor file budget.

### Fifth pull request group — governance and enterprise boundary

1. Add core admission checklist.
2. Add package-doc rule for new core packages.
3. Define enterprise overlay boundary.
4. Add CI architecture report artifact.

---

## Acceptance definition for the whole effort

The de-slopification effort can be considered successful when:

- `runtimebundle.Build` is a readable orchestration facade over smaller build units.
- `pluginreg` no longer owns concrete standard bundle tables and legacy feature migration logic.
- `runtime.Executor` still exposes `Execute`, but internal concerns are grouped and testable.
- `stdhttp` is split by HTTP concern and no longer mixes all server behavior in one file.
- Architecture tests cover not only forbidden imports, but also critical file/package drift.
- The hexagonal baseline has fewer `extract`/`exception` entries or more precise retirement targets.
- No behavior changes are visible to clients.
- The project remains Go-idiomatic: explicit construction, small interfaces, no DI container, no reflection magic, no hidden registration.

