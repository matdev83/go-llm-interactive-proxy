# Implementation Plan

Implementation is TDD-first. Every production task follows RED → GREEN → refactor. Keep changes localized to the security/config/composition seams identified in `design.md`; do not introduce a new generic extension stage, backend/frontend-specific GeoIP logic, or a parallel reload system.

Each sub-task contains no more than five concrete actions and includes dependency, validation, and requirement traceability.

## 1. Freeze Policy, Configuration, and Reload Contracts With RED Tests

- [ ] 1.1 Freeze exact `deny_allow` / `allow_deny` semantics
  - Add RED table tests for allow-only, deny-only, both, and neither under both policy orders.
  - Add overlap fixtures where country deny + office allow CIDR produces the expected second-phase override.
  - Freeze deterministic finite decision reasons without literal IP/country/rule payloads.
  - Add no-country versus lookup-error tests with distinct outcomes.
  - _Boundary: core domain tests_
  - _Depends: none_
  - _Validation: `go test ./internal/core/geoip/...`_
  - _Requirements: 2.1-2.7, 4.5-4.7, 11.1-11.2, 12.7_

- [ ] 1.2 Freeze IP/CIDR normalization and decision-plan contracts
  - Add RED tests for IPv4, IPv6, exact-address host prefixes, `Prefix.Masked()`, and IPv4-mapped IPv6 `Unmap()` behavior.
  - Reject malformed CIDRs/addresses and hostnames without DNS.
  - Cover duplicate/overlapping prefixes and deterministic matching independent of rule order within a class.
  - Assert `NeedsCountryLookup`/short-circuit behavior never calls lookup when the compiler proves CIDR/default outcome is final.
  - _Boundary: core domain/compiler tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/geoip/...`_
  - _Requirements: 3.1-3.7, 7.7-7.8, 8.9, 13.1-13.3_

- [ ] 1.3 Freeze GeoIP config model and static validation
  - Add RED YAML/config tests for absent/disabled, valid deny-list/allow-list, invalid order/country/CIDR, and managed/local source one-of semantics.
  - Require forwarded client-IP sources to have non-empty valid trusted-proxy prefixes and reject local-source managed updater fields.
  - Pin bounded update interval/header/hop settings and safe defaults.
  - Prove managed credentials are not accepted as ordinary reloadable diagnostic data and are never rendered by config diagnostics.
  - _Boundary: core config tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/core/config/...`_
  - _Requirements: 4.1-4.2, 6.1, 9.4-9.5, 13.12-13.13, 15.1-15.3_

- [ ] 1.4 Freeze reload classification and network-free `check-config`
  - Add RED classifier tests proving `access.mode` remains restart-required while pure GeoIP policy/resolver fields are reloadable.
  - Prove database source/path/edition/update lifecycle/credential-source changes are restart-required and mixed candidates reject atomically.
  - Add validation-only/check-config tests proving static GeoIP compilation performs no MaxMind network/update call and does not require a live reader.
  - Preserve existing unrelated reload classifications.
  - _Boundary: config reload / dry-run contract tests_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/core/configreload/... ./internal/infra/runtimebundle/...` plus focused `check-config` tests_
  - _Requirements: 10.1-10.9, 14.5, 15.4-15.6_

## 2. Implement the Pure GeoIP Policy Core

- [ ] 2.1 Implement `internal/core/geoip` value types and compiler
  - Add `Order`, finite `Reason`, `Decision`, normalized rule-class, immutable `Policy`, and narrow `CountryLookup` contracts.
  - Compile country codes and `netip.Prefix` rules once; defensively own slices/maps and canonicalize exact addresses/prefixes.
  - Compute a safe decision plan/`NeedsCountryLookup` from rule semantics.
  - Keep package free of HTTP, MaxMind, logger, Prometheus, runtimebundle, and frontend/backend imports.
  - _Boundary: core domain_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test ./internal/core/geoip/...`; architecture import tests_
  - _Requirements: 2.1-2.7, 3.1-3.7, 4.1-4.7, 7.2, 7.7, 8.9_

