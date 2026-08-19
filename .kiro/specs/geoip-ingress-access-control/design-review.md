# Design Validation Review

## Review Method

The initial design was validated as a brownfield HTTP-security/process-lifecycle change against:

- root and `.kiro` repository guidance;
- Go-LIP `main` at `ca43dde919f4d53716a98bf53ffb57bd61560607`;
- `internal/stdhttp/middleware.go` and normal access logging;
- `internal/stdhttp/request_plane.go` / `internal/stdhttp/contract` dependency direction;
- auth peer attribution (`RemoteAddr`, no forwarded-header trust);
- immutable generation compilation and atomic runtime reload (ADR 0008);
- `ProcessServices` ownership and close semantics;
- current `configreload.Classify` typed field classification;
- process-owned management listener separation;
- current metrics composition/registry model;
- all reconciled requirements and `gap-analysis.md`;
- MaxMind reader/updater lifecycle constraints documented in `research.md`.

The review treats security bypass, fail-open behavior, import cycles, hidden per-request network/I/O, and broken reload/management recovery semantics as NO-GO findings.

## Round 1: Middleware Placement and Scope

### Assessment

**Decision: PASS**

The proposed position is the earliest useful request-handler boundary that still preserves global security headers/server policy and outer panic containment.

It correctly bypasses on denial:

- general OTel HTTP instrumentation;
- general Prometheus HTTP middleware;
- request/trace ID setup;
- normal access log;
- transport auth;
- frontend parsing/routes;
- runtime/routing/DB/model work.

The design also correctly limits v1 to the standard data-plane generation. The separate process-owned reload management listener remains outside the gate.

### Guardrail

An implementation that wraps the process-level management server/common listener instead of the canonical standard data-plane generation must return to design review.

## Round 2: HTTP Contract Dependency Direction

### Assessment

**Decision: NO-GO pending clarification**

The initial design suggested putting `httpgeoip.ResolverConfig` into `internal/stdhttp/contract.HTTPSecurityInput` "if dependency direction permits." That leaves an avoidable ambiguity: `runtimebundle` intentionally imports the cycle-neutral `stdhttp/contract`, not root stdhttp, and should not become coupled to an implementation adapter to pass one config value.

### Resolution

Make the composition contract exact:

- `internal/core/geoip` owns `Policy`, `CountryLookup`, decision/reason values;
- `internal/stdhttp/contract` owns a **cycle-neutral data-only** `GeoIPSecurityInput` plus resolver configuration values (`Source string/enum`, `[]netip.Prefix`, byte/hop bounds) and imports only lower-level core/stdlib types;
- `internal/stdhttp/geoip` consumes that contract and owns the HTTP parser/middleware implementation;
- `runtimebundle` projects process lookup + compiled generation policy into the contract without importing `internal/stdhttp/geoip`.

Do not pass the whole process service, `any`, a generic service locator, or a preconstructed HTTP middleware closure through runtimebundle.

**Required design amendment: YES.**

## Round 3: `check-config` vs Live Readiness

### Assessment

**Decision: NO-GO pending clarification**

The initial generation-compilation flow said "if enabled and policy may require country lookup, require ready process lookup." ADR 0008 also shares generation compilation with `check-config` dry-run semantics. If readiness is unconditional, static validation can accidentally depend on an MMDB file, credentials, or network acquisition.

### Resolution

Separate two gates without duplicating validation rules:

1. **Static compile/validation** — parses/compiles GeoIP config and produces immutable policy/process config descriptors; used by startup, reload, and `check-config`; performs no update/download and requires no live MaxMind network.
2. **Runtime activation readiness** — normal serving composition checks the already process-owned lookup service only when the compiled enabled policy can require country lookup. It never downloads on a request/candidate path; startup process-service initialization is the only place allowed to make the bounded initial managed acquisition.

`check-config` invokes the same static compiler but skips runtime readiness. If the repository's existing dry-run build path has an explicit mode/capability for non-published resources, use it; otherwise introduce the smallest explicit compile-purpose flag at the composition boundary rather than a hidden global.

Local-file structural checking may occur only when the file is intentionally available to the validation environment; lack of MaxMind network must never invalidate static config.

**Required design amendment: YES.**

## Round 4: MMDB Publication Transaction

### Assessment

**Decision: NO-GO pending ordering fix**

The initial updater flow listed reader swap before LKG manifest persistence, while the failure table said a manifest publication failure should leave the old active/LKG unchanged. Those two statements cannot both be true.

### Resolution

Use a publication transaction with a non-failing in-memory commit step:

1. download/write candidate version;
2. close candidate write and open/verify candidate reader;
3. prepare and atomically replace a tiny LKG manifest selecting the verified candidate;
4. only if manifest publication succeeds, acquire the short reader publication lock and swap the active reader/version;
5. release lock, close retired reader, then GC old versions;
6. if manifest publication fails, close/delete candidate and keep old active reader/LKG.

A process crash after manifest commit but before in-memory swap is acceptable: the process is gone, and restart validates/loads the newly committed verified LKG. During a live process the manifest-to-reader interval is tiny and does not affect request correctness because requests use the in-memory active reader.

