# Design Document

## Overview

This design implements issue #387 as an **early HTTP ingress access-control layer** for Go-LIP. It deliberately separates two lifetime domains:

1. an immutable **generation-scoped enforcement policy** used by the standard data-plane handler graph;
2. a **process-scoped country database service** that owns MMDB readiness, local/versioned files, and optional managed MaxMind updates.

The central architectural rule is that GeoIP rejects traffic before general request instrumentation/authentication/frontend/runtime work without creating a parallel reload/control-plane architecture.

Brownfield baseline: `ca43dde919f4d53716a98bf53ffb57bd61560607`.

This revision incorporates the required fixes from `design-review.md`: an exact cycle-neutral HTTP composition DTO, a hard separation between static compilation and runtime resource readiness, and transactional LKG manifest-before-reader publication ordering.

## Goals

- Reject configured geographic/IP traffic before auth, frontend decode, routing, DB, billing, and model work.
- Preserve exact `deny_allow` / `allow_deny` class-precedence semantics.
- Support IPv4/IPv6 exact addresses and CIDRs.
- Make direct peer authoritative by default and forwarded addresses safe only through explicit trusted-proxy configuration.
- Use a local concurrent Country MMDB reader on the hot path.
- Manage MMDB updates automatically without compromising availability when an LKG exists.
- Hot reload pure policy through existing immutable generations.
- Keep database/updater lifecycle process-owned and restart-classified.
- Preserve current management-listener, auth-attribution, and in-flight generation semantics.
- Keep denial observability bounded and outside general hostile-traffic logging.
- Keep `check-config` deterministic and network-independent.

## Non-Goals

- City/ASN/VPN/threat-intelligence policy.
- PROXY protocol or CDN-specific real-IP headers.
- WAF/firewall orchestration.
- Per-request GeoIP network service calls.
- Mandatory per-IP caching.
- Management-listener GeoIP enforcement.
- Retroactive disconnection of existing SSE/WebSocket sessions.
- Changes to frontend wire schemas, backend plugins, or auth peer attribution.
- Distributed updater coordination.

## Architecture

### HTTP placement

Current standard handler runtime order is effectively:

```text
SecurityHeaders
  -> DownstreamServer
    -> OuterRecovery
      -> OTelHTTP?
        -> PromHTTP?
          -> Trace + RequestID
            -> AccessLog
              -> InnerRecovery
                -> TransportAuth
                  -> RouteMux
```

The target becomes:

```text
SecurityHeaders
  -> DownstreamServer
    -> OuterRecovery
      -> GeoIPIngress?        # omitted entirely when disabled
        -> OTelHTTP?
          -> PromHTTP?
            -> Trace + RequestID
              -> AccessLog
                -> InnerRecovery
                  -> TransportAuth
                    -> RouteMux
```

In `stackHTTPHandler` composition order, build OTel/general Prometheus first, then wrap that handler with GeoIP if enabled, then apply `outerRecoveryMiddleware`, `DownstreamServerMiddleware`, and security headers.

This location preserves outer panic containment/security headers while ensuring denied traffic does not reach general OTel/HTTP Prometheus/request-ID/access-log/auth/frontend/runtime work.

### Lifetime split

```text
Process lifetime
┌──────────────────────────────────────────────────────────────┐
│ ProcessServices                                             │
│  └─ GeoIPDatabaseService                                   │
│      ├─ active CountryLookup reader/version                 │
│      ├─ LKG/versioned files + manifest                      │
│      ├─ updater client/timer (managed mode only)            │
│      ├─ readiness/status                                    │
│      └─ bounded metrics                                     │
└──────────────────────────────────────────────────────────────┘
                    │ non-owning CountryLookup
                    ▼
Generation N                       Generation N+1
┌─────────────────────────┐       ┌─────────────────────────┐
│ compiled GeoIP Policy   │       │ compiled GeoIP Policy   │
│ resolver config         │       │ resolver config         │
│ GeoIP middleware?       │       │ GeoIP middleware?       │
│ rest of handler graph   │       │ rest of handler graph   │
└─────────────────────────┘       └─────────────────────────┘
```

A generation never closes/reconfigures the process reader/updater. Process shutdown closes it only under existing host/process-service ownership after generations are retired.