- [ ] 2.2 Implement deterministic policy evaluation
  - Implement CIDR class matching, safe final-phase short circuits, optional country lookup, and exact Apache truth-table resolution.
  - Treat `found=false` as ordinary no-country input and lookup error as typed fail-closed evidence.
  - Normalize request addresses with `Unmap()` before all matches/lookups.
  - Preserve finite decision reasons suitable for bounded metrics and generic denial rendering.
  - _Boundary: core domain_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/core/geoip/...`_
  - _Requirements: 2.1-2.7, 3.4, 4.3-4.7, 7.6-7.8, 11.1, 12.7_

## 3. Build the HTTP Client-Address Security Boundary

- [ ] 3.1 Implement direct-peer resolution
  - Add `internal/stdhttp/geoip` direct `RemoteAddr` parsing with host:port, IPv6, and host-only test/server forms.
  - Parse only literal IPs with `netip`, apply `Unmap()`, and reject hostnames/invalid values without DNS.
  - In direct mode ignore XFF/`Forwarded` completely.
  - Keep existing auth `peerIPFromRemoteAddr` behavior untouched.
  - _Boundary: HTTP driving adapter_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/stdhttp/geoip/... ./internal/stdhttp/auth/...`_
  - _Requirements: 5.1-5.5, 14.7_

- [ ] 3.2 Implement bounded trusted X-Forwarded-For resolution
  - Add RED/green parser for bounded byte/hop lists with strict IP parsing and right-to-left trusted-hop evaluation.
  - Ignore XFF authority entirely when immediate peer is untrusted.
  - Reject malformed/empty/over-limit authoritative chains instead of skipping ambiguity or trusting leftmost input.
  - Cover IPv4, IPv6, attacker-prepended values, and multiple trusted proxies.
  - _Boundary: HTTP security adapter_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/stdhttp/geoip/...`_
  - _Requirements: 6.1-6.3, 6.5-6.7, 13.4, 13.6_

- [ ] 3.3 Implement bounded RFC 7239 `Forwarded` resolution
  - Parse only the ordered `for=` information needed for client authority, including quoted values and bracketed IPv6.
  - Apply the same trusted-peer/right-to-left and byte/hop bounds as XFF.
  - Reject malformed/ambiguous/obfuscated/`unknown` authoritative values when they prevent safe resolution.
  - Add fuzz targets for XFF/Forwarded parsers with panic/allocation/bounds assertions.
  - _Boundary: HTTP security adapter_
  - _Depends: 3.1, 3.2_
  - _Validation: `go test ./internal/stdhttp/geoip/...`; targeted fuzz corpus/CI-compatible fuzz smoke tests_
  - _Requirements: 6.1-6.8, 13.4, 13.6_

## 4. Implement the Local MMDB Process Service

- [ ] 4.1 Add the MMDB CountryLookup adapter
  - Add `internal/infra/geoip` using `github.com/oschwald/maxminddb-golang/v2` behind the core `CountryLookup` port.
  - Open/verify an expected Country MMDB and decode only `country.iso_code`; do not fall back to registered-country.
  - Reuse one concurrency-safe active reader rather than opening per request.
  - Add contract fixtures for found, absent-country, decode/error, incompatible/corrupt database cases.
  - _Boundary: driven infrastructure adapter_
  - _Depends: 2.2_
  - _Validation: `go test ./internal/infra/geoip/...`_
  - _Requirements: 4.3-4.7, 7.1-7.6, 13.1-13.3_

- [ ] 4.2 Implement synchronized active-reader publication
  - Add service-owned reader version state with lookups holding a read lease/`RLock` through decode.
  - Open/Verify candidate readers outside the writer lock and swap only through a short publication critical section.
  - Prove old readers are never closed while outstanding lookup operations can still use them.
  - Add high-concurrency lookup/swap tests and targeted `go test -race` coverage.
  - _Boundary: process service / concurrency_
  - _Depends: 4.1_
  - _Validation: `go test -race ./internal/infra/geoip/...`_
  - _Requirements: 7.5, 9.9, 13.5, 13.9_

- [ ] 4.3 Implement local-source readiness and status
  - Implement operator-owned local MMDB source loading/readiness without updater ownership.
  - Fail normal serving readiness when an enabled country-dependent policy requires a missing/corrupt/incompatible local file.
  - Expose bounded internal readiness/version/age metadata with no client data or secrets.
  - Ensure validation-only/check-config does not require external network and keeps live readiness distinct from static validity.
  - _Boundary: process service / status_
  - _Depends: 4.1, 1.4_
  - _Validation: `go test ./internal/infra/geoip/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 8.1, 8.5-8.9, 12.5-12.6, 15.4-15.6_

