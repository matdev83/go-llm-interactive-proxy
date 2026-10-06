# PR 726 focused review remediation

Current disposition: tasks 12.1-12.3 close the full-head multipart, hybrid complexity, JSON scalar and partial log-mode findings with independent task approval. Tasks 13.1–13.5 address surviving CodeRabbit concerns and the full-path JSON allocation blocker, including cancellation and unused exact-only work. Final-head GitHub certification and focused maintainer re-review control readiness. PR #726 stays open with auto-merge disabled.

Artifact labels below identify locally retained evidence bundles, not repository links. Public CI run and artifact links are recorded in PR #726; local profile bundles are not claimed to be downloadable artifacts.

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

Task 10.1 received an independent structured APPROVED review after ambiguity and value-group negative controls were corrected. Final adapter boundary checks additionally verified that concrete BetterLeaks types stay inside the adapter. The host rejected further delegation with agent thread limit reached; tasks 10.2â€“10.4 used the kiro-review controller fallback with actual diff inspection, negative controls, and mechanical verification. The subsequent independent user PR re-review at 49eefcfb closed all four remediation findings and identified integration with newer main as the sole remaining blocker.

Final production revision is 424768c80ba1969e271f6e5adbf1d6c9996d9bdc. make quality-checks, make test-unit, make parity-checks, targeted Linux race, CLI build, and CLI help each exited 0. Race command: go test -race -count=1 ./internal/plugins/features/secretguard/... ./internal/stdhttp/auth ./internal/standardplugins/featurehost/secretguard ./pkg/lipsdk/secretguard. Evidence bundle betterleaks-review-repairs-424768c8/ is retained in native Linux and Windows copies. Remote checks must be read on the latest PR head; earlier green runs do not authorize merge. Historical daa38a0b/22d7b25b evidence remains historical and does not certify these changed paths.

## Current-main integration

