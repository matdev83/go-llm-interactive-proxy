# Implementation Plan

The implementation is intentionally staged around the smallest useful v1. Every production task starts with focused RED evidence; no threat-feed/WAF/persistence framework is introduced.

- [ ] 1. Establish the self-defense configuration and domain contracts
  - [ ] 1.1 RED-test default-on configuration, validation, and reload classification
    - Add tests proving omitted `access.self_defense` resolves to enabled + impossible paths enabled + the documented adaptive defaults, while explicit `false` is preserved.
    - Cover duration/count/CIDR bounds, `max_quarantine >= initial_quarantine`, offline `check-config`, and invalid candidate rollback.
    - Prove policy fields are reloadable while `state_ttl` / `max_entries` are restart-required and mixed changes reject atomically.
    - _Requirements: 1.1,1.2,7.1-7.6,10.4_
    - _Boundary: config/wiring_
    - _Depends: none_
    - _Validation: `go test ./internal/core/config/... ./internal/core/configreload/...`_

  - [ ] 1.2 Define the minimal `core/ingressdefense` policy/state types and architecture admission
    - Add RED compile/architecture tests before introducing the new top-level core package.
    - Define immutable policy/state-limit values, closed reason/transition values, and exact-IP-only state semantics with no HTTP/provider/public-SDK types.
    - Add the core-ownership census rationale: default provider-neutral ingress security remains required when optional features are absent.
    - Keep YAML parsing/defaulting in existing `internal/core/config`; do not add a public `pkg/*` contract.
    - _Requirements: 4.3-4.6,5.6,6.1-6.4,10.1-10.2_
    - _Boundary: core domain policy + architecture guard_
    - _Depends: 1.1_
    - _Validation: `go test ./internal/core/ingressdefense/... ./internal/archtest/...`_

- [ ] 2. Implement bounded process-local adaptive state
  - [ ] 2.1 Implement failure-window and exponential-quarantine transitions with fake time
    - RED-test isolated failures, threshold crossing, window reset, repeated offense escalation, maximum cap, quarantine expiry, probe offense, successful-auth clear, and inactivity TTL.
    - Use saturating duration arithmetic; reads must not refresh hostile inactivity TTL.
    - An impossible-path offense increments the same offense level used by thresholded auth failures; no generic weighted scoring engine.
    - _Requirements: 3.4-3.5,4.1-4.6,5.3,5.5,6.1,6.4_
    - _Boundary: core domain policy/state_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/core/ingressdefense/...`_

  - [ ] 2.2 Prove hard capacity, eviction, exact-IP isolation, and concurrency bounds
    - RED-test unique-IP churn above capacity, lazy TTL expiry, deterministic bounded eviction/replacement, IPv4-mapped normalization at the adapter boundary, and exact address isolation.
    - Prove one source never creates `/24`, `/64`, ASN or country state.
    - Use sharded or equivalently fine-grained synchronization; add barriers/race evidence proving unrelated addresses do not wait on a long-held global request lock.
    - No per-address goroutine/timer and no persistence.
    - _Requirements: 5.6,6.1-6.5,9.4_
    - _Boundary: core domain state_
    - _Depends: 2.1_
    - _Validation: `go test ./internal/core/ingressdefense/...`; `go test -race ./internal/core/ingressdefense/...` where supported_

- [ ] 3. Add the conservative early HTTP gate
  - [ ] 3.1 (P) Implement and certify the fixed impossible-path matcher
    - RED-test the small audited v1 exact/prefix/traversal set (for example `.env`, `.git`, WordPress, phpMyAdmin/Adminer, PHPUnit and selected obvious CGI/file traversal probes) plus near-miss legitimate paths.
    - Match request-target path structure only; never read request body/canonical messages/tool arguments or arbitrary query values.
    - Add a regression that evaluates all current standard frontend route claims and management/recovery paths against the built-in matcher and fails on collision.
    - _Requirements: 3.1-3.3,3.6,10.3,10.5_
    - _Boundary: stdhttp driving adapter/tests_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/stdhttp/selfdefense/... ./internal/stdhttp/...`_

  - [ ] 3.2 Reuse GeoIP client-IP resolution and implement early path/quarantine middleware
    - RED-test direct peer, untrusted XFF/Forwarded spoofing, trusted chains, malformed authoritative chains, parser bounds, and IPv4-mapped normalization using the existing #387 fixtures/semantics.
    - Resolve once, attach the normalized address to the cycle-neutral request context, apply impossible-path behavior, adaptive exemptions, quarantine lookup, and generic 403/404/429 responses.
    - Impossible-path hit records one offense only for non-exempt sources; adaptive exemption bypasses quarantine lookup/mutation but not deterministic 404.
    - Middleware must not read bodies, call external services, or retain raw paths.
    - _Requirements: 1.4,2.1-2.5,3.1-3.5,5.1-5.2,8.3,9.1-9.2_
    - _Boundary: stdhttp driving adapter + cycle-neutral HTTP contract_
    - _Depends: 2.1,3.1_
    - _Validation: `go test ./internal/stdhttp/selfdefense/... ./internal/stdhttp/geoip/...`_

