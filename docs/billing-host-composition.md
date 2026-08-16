# Billing host composition

Authoritative billing is injected into the host through one already-opened Bun-backed `billingstore.DurableStore` and one immutable snapshot catalog. Composition does not open a DSN, invent accounts, or add money fields to public `pkg/lipruntime.Options`.

## Composition path

1. Open or migrate `billingstore.DurableStore` in the owning host.
2. Publish immutable customer-pricing, charge-policy, and operator-rate snapshots into `billingcompose.SnapshotCatalog`.
3. Call `runtimebundle.ComposeBilling` with the store, catalog, currency, and a finite model-output bound.
4. Pass the returned `ProductionOptions` to `runtimebundle.BuildHost`.

`ComposeBilling` requires durable call-leg append, call-closure append, cheap credit screening, atomic exposure admission, complete-call claiming, call settlement, independent provider-cost posting, and immutable snapshot resolution. An in-memory spool is not accepted for authoritative composition.

Authoritative hosts must keep a request token estimator wired on the executor (the standard model-catalog attach does this). Billing quoting uses that estimate even when the route selector has no request-size constraints, so `IncludeInputTokens` policies do not depend on a flat `ConservativeCeiling` for ordinary routes.

```go
prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
    Store:          store,
    Catalog:        catalog,
    Currency:       "USD",
    ModelMaxOutput: modelMaxOutput,
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

There is one authoritative flow:

```text
cheap credit screen
 -> route + pessimistic quote
 -> atomic open-exposure admission
 -> execute with no billing/exposure mutation
 -> durable terminal B-leg records + one call closure
 -> deterministic customer settlement + exposure close
 -> independent per-B-leg provider-cost posting
```

`BillingCallID` is generated once per incoming inference call. It is shared by failover and parallel B-legs, while A-leg and session IDs remain correlation/reporting metadata. Customer settlement is keyed by account plus `BillingCallID`; provider cost is keyed by `BillingCallID` plus B-leg identity.

## Snapshot catalog

Snapshot bodies are process-local; durable exposure and call records store only immutable `VersionRef` identities. Publish every referenced pricing, policy, model-pricing, and operator-rate version before serving. Missing or mismatched snapshots fail closed. A price change publishes a new version rather than mutating an existing body.

## Operational exposure and settled money

An exposure row is operational risk, not money. Admission reads settled account headroom and the sum of open exposure rows, then inserts one immutable open row under the account serialization point. It does not update account balance, reserved balance, or a journal.

Execution never renews, decrements, or closes exposure. Terminal usage persistence never rates or posts money. The post-usage worker joins the closure with expected B-leg rows. Customer settlement atomically verifies actual charge versus admitted maximum, posts a non-zero financial journal operation when needed, records zero-charge processing evidence, and closes the exposure. Provider COGS is retried independently and cannot block valid customer settlement.

Historical authorization-hold rows are migration/reconciliation evidence only. New authoritative composition does not require authorization, hold lookup, hold release, expiry, or authorization-book capabilities. Open-hold inventory blocks ready accounts until reconciled; after open inventory is empty, `authorization_holds` is dropped and is not part of the normal call path.

## Reports and trusted provisioning

`ReportingStore` separates settled customer spend, open operational exposure, and independent provider cost. Use `QueryOpenExposures` and `CallExplanation` (admin `/exposures` and `/call`) for call-path diagnostics. Retired TUR-processing and authorization-hold report endpoints are removed; `reserved_nano` remains a legacy always-zero account column rejected on ready accounts. Session/A-leg reports may aggregate multiple BillingCallIDs but never use A-leg or session identity as a financial idempotency key.

Trusted operator recovery uses `POST /exposure-repair` with `{call_id, source_key, mode}`:

- `mode=complete` requires a joinable call closure + expected legs, then closes exposure at zero charge.
- `mode=incomplete` requires a durable call closure, synthesizes `never_started` legs for any missing `ExpectedBLegIDs`, then closes exposure at zero charge.

TTL alone never closes exposure.

Accounts are created and funded only through the protected trusted billing admin surface. Stock `lipstd` does not call `ComposeBilling` or invent billing accounts. The authoritative flag is a fail-closed composition gate: enabled billing without a complete durable injection refuses to serve.

## Failure posture

- Account unavailability at the cheap screen or detailed admission denies the new call.
- Usage-spool failure after output preserves the selected response and schedules bounded detached retry/diagnostics.
- Incomplete terminal evidence leaves exposure open; TTL alone never closes it.
- Operator `POST /exposure-repair` (`complete` or `incomplete`) is the approved no-charge recovery path; incomplete mode may synthesize missing never-started legs only when a call closure already proves the BillingCallID is no longer executable.
- Actual charge above the admitted maximum, floor violation, replay conflict, or journal mismatch enters `reconcile_required`.
- No provider-cost failure can terminate an already-admitted call or undo valid customer settlement.
- Simultaneous loss of every durable replica before terminal evidence is persisted is outside the at-least-once guarantee.

## Related

- [enterprise-extension-boundaries.md](enterprise-extension-boundaries.md)
- [legacy-options-migration.md](legacy-options-migration.md)
