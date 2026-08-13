# Implementation Plan

TDD throughout (RED → GREEN → REFACTOR). Do not modify stream handlers, TUR sealing, rating formulas, connector FinalizeBilling, `cmd/lipstd` Production injection, or public `lipruntime.Options`. Journal open stays host-supplied; this spec never adds a YAML billing factory.

## 1. Foundation: trusted command and hold-read ports

- [x] 1.1 Expose trusted account create, funding, and credit-policy as a domain command port
  - Add a narrow provisioner port matching the existing durable-store create, funding, and credit-policy commands.
  - Do not add those methods to the authoritative settlement/reporting cutover interface.
  - Compile-time assert the durable store implements the port; admission-only fakes must still compile without it.
  - Observable: a test helper can create an account, post funding, and change credit policy through the port without importing the store package.
  - _Requirements: 5.1, 5.2, 5.3, 5.6_
  - _Boundary: AccountProvisioner_
  - _Validation: `go test -run TestDurableStore_AccountProvisionerPort ./internal/infra/billingstore`_

- [x] 1.2 (P) Expose a read of the durable authorization hold for one turn
  - Add a narrow hold-lookup port that returns the existing hold for an account and turn key without creating, extending, or releasing it.
  - Implement it on the durable store using the existing hold row; missing hold fails closed.
  - Observable: after Authorize, lookup returns the same id, amount, and snapshot refs; a missing turn key does not invent a hold.
  - _Requirements: 3.1, 7.1_
  - _Boundary: AuthorizationLookup_
  - _Validation: `go test -run TestDurableStore_GetAuthorization ./internal/infra/billingstore`_

## 2. Catalog, identity, and stock rating join

- [x] 2.1 Publish immutable pricing, policy, and operator-rate snapshots
  - Catalog put of a new version succeeds; put of the same version with a different body is rejected; identical replay is allowed.
  - Defaults and optional per-route / per-model bindings must already exist in the catalog.
  - Lookup by stored version refs returns exact bodies or fails closed with no substitute version.
  - Bodies stay in the catalog; nothing is written to the journal.
  - Observable: unit tests prove immutability, missing-ref failure, default bind, and route override lookup.
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: SnapshotCatalog_
  - _Validation: `go test ./internal/infra/billingcompose`_

- [ ] 2.2 (P) Map billing identity from authenticated principal and authoritative session
  - Stock mapping uses the authenticated principal id as the billing account id.
  - Authorization identity uses proxy-owned session id plus A-leg id; client session hints are ignored.
  - Missing principal or authoritative session yields empty identity so admission denies; mapping never creates an account.
  - Snapshot-ref funcs are supplied by the catalog (or a test stub of those funcs).
  - Observable: unit tests cover happy path, missing principal, client-hint-only session, and empty A-leg.
  - _Requirements: 4.1, 4.2, 4.3, 1.5_
  - _Boundary: PrincipalSessionIdentity_
  - _Validation: `go test -run TestPrincipalSessionIdentity ./internal/infra/billingcompose`_

- [ ] 2.3 Join hold lookup with catalog bodies as the stock rating resolver
  - Stock rating lookup loads the durable hold, then catalog bodies for the sealed usage record’s version refs.
  - Missing hold or missing snapshot fails closed and does not invent rates or a synthetic hold amount.
  - Do not change the post-turn worker.
  - Observable: resolver tests return a full rating input whose authorization amount matches the hold and whose snapshot refs match the usage record.
  - _Depends: 1.2, 2.1_
  - _Requirements: 3.1, 3.2, 7.1, 7.3_
  - _Boundary: JoinRatingResolver_
  - _Validation: `go test -run TestNewRatingResolver ./internal/infra/billingcompose`_

## 3. Trusted admin provisioning commands

- [ ] 3.1 (P) Accept trusted create, funding, and credit-policy commands on the admin billing surface
  - POST create-account, funding, and credit-policy on the existing protected billing mux; GET reports stay unchanged.
  - Map JSON to existing domain commands; prepaid opens with zero balance and zero credit limit; funding is a separate command.
  - Invalid input is 400, identity conflict 409, missing account 404; no payment, invoice, VAT, or FX fields.
  - Observable: handler tests create, fund, and change policy, then GET account reflects journal state; untrusted methods on those paths are rejected.
  - _Depends: 1.1_
  - _Requirements: 5.1, 5.2, 5.3, 5.5, 5.6, 6.1, 6.4, 6.5_
  - _Boundary: Admin billing commands_
  - _Validation: `go test ./internal/stdhttp/admin/billing`_

- [ ] 3.2 Protect provisioning with the existing diagnostics secret and mount gate
  - Pass the provisioner through HTTP operations into the billing mount; empty diagnostics secret still mounts nothing, including POSTs.
  - Missing diagnostics protection rejects provisioning; client frontend routes stay free of these commands.
  - Update the mount-contract inventory for the new operations field.
  - Observable: empty-secret and missing-header tests fail closed for POST; a mounted surface with secret accepts POST and GET.
  - _Depends: 3.1_
  - _Requirements: 5.4, 6.1, 6.2, 6.3, 6.5_
  - _Boundary: Admin billing commands_
  - _Validation: `go test -run TestBillingReports ./internal/stdhttp`_

## 4. Injection composer

