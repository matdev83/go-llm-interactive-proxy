# Research & Design Decisions

## Summary
- **Feature**: billing-host-composition
- **Discovery Scope**: Extension (brownfield composition around a merged billing engine)
- **Key Findings**:
  - The billing engine, Bun journal, admission adapter, post-turn worker, and read-only `/admin/billing` reports already exist. Stock `lipstd` and public `lipruntime.Options` still cannot turn them on.
  - The real gaps are a versioned snapshot catalog, a stock `RatingResolver`, principal/session identity helpers, an in-tree composer into `ProductionOptions`, and trusted provisioning HTTP. `CreateAccount` / `PostFunding` / `ChangeCreditPolicy` exist only as `billingstore` methods.
  - Injection-only is already encoded in `build_executor.go` (`ErrAuthoritativeBillingRequired`) and `TestOptions_DoesNotExposeBillingStore`. This spec must preserve those fences, not copy metering/usage-authority YAML store-open.

## Gap Analysis (brownfield)

Analyzed against `.kiro/specs/billing-host-composition/requirements.md` (Requirements 1–8, including catalog-at-admission 3.6).

### Current State

| Area | Existing assets | Pattern |
| --- | --- | --- |
| Fail-closed YAML | `internal/infra/runtimebundle/build_executor.go`, `errors.go`, `billing_post_turn_worker_build_test.go` | `accounting.billing.authoritative` without store+admission+identity+resolver+ledger → `ErrAuthoritativeBillingRequired` |
| Public library fence | `pkg/lipruntime/options.go`, `build.go`, `build_test.go` | No billing fields; `TestOptions_DoesNotExposeBillingStore`; YAML authoritative fails closed without injection |
| Stock binary | `cmd/lipstd/command.go` | `BuildHost` with zero `Production`; does not open billing journal |
| Injection seam | `runtimebundle.ProductionOptions`, `BuildHostInput.Production` | `BillingStore`, `BillingAdmission`, `BillingIdentity`, `BillingRatingResolver`, reports path |
| Admission | `internal/infra/billingadmission.NewAdapter` | Requires store, releaser, AccountID+AuthorizationID, Policy, Pricing, currency |
| Identity stamp | `internal/core/runtime/billing_admission.go` `stampBillingIdentity` | Once after Authorize; handoff reads stamp |
| Identity source | `pkg/lipsdk/scope.ScopeFromContext`, `pkg/lipsdk/transport/httpauth` | Auth middleware attaches `PrincipalScopeView`; `lipapi.Call` has session, not principal |
| Rating | `billing.RatingResolver`, `RatingInput`, `PricingSnapshot`, `ChargePolicy`, `OperatorRateSnapshot` | Interface + test fakes only |
| Journal | `internal/infra/billingstore` `NewDurableStore` / `OpenStore` / `Migrate` | Dual SQLite/Postgres; `ComponentBilling` already in `dbmigrate` |
| Trusted commands | `DurableStore.CreateAccount`, `PostFunding`, `ChangeCreditPolicy` | No domain port; HTTP does not call them |
| Admin HTTP | `internal/stdhttp/admin/billing` GET-only; `mountBillingReports` + `diag.WrapDiagnosticsProtect` | Empty diagnostics secret → not mounted |
| Catalog | None for billing snapshots | `accounting.pricing` / `economics.RatingSnapshotSource` is dual-plane metadata, not TUR rating |
| Handoff outbox | `MemoryHandoffOutbox` | Durable Bun outbox out of scope |

Conventions: explicit constructors, no DI container, domain ports in `internal/core/billing`, Bun in `billingstore`, HTTP in `stdhttp/admin/*` talking only to domain ports. Tests: package `*_test.go` with fakes; integration tags for Postgres.

### Requirement-to-Asset Map

