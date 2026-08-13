# Billing host composition

Internal hosts enable usage-record billing by **injecting** an already-opened journal and a catalog into the existing host builder. YAML does not open the billing journal, invent accounts, or publish TUR rating snapshots.

This is the operator/maintainer recipe. Enterprise attach seams stay in [enterprise-extension-boundaries.md](enterprise-extension-boundaries.md).

## Composition path

1. Open a Bun `billingstore.DurableStore` in the host (`NewDurableStore` / `OpenStore`). `ComposeBilling` does not take a DSN and does not open a database.
2. Publish immutable pricing, charge-policy, and operator-rate bodies into `billingcompose.SnapshotCatalog`, then bind defaults (and optional route/operator bindings).
3. Call `runtimebundle.ComposeBilling` with that store, catalog, currency, and a `ModelMaxOutput` bound.
4. Pass the returned `ProductionOptions` as `BuildHostInput.Production` to `runtimebundle.BuildHost`.

There is no second host builder, no DI container, and no YAML journal DSN factory. Stock `lipstd` does not call `ComposeBilling` and does not invent billing accounts. Public `pkg/lipruntime.Options` stays non-money; do not add journal, catalog, or rating fields there.

`ComposeBilling` fills admission, identity, rating lookup, reports, and authoritative enablement. It does not start the post-turn worker; `BuildHost` starts that worker at generation publish.

```go
catalog := billingcompose.NewSnapshotCatalog()
if err := catalog.PutPricing(pricing); err != nil {
    return err
}
if err := catalog.PutPolicy(policy); err != nil {
    return err
}
if err := catalog.PutOperatorRate(operator); err != nil {
    return err
}
if err := catalog.SetDefaults(pricing.Ref, policy.Ref); err != nil {
    return err
}
if err := catalog.SetOperatorRateBinding(backend, model, operator.Ref); err != nil {
    return err
}

prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
    Store:    store, // already opened DurableStore
    Catalog:  catalog,
    Identity: nil, // PrincipalSessionIdentity
    Currency: "USD",
    HoldTTL:  cfg.Accounting.Billing.EffectiveHoldTTL(), // or omit and let YAML overlay
    ModelMaxOutput: func(ctx context.Context, backend, model string) (int64, bool, error) {
        return 128000, true, nil
    },
})
if err != nil {
    return err
}
host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
    ConfigPath:      configPath,
    Mandatory:       lipsdk.StandardDistributionRequirements(),
    HandlerComposer: stdhttp.ComposeStandardHTTP,
    Production:      prod,
})
```

## ComposeBilling inputs

`runtimebundle.ComposeBillingInput`:

| Field | Required | Role |
| --- | --- | --- |
| `Store` | yes | Opened `billing.AuthoritativeBilling`. Must also implement usage-record append, post-turn processing, hold release, account provisioning, authorization lookup, and authorization (hold) store. `billingstore.DurableStore` does. Incomplete stores return `ErrComposeBillingIncomplete`. |
| `Catalog` | yes | `*billingcompose.SnapshotCatalog` with defaults already bound. Missing defaults fail closed. |
| `Currency` | yes | Non-empty (whitespace is empty). |
| `ModelMaxOutput` | yes | Admission hold bound. `nil` fails closed so a hold cannot be unbounded. |
| `Identity` | no | `nil` selects `billingcompose.PrincipalSessionIdentity`. A custom mapping that returns empty identity still fails closed at admission. |
| `Strict`, `ConservativeCeiling` | no | Passed through to `billingadmission.NewAdapter`. |
| `ReportsPath` | no | Admin mount path. Empty falls back to YAML `accounting.billing.reports_path`, then `/admin/billing`. |
| `HoldTTL` | no | Authorization hold lifetime. Zero lets `BuildHost` apply YAML `accounting.billing.hold_ttl` (default 15m) onto the stock admission adapter. An explicit duration wins over YAML. Custom `BillingAdmission` implementations are not overlaid. |
| `PostTurnBatchSize`, `PostTurnInterval` | no | Post-turn worker tuning used by `BuildHost`. |

One store is the authority for append, settlement, processing, hold release, reports, and trusted provisioning. Do not inject a second journal as handoff or reports.

## Catalog publish

Snapshot **bodies** live in the in-memory catalog. Holds and sealed usage records store `VersionRef` (ID + Version) only. The catalog is process-local: republish it at every start from host configuration, including **every version still referenced by non-terminal holds and TURs** — a missing referenced version fails closed at rating and blocks settlement until republished. A price or policy change is a new version identity, not an in-place mutation of a published version.

Publish, then bind:

1. `PutPricing` / `PutPolicy` / `PutOperatorRate` — identity is ID+Version. Identical replay of the same body is allowed; a different body for the same identity is rejected.
2. `SetDefaults(customerPricing, chargePolicy)` — both refs must already be published; the policy `PricingRef` must equal the customer pricing identity.
3. Optional `SetRoutePricing(backend, model, ref)` and `SetOperatorRateBinding(backend, model, ref)` — refs must already be published; bindings are immutable (rebinding to a different version is rejected). Unbound operator rates resolve to an empty `VersionRef`.

Admission (`RoutePricing` / `Policy`) and post-turn rating (`SnapshotsFor`) use this same catalog. Missing, withdrawn, or mismatched refs fail closed; the resolver does not invent or substitute another version.

Route and operator bindings are immutable: `SetRoutePricing` / `SetOperatorRateBinding` reject rebinding a route to a different version (identical replay is allowed). Settlement re-resolves the current binding, and immutability guarantees it matches the version admitted with. Publish a new version identity and rebuild the catalog at restart to change a price.

Leftover YAML `accounting.pricing` is **not** TUR rating truth. It is dual-plane / shadow characterization metadata. Do not treat it as the snapshot catalog.

## Principal and session identity

Stock mapping (`Identity: nil`):

| Billing field | Source | Not used |
| --- | --- | --- |
| Account ID | Authenticated `scope.ScopeFromContext` → trimmed `PrincipalID` | Client claims, `ClientSessionID` |
| Authorization ID | Trimmed `call.Session.AuthoritativeSessionID` + `:` + trimmed A-leg ID | `ClientSessionID` |

Missing principal, unknown principal, missing authoritative session, or missing A-leg yields empty identity. Admission then denies and does not start upstream. Mapping **never** calls `CreateAccount`. A missing journal account also denies; the request path does not invent customers.

Identity is stamped once at admission. Usage-record handoff does not re-map.

## Trusted admin provisioning

Accounts exist only after a trusted operator command. Mount is the existing protected billing surface (`internal/stdhttp/admin/billing`), default `/admin/billing`.

| Method | Path | Body (snake_case, integer nano-units) | Success |
| --- | --- | --- | --- |
| POST | `/admin/billing/account` | `{account_id, currency, mode, credit_limit_nano?}` | 201 `{account_id}` |
| POST | `/admin/billing/funding` | `{account_id, amount_nano, currency, source_key, reason}` | 200 posting JSON |
| POST | `/admin/billing/credit-policy` | `{account_id, mode, currency, credit_limit_nano, source_key, reason}` | 200 policy-change JSON |
| GET | existing report paths | unchanged | journal-backed reports |

`credit_limit_nano` is required for postpaid create and credit-policy; prepaid credit limit must be 0. Create opens at **balance 0** (and prepaid credit limit 0). Funding is a separate POST. There is no payment gateway, invoice, VAT, or FX on this surface.

The surface mounts only when `diagnostics.shared_secret` is non-empty **and** reports are injected. Empty secret mounts nothing, including POSTs. Callers send `X-LIP-Diagnostics-Secret`. Missing or wrong secret is rejected. These commands are not mounted on client frontend routes.

## Fail-closed YAML flag

`accounting.billing.authoritative: true` is a composition **gate**, not a store factory. YAML billing config is `authoritative`, `reports_path`, and `hold_ttl` — there is no journal DSN for `ComposeBilling` to consume.

| Situation | Result |
| --- | --- |
| Flag unset/false, no Production billing | Host serves without a billing journal, catalog, or invented accounts. |
| Flag true, no complete injection | `BuildHost` / `lipruntime.Build` fail with `ErrAuthoritativeBillingRequired` before serving. |
| Incomplete `ComposeBilling` input | `ErrComposeBillingIncomplete`; do not call `BuildHost`. |
| Leftover `accounting.ledger.*` or `accounting.pricing` | Not a billing factory. Does not open the Bun journal or rate sealed TURs. |

## Stock binary and public library

- `cmd/lipstd` `runServeCommand` does not set `BuildHostInput.Production` billing fields and does not call `ComposeBilling`.
- `pkg/lipruntime.Build` copies only non-money registrations onto `ProductionOptions`. `lipruntime.Options` has no billing journal, account, catalog, or rating-lookup fields.
- Internal/enterprise binaries that already import `runtimebundle` attach at `ComposeBilling` → `BuildHost` `Production`. Do not fork a second host or hide billing behind a global registry.

## Related

- [enterprise-extension-boundaries.md](enterprise-extension-boundaries.md) — allowed ProductionOptions seam
- [legacy-options-migration.md](legacy-options-migration.md) — public Options stay non-money
