## Review Verdict

- VERDICT: APPROVED
- TASK: 17.4.
- Authorization: the controller may check task 17.4 and parent task 17, given the supplied approved Phase17.3 baseline and prior completed Phase17.1/17.2 work.
- Baseline: `7a5d7961`; worktree `go-llm-interactive-proxy-feat-b-leg-usage-economics`, branch `feat/b-leg-usage-economics`.
- This final review supersedes the earlier F1-F4 rejections. All four findings are resolved. No remaining concrete task-local blocker was found.

## Mechanical results

Final F4 verification, independently executed after inspecting the actual diff:

- Architecture: PASS, eight selected checks covering Phase14 stream isolation, monetary authority classification, provider-cost customer-lock isolation, runtime quote/settlement isolation, public-binding admission math/import closure/typed neutrality, and Phase17.2 no-post shadow. `go test ./internal/archtest -run 'TestPhase14StreamFilesStayOffQuoteSettleAndBalance|TestPhase14SingleMonetaryAdmissionAuthority|TestPhase14ProviderCostProcessingTakesNoCustomerLock|TestPhase14RuntimeStaysOffQuoteAndSettleMath|TestBillingBindingAdapterHoldsNoAdmissionMath|TestBillingBindingImportClosureIsPublicAndNeutral|TestBillingBindingContractStaysTypedAndNeutral|TestPhase172ShadowV2HasNoMonetaryWriters' -count=1`, exit 0, 0.432s.
- Recovery/startup/regressions: PASS, `go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle -run 'Remediation174|Recovery|Cutover|Shadow|TestBuildProcessBillingRuntime|TestVerifyBillingAccountingStartupWiring' -count=1`, exit 0; billing 0.159s, store 14.278s, runtime 2.519s.
- Static checks: PASS, `go vet ./internal/archtest/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`, exit 0.
- Formatting/whitespace: CLEAN, gofmt on all changed tracked/untracked Go files and `git diff --check`.
- Placeholder/secrets scans: CLEAN for TBD/TODO/FIXME/HACK/XXX and obvious private-key/AWS credential markers, plus manual source inspection. Not a comprehensive security audit.
- Boundary: WITHIN migration recovery, composition verification and tests. No new persistence schema, raw-retention subsystem or external binding redesign.
- Boundary audit: CLEAN. Core compatibility policy is separate from SQL reads and runtime startup ownership. The quiescence wrapper performs denial/delegation, not financial math.
- RED phase: VERIFIED. Inspected `C:/Users/Mateusz/AppData/Local/Temp/opencode/phase17-4-remediation-red.txt` and GREEN sibling for F1, and `phase17-4-f4-red.txt` / GREEN sibling for F4. F1 RED shows production build incorrectly returning nil for a hidden recovery port; F4 RED matches the architecture failure independently reproduced in the previous review. Fresh tests now pass. Artifact consistency is verified; their creation was not independently observed.

Earlier independent verification in this same review sequence remains applicable because the final F4 change modifies only an architecture test:

- SQLite catalog: PASS, `make test-db-parity-sqlite`, all ten components.
- Configured real PostgreSQL recovery: PASS, `LIP_REQUIRE_POSTGRES=1; go test -tags=integration ./internal/infra/billingstore -run TestRecovery174PostgresParityWhenConfigured -count=1 -v`, exit 0, 16.750s. No DSN values disclosed.
- Broad core/store tests: PASS, 0.468s / 182.368s. The parallel broad command's runtime package failed once (57.621s); output truncation lost the exact case. An independent standalone `go test ./internal/infra/runtimebundle/... -count=1` rerun passed, 40.634s. This transient failure is reported as residual verification uncertainty, not silently converted into a green broad command.
- Initial `make test-db-parity`: SQLite and PostgreSQL billing passed; the aggregate then failed at the known unrelated metering-journal `value_present` integer/int4 versus boolean mismatch. Full-catalog PostgreSQL is not claimed green.
- Known unrelated lipapi import-closure failures remain outside this task. Windows race, full make test/qa, and opt-in test-cost were not run.

## Findings disposition

1. **F1 resolved — mandatory internal startup verification.** Any non-nil internal monetary store lacking `AccountingRecoveryStore` now fails before worker/sink startup or resource registration. Malformed/unreadable snapshots fail closed. Process-build tests exercise missing-port and malformed-snapshot cases, while doubles explicitly provide safe V1 snapshots. The storeless external binding remains a separate owner.
2. **F2 resolved — supported optional-raw-absent contract.** Recovery does not consume optional raw transport bytes; canonical economic evidence and pending work are independent. The new fixture explicitly asserts disabled raw capture and proves compatible financial recovery while raw is unavailable. Queue pruning remains honestly described as operational retention. Its observation ref/fingerprint assertion is in memory; durable reopen and idempotent replay are proven by the separate existing same-file recovery tests. No raw retention backend or raw-expiry operation is claimed.
3. **F3 resolved — relevant RED evidence supplied.** The recorded behavioral failure and subsequent GREEN correspond to the inspected hidden-port implementation and process-build fixture.
4. **F4 resolved — explicit decorator classification.** The scanner still detects admission implementations. It requires exactly the original two authority files plus the one named quiescence-decorator file, checks exactly one correctly owned Admit method in the exception, rejects listed financial math/store operations there, and retains the sole durable exposure-store assertion. Existing behavior tests independently prove zero inner calls while quiesced and one delegation when compatible. The static helper alone is not a semantic proof of ordering; the runtime tests provide that evidence. No scanner evasion or arbitrary additional-implementation allowance was introduced.

## Acceptance assessment

Capture-only V1/shadow rollback remains allowed; V2 pins, monetary economic work and the V2 compatibility floor reject V1-only or non-epoch-aware readers. Marker validation rejects unknown states/generations/floors; the marker Version is a CAS counter, not a schema-version enum. Store verification is read-only and scoped. Same-file forward recovery drains real pending customer, provider and economic work exactly once, retaining pending evidence and canonical audit linkage. Strict quiescence denies before the inner admission. Production uses mandatory startup verification; the explicit quiescence wrapper is not falsely claimed to be automatically installed.

- FINDINGS: no remaining blocking task-local findings; verification limitations are recorded above.
- REMEDIATION: none required for task 17.4.
- SUMMARY: compatible rollback/recovery checks and the architecture classification satisfy task 17.4 within the supported accounting contract; task 17.4 and parent 17 may be checked.
