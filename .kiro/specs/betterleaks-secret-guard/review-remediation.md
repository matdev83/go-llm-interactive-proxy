# PR 726 focused review remediation

Current disposition: technical repairs verified; independent focused PR re-review remains required before merge. The PR stays open with auto-merge disabled.

## Repair scope

- **Location projection (10.1):** A fragment-scoped line-start index replaces per-finding splitting. Private occurrences retain validated absolute offsets. Value-group normalization searches only the validated reported range, and rewrite/eligibility checks reuse retained offsets. Ambiguous literal spans fail closed without rebuilding the index.
- **Generation redaction policy (10.2):** Composition passes resolved mask and prefix settings into immutable generation services. The feature applies this policy to positional exact matchers and BetterLeaks rewrites. Already configured exact engines retain their own equivalent rewrite implementation; request-credential attribution remains independent of presentation policy.
- **Request-credential overlap (10.3):** The optional SDK positional capability embeds Matcher and returns start/end plus safe Finding attribution. Auth supplies concrete credential occurrences without importing the feature engine or exposing secret bytes/hashes. The feature converts admitted content to private identity and deduplicates exact/BetterLeaks overlap.
- **Complete rewrite coverage (10.4):** Every verified discovery occurrence must have a completed rewrite or complete exact-overlap masking. Missing JSON mapping, length mismatch, or incomplete coverage returns unrewritable_detected_secret even when another mutation succeeds. Guard failure keeps the original call and reports zero committed mutations.

## Regression evidence

- The 2 MiB fixture contains over one million short lines and 256 findings near its tail. Tests cover full-match versus extracted-value locations, stored offsets through rewrite planning, and ambiguous-value eligibility. Restoring the old per-occurrence index allocation makes the corrected private-field-ID regression fail; current eligibility allocates zero objects.
- The actual built-in authenticated credential matcher produces one coherent hybrid finding with two concrete occurrences and preserves accepted-key attribution. Removing its positional capability reproduces separate exact and BetterLeaks findings.
- The configured policy cross-product covers single/multi-user, exact/BetterLeaks/hybrid, text/JSON, custom masks #/@, and prefix preservation true/false (48 cases). Environment access panics in these fixtures, so forbidden access cannot silently pass.
- Coverage negative controls show that an unrelated successful mutation cannot authorize dispatch with an unchanged discovery and that a missed second JSON mapping remains unrewritable after the first rewrite succeeds. Disabling the coverage assertion makes both controls fail.
- Focused feature, auth, SDK, host-composition, runtime, runtime-bundle, and architecture tests passed on Windows. Vet and diff checks passed. Windows race cannot start with the installed cgo toolchain; native Linux supplies race evidence.
- Native full-unit certification exposed an upstream research reference left stale by spec archiving. The test-fixture reference now resolves to the same archived research file; the integrity assertion is unchanged. The architecture shrinkage bound is unchanged and passes after replacing duplicated credential counting with bytes.Count and reusing safe scan results during redaction.

## Adversarial performance evidence

Measured on Windows amd64 / AMD Ryzen 7 5800X / Go 1.26.6. Scanner construction and fixture preparation are outside steady scan measurements. Location projection is explicitly separate from the full scanner:

| Scope | Time | Bytes/op | Allocs/op | Sample |
| --- | ---: | ---: | ---: | --- |
| Projection only, prebuilt index | 83 microseconds | 34,816 | 512 | one iteration |
| Index construction plus 256 near-tail value-group projections | 3.403 milliseconds | 8,406,120 | 521 | one iteration |
| Full scanner, isolated github-pat, 256 real PAT findings | 30.683 milliseconds | 54,570,552 | 22,075 | one iteration |
| Full scanner, default confidence/decode/rule policy, workers=1 | 45.073 milliseconds | 54,477,314 | 22,245 | three iterations; 256 findings, zero cap failures |

The index is roughly 8 MiB for this newline density; the 34,816-byte projection-only result does not include it. The complete default-policy scanner retains substantial upstream allocation cost, but no per-finding million-line descriptor/index allocation remains. These measurements are topology-specific regression evidence, not a deployment latency SLO. The configurable 64 MiB admission ceiling was not benchmarked here.

## Review and certification provenance

Task 10.1 received an independent structured APPROVED review after ambiguity and value-group negative controls were corrected. Final adapter boundary checks additionally verified that concrete BetterLeaks types stay inside the adapter. The host rejected further delegation with agent thread limit reached; tasks 10.2–10.4 use the kiro-review controller fallback with actual diff inspection, negative controls, and mechanical verification. Independent PR re-review is still required before merge; local fallback review is not presented as that approval.

Final production revision is 424768c80ba1969e271f6e5adbf1d6c9996d9bdc. make quality-checks, make test-unit, make parity-checks, targeted Linux race, CLI build, and CLI help each exited 0. Race command: go test -race -count=1 ./internal/plugins/features/secretguard/... ./internal/stdhttp/auth ./internal/standardplugins/featurehost/secretguard ./pkg/lipsdk/secretguard. Evidence is /home/ciuser/betterleaks-review-repairs-424768c8/ and C:/Users/Mateusz/betterleaks-review-repairs-424768c8/. Remote checks must be read on the latest PR head; earlier green runs do not authorize merge. Historical daa38a0b/22d7b25b evidence remains historical and does not certify these changed paths.