## 5. Implement Managed MaxMind Updates and Last-Known-Good Storage

- [ ] 5.1 Integrate MaxMind's supported Go update client
  - Add `github.com/maxmind/geoipupdate/v8/client` behind a small updater adapter with injected bounded HTTP client/clock/jitter for tests.
  - Use account/license secrets from process-owned secure environment/config convention and redact them everywhere.
  - Use `Download(ctx, edition,currentChecksum)` semantics; treat returned MD5 only as upstream change detection, not cryptographic authenticity.
  - Add fake-server/client tests for unchanged, auth failure, timeout, and changed download flows.
  - _Boundary: driven update adapter_
  - _Depends: 4.1_
  - _Validation: `go test ./internal/infra/geoip/...`_
  - _Requirements: 9.1-9.5, 13.12, 15.3_

- [ ] 5.2 Implement bounded versioned download and candidate validation
  - Stream updates to same-directory temporary/versioned files with hard byte/time limits; never overwrite active MMDB in place.
  - Complete/close the candidate, open it, Verify it, and validate expected Country semantics before any durable publication.
  - Reject/trash truncated, oversized, corrupt, wrong-edition/type, or disk-failed candidates while preserving active LKG.
  - Add fault-injection tests for every pre-publication boundary.
  - _Boundary: process file lifecycle_
  - _Depends: 4.2, 5.1_
  - _Validation: `go test ./internal/infra/geoip/...`_
  - _Requirements: 9.5-9.8, 13.9_

- [ ] 5.3 Implement transactional manifest-before-reader publication
  - Persist a tiny secret-free LKG manifest selecting the verified candidate via safe same-directory atomic replacement before active-reader swap.
  - If manifest publication fails, close/delete candidate and leave old active reader/LKG untouched.
  - After durable commit, perform the non-I/O reader swap, retire the old reader only after prior lookups drain, then GC obsolete files.
  - Add crash/restart-style tests where a committed manifest is recovered even if no prior in-memory swap is assumed.
  - _Boundary: process persistence/lifecycle_
  - _Depends: 4.2, 5.2_
  - _Validation: `go test -race ./internal/infra/geoip/...`_
  - _Requirements: 8.4, 9.6-9.11, 13.5, 13.9_

- [ ] 5.4 Implement LKG startup recovery and managed update scheduler
  - Load/validate manifest target first and deterministically recover a retained valid version if manifest is missing/invalid.
  - When enabled country-dependent startup has no LKG, perform one bounded managed acquisition attempt before serving; fail if still unavailable.
  - Start an approximately daily configurable update loop with randomized jitter only for managed + update-enabled process configuration.
  - On update errors retain LKG and emit bounded state-change telemetry; stop scheduler through process service Close without background leaks.
  - _Boundary: process lifecycle_
  - _Depends: 5.1-5.3_
  - _Validation: `go test ./internal/infra/geoip/...`; `go.uber.org/goleak` where repository conventions use it_
  - _Requirements: 8.2-8.8, 9.1, 9.4, 9.7, 9.11-9.12_