## Components and Dependency Direction

### `internal/core/geoip`

Pure policy/domain package.

Responsibilities:

- order, reason, decision value types;
- normalized immutable country/address rule sets;
- Apache-compatible class precedence;
- safe decision-plan compilation/short-circuit metadata;
- narrow country lookup port.

Conceptual contracts:

```go
package geoip

type Order uint8
const (
    OrderDenyAllow Order = iota + 1
    OrderAllowDeny
)

type RuleClass struct {
    Countries map[string]struct{} // immutable after compile
    Prefixes  []netip.Prefix
}

type Policy struct {
    Order              Order
    Allow              RuleClass
    Deny               RuleClass
    NeedsCountryLookup bool
}

type CountryLookup interface {
    LookupCountry(netip.Addr) (country string, found bool, err error)
}

type Decision struct {
    Allow  bool
    Reason Reason // finite enum only
}
```

Core imports no `net/http`, MaxMind implementation, logger, Prometheus, runtimebundle, or root stdhttp.

### `internal/stdhttp/contract`

This package remains the exact cycle-neutral composition boundary between `runtimebundle` and root `stdhttp`.

Add a data-only security projection; do **not** make `runtimebundle` import `internal/stdhttp/geoip`.

Conceptually:

```go
type GeoIPResolverConfig struct {
    Source         string
    TrustedProxies []netip.Prefix
    MaxHeaderBytes int
    MaxHops        int
}

type GeoIPSecurityInput struct {
    Policy   *coregeoip.Policy
    Lookup   coregeoip.CountryLookup
    Resolver GeoIPResolverConfig
    Observer GeoIPObserver
}

type GeoIPObserver interface {
    Decision(reason coregeoip.Reason, allow bool)
}

type HTTPSecurityInput struct {
    // existing fields...
    GeoIP GeoIPSecurityInput
}
```

The contract imports only stdlib/lower-level core types. Slices are defensively copied using the same pattern as other contract projections.

`Policy == nil` means no gate is installed. A whole `ProcessServices`, `any`, service locator, or middleware closure must not cross this contract.

### `internal/stdhttp/geoip`

HTTP adapter.

Responsibilities:

- direct `RemoteAddr` parsing;
- bounded XFF parsing;
- bounded RFC 7239 `Forwarded` parsing;
- trusted-proxy chain resolution;
- middleware using `contract.GeoIPSecurityInput`;
- generic 403 rendering.

It consumes the cycle-neutral contract and core policy but owns all HTTP/header semantics.

### `internal/infra/geoip`

Driven infrastructure adapter / process service.

Responsibilities:

- open/verify Country MMDB;
- decode only required `country.iso_code` semantics;
- own synchronized active reader;
- maintain versioned files and LKG manifest;
- managed MaxMind update checks/downloads;
- transactional publication/retirement;
- readiness/status;
- close/cleanup lifecycle.

Recommended implementation dependencies:

- `github.com/oschwald/maxminddb-golang/v2`
- `github.com/maxmind/geoipupdate/v8/client`

MaxMind types never cross into core/stdhttp contracts.

## Configuration Model

Extend `AccessConfig` with a focused `GeoIP` subtree. Semantic model:

```go
type AccessConfig struct {
    Mode  string      `yaml:"mode"`
    GeoIP GeoIPConfig `yaml:"geoip"`
}

type GeoIPConfig struct {
    Enabled  bool              `yaml:"enabled"`
    Order    string            `yaml:"order"`
    Allow    GeoIPRuleConfig   `yaml:"allow"`
    Deny     GeoIPRuleConfig   `yaml:"deny"`
    ClientIP GeoIPClientConfig `yaml:"client_ip"`
    Database GeoIPDBConfig     `yaml:"database"`
}
```

Recommended YAML:

```yaml
access:
  geoip:
    enabled: true
    order: deny_allow
    deny:
      countries: [BY, CN, IR, RU]
      cidrs: []
    allow:
      countries: []
      cidrs:
        - 203.0.113.64/27
        - 2001:db8:1234::/48
    client_ip:
      source: direct            # direct | x_forwarded_for | forwarded
      trusted_proxies: []
    database:
      source: managed           # managed | local
      edition: GeoLite2-Country
      directory: /var/lib/lip/geoip
      local_path: ""             # local source only
      update:
        enabled: true            # managed source only
        interval: 24h
```