| Req | Need | Existing | Gap |
| --- | --- | --- | --- |
| 1 | lipstd / public Options stay non-money; YAML flag fail-closed; no request-time account create | Tests + `BuildHost` fail-closed + `CreateAccount` not on request path | **Constraint**: keep tests; do not add YAML factory. **Missing**: docs that this is intentional for this spec |
| 2 | In-tree composer into existing host builder; single journal authority; no second host | `ProductionOptions` wiring already overwrites handoff/reports from store; worker start at Publish | **Missing**: a host-owned store-open recipe plus a composer that receives an already-opened store (the host opens it) and fills identity+catalog+resolver, returning `ProductionOptions`; no DSN/YAML billing factory |
| 3 | Immutable versioned catalog; stock lookup; refs on TUR/hold; same catalog at admit and rate | Domain snapshot types + `ErrRatingSnapshotMismatch`; admission `RoutePricing`/`Policy` funcs | **Missing**: catalog type, publish-immutability, stock `RatingResolver`, admission binding from catalog (3.6) |
| 4 | Principal/session → account/auth IDs; fail closed; no invent account; no re-resolve at handoff | `BillingIdentity` funcs; context principal; session on `Call`; stamp-once | **Missing**: stock helper using `ScopeFromContext` + session; default snapshot-ref funcs from catalog |
| 5 | Trusted create/fund/credit-policy; reject untrusted; no payment gateway | Store methods + journal intents + validation | **Missing**: domain command port so HTTP/tests do not import `billingstore`; operator-visible command surface |
| 6 | Protected admin HTTP mutations; empty secret does not mount; reports reflect writes | GET handler + diagnostics wrap | **Missing**: POST (or equivalent) routes; handler options for commands; tests for secret/method |
| 7 | Injected-host money-loop proof | Piecewise tests (admission, worker, settlement, reports) | **Missing**: one composition test: provision → inject BuildHost → execute → TUR → rate → journal → report |
| 8 | Do not touch stream/B2BUA/FinalizeBilling/usage-authority/public Options | Parent engine + arch tests | **Constraint**: composition-only diffs |

### Unknowns / Research Needed (for design)

1. Catalog packaging: in-memory constructed catalog vs file loader vs both. Requirements allow any host-supplied catalog; stock in-memory + constructor is enough if file load stays host-side.
2. Stock snapshot-ref policy: one default catalog version for all calls vs per-principal/per-route selection at admit. Admission already prices per planned backend:model via `RoutePricing`.
3. Domain port for `CreateAccount`: new small `billing.AccountProvisioner` vs widening `AuthoritativeBilling`. Widening the cutover interface risks forcing every fake store to implement writes.
4. Authorization ID stock formula: session + A-leg vs principal + A-leg. Engine requires deterministic hold identity per account + TUR/A-leg.
5. Whether composer lives in `runtimebundle`, `internal/infra/billingcompose`, or beside `billingadmission`. `runtimebundle` already owns host lifecycle; a dedicated package avoids bloating `build_executor.go`.

### Implementation Approach Options

#### Option A: Extend existing components only

Extend `admin/billing/handler.go` with POSTs, add catalog types into `internal/core/billing`, put composer functions in `runtimebundle`, identity helpers in `billingadmission`.

- **Strengths**: Few packages; follows current admission/reports homes.
- **Risks**: `handler.go` and `build_executor.go` grow; catalog policy leaks into domain rating; every `AuthoritativeBilling` fake gains provision methods if the interface is widened.
- **Compatibility**: Preserves public Options and YAML fail-closed.

#### Option B: New packages for catalog, identity, composer, provisioner

New `internal/core/billingcatalog` or `internal/infra/billingcatalog`, `internal/infra/billingidentity`, `internal/infra/billingcompose`, domain `AccountProvisioner` port implemented by `billingstore`, thin admin command adapter.

- **Strengths**: Clear SRP; HTTP stays on domain ports; composer testable without full `BuildHost` internals.
- **Risks**: Too many packages for a composition spec; catalog in core vs infra is a real hexagonal choice.
- **Compatibility**: Same fences.

#### Option C: Hybrid (preferred candidate for design)

- **New**: immutable catalog + stock `RatingResolver` (composition/infra, domain types reused); stock identity helper; small domain provisioner port; composer that returns `ProductionOptions` pieces; admin command routes.
- **Extend**: `admin/billing` handler/options; `billingstore` implements provisioner; keep `ProductionOptions` / `BuildHost` fail-closed; keep `lipstd` and `lipruntime.Options` unchanged.
- **Do not**: YAML journal factory; public Options money fields; stream/TUR/rating formula edits; durable outbox.