- [ ] 5.5 Certify Windows-safe active-file lifecycle
  - Add platform-aware tests/helpers proving no active/mapped file is overwritten/deleted before its reader closes.
  - Use versioned filenames and manifest publication on Windows exactly as on Unix rather than a special in-place replacement shortcut.
  - Cover stale temp cleanup and retained-version cleanup after service restart/close.
  - Document any OS-specific atomic-replace primitive behind a narrow file adapter with equivalent semantics.
  - _Boundary: process file lifecycle / cross-platform_
  - _Depends: 5.3, 5.4_
  - _Validation: Windows CI targeted package tests + standard Unix package tests_
  - _Requirements: 9.8-9.11, 13.9_

## 6. Add Bounded GeoIP Observability

- [ ] 6.1 Add process metrics and observer adapter
  - Extend existing metrics bundle/registry with finite-label GeoIP decision/update/readiness/age metrics following repository naming conventions.
  - Implement the narrow `contract.GeoIPObserver` projection used by generation middleware.
  - Prohibit IP, CIDR, raw header, license key, and arbitrary strings as metric labels.
  - Add cardinality tests enumerating the complete allowed decision/update label space.
  - _Boundary: observability adapter_
  - _Depends: 2.2, 4.3_
  - _Validation: `go test ./internal/infra/metrics/... ./internal/infra/geoip/...`_
  - _Requirements: 12.1-12.2, 12.5-12.7_

- [ ] 6.2 Add bounded operational logging without denial spam
  - Add only lifecycle/update status logs needed for LKG selected/update failed/recovered/updated with safe bounded fields.
  - Do not add one normal log entry per GeoIP denial and do not route denials through normal access logging.
  - Add tests/guards that credentials and raw forwarding headers are absent from logs/status/diagnostics.
  - Keep any optional denial security diagnostic disabled or rate-limited by explicit bounded policy.
  - _Boundary: observability/security_
  - _Depends: 5.4, 6.1_
  - _Validation: focused logging/diagnostic tests_
  - _Requirements: 12.3-12.6_

## 7. Compose Process Ownership and Generation Security Projection

- [ ] 7.1 Extend the cycle-neutral HTTP security contract
  - Add data-only `GeoIPResolverConfig`, `GeoIPSecurityInput`, and observer contract under `internal/stdhttp/contract` using only stdlib/lower-level core dependencies.
  - Add defensive copy helpers for trusted-proxy slices and any other mutable projections.
  - Prove `runtimebundle` does not import `internal/stdhttp/geoip` or root stdhttp and no import cycle/service locator is introduced.
  - Add compile/architecture guardrail tests for the new dependency direction.
  - _Boundary: composition contract_
  - _Depends: 2.1, 3.1, 6.1_
  - _Validation: `go test ./internal/stdhttp/contract/... ./internal/archtest/...`_
  - _Requirements: 7.2, 10.2, 14.5-14.6_

- [ ] 7.2 Add GeoIP service to `ProcessServices`
  - Construct `internal/infra/geoip.Service` once from startup-fixed database configuration and transfer close ownership to `ProcessServices`.
  - Permit configured service provisioning/updating while request enforcement is disabled; create no service when no database source is configured.
  - Guarantee generation compilation receives only a non-owning lookup/status capability and cannot Close/reconfigure the service.
  - Preserve ProcessServices close ordering so readers/updater stop only after request generations no longer use them.
  - _Boundary: composition root / lifecycle_
  - _Depends: 4.3, 5.4, 6.1_
  - _Validation: `go test ./internal/infra/runtimebundle/...`_
  - _Requirements: 1.7, 8.1-8.8, 10.4, 10.7, 10.9, 14.5_

- [ ] 7.3 Compile and project generation-scoped GeoIP policy
  - Reuse the static compiler for startup/reload/check-config; keep normal-serving readiness as a separate activation gate.
  - In normal serving composition, reject enabled country-dependent candidates when the existing process lookup is absent/not ready; allow proven CIDR-only policies without MMDB.
  - Build a defensive `GeoIPSecurityInput` for enabled generations and a zero/nil projection for disabled generations.
  - Ensure validation-only/check-config never triggers DB acquisition/update and requires no live MaxMind network.
  - _Boundary: composition root / generation compiler_
  - _Depends: 1.4, 7.1, 7.2_
  - _Validation: `go test ./internal/infra/runtimebundle/...`_
  - _Requirements: 1.3-1.4, 8.1, 8.7-8.9, 10.1-10.9, 15.4-15.6_