- [ ] 4. Make dynamic quarantine authentication-aware
  - [ ] 4.1 (P) Add the private conservative credential-presence probe
    - RED-test local API-key requests with absent/present effective key headers, local-noop credential-free auth, future/custom/unknown providers, and mixed provider chains.
    - Implement a private optional provider capability; arbitrary providers that do not implement it must aggregate to `MayAuthenticate`.
    - Return `DefinitelyNoCredential` only when the complete active provider set can prove the request cannot authenticate as presented; never return or retain secret values.
    - Preserve existing public `httpauth.Provider` and SDK contracts.
    - _Requirements: 5.1-5.2,9.6,10.2_
    - _Boundary: stdhttp auth adapter/private contract_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/stdhttp/auth/...`_

  - [ ] 4.2 Observe authoritative auth outcomes at the provider-chain chokepoint
    - RED-test pre-principal 401, 403, 5xx/provider error, principal-then-later-reject, full successful chain, and disabled self-defense.
    - Track whether any principal was accepted; count only terminal 401 before principal, and clear exact-address adaptive state only after the full provider chain succeeds immediately before route delegation.
    - Preserve existing auth renderer/status/body semantics; state observation must not replace or infer from downstream final status.
    - Keep the existing `auth.Middleware` entry point as a compatibility wrapper if an internal options variant is introduced.
    - _Requirements: 1.2,4.1-4.2,5.2-5.5,9.6,10.4_
    - _Boundary: stdhttp auth driving adapter_
    - _Depends: 2.1,3.2,4.1_
    - _Validation: `go test ./internal/stdhttp/auth/...`_

- [ ] 5. Compose process state and immutable generation policy
  - [ ] 5.1 Own one bounded state instance in ProcessServices
    - RED-test construction, disabled-start lightweight ownership, process close, partial startup rollback and one-instance reuse across generation reload.
    - Construct state from effective startup-fixed `max_entries` / `state_ttl`; it owns no goroutine, file, database or network resource.
    - Add the non-owning self-defense state/policy projection to the cycle-neutral HTTP security contract; generations never close/resize process state.
    - _Requirements: 1.2,6.1-6.5,7.4,7.6_
    - _Boundary: composition root / process lifecycle_
    - _Depends: 1.2,2.2_
    - _Validation: `go test ./internal/infra/runtimebundle/...`_

  - [ ] 5.2 Wire the self-defense gate and auth hooks in the canonical standard stack
    - RED-test exact outer-to-inner ordering: global security/server/recovery -> existing GeoIP -> self-defense -> general tracing/metrics/request-id/access-log -> inner recovery -> auth -> routes.
    - Project the existing compiled GeoIP resolver source/trusted prefixes to self-defense even when GeoIP enforcement policy is disabled; do not require a country database.
    - Prove GeoIP hard denial wins before self-defense, self-defense impossible/quarantine denial never reaches expensive/noisy inner layers, and outer headers/recovery remain effective.
    - Disabled generation omits both ingress self-defense and auth observation structurally.
    - _Requirements: 1.1-1.4,2.1,7.3,8.1,8.4-8.5,10.4_
    - _Boundary: stdhttp + runtimebundle generation composition_
    - _Depends: 3.2,4.2,5.1_
    - _Validation: `go test ./internal/stdhttp/... ./internal/infra/runtimebundle/...`_

  - [ ] 5.3 Prove reload, last-good rollback, and management recovery isolation
    - Policy-only reload must publish a new immutable handler graph while retaining adaptive state; in-flight requests remain on their admitted generation policy.
    - Invalid/mixed restart-required candidates leave last-good generation and process state active.
    - Explicitly prove the separate management/recovery listener is not wrapped by self-defense and remains usable after bad data-plane policy candidates.
    - _Requirements: 1.3,7.2-7.6,8.4,10.5_
    - _Boundary: config reload + composition/integration tests_
    - _Depends: 5.2_
    - _Validation: `go test ./internal/core/configreload/... ./internal/infra/runtimebundle/... ./internal/stdhttp/admin/configreload/...`_

- [ ] 6. Add bounded observability and operator documentation
  - [ ] 6.1 (P) Add the three self-defense process metrics
    - RED-test counters/gauge registration and finite label values before wiring them into the metrics bundle.
    - Provide denial count by closed reason, quarantine-transition count by closed reason, and current state entry gauge.
    - Prove source IP/CIDR/path/query/header/User-Agent/credential/principal/country cannot become labels.
    - Metrics absence must not affect decisions; no per-request hostile log is added by default.
    - _Requirements: 9.1-9.5_
    - _Boundary: observability infrastructure_
    - _Depends: 1.2_
    - _Validation: `go test ./internal/infra/metrics/... ./internal/core/ingressdefense/...`_

  - [ ] 6.2 Document the deliberately small v1 configuration and safety trade-offs
    - Update canonical config/operator documentation with default-on/explicit opt-out behavior, shared `access.geoip.client_ip` trust, adaptive exemptions, defaults, reload/restart fields and generic response semantics.
    - State that fixed IP/CIDR policy remains under `access.geoip`, successful auth resets rather than permanently trusts an IP, and credential-bearing/unknown-auth traffic may still reach auth to protect legitimate shared-IP users.
    - List external feeds, WAF/body inspection, persistent/distributed state and subnet escalation as deferred/non-goals.
    - _Requirements: 1.1-1.5,5.1-5.6,7.1-7.4,8.1-8.5,10.1-10.4_
    - _Boundary: docs/config examples_
    - _Depends: 5.2_
    - _Validation: config example parse/check-config tests where available + doc review against effective defaults_

- [ ] 7. Run the focused adversarial acceptance matrix
  - [ ] 7.1 Certify shared-address safety and auth classification
    - Scenario: hostile no-credential traffic at IP X triggers quarantine; a valid credential from IP X still reaches auth, succeeds, clears the entry, and reaches a legitimate frontend.
    - Cover local API key, local-noop, unknown/custom provider, invalid credential, principal-then-reject, 403 and 5xx outcomes.
    - Prove success is not permanent trust: a later impossible path is still 404 and later hostile failures can create fresh state.
    - _Requirements: 4.1-4.6,5.1-5.5,9.6_
    - _Boundary: stdhttp composed acceptance tests_
    - _Depends: 5.2_
    - _Validation: focused composed `httptest` suite through the standard handler stack_

  - [ ] 7.2 Certify false-positive, spoofing and resource-exhaustion boundaries
    - Legitimate LLM routes with bodies containing SQLi/XSS/shell/malware/traversal examples must not match self-defense solely because of content.
    - Cover untrusted forwarding spoof, malformed trusted forwarding, IPv4-mapped IPv6, exact-IP-only isolation, no `/24`/`/64` escalation, adaptive exemption, existing GeoIP hard denial, route non-collision and management isolation.
    - Drive more unique hostile addresses than `max_entries` and prove bounded state/metric cardinality with no unbounded path retention.
    - _Requirements: 2.1-2.5,3.2,3.5-3.6,5.6,6.2-6.5,8.1-8.5,9.3-9.5,10.3-10.5_
    - _Boundary: security/concurrency/integration tests_
    - _Depends: 2.2,5.3,6.1,7.1_
    - _Validation: focused stdhttp/runtimebundle tests; race test for ingressdefense where supported_

- [ ] 8. Run final architecture and repository certification
  - [ ] 8.1 Run focused quality/architecture gates and review scope minimality
    - Run core ingress-defense, config/reload, stdhttp/auth/selfdefense, runtimebundle and metrics focused suites plus architecture guards.
    - Grep/review that no threat-feed client, persistence schema, generic WAF/rule DSL, body scanner, public SDK contract or automatic subnet escalation entered the implementation.
    - Run `make quality-checks`; run `make test-cost` if the implementation materially changes default-suite cost.
    - _Requirements: 1-10_
    - _Boundary: repository certification_
    - _Depends: 6.2,7.2_
    - _Validation: focused `go test` commands; `make quality-checks`; `make test-cost` when applicable_

  - [ ] 8.2 Run wide QA and close the spec truthfully
    - Run `make qa` after focused gates pass, plus race evidence where supported for the bounded state.
    - Revalidate implementation-time middleware/auth/GeoIP assumptions from `research.md`; repair the design rather than silently diverging if main changed materially.
    - Mark implementation tasks complete/archive the SDD only after merged-main evidence satisfies all applicable acceptance criteria.
    - _Requirements: 1-10_
    - _Boundary: final release-grade certification/spec lifecycle_
    - _Depends: 8.1_
    - _Validation: `make qa`; applicable race run; merged-main focused rerun_