Phasing: catalog+resolver → identity helper → provisioner port+store wiring → admin HTTP → composer → BuildHost injection test + docs.

### Complexity and Risk

- **Effort**: M (3–7 days). Engine exists; work is composition, catalog, HTTP, and one proof test. Not XL because stream/rating/journal invariants stay frozen.
- **Risk**: Medium. Identity source is context-not-Call (easy to get wrong); catalog must serve both admission and post-turn without duplicating snapshots; admin mutations must not bypass diagnostics protection; composer must not accidentally teach `lipstd` to auto-open.

### Design-phase recommendations (not decisions)

- Prefer Option C.
- Keep catalog bodies out of the journal; `VersionRef` remains `billing.VersionRef`.
- Stock identity: `scope.ScopeFromContext` / `httpauth` principal ID → account ID; `Call.Session.AuthoritativeSessionID` (else fail closed, do not use client session hint as authority) → authorization identity input; never `CreateAccount` on miss.
- Do not put `CreateAccount` on `AuthoritativeBilling`; add a narrow provisioner port.
- Composer is an explicit function called by tests and internal hosts, never by `cmd/lipstd`.
- Admin provisioning uses the same `WrapDiagnosticsProtect` as reports.
- Proof test should call `runtimebundle.BuildHost` with injected `Production`, not `lipruntime.Build`.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| YAML factory in lipstd | Open journal from `accounting.billing.*` like metering | Dogfood in stock binary | Invents accounts/prices/funds; contradicts Req 1 and parent 17.5 | Rejected by product decision |
| Public `lipruntime.Options` money fields | Library embedders inject billing | Convenient for OSS libraries | Breaks non-money Options fence and enterprise-extension doc | Out of scope |
| Injection composer + catalog + admin commands | Fill `ProductionOptions`; stock catalog/identity; trusted HTTP | Matches engine seams; testable; no lipstd invention | Hosts still write a few lines of wiring | Aligns with Req 1–8 |

## Design Decisions

### Decision: Injection composer plus catalog, not YAML or public Options
- **Context**: Req 1 and parent 17.5 forbid lipstd/YAML journal open and public library money fields.
- **Alternatives Considered**:
  1. YAML factory in lipstd like metering
  2. Billing fields on `lipruntime.Options`
  3. In-tree composer that fills `ProductionOptions` for tests and internal hosts
- **Selected Approach**: Option 3. `cmd/lipstd` stays unchanged. Public Options stay non-money.
- **Rationale**: Matches the product fence; still gives a testable enablement path.
- **Trade-offs**: Internal hosts write explicit wiring; OSS binary still does not bill.
- **Follow-up**: Architecture tests keep the two fences.

### Decision: One catalog serves admission and post-turn rating
- **Context**: Gap analysis showed admission already needs Policy and RoutePricing; post-turn needs RatingResolver bodies (Req 3.6).
- **Alternatives Considered**:
  1. Separate admit pricebook and rating catalog
  2. Single immutable `SnapshotCatalog` bound to both seams
- **Selected Approach**: Single catalog. Published versions are immutable. Defaults and optional per-route overrides must already exist in the catalog.
- **Rationale**: Prevents hold refs from diverging from rating bodies.
- **Trade-offs**: Hosts must publish complete snapshots before traffic.
- **Follow-up**: Catalog tests for missing ref, mutate-in-place rejection, admit/rate ref equality.

### Decision: Narrow AccountProvisioner port, not AuthoritativeBilling growth
- **Context**: Create/fund/credit-policy exist on `DurableStore` but HTTP must not import billingstore (Req 5, 6).
- **Alternatives Considered**:
  1. Add methods to `AuthoritativeBilling`
  2. New `billing.AccountProvisioner` implemented by the store
- **Selected Approach**: Narrow provisioner port. Fakes used only for admission do not implement it.
- **Rationale**: Interface segregation; reports remain read-only for query-only mounts.
- **Trade-offs**: Mount wiring must pass provisioner separately from reports.
- **Follow-up**: `var _ billing.AccountProvisioner = (*billingstore.DurableStore)(nil)`.