## 8. Integrate the Early HTTP Gate

- [ ] 8.1 Implement generic GeoIP middleware and 403 renderer
  - Build `internal/stdhttp/geoip` middleware from the cycle-neutral security input; resolve client IP, evaluate policy, and delegate allowed traffic unchanged.
  - Return one bounded generic 403 for policy denial, client-IP resolution failure, or fail-closed lookup error without frontend-specific rendering.
  - Record only bounded GeoIP observer decisions and reveal no IP/country/rule/proxy/database detail.
  - Add unit tests proving no downstream handler call on denial/error and unchanged pass-through on allow.
  - _Boundary: HTTP driving adapter_
  - _Depends: 2.2, 3.1-3.3, 7.1_
  - _Validation: `go test ./internal/stdhttp/geoip/...`_
  - _Requirements: 5.1-6.8, 11.1-11.5, 12.1-12.4_

- [ ] 8.2 Install the gate at the exact standard middleware boundary
  - Modify `stackHTTPHandler` so enabled GeoIP wraps inside outer recovery/global server-security middleware and outside OTel/general Prometheus/request-ID/access-log/auth/routes.
  - Omit the wrapper entirely when `GeoIPSecurityInput.Policy` is nil/disabled.
  - Add order spies proving denied traffic never reaches each expensive/noisy inner layer while security/server headers and recovery remain effective.
  - Preserve the separate management listener and existing `ComposeStandardHTTP` ownership boundary.
  - _Boundary: standard HTTP composition_
  - _Depends: 7.3, 8.1_
  - _Validation: `go test ./internal/stdhttp/...`_
  - _Requirements: 1.1-1.6, 12.3, 13.7, 14.1-14.2, 14.6_

- [ ] 8.3 Certify auth/frontend/runtime non-regression
  - Prove allowed direct traffic retains current auth `PeerIP` attribution and forwarded GeoIP mode does not rewrite auth metadata.
  - Prove no official frontend/backend needs GeoIP-specific DTO/connector branches.
  - Verify denied traffic causes zero auth-provider calls, frontend decode, route/runtime/model/DB fake calls, and zero normal access-log/general trace/general HTTP metric events.
  - Run focused standard frontend/auth contract suites for allowed requests.
  - _Boundary: brownfield integration tests_
  - _Depends: 8.2_
  - _Validation: focused `go test ./internal/stdhttp/... ./internal/plugins/frontends/...`_
  - _Requirements: 1.1, 5.4, 13.7, 14.6-14.7_

## 9. Complete Reload, Generation-Pinning, and Management-Plane Certification

- [ ] 9.1 Implement explicit GeoIP reload classification
  - Refactor `classifyAccess` at field granularity, keeping `access.mode` restart-required.
  - Mark pure policy/resolver paths reloadable and DB/updater lifecycle paths restart-required exactly as `design.md` specifies.
  - Preserve existing sorted/compacted mixed-candidate failure semantics and bounded restart-required diagnostics.
  - Add regression tests for configs without GeoIP and unrelated access/auth behavior.
  - _Boundary: config reload_
  - _Depends: 1.4, 1.3_
  - _Validation: `go test ./internal/core/configreload/...`_
  - _Requirements: 10.1-10.5, 14.5, 15.1_

- [ ] 9.2 Certify runtime enable/disable and immutable generation behavior
  - Add end-to-end reload tests from disabled→enabled→policy changed→disabled with request assertions against each published generation.
  - Prove invalid candidates and enable-without-required-ready-lookup leave active generation unchanged.
  - Prove disabled generations contain no gate/lookup invocation while a pre-provisioned process DB service may continue its own lifecycle.
  - Prove existing in-flight/pinned request or long-lived transport continues on its acquired generation instead of being retroactively revoked.
  - _Boundary: runtime reload integration_
  - _Depends: 7.3, 8.2, 9.1_
  - _Validation: `go test ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/... ./internal/stdhttp/...`_
  - _Requirements: 1.3-1.7, 8.7-8.8, 10.1-10.9, 14.3-14.4_