Validation:

- omitted GeoIP block => disabled/no process service;
- enabled requires valid `order`;
- country values normalize uppercase and validate against ISO-3166 alpha-2 set;
- CIDR/exact addresses parse with `net/netip` during static compilation;
- prefixes normalize via `Masked()`;
- forwarded source requires non-empty trusted proxies;
- `managed`/`local` fields are mutually consistent;
- local source rejects managed updater settings;
- update interval has a safe minimum/maximum;
- credentials are process secrets, not ordinary reloadable YAML.

Candidate environment names are `LIP_GEOIP_MAXMIND_ACCOUNT_ID` and `LIP_GEOIP_MAXMIND_LICENSE_KEY`; implementation must align final names with existing env naming conventions.

## Static Compilation vs Runtime Readiness

This is a hard two-phase contract.

### Phase A — static compile/validation

Shared by normal startup, reload candidate parsing, and `check-config`:

1. validate config shape/source-mode consistency;
2. normalize/validate countries;
3. parse/normalize CIDRs/trusted proxies;
4. compile immutable `coregeoip.Policy` and resolver values;
5. determine `NeedsCountryLookup` from the decision plan;
6. classify reload/restart paths.

**Prohibited in Phase A:** MaxMind download/update, external network dependency, request-path I/O, or mutation of process services.

### Phase B — serving activation readiness

Only normal serving composition/publication performs live readiness checks:

- if policy disabled: no gate, no lookup requirement;
- if enabled and `NeedsCountryLookup=false`: gate may operate without MMDB;
- if enabled and `NeedsCountryLookup=true`: process-owned lookup must already be provisioned and ready or candidate/startup fails.

Startup process-service construction may perform the one bounded managed acquisition needed to establish readiness **before** serving activation. Reload candidate compilation must never start/download/reconfigure a process service.

`check-config` uses Phase A and explicitly skips Phase B. It may structurally inspect an intentionally available local file through an existing dry-run facility, but lack of MaxMind network can never fail static validation.

If the current generation compiler needs a mode/capability to distinguish publish-serving from validation-only compilation, add the smallest explicit compile-purpose value at the composition boundary; do not use a global flag or duplicate validation function.

## Reload Classification

Refactor current broad `classifyAccess` field handling.

| Field | v1 disposition | Reason |
|---|---|---|
| `access.mode` | restart-required | preserve existing deployment posture |
| `access.geoip.enabled` | reloadable | generation wrapper presence |
| `access.geoip.order` | reloadable | immutable policy |
| allow/deny countries | reloadable | immutable policy |
| allow/deny CIDRs | reloadable | immutable policy |
| client IP source | reloadable | immutable resolver |
| trusted proxies | reloadable | immutable resolver |
| database source | restart-required | process service |
| directory/local path | restart-required | process file lifecycle |
| edition | restart-required | reader/updater contract |
| update enabled/interval | restart-required | process scheduler lifecycle |
| credential source | restart-required | process secret/client construction |

Existing mixed-change all-or-nothing rejection remains unchanged.

## Process Service Construction

At normal process startup:

1. run static GeoIP compile/validation;
2. if no database source configured, leave `ProcessServices.GeoIP` nil;
3. if configured, construct `internal/infra/geoip.Service` and transfer ownership to `ProcessServices`;
4. load/verify local database or managed LKG;
5. if startup policy is enabled + needs country lookup + managed source lacks LKG, make one bounded initial acquisition attempt;
6. if required readiness is still absent, fail normal serving startup;
7. start periodic updater only for managed + update-enabled configuration;
8. close through normal ProcessServices ownership after request generations retire.

If enforcement is disabled while DB configuration exists, the service may remain provisioned/update-ready for later pure-policy enable reload. This is process work only; the disabled request path has no wrapper.

## Candidate Generation Composition

During a normal serving candidate build:

1. static policy compilation succeeds;
2. `configreload.Classify` has rejected process-resource changes;
3. serving readiness checks the existing `ProcessServices.GeoIP` only when required;
4. runtimebundle creates a defensive `contract.GeoIPSecurityInput` containing compiled policy/resolver, non-owning `CountryLookup`, and bounded observer;
5. candidate security group carries that projection;
6. `ComposeStandardHTTP` installs `stdhttp/geoip` middleware iff `Policy != nil`/enabled.