### Decision: Catalog and identity live in billingcompose; wiring helper lives in runtimebundle
- **Context**: Avoid import cycles and avoid bloating `build_executor.go`.
- **Selected Approach**: `internal/infra/billingcompose` owns catalog, stock resolver, and principal/session identity. `runtimebundle.ComposeBilling` copies those plus `billingadmission.NewAdapter` onto `ProductionOptions`. `cmd/lipstd` does not call ComposeBilling.
- **Rationale**: Composition root owns ProductionOptions; catalog package stays free of host lifecycle.
- **Trade-offs**: Two packages instead of one.
- **Follow-up**: Host-loop test calls ComposeBilling then BuildHost.

### Decision: Stock identity reads scope context and authoritative session only
- **Context**: `lipapi.Call` has no principal field. Auth middleware stores `PrincipalScopeView` on context (Req 4).
- **Selected Approach**: AccountID from `scope.ScopeFromContext` PrincipalID. AuthorizationID from `Call.Session.AuthoritativeSessionID` plus A-leg ID. Client session hints are not authority. Missing either value yields empty identity and admission fails closed. Snapshot ref funcs come from the catalog.
- **Rationale**: Reuses existing auth attribution; does not invent accounts.
- **Trade-offs**: Unauthenticated or session-less calls cannot bill under stock mapping.
- **Follow-up**: Identity unit tests for missing principal, client-hint-only session, and custom mapping override.

### Decision: Admin provisioning extends the existing protected billing handler
- **Context**: Req 6 wants the same surface as read-only reports.
- **Selected Approach**: POST commands on the existing handler; same `WrapDiagnosticsProtect` and empty-secret no-mount gate. JSON maps to domain command types. No new auth scheme.
- **Rationale**: Operators already know `/admin/billing` and diagnostics secret.
- **Trade-offs**: Handler grows beyond GET; keep command handlers in a sibling file.

### Decision: Stock RatingResolver joins hold lookup with catalog bodies
- **Context**: `PostTurnWorker.processRecord` uses `input.Authorization` from `RatingResolver`. DurableStore had no domain read for holds; test fakes inlined Authorization.
- **Alternatives Considered**:
  1. Change the worker to load the hold itself
  2. Reconstruct Authorization from TUR refs without Amount
  3. Add `AuthorizationLookup.GetAuthorization` and a compose join resolver
- **Selected Approach**: Option 3. Do not modify the worker. Do not invent hold amounts from TUR refs.
- **Rationale**: Smallest composition-shaped gap; keeps rating formulas frozen.
- **Trade-offs**: One new read port on the store.
- **Follow-up**: Store test that GetAuthorization returns the hold created by Authorize.

## Synthesis
- **Generalization**: One immutable snapshot catalog and one provisioner port cover admit, rate, HTTP, and tests. The stock RatingResolver is a join of that catalog with AuthorizationLookup, not a second pricebook.
- **Build vs adopt**: Adopt existing engine ports, store methods, diagnostics wrap, and scope context. Build only catalog, stock identity, join resolver, composer helper, provisioner/lookup ports, and admin POSTs. No new modules or libraries.
- **Simplification**: No file-format loader, no memory billing store, no durable outbox, no public Options, no lipstd YAML, no widening of AuthoritativeBilling, no PostTurnWorker rewrite.

## Risks & Mitigations
- Catalog/admission drift (different versions at hold vs rate) — bind both from one catalog (Req 3.6).
- Principal missing on `Call` — read authenticated scope from context; fail closed if absent.
- Admin POST without secret — reuse existing mount gate; tests for empty secret and missing header.
- Composer pulled into `lipstd` later — keep composer off `cmd/lipstd`; architecture test that serve path still passes zero Production billing.

## References
- Parent spec: `.kiro/specs/usage-record-ledger-billing/` (frozen; do not reopen task checkboxes)
- `docs/usage-record-billing-phase8-certification.md` — fail-closed composition gate
- `docs/enterprise-extension-boundaries.md` — billing attaches via `ProductionOptions`
- `internal/infra/runtimebundle/build_executor.go` — authoritative injection checks
- `pkg/lipruntime/build_test.go` — `TestOptions_DoesNotExposeBillingStore`