Merge 7e1827b73de915d3dac8f1db978842fa46d1631d integrates main 987e1f7d527cfd84cae61d294709538de8d03815 (#732) into reviewed head 49eefcfb. The only conflict was compression_attempt_poll_test.go; the resolution takes main's mutex-protected fixture verbatim. Main's release race partition and associated QA/budget contracts are retained. The shared archived evidence reference and companion adoption fixture already matched main and needed no further edits.

The merge delta from 49eefcfb contains five unrelated upstream files. Git diff is empty for internal/plugins/features/secretguard, internal/stdhttp/auth, internal/standardplugins/featurehost/secretguard, and pkg/lipsdk/secretguard. No feature production semantics changed.

Fresh native Linux commands on merge 7e1827b7 both exited 0:

- go test -race -count=1 ./internal/plugins/features/secretguard/... ./internal/stdhttp/auth ./internal/standardplugins/featurehost/secretguard ./pkg/lipsdk/secretguard ./internal/plugins/features/reasoningpreservation
- go test ./tools/backendplugin/release_gates ./internal/qa ./internal/archtest ./internal/plugins/features/sessionclassification/testfixtures

Evidence bundle: betterleaks-main-integration-7e1827b7/ (native Linux and Windows copies of commit.txt, exits.txt, race.log, integration.log). Fresh remote checks are required on the delivered head. This integration does not authorize merging or auto-merge.

## Provenance and active-redaction streaming correction

The maintainer approved a narrower contract than original issue #714 and the old walker. Requirements 4.7 and 10.9, design/tasks, issue #714 and operator docs now require only user prompts and tool execution output. Instructions, assistant/system/developer/unknown-role history, model tool calls (including legacy JSON parts with tool metadata), reasoning/refusal/reference items and tool definitions remain untouched and uncharged. One feature-owned walker supplies admitted fragments and replacement handles to exact, BetterLeaks and hybrid paths; no response-path production code changed.

Canonical-valid guard matrices cover three detector modes, message/item authority and block/log/redact (18 mixed-history cases plus 18 excluded-only cases). Excluded-only content passes with a one-byte budget and no findings/mutations. Restoring broad traversal or removing the legacy tool-call metadata exclusion causes the provenance regressions to fail. Fixture validation initially failed for nine item cases; correcting IDs and separating output-only/parts-only results restored green without dropping JSON assertions. Independent review approves the corrected fixtures and full feature suite.

The controllable runtime stream emits response/message starts, two secret-bearing text events and finish/EOF. Both text events reach downstream unchanged before completion is released, while the actual provider receives a redacted user request. ValidateEventSequence proves the canonical event sequence. A completion-buffered negative control times out while gated and then releases the unchanged event, proving delay rather than permanent loss. Execution waits are bounded and cancellation joins the owned receiver and closes the provider. Independent runtime and workflow reviews approve these controls.

Current source head b31053f82d98f7b67cb0998b30922088758fac03 passed all executed PR checks. Scoped Linux run [37296483084](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/37296483084) passed:

- go test -race -count=1 -timeout=5m ./internal/plugins/features/secretguard/... ./internal/stdhttp/auth ./internal/standardplugins/featurehost/secretguard ./pkg/lipsdk/secretguard ./internal/core/runtime ./internal/infra/runtimebundle
- make parity-checks
- go build -o "$RUNNER_TEMP/lipstd" ./cmd/lipstd; "$RUNNER_TEMP/lipstd" --help

Artifact secretguard-contracts-37296483084 records tested merge 935ec1b8c11f7cf52647f9bab311fb2eb2fb4d6f. Its parents were independently verified as main 987e1f7d and PR head b31053f8. Downloaded logs and CLI help output in job.log are retained at remote-b31053f8/. The delivery workflow also captures help stderr in smoke.log.

Local Linux quality passed at 3eeeb3f2, but subsequent local certification is failed/interrupted evidence: C: filled, WSL became emergency read-only, and Windows-mounted test temporaries caused Git/chmod and storage permission failures. The retry was stopped before further disk pressure. Logs remain at betterleaks-provenance-3eeeb3f2/ and provenance-3eeeb3f2-recovered/ (plus native-temp retry). Older task bundles and one generated binary were moved to F: without discarding evidence. Remote full CI supplies current comprehensive test evidence. No tests, limits or existing workflows were weakened to bypass the local environment failure.

## Full-head multipart and complexity remediation (tasks 12.1–12.3)

The full-head review at b76407f9 reopened readiness for three defects plus log-mode partial failure. All repairs were implemented and independently reviewed with the requested gpt-6.1-sol/medium worker route.

- **12.1:** A private incomplete-coverage flag preserves upstream `ComponentSetsTruncated` and local component/occurrence exhaustion. Redaction eligibility rejects it before any clone publication. The real pinned scanner's 100-set truncation regression now blocks even with another rewritable candidate. Boundary, optional-component and known-rule-at-cap controls pass. Log mode still scans exact/request-credential findings from admitted fragments after discovery failure. Simultaneous byte-limit/detector failure retains the SDK-required scan_limit kind and a bounded combined reason. Input remains unchanged.
- **12.2:** Typed group maps preserve alias boundaries. Private location/field/span/value indexes handle overlap and accumulated deduplication; ordered provenance lists are consumed once while membership remains available. An actual-call benchmark exposed a second index rebuilt per fragment in scan.go, so the approved boundary was extended to retain one scan-local safe-finding index. Baseline controls fail at 2,110,468 allocations for 2,048 exact groups and approximately 180.4 MB for a 2,048-fragment call. Current bounds pass. At 4,096 groups, private merge drops from approximately 670 ms / 539.53 MB to 3.17 ms / 3.57 MB; the actual call drops from the intermediate 504 ms / 710.94 MB to 39.38 ms / 21.37 MB. Three short samples show scaling, not a latency guarantee.
- **12.3:** The outer UseNumber decoder validates and bounds the first JSON value. Scalar mapping advances to delimiters without per-token decoders. Numeric spelling, booleans/null, duplicate keys, escaped ranges, unsupported-key/scalar failure and ignored trailing content remain covered. Independent baseline allocation controls fail; focused/full feature tests and 30-second fuzzing pass. Mapping a 2,097,144-byte/125,200-scalar fragment drops from 216,846,744 to 91,156,008 B/op and 1,502,511 to 438,319 allocs/op. Complete real BetterLeaks plus positional exact redaction of that topology still costs approximately 377,677,912 B/op; it preserves scalar bytes and yields one deduplicated credential finding. Single-iteration timing is not an SLO.

The pinned upstream [Finding contract](https://github.com/betterleaks/betterleaks/blob/v2.0.0-rc.1/report/finding.go) and [component-set construction](https://github.com/betterleaks/betterleaks/blob/v2.0.0-rc.1/scan/components.go) were checked directly. Concrete upstream types remain inside the allowed private adapter files. The architecture gate caught imports in the initially separate regression file; its test bodies were moved unchanged into betterleaks_adapter_test.go. Focused architecture and multipart checks then passed without changing the gate.

Source commits: 064a7980 (multipart/log), 36c20140 (merge and per-fragment accumulation), 682af986 (scalar mapping), 868e7ede (adapter-test placement), da91dcd3 (clean integration of main e4eca6e0). Main integration changes no SecretGuard production code. The branch changes 75 Go paths, below the unchanged 100-file gate.

Current affected-consumer tests pass: auth, host composition, SDK, runtime and runtime bundle. Evidence and benchmark logs are under full-head-review/. The optional Windows test-cost ratchet did not produce a valid comparison: the first attempt failed in its historical baseline with cross-drive/SQLite errors; the same-volume retry exposed the repaired adapter placement, Windows Bash/QA failures and a nested Go build-cache failure. Both logs are retained. It is not reported as successful, and its policy was not relaxed. No further broad local retry was made.

The final-head GitHub check set and dedicated SecretGuard Linux race/parity/CLI artifact are the delivery certification. Their current head, result and run link are recorded in the PR body after completion. Earlier green CI and the task approvals do not establish that a changed final head passed. PR #726 stays open for focused maintainer review; no merge or auto-merge is authorized.

## Surviving CodeRabbit findings (tasks 13.1–13.4)

Log-mode cancellation and deadline expiry now propagate through the error return instead of becoming successful log decisions. Explicitly disabled BetterLeaks skips private hybrid occurrence collection in scan, log and redact. Scalar-dense 128 KiB JSON previously invoked positional scanning 7,821 times during scan/log and 15,641 times during redact; the regression now requires zero unused calls while enabled hybrid overlap retains attribution and coverage. Older detector-failure fixtures now distinguish cancellation from ordinary scanner failure rather than ratcheting the incorrect behavior.

Parallel benchmark workers report errors without calling FailNow from a worker goroutine, and corpus selection uses explicit first-match flags. The unrewritable failure-kind constant aliases its SDK authority. SDK occurrences now document input-relative half-open byte spans, one-occurrence attribution and legacy empty detector IDs; the auth fallback explicitly documents its fixed mask. Archive creation and extraction have independent exit checks and a finally-owned temporary archive. Mocked archive failure, extraction failure and successful transition to the benchmark passed without rebuilding the historical baseline. Historical certificates are clearly labeled, local evidence uses bundle-relative labels, and the benchmark report ends with a newline.

The billing allocation assertion previously multiplied already-normalized per-node costs by depth, accepting quadratic total allocation. Correcting the test exposed production allocation defects in billing-owned graph diagnostics: disjoint chains built transitive ancestor maps unnecessarily, and one-hop walks reserved queues for the entire graph. The repair uses the existing validated-DAG invariant to avoid impossible chain intersections and sizes traversal storage by visited nodes. Branching/diamond diagnostics retain their pinned pairs. No valuation or solver outcome changes.

Independent current measurements at depths 200/800/3200/8192 are about 2,938/2,151/1,960/1,984 bytes per node, compared with 11,553/35,528/135,960/359,007 before the repair. A constant 2x envelope checks every deeper sample and independent linear-pass/quadratic-fail controls test the assertion. All four valuation fingerprints remain pinned. Stack growth remains 98,304 bytes at every depth (zero spread against the original 65,536-byte spread bound); small/large recursion controls remain 2,064,384/16,744,448 bytes. The independent named integration certificate passed in 3.68 seconds. The one-hop allocation regression uses bounded fixed iterations rather than embedded adaptive benchmark calibration.

Fresh coherent feature/engine and billing package tests pass, as do auth/SDK tests, focused runtime SecretGuard integration and short parallel benchmark execution. The worker independently approved the parent-owned small source/script changes; the controller reviewed the worker-owned fixes and independently verified the named billing certificate and allocation controls. No local repository-wide cost, QA, parity or race retry was used for these repairs. Task 13.5 evidence follows. Final delivered-head CI remains required; historical runs do not certify these changes.


## Full-path JSON allocation remediation (task 13.5)

The reviewer correctly rejected the earlier scalar-decoder fix as insufficient: complete positive redaction still allocated approximately 377.7 MB for a 2,097,144-byte JSON array with 125,200 scalars. The full-path RED control reproduced 377,470,848 bytes against an unchanged 32x admitted-byte envelope (67,108,608 bytes). Current complete redaction allocates approximately 64.14 MB, about 83% less, including real BetterLeaks scanning, positional exact matching, canonical decode/rewrite and cloning. It retains one candidate token instead of the entire scalar mapping tree.

The first-value decoder validates syntax without constructing a second decoded tree. Canonical token traversal streams arrays and retains only last-wins sorted object entries; deferred container ends avoid repeated nested-payload rescanning. Scalars and unescaped valid UTF-8 strings borrow identity spans, with independently owned occurrence values for cleanup. Exact no-hit matching defers unused hit-buffer allocation.

Focused review also exposed a large escaped token whose partial mapping was reconstructed for every occurrence. At 128 KiB, twelve hits allocated roughly 70 MB versus 7 MB for one. Token-owned reusable mapping removes that multiplier; offset lookup preserves earliest repeated-boundary semantics. A subsequent full-path profile attributed about 50.35 MB to detailed per-byte escaped mappings. Compact exceptional rune/escape intervals now map ASCII stretches arithmetically, with a bounded dense fallback for exception-heavy input. The full near-2 MiB escaped-string RED allocated 126,292,520 bytes against an 83,886,080-byte envelope; GREEN allocates 75,944,496 bytes, about 40% less. Repeated inverse lookups allocate zero. Invalid UTF-8, unpaired surrogates, multibyte text and dense escapes retain the eager decoder's boundaries.

The independent gpt-6.1-sol/medium reviewer approved the final change. Temporary overlays checked 300 deterministic mixed sparse/dense fixtures, every decoded boundary and 30,000 inverse interval samples against the eager oracle. An endpoint-shift mutation failed immediately; repository sources were unchanged by the review. Duplicate-key, numeric spelling, first-value/trailing-content, candidate ordinal, complete rewrite coverage and original-call rollback contracts remain intact.

Fresh local verification: complete feature/engine suite; billing/auth/SDK packages; focused runtime SecretGuard integration; named maximum-depth billing integration and negative controls; unchanged BetterLeaks/billing architecture ratchets; scoped configured lint (zero issues); and 30-second parser fuzzing with two workers (36,292 executions). The fuzzer now checks streaming canonical tokens and compact/dense boundaries, with the existing 16 KiB input cap. Lint-driven test type assertions and formatting were corrected and the affected regressions rerun. Archive creation/extraction controls and short parallel benchmark execution passed earlier in the same remediation.

Evidence bundles are locally retained under coderabbit-review/ and the JSON allocation profiles; they are not claimed as public downloadable artifacts. Remaining full-path cost comes mainly from canonical decoder buffers, clone ownership and decoded strings. The 64 MiB configurable scan ceiling was not certified by these two default-limit corpora. These bounds are topology-specific allocation regressions, not an ingress latency SLO or an unconditional merge GO. PR #726 remains open with auto-merge disabled; final-head GitHub checks and focused maintainer review control delivery readiness.


## Linux CI failure and renewed main integration (tasks 14.1–14.3)

Head 60930748 failed its dedicated Linux race allocation assertion: 68,400,688 bytes exceeded the unchanged 67,108,608-byte ceiling. This failure supersedes the earlier Windows-only local acceptance for delivery. Other failed downstream jobs reported cancelled scope detection rather than executed security/platform failures. Their logs and the substantive race failure are retained under remote-60930748/. All relevant checks must run successfully on the new head.

Merge ff252916 integrates current main 3d5697e6. The dependency resolution retains Smithy 1.28.2 and BetterLeaks v2.0.0-rc.1. The execution-plane resolution preserves main's named locals and category isolation together with every BetterLeaks capability/diagnostic field. A frozen-plane regression checks opaque capability identity and explicit-zero detector posture; a temporary main-only source overlay fails it, while the merged source passes. One upstream documentation example was replaced with a symbolic access-key placeholder to pass the unchanged secret-pattern gate.

Profiling identified approximately 16.77 MB of avoidable growing decoder input buffers across two mapping traversals. The seven-line production fix uses standard json.Valid syntax validation for complete input and trims only trailing JSON whitespace. The existing canonical first-value decoder remains the fallback for invalid or trailing content. The syntax-only allocation RED measured 8,386,944 bytes; GREEN measured 2,392. Parent-independent full-path measurement is 47,368,576 bytes, approximately 29% below the unchanged ceiling and 26% less than the preceding Windows measurement. No threshold, corpus, race selector or confidence policy changed.

The gpt-6.1-sol/medium reviewer independently approved complete-value and fallback acceptance/offset behavior against the standard decoder: leading/trailing JSON whitespace, large/exponent numbers, invalid UTF-8, surrogates, concatenated/trailing content, non-JSON whitespace, and depths 10,000/10,001. Fresh full feature/engine tests and 30-second canonical fuzzing (81,054 executions) passed. Runtimebundle/runtime SecretGuard integration, module verification and unchanged architecture gates passed. Fresh-head remote certification remains required and is task 14.3; the PR stays open and unmerged.
