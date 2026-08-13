# Requirements Document

## Introduction

Operators and internal hosts have a merged usage-record billing engine (authorize before upstream, one sealed turn usage record after the existing terminal owner, post-turn rating and journal settlement) but cannot turn it on from stock composition. The standard `lipstd` binary and public `lipruntime` library options do not invent billing accounts or open the durable billing journal. Rating lookup exists only as test doubles. Holds and usage records store snapshot references without a catalog of snapshot bodies. Account create, funding, and credit-policy changes exist as trusted store commands with read-only admin reports.

This specification adds the **injection-only host-composition path**: identity mapping from an authenticated principal and session, journal open and admission wiring through the existing internal host builder, an immutable versioned pricing/policy/operator-rate catalog with a stock rating lookup, and trusted account provisioning (create, fund, credit-policy) on the existing protected admin billing surface. It does not reopen stream, routing, turn-usage sealing, or public library money fields.

## Boundary Context

- **In scope**: Injection-only enablement of authoritative billing; in-tree composition so internal hosts do not each invent identity, catalog, journal open, and admission wiring; stock immutable versioned snapshot catalog used for both admission snapshot references and post-turn rating lookup; identity mapping from authenticated principal/session without creating accounts as a request side effect; trusted operator provisioning (create account, post funding, change credit policy), including protected admin HTTP commands; fail-closed behavior when authoritative billing is requested without a complete injection; proof that an injected host can authorize, execute, seal usage, rate, and journal; operator/maintainer documentation of the composition path.
- **Out of scope**: Opening the billing journal from `lipstd` YAML or from `accounting.billing.authoritative` alone; adding billing or journal fields to public `lipruntime` options; payment gateway, invoicing, VAT, or FX; stream-time money or token-ledger settlement; changing B2BUA, retry, failover, or stream semantics; changing connector FinalizeBilling money ABI; usage-authority quota and concurrency; durable cross-process billing handoff outbox; inventing a default or bootstrap billing account inside the standard binary.
- **Adjacent expectations**: The parent `usage-record-ledger-billing` engine already owns authorization, TUR/LUR sealing, rating formulas, journal invariants, post-turn processing, and read-only reports. This feature consumes those ports and must not change their financial semantics. Metering and usage-authority may continue to open their own stores from YAML; billing must not copy that pattern. Identity is stamped once at admission by the existing engine; this feature supplies the mapping used at stamp time and does not re-resolve identity at usage handoff. Leftover `accounting.pricing` YAML remains dual-plane metadata and is not TUR rating truth.
- **Boundary ownership**: Composition root and internal host builder for enablement; driving adapter for trusted admin provisioning HTTP; catalog and identity helpers as composition-owned adapters. Core billing policy, runtime collector, and stream handlers stay with the parent engine.
- **Optional hexagonal lens**: Domain policy stays in existing billing; this spec adds application composition, a driven catalog adapter, and a driving trusted-admin adapter. No new core orchestration.
- **Revalidation triggers**: Internal host startup composition; protected admin billing HTTP. Not routing, streaming, capability negotiation, or secure-session protocol.

## Requirements

### Requirement 1: Keep Stock Binary and Public Library Injection-Only
**Objective:** As a maintainer, I want the standard binary and public library to stay non-money unless a host injects a complete billing assembly, so OSS `lipstd` does not invent customers, prices, or funds.

#### Acceptance Criteria
1. When the standard distribution starts with `accounting.billing.authoritative` unset or false, the Host Composition shall not open a billing journal, shall not invent billing accounts, and shall not require a catalog or rating lookup.
2. When the standard distribution starts with `accounting.billing.authoritative` true and no complete billing injection, the Host Composition shall fail closed before serving traffic.
3. The Host Composition shall not treat leftover `accounting.ledger.*` or `accounting.pricing` configuration as sufficient to open a billing journal or to rate sealed usage records.
4. The public library build options shall remain free of billing journal, account, catalog, and rating-lookup fields.
5. The Host Composition shall not create a billing account, post funding, or assign a default customer as a side effect of serving a client request.

### Requirement 2: Enable Authoritative Billing Only Through Complete Internal Injection
**Objective:** As an internal-host maintainer, I want a documented in-tree composition path that wires the existing engine ports, so I do not invent store-open, admission, identity, and rating lookup in every binary.

#### Acceptance Criteria
1. When an internal host supplies a complete billing injection (durable journal, admission, identity mapping, catalog-backed admission snapshots, catalog-backed rating lookup, and authoritative enablement) to the existing internal host builder, the Host Composition shall enable authorize-before-upstream and post-turn rating/settlement without changing stream or routing behavior.
2. If any required piece of that injection is missing, the Host Composition shall fail closed before serving traffic and shall not run a partial money path.
3. When composition overwrites handoff and reports from the injected journal, the Host Composition shall keep a single journal as the authority for append, settlement, processing, hold release, and reports.
4. The Host Composition shall not introduce a second host builder, a hidden global billing registry, or a DI container to enable billing.
5. The Host Composition shall document the injection path so an internal host can assemble it without reading test doubles as the contract.

### Requirement 3: Provide an Immutable Versioned Snapshot Catalog and Stock Rating Lookup
**Objective:** As a post-turn processor, I want exact pricing, charge-policy, and operator-rate snapshot bodies for the references stored on holds and usage records, so rating is deterministic and the journal never stores snapshot bodies.