- [ ] 4.1 (P) Compose a complete Production injection from an opened journal and catalog
  - Helper fills store, admission (catalog-backed pricing and policy), identity, rating resolver, reports, and authoritative enablement without opening a database.
  - Incomplete input (missing store capabilities, catalog defaults, currency, or model max-output bound) fails closed.
  - Nil identity uses the stock principal/session mapping; a custom mapping that returns empty identity still fails closed at admission.
  - Single store is handoff, reports, hold release, provisioner, and hold lookup. No second host builder and no DI container. `lipstd` does not call this helper.
  - Observable: compose tests produce a Production value that BuildHost accepts; incomplete compose returns an error before BuildHost.
  - _Depends: 2.1, 2.2, 2.3, 1.1_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 3.6, 4.6, 7.4_
  - _Boundary: ComposeBilling_
  - _Validation: `go test -run TestComposeBilling ./internal/infra/runtimebundle`_

- [ ] 4.2 (P) Copy the provisioner onto the HTTP operations projection when the store implements it
  - Generation HTTP compile copies the existing provisioner port from the injected store onto HTTP operations. Do not redefine the operations field here; task 3.2 owns that contract.
  - This is an explicit integration task between the durable store and the admin mount.
  - Observable: an authoritative BuildHost test with an injected store that implements the provisioner exposes that port on HTTP operations.
  - _Depends: 3.2, 1.1_
  - _Requirements: 6.1, 2.3_
  - _Boundary: runtimebundle HTTP projection_
  - _Validation: `go test -run TestBuildConfigAuthoritativeBilling ./internal/infra/runtimebundle`_

## 5. Injected money-loop proof and fences

- [ ] 5.1 (P) Prove an injected host can authorize, execute, seal, rate, and journal
  - Open a durable sqlite journal, publish catalog versions, compose injection, BuildHost with Production, provision a funded prepaid account through the trusted command port (not admin HTTP), attach authenticated principal and authoritative session, execute one billable turn against a stub backend.
  - Stamped admission snapshot refs must be the same catalog versions the post-turn rating lookup uses.
  - After the post-turn worker processes, journal-backed account/turn reports show customer and operator results, not stream reinterpretation.
  - Proof must not start `lipstd` from YAML and must not add public library billing fields.
  - Observable: focused host-loop test reaches processed usage and a journal-backed customer result consistent with the catalog.
  - _Depends: 4.1_
  - _Requirements: 7.1, 7.2, 7.4, 3.6, 4.1, 4.5, 8.5_
  - _Boundary: Host loop test_
  - _Validation: `go test -run TestBillingHostLoop ./internal/infra/runtimebundle`_

- [ ] 5.2 Fail closed when stamped snapshot refs are missing from the catalog
  - Same injected host stamps refs the catalog does not contain; processing must not invent rates or mark the turn fully processed as a successful settlement.
  - Observable: test asserts retryable or unreconciled/terminal processing without a fabricated customer charge.
  - _Depends: 5.1_
  - _Requirements: 7.3, 3.2_
  - _Boundary: Host loop test_
  - _Validation: `go test -run TestBillingHostLoop_MissingCatalogRefs ./internal/infra/runtimebundle`_

- [ ] 5.3 (P) Keep stock binary and public library injection-only
  - Public library options still have no billing journal, account, catalog, or rating-lookup fields.
  - YAML `accounting.billing.authoritative` without injection still fails closed; leftover `accounting.ledger.*` and `accounting.pricing` YAML is not a billing factory and does not open a journal.
  - Standard distribution still does not inject Production billing or invent accounts when the flag is unset or when it is set without injection.
  - Diff does not change stream money, B2BUA/retry/failover, FinalizeBilling money ABI, usage-authority monetary rules, TUR sealing, or rating formulas.
  - Observable: existing Options and fail-closed tests stay green; a lipstd/config test proves flag-unset serve composition injects no billing Production and leftover ledger/pricing YAML does not open a journal; `git diff` for this spec excludes stream/collector/rating-formula/FinalizeBilling paths.
  - _Depends: 4.1_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 3.5, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6_
  - _Boundary: Fence tests/docs_
  - _Validation: `go test -run TestOptions_DoesNotExposeBillingStore ./pkg/lipruntime` ; `go test -run TestBuildConfigAuthoritativeBillingRequiresInjectedStore ./internal/infra/runtimebundle` ; `go test -run TestBuildHost_DoesNotInjectBillingWithoutProduction ./cmd/lipstd`_

- [ ] 5.4 Deny admission when identity is missing or the mapped account does not exist
  - On an injected host, a custom mapping that returns empty identity denies admission and does not start upstream.
  - A stock mapping to a principal with no billing account denies admission and creates zero accounts.
  - Observable: two host-loop cases show deny-before-upstream and unchanged account count.
  - _Depends: 5.1_
  - _Requirements: 1.5, 4.4, 4.6_
  - _Boundary: Host loop test_
  - _Validation: `go test -run TestBillingHostLoop_AdmissionDeny ./internal/infra/runtimebundle`_

## 6. Document the injection path

- [ ] 6. Document how an internal host injects billing without YAML auto-open
  - Describe compose inputs, catalog publish, principal/session identity, admin provisioning, and fail-closed YAML flag.
  - State that leftover pricing YAML is not TUR rating truth and that `lipstd` does not invent accounts.
  - Point enterprise attach docs at the composer without adding public Options money fields.
  - Observable: `docs/billing-host-composition.md` exists and enterprise-extension billing row cites it.
  - _Depends: 4.1_
  - _Requirements: 2.5, 1.3, 3.5_
  - _Boundary: Fence tests/docs_
  - _Validation: file exists and is linked from `docs/enterprise-extension-boundaries.md`_

## Implementation Notes
- SnapshotCatalog route overrides store distinct versioned bodies, but RateTurn/MaxCharge require one customer catalog identity: emit ModelPricing/RoutePricing amounts under the TUR/default CustomerPricingRef, with a card for every billed backend/model when any override applies.