The manifest contains no secrets and uses same-directory atomic replacement where supported.

**Required design amendment: YES.**

## Round 5: Reader Concurrency and Close Safety

### Assessment

**Decision: PASS with explicit invariant**

The short `RWMutex` design is sufficient:

- lookup holds `RLock` through MMDB decode;
- update download/open/Verify never holds the writer lock;
- publication acquires `Lock`, which drains earlier lookups;
- after the swap, no new lookup can acquire the retired reader;
- old reader is safe to close after unlock because pre-swap readers have drained and new readers see only the new pointer.

Alternative reference-counted/RCU designs are unnecessary unless benchmark evidence shows lock contention.

### Invariant

`Reader.Close` must never race an operation using that reader.

## Round 6: Reload/Process Ownership

### Assessment

**Decision: PASS**

The policy/process split is aligned with ADR 0008 and current `ProcessServices`:

- pure policy fields reloadable;
- database/updater lifecycle fields restart-required;
- generation owns no updater goroutine/file/reader close;
- process service may stay warm while enforcement is disabled;
- enabling a country-dependent generation requires already provisioned readiness;
- mixed candidate changes still reject atomically.

The design correctly identifies the brownfield `classifyAccess` trap: current broad restart classification must be split by path so adding `GeoIP` does not silently make policy reload impossible.

## Round 7: Policy Correctness and Lookup Short-Circuiting

### Assessment

**Decision: PASS**

The explicit Apache truth table is testable and does not depend on rule order within a class.

The proposed short circuits are safe:

- `deny_allow` + allow-CIDR match can allow immediately because allow is the final phase;
- `allow_deny` + deny-CIDR match can deny immediately because deny is final;
- country lookup may be skipped when the compiler proves it cannot affect the result.

`NeedsCountryLookup` must be a property of the compiled decision plan, not a mutable request heuristic that might fail open when the reader is absent.

## Round 8: Client-IP Trust Boundary

### Assessment

**Decision: PASS**

Direct-peer default and explicit trusted-proxy recursion are appropriate.

Security-critical rules are present:

- forwarded header ignored for an untrusted direct peer;
- right-to-left trust evaluation;
- no "leftmost XFF is client" shortcut;
- bounded bytes/hops;
- malformed authoritative chain fails closed;
- IPv4-mapped IPv6 normalized;
- auth attribution remains direct-peer based.

The implementation should fuzz both forwarding parsers and use table fixtures for RFC quoting/bracket handling.

## Round 9: Cache and Resource-Abuse Model

### Assessment

**Decision: PASS**

Not requiring a per-IP cache is the safer design. One persistent local MMDB reader removes backend spam and file-open overhead. A cache keyed by hostile source IPs would add memory-DoS risk and database-version invalidation complexity.

Any future cache must be bounded and benchmark-justified.

## Round 10: Observability and Denial Rendering

### Assessment

**Decision: PASS**

The design intentionally makes denied hostile traffic invisible to normal access logging/general request spans and compensates with dedicated finite-label GeoIP metrics.

Generic 403 is correct at this early protocol-agnostic boundary. Pulling frontend error renderers upward would couple the security gate to routing/DTO concerns and defeat early rejection.

## SOLID Review

### Single Responsibility — PASS

- core policy decides only access rules;
- HTTP adapter resolves client address/renders denial;
- infrastructure service owns database lifecycle;
- config owns syntax/validation;
- runtimebundle owns composition/lifetime projection.

### Open/Closed — PASS

New database implementations can satisfy `CountryLookup`; new HTTP source modes can be deliberately added to the resolver without changing policy semantics.

### Liskov Substitution — PASS

Fake/local/MMDB `CountryLookup` implementations share the same found/error semantics and are contract-testable.

### Interface Segregation — PASS

The request gate depends on a country lookup, not updater/files/credentials. Runtimebundle passes a narrow security input, not `ProcessServices` wholesale.

### Dependency Inversion — PASS

Core owns ports; MaxMind remains an infrastructure adapter. Runtime generations depend on immutable values/interfaces.

## Hexagonal Architecture Review

**Decision: PASS after required amendments.**

- domain: policy/value objects;
- driving adapter: HTTP request/client-address resolver;
- driven adapter: MMDB/MaxMind update/files;
- composition root: runtimebundle/process services;
- no global locator;
- no backend/frontend pairwise implementation.

## Requirements Traceability Review

**Decision: PASS.**

Every Requirement 1-15 has at least one explicit design owner and planned verification category. The three required design amendments strengthen implementation precision without changing product scope.

## Required Design Amendments

Before tasks are generated, revise `design.md` to:

1. make `internal/stdhttp/contract.GeoIPSecurityInput` the explicit cycle-neutral composition DTO and prohibit runtimebundle→HTTP-adapter coupling;
2. split static GeoIP compile/validation from serving-time process-resource readiness so `check-config` stays offline;
3. fix MMDB publication order so LKG manifest commit precedes the in-memory active-reader swap and any manifest failure leaves the old active version untouched.

## Final Validation Decision

**PASS AFTER AMENDMENTS.**

No unresolved architectural blocker remains. Tasks must be generated only from the amended design.
