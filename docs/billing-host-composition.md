# Billing host composition

Billing is an injection-only, all-or-none host capability. A billing-enabled host supplies one complete composition; a stock host supplies no billing ports. The executor receives only the cheap credit gate, atomic exposure admission, `TerminalUsageSink`, and `BillingIdentity`. It never receives a billing mode selector, customer worker, provider worker, journal, or outbox.

## Final composition

1. The owning host opens the durable `billingstore.DurableStore` and publishes immutable pricing, charge-policy, and operator-rate snapshots.
2. The host calls `runtimebundle.ComposeBilling` with the store, catalog, currency, finite model-output bound, and terminal spool.
3. The host passes the returned `ProductionOptions` to `runtimebundle.BuildHost`.
4. `BuildHost` accepts either all four executor billing ports or none. Any partial set fails before serving traffic. The stock `lipstd` path remains non-billing and does not infer billing from YAML, ledger configuration, or pricing configuration.

```go
prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
    Store:          store,
    Catalog:        catalog,
    TerminalUsageSink: spool,
    Currency:       "USD",
    ModelMaxOutput: modelMaxOutput,
})
if err != nil {
    return err
}

host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
    ConfigPath: configPath,
    Production: prod,
})
```

## Authoritative flow

```text
settled-credit screen
 -> route and pessimistic quote
 -> atomic exposure admission
 -> execute without money or exposure mutation
 -> local durable terminal spool
 -> complete-call gate over every expected B-leg
 -> native customer rating and settlement
 -> independent provider COGS ordered by (recorded_at, transaction_id)
```

`BillingCallID` is generated once per inference call and is shared by failover and parallel B-legs. A-leg and session identifiers are correlation data, never financial idempotency keys. Customer settlement is keyed by account and `BillingCallID`; provider COGS is keyed by `BillingCallID` and B-leg identity.

## Terminal spool contract

The host constructs the durable terminal handoff and injects it through `ComposeBilling`; `billingspool` is the standard SQLite implementation. It uses SQLite WAL mode, synchronous durability, a stable process-state path, idempotent source fingerprints, bounded capacity, and bounded retry backoff. Append admission fails closed when capacity is exhausted; it never falls back to a central append or an unbounded request goroutine. When the injected sink exposes `billingspool.Health`, readiness reports its bounded health state.

At process startup `runtimebundle` starts the injected sink when it exposes the billing-spool lifecycle, and at process shutdown it stops and closes that sink. For `billingspool`, startup reclaims stale `delivering` claims and starts exactly one process-owned flusher; rows are pruned only after successful central acknowledgement, and a restart replays durable pending rows idempotently. Terminal call closure is not released to customer settlement until every frozen `ExpectedBLegIDs` entry has a valid sealed central B-leg row; call-first or out-of-order delivery cannot bypass this gate.

## Exposure and settlement

An exposure is operational risk, not settled money. Admission reads settled headroom and open exposure rows, then inserts one immutable exposure atomically under account serialization. Execution does not renew, decrement, close, journal, or mutate account money. Terminal persistence does not rate or post money.

The process-owned complete-call worker claims a closure only after all expected B-legs are present. It performs native customer rating, an idempotent customer journal posting, and exposure close. The provider-cost worker is independent and orders eligible provider work by `(recorded_at, transaction_id)`; missing or unreconciled provider COGS cannot block valid customer settlement.

## Worker ownership and health

Customer completion and provider COGS workers are process-owned lifecycle services started and stopped by the host composition. They are not executor fields and do not run one goroutine per request. Readiness currently reports the configured store/journal rows and, when available from the injected terminal sink, a distinct advisory `billing_spool` row; worker and reconciliation readiness are not wired into that report. An incomplete terminal evidence set leaves exposure open; TTL alone never closes it.

## Destructive migration procedure

PostgreSQL and SQLite migrations use the same preserve-or-resolve procedure. First stop all old writers and quiesce old workers. Under the dialect-specific migration critical section, lock the affected table where PostgreSQL requires it, prove that pending/error rows are zero, replay identical rows idempotently, reject malformed or conflicting rows, and reconcile source keys and fingerprints. Drop retired tables and columns only after the proof; run schema verification and a post-migration reconciliation before enabling the new writer.

For SQLite, use the table-copy migration transaction with WAL and the required immediate lock semantics; verify the replacement schema and migration history before reopening the host. For PostgreSQL, use the schema-local lock and transactional DDL. A pending or unprovable row blocks the migration and requires explicit operator reconciliation. Historical migration DDL and isolated preserve-or-block readers may remain as migration evidence, but they are never runtime writers.

No mixed-version deployment is supported: all processes must run the final spool and terminal interfaces before any retired central append, TUR/LUR, authorization-hold, or outbox writer is removed. Roll forward only after every process is quiesced or upgraded; do not run legacy writers beside the final writer.

## Snapshot and identity rules

Snapshot bodies remain process-local and durable records store immutable `VersionRef` values. Publish every referenced version before serving. Missing or mismatched versions fail closed. `BillingIdentity` is stamped once at exposure admission and reused for terminal closure; it is not re-resolved at handoff.

## Failure posture

- Credit-screen or atomic-admission failure denies the new call before provider open.
- A spool append failure after output preserves the selected response and reports bounded retry state.
- Incomplete expected B-leg evidence keeps the exposure open.
- Charge-above-maximum, replay conflict, journal mismatch, and reconciliation failure fail closed.
- Provider-cost failure cannot terminate an admitted call or undo customer settlement.
- Simultaneous loss of every durable replica before terminal evidence is persisted is outside the at-least-once guarantee.

## Related

- [enterprise-extension-boundaries.md](enterprise-extension-boundaries.md)
- [legacy-options-migration.md](legacy-options-migration.md)