For validation-only/check-config compilation, steps 3-6 must not require live MMDB readiness or cause process-resource acquisition.

No generation owns updater goroutines, files, credentials, MMDB close, or mutable policy state.

## Policy Evaluation Algorithm

Compile countries into immutable sets and prefixes into normalized slices.

Conceptual evaluation:

```text
addr = addr.Unmap()
allowCIDR = allow.prefixContains(addr)
denyCIDR  = deny.prefixContains(addr)

if order == deny_allow and allowCIDR:
    allow(cid r_allow)          # final allow phase already matched
if order == allow_deny and denyCIDR:
    deny(cidr_deny)             # final deny phase already matched

if compiled plan proves country cannot affect result:
    decide from CIDR flags + order default

country, found, err = lookup(addr)
if err:
    deny(lookup_error)
allowCountry = found && allowCountries.contains(country)
denyCountry  = found && denyCountries.contains(country)

allowMatch = allowCIDR || allowCountry
denyMatch  = denyCIDR || denyCountry
apply exact order truth table
```

Reason selection must be deterministic and finite; do not expose literal rule/IP/country values through the reason enum.

## Client-IP Resolution

### Direct mode

- extract host from `RemoteAddr` using robust host:port handling;
- accept host-only forms used by Go/test servers;
- parse with `netip.ParseAddr`;
- `Unmap()`;
- reject hostname/non-IP values.

### Trusted XFF

If direct peer is untrusted: ignore XFF and use direct peer.

If direct peer is trusted:

1. reject header exceeding `MaxHeaderBytes`;
2. parse at most `MaxHops` comma-separated elements;
3. reject empty/invalid authoritative elements rather than silently skip them;
4. normalize addresses;
5. walk chain right-to-left, treating direct peer as trusted terminal hop;
6. return first non-trusted address;
7. fail closed if none is unambiguous.

### RFC 7239 `Forwarded`

Implement only robust extraction of the ordered `for=` chain needed for client resolution:

- support quoted values and bracketed IPv6;
- respect comma-separated elements and parameter syntax;
- reject `unknown`/obfuscated values when they prevent unambiguous authority;
- enforce same byte/hop bounds;
- do not generalize into unrelated Forwarded metadata processing.

Auth peer attribution remains untouched/direct.

## MMDB Reader Service

Conceptual state:

```go
type Service struct {
    mu     sync.RWMutex
    active *readerVersion
    // updater, lifecycle, status
}

type readerVersion struct {
    reader   *maxminddb.Reader
    path     string
    checksum string
    modified time.Time
}
```

Lookup holds `RLock` through required field decode. Candidate download/open/verify happens without writer lock.

`Reader.Close` must never race any operation using that reader.

## Transactional Managed Update and LKG Publication

### Update flow

```mermaid
sequenceDiagram
    participant T as Update Timer
    participant U as GeoIP Updater
    participant M as MaxMind Client
    participant FS as Versioned Files/Manifest
    participant R as Active Reader

    T->>U: check(ctx)
    U->>M: Download(edition,currentChecksum)
    alt unchanged
        M-->>U: UpdateAvailable=false
        U-->>T: unchanged metric
    else changed
        M-->>U: bounded MMDB stream + metadata
        U->>FS: write/close candidate version
        U->>U: open + Verify + expected Country type
        U->>FS: atomically commit LKG manifest to candidate
        alt manifest commit fails
            U->>U: close candidate reader; retain old active/LKG
        else manifest committed
            U->>R: short-lock swap active reader/version
            U->>U: close retired reader after pre-swap lookups drained
            U->>FS: GC obsolete retired versions
            U-->>T: updated metric
        end
    end
```

### Why manifest-before-reader

Manifest publication is the fallible durable commit step. A failure must leave the old active/LKG untouched. The in-memory pointer swap under lock is deliberately a non-I/O/non-failing commit step after durable selection.

If the process crashes after manifest commit but before in-memory swap, restart validates and loads the committed verified candidate. While live, requests continue using the old in-memory reader until the tiny swap step completes.

### File layout

Managed directory concept:

```text
geoip/
  active.json
  GeoLite2-Country.<hash>.mmdb
  GeoLite2-Country.<old>.mmdb
  .download-<random>.tmp
```

Rules:

- temp files never active;
- candidate fully written/closed/verified before manifest commit;
- manifest same-directory atomic replacement where supported;
- manifest contains only version/edition/checksum/timestamps/path basename, never credentials;
- restart validates manifest target; if invalid/missing, may scan retained candidates deterministically for newest valid LKG;
- current/retained file deleted only after reader close;
- stale temp cleanup is bounded.

### Safe reader swap

1. verified candidate + durable manifest already exist;
2. acquire `mu.Lock()` (waits for all prior RLock lookups);
3. replace active pointer;
4. release lock;
5. close retired reader; no lookup can still hold it because writer acquisition drained prior readers and post-swap readers see new pointer;
6. GC retired file after close.

If implementation uses reference counting/RCU instead, it must prove the same close invariant with race tests. Prefer RWMutex until benchmarks show need.

## MaxMind Update Client

Use `github.com/maxmind/geoipupdate/v8/client`, not copied URL/protocol logic or a subprocess.

Reviewed current client behavior:

- authenticated `New(accountID, licenseKey, ...)`;
- default `updates.maxmind.com` endpoint;
- `Download(ctx, editionID, currentMD5)` metadata check;
- unchanged database returns no new download;
- changed download follows current upstream flow.

The MD5 value is change detection only, not cryptographic authenticity. Candidate trust comes from HTTPS/authenticated upstream path plus strict MMDB verification/type validation before durable publication.

Use bounded HTTP client/timeouts and hard maximum downloaded database size. Approximately daily jittered checks are the default posture; document fleet quota/distribution considerations.

## Denial Contract

All gate failures/denials use one bounded protocol-agnostic response:

```text
HTTP 403 Forbidden
Content-Type: text/plain (or equivalent existing generic-safe standard)
Body: "Forbidden\n" or similarly generic bounded text
```

Do not reveal IP, country, rule, order, header, proxy chain, database status, or upstream failure.

No frontend-specific renderer is invoked because frontend identification is intentionally downstream.

## Observability

Integrate with existing process metrics bundle/registry; observer is a narrow contract projected into each generation.

Suggested metric semantics (final names follow repository conventions):

- `lip_geoip_decisions_total{decision,reason}`
- `lip_geoip_update_total{result}`
- `lip_geoip_database_ready`
- `lip_geoip_database_age_seconds`

Finite reason classes only, e.g.:

- `cidr_allow`
- `cidr_deny`
- `country_allow`
- `country_deny`
- `default_allow`
- `default_deny`
- `client_ip_error`
- `lookup_error`

No IP/CIDR/header/license-key labels. Country is omitted as a default metric label to avoid unnecessary policy/privacy exposure.

Denied hostile requests intentionally do not enter normal access log/general OTel/general HTTP metrics. Per-denial logging is off by default; if implementation needs a diagnostic it must be bounded/rate-limited.

Updater state transitions/failures use bounded operational logs and metrics with secret redaction.

## Failure Model

| Failure | Behavior |
|---|---|
| invalid static config | reject startup/candidate/check-config |
| malformed direct peer | 403 `client_ip_error` |
| malformed authoritative forwarded chain | 403 `client_ip_error` |
| country absent | normal no-country match |
| active MMDB lookup/decode error | 403 `lookup_error` |
| normal serving enable without required ready lookup | reject candidate/startup |
| validation-only/check-config without live lookup | static validation succeeds/fails only on config semantics; no network |
| managed initial acquisition fails and no LKG | fail enabled normal startup |
| periodic update fails with LKG | retain LKG; bounded telemetry |
| corrupt/oversized candidate | reject candidate DB; retain old active/LKG |
| manifest commit fails | close/delete candidate; retain old active/LKG |
| panic inside gate | outer recovery contains it |

## Security Considerations

### Trusted address boundary

Forwarding metadata has no authority unless immediate peer is explicitly trusted. Trust list/source mode reload atomically with policy.

### Secrets

MaxMind account/license credentials are process secrets. Never include them in request contexts, manifest, status, metrics, debug summary, or logs.

### GeoIP limitations