#### Acceptance Criteria
1. When post-turn rating receives a sealed usage record whose snapshot references are present in the catalog, the stock rating lookup shall return the exact immutable snapshot bodies for those references.
2. If a required snapshot reference is missing, withdrawn, or does not match the stored reference identity, the stock rating lookup shall fail closed and shall not invent, coerce, or silently substitute another version.
3. When a host publishes a catalog version, the Host Composition shall treat that version as immutable; a price or policy change shall require a new version identity rather than in-place mutation of a published version.
4. The Host Composition shall keep snapshot bodies in the catalog and shall continue to persist only snapshot references on holds and usage records.
5. The Host Composition shall not use leftover `accounting.pricing` YAML as the TUR rating catalog.
6. When admission authorizes a call under complete injection, the Host Composition shall resolve customer pricing, charge-policy, and operator-rate snapshot references from the same catalog that post-turn rating uses, and shall fail closed if those versions are missing.

### Requirement 4: Map Billing Identity From Authenticated Principal and Session
**Objective:** As an admitting host, I want billing account and authorization identity derived from the authenticated caller, so holds and usage records are attributed without inventing accounts at request time.

#### Acceptance Criteria
1. When a call is admitted under authoritative billing, the Host Composition shall establish billing account identity and authorization identity from the host-supplied mapping of authenticated principal and session before upstream execution starts.
2. Where the host uses the stock mapping, the Host Composition shall use the authenticated principal identifier as the billing account identifier and shall use session identity as input to authorization identity.
3. If principal or session mapping cannot produce billing account identity or authorization identity, the Host Composition shall deny admission and shall not start upstream execution.
4. If the mapped billing account does not exist in the injected journal, the Host Composition shall deny admission and shall not create that account as a request side effect.
5. When usage-record handoff runs after admission, the Host Composition shall use stamped admission identity and shall not perform a second identity mapping for that call.
6. When a host supplies a mapping other than the stock principal/session helper, the Host Composition shall still fail closed if that mapping returns no identity.

### Requirement 5: Provision Accounts, Funding, and Credit Policy Through Trusted Commands
**Objective:** As a trusted operator, I want to create billing accounts, post funding, and change credit policy before traffic, so prepaid and postpaid accounts can exist without the request path inventing them.

#### Acceptance Criteria
1. When a trusted operator submits a valid create-account command, the Host Composition shall persist a billing account with an explicit prepaid or postpaid mode and one authoritative currency, and shall not treat that command as a client request side effect.
2. When a trusted operator submits a valid funding command for an existing account, the Host Composition shall post funding through the existing journal funding command so prepaid spendable can increase.
3. When a trusted operator submits a valid credit-policy command for an existing account, the Host Composition shall apply the policy change through the existing journal credit-policy command.
4. If an untrusted client attempts to create an account, post funding, or change credit policy, the Host Composition shall reject the attempt.
5. If a create, funding, or credit-policy command is incomplete or conflicts with existing journal identity, the Host Composition shall reject it without corrupting journal history.
6. The Host Composition shall not require a payment gateway, invoice, VAT, or FX conversion to complete create, funding, or credit-policy commands.

### Requirement 6: Expose Trusted Provisioning on the Protected Admin Billing Surface
**Objective:** As an operator, I want to provision accounts on the same protected admin billing surface that already serves read-only reports, so I do not need an unofficial store-only backdoor.

#### Acceptance Criteria
1. When the protected admin billing surface is mounted, the Host Composition shall accept trusted create-account, funding, and credit-policy commands on that surface in addition to existing read-only reports.
2. If the diagnostics shared secret is empty, the Host Composition shall not mount the admin billing surface, including provisioning commands.
3. If a provisioning command is presented without the existing admin billing protection, the Host Composition shall reject it.
4. When a trusted provisioning command succeeds, subsequent read-only billing reports on the same surface shall reflect the resulting account, funding, or policy state from the journal.
5. The Host Composition shall not add provisioning commands to client-facing frontend routes.

### Requirement 7: Prove an Injected Host Can Close the Money Loop
**Objective:** As a maintainer, I want an in-tree proof that injection, catalog, identity, and provisioning together settle a turn, so composition is not a documentation-only recipe.

#### Acceptance Criteria
1. When a test or internal host injects a complete billing assembly, a provisioned funded account, stock identity mapping, and catalog versions that match the references that will be stamped, and then executes a successful billable turn, the Host Composition shall authorize before upstream, persist one sealed turn usage record after the existing terminal owner, rate from catalog snapshot bodies that match stored references, and post settlement to the journal.
2. When that proof turn completes, billing reports for the account shall show journal-backed customer and operator results rather than stream-event reinterpretation.
3. If the catalog does not contain the stamped snapshot references, the Host Composition shall not mark the turn fully processed by inventing rates.
4. The Host Composition shall demonstrate Requirement 7 without starting the standard `lipstd` binary from a YAML-only billing factory and without adding billing fields to public library options.

### Requirement 8: Preserve Adjacent Engine, Stream, and Non-Money Boundaries
**Objective:** As a platform owner, I want composition to attach at existing billing ports, so stream, routing, usage-authority, and connector evidence stay unchanged.

#### Acceptance Criteria
1. The Host Composition shall not change B2BUA continuity, retry, failover, or stream event semantics.
2. The Host Composition shall not enrich prices on the stream, write the legacy token ledger, or settle money inside stream handlers.
3. The Host Composition shall not change connector FinalizeBilling into a money ABI; provider evidence remains usage/cost evidence for terminal sealing, not settlement.
4. The Host Composition shall not replace usage-authority quota or concurrency with billing holds, and shall not reopen monetary budget rules on usage-authority configuration.
5. The Host Composition shall not persist snapshot bodies into the journal, shall not re-resolve billing identity at usage-record handoff, and shall not alter TUR/LUR sealing or rating formulas owned by the parent engine.
6. Architecture and composition tests shall continue to enforce that public library options stay non-money and that `accounting.billing.authoritative` without injection fails closed.