- [ ] 9.3 Certify management-plane recovery isolation
  - Add a test/proof that data-plane GeoIP policy does not wrap the process-owned reload management listener.
  - Preserve existing loopback/dedicated-token management authorization and route ownership.
  - Demonstrate a data-plane deny-all candidate does not remove the existing management recovery path.
  - Document that management-plane GeoIP would require a separate explicit feature/design.
  - _Boundary: management/data-plane integration_
  - _Depends: 8.2, 9.2_
  - _Validation: focused management/reload contract tests_
  - _Requirements: 14.1-14.2_

## 10. Performance, Security, Documentation, and Final Gates

- [ ] 10.1 Add security/property/fuzz regression suite
  - Add adversarial fixtures for forwarded spoofing, mapped-address bypass attempts, overlapping policies, malformed/oversized headers, corrupt databases, and unique-source/cache memory pressure.
  - Fuzz forwarding parsers and pure rule compiler/evaluator boundaries with fixed allocation/input limits where measurable.
  - Run targeted race tests for lookup/update/reload interaction and verify no goroutine/resource leak.
  - Add secret/log/metric cardinality assertions for attacker-controlled inputs.
  - _Boundary: cross-cutting security tests_
  - _Depends: 3.3, 5.4, 6.2, 9.2_
  - _Validation: focused fuzz smoke tests; `go test -race` on affected packages_
  - _Requirements: 6.5-6.7, 9.5-9.12, 12.2-12.6, 13.3-13.9_

- [ ] 10.2 Benchmark and profile the hot path before optional optimization
  - Benchmark baseline/disabled stack proving the wrapper is absent, enabled CIDR-only decisions, Country MMDB lookup, XFF/Forwarded resolution, and representative prefix counts.
  - Record allocation/latency results in implementation PR notes or benchmark docs and compare against existing standard HTTP baseline.
  - Do not add a prefix trie or per-IP cache unless profiles show a material bottleneck and the optimization remains strictly bounded/version-safe.
  - Add performance regression thresholds only where stable enough for the repository's CI environment.
  - _Boundary: performance validation_
  - _Depends: 8.2, 10.1_
  - _Validation: `go test -bench` focused packages + profiling as needed_
  - _Requirements: 7.5, 13.1-13.3, 13.10-13.11_

- [ ] 10.3 Add operator/security documentation and examples
  - Document deny-list, allow-list, Russia+office exception, direct peer, trusted XFF/Forwarded, managed update, and local-MMDB examples.
  - Document process-vs-generation reload semantics, warm provisioning while disabled, restart-required DB changes, `check-config`, and management-plane exclusion.
  - Document MaxMind credential setup, GeoLite licensing/attribution/update obligations, multi-replica quota/distribution considerations, and LKG recovery behavior.
  - Warn clearly that GeoIP is approximate defense in depth and not identity/citizenship/sanctions proof.
  - _Boundary: operator/security docs_
  - _Depends: 9.3, 10.2_
  - _Validation: docs examples parsed by config tests where practical_
  - _Requirements: 9.4, 13.12-13.13, 14.1-14.7, 15.7-15.8_

- [ ] 10.4 Run architecture, quality, cross-platform, and release gates
  - Run affected package tests, `go test -race`, config/reload/management integration suites, architecture import/complexity guardrails, and Windows-targeted GeoIP file lifecycle tests.
  - Run repository standard formatting/lint/vet/security/vulnerability/release gates required by `AGENTS.md` and current CI.
  - Verify no production dependency leaks MaxMind types into core/stdhttp contract and no new backend/frontend compatibility matrix is added.
  - Review final diff for spec invariants: disabled wrapper omission, fail-closed errors, LKG retention, bounded telemetry, no secret leakage, no management-plane lockout.
  - _Boundary: final certification_
  - _Depends: all prior tasks_
  - _Validation: repository standard release gates + targeted cross-platform CI_
  - _Requirements: 1.1-15.8_