Documentation must state that VPN/proxy/relay/mobile networks and database lag can cause false positives/negatives. GeoIP is defense in depth, not identity, citizenship, sanctions, or legal-compliance proof.

### Abuse bounds

- bounded header bytes/hops;
- no DNS;
- no request network/filesystem;
- no unbounded per-IP cache;
- no per-denial normal log;
- bounded updater timeout/download size;
- strict candidate MMDB validation.

## Brownfield Compatibility

### Management plane

The process-owned reload management listener remains outside `ComposeStandardHTTP` and is not wrapped. Its existing loopback/dedicated-token trust model remains the recovery path after a bad data-plane policy candidate.

### Generation pinning

New policy applies to newly admitted requests/connections routed through the new generation. Existing SSE/WebSocket/in-flight work remains pinned to its original generation and is not actively revoked.

### Authentication

Auth `PeerIP` remains direct `RemoteAddr`; GeoIP's forwarded resolver is private to the gate.

### Frontends/backends

No DTO/connector changes. Allowed traffic reaches the unchanged downstream handler graph; denied traffic never identifies a frontend.

### No parallel runtime mechanisms

No new file watcher, config reload endpoint, service locator, mutable global policy, or feature stage.

## Testing Strategy

### RED pure policy/config contracts

- both complete order truth tables;
- overlapping country/CIDR; Moscow-office exception;
- unknown country vs lookup error;
- IPv4/IPv6/mapped IPv4;
- exact address host-prefix conversion;
- invalid countries/prefixes/source combinations;
- `NeedsCountryLookup` and no-lookup short circuits;
- reload/restart field classification;
- check-config static/no-network behavior.

### Client-IP tests/fuzzing

- direct host:port/IPv6/host-only;
- untrusted peer spoofing XFF/Forwarded;
- trusted one/multi-hop chain;
- attacker-prepended values;
- quoted/bracketed RFC values;
- unknown/obfuscated/malformed values;
- byte/hop limits;
- parser fuzzing for panic/allocation safety.

### Middleware-order integration

Spies must prove denied request never reaches:

- OTel;
- general HTTP metrics;
- trace/request-ID;
- normal access log;
- auth provider;
- frontend mux/decode;
- runtime/model/DB fake.

Also prove outer recovery and global security/server headers still wrap the response.

### MMDB/updater

- local valid/invalid database;
- managed LKG startup;
- initial download success/failure;
- unchanged update;
- timeout/auth failure;
- oversized/truncated/corrupt candidate;
- candidate file/manifest failure before activation;
- reader swap under concurrent lookup with `-race`;
- restart manifest/LKG recovery;
- stale temp/old-version cleanup;
- platform-aware Windows file lifecycle.

### Reload

- valid policy atomic change;
- invalid candidate preserves active;
- enable/disable wrapper presence;
- enable without required process lookup fails only in normal serving activation;
- DB/updater changes restart-required;
- `access.mode` behavior preserved;
- in-flight generation remains pinned.

### Performance

Benchmarks:

- disabled baseline (wrapper absent);
- enabled CIDR-only;
- enabled Country MMDB lookup;
- XFF/RFC Forwarded resolution;
- representative prefix scaling.

Do not add trie/cache complexity without benchmark evidence.

## Requirement Traceability

| Requirement | Primary design owner |
|---|---|
| R1 | middleware placement + wrapper omission |
| R2 | core order/evaluator |
| R3 | static `netip` compiler/matcher |
| R4 | CountryLookup semantics |
| R5 | direct resolver |
| R6 | trusted forwarded resolver |
| R7 | MMDB driven adapter |
| R8 | process readiness/LKG/provisioning |
| R9 | updater + transactional version publication |
| R10 | generation/process split + classifier |
| R11 | generic ingress 403 |
| R12 | bounded observer/metrics/log policy |
| R13 | hard bounds/races/benchmarks |
| R14 | data/management plane + generation compatibility |
| R15 | config/static compiler/check-config/docs |

## Migration and Delivery

No state/data migration is required. Existing configs without `access.geoip` behave exactly as before: no GeoIP process service, no wrapper, no lookup.

Implementation should be TDD-first and preserve architecture guardrails. The task plan should sequence contract tests before production implementation, then integrate process/generation composition, then run race/cross-platform/performance/release gates.
