# Parent Phase 15 independent review

## Verdict

**APPROVED.** All concrete Phase 15 review blockers are closed. This review authorizes checking Tasks **15, 15.1, 15.2 and 15.3** and proceeding to **Phase 16**. No task checkbox, spec status, production source or Git history was changed by the reviewer.

## Scope

Reviewed the cumulative Phase 15 implementation on `feat/b-leg-usage-economics` against approved base `4f12669d`, Tasks 15.1-15.3, C7/public binding port map, Boundary Commitments, and their referenced requirements. The review includes independent source inspection, reproduced negative cases, successive remediation checks, and fresh focused verification.

## Findings closed

- The public binding is typed/versioned, rejects incomplete/typed-nil/duplicate monetary bindings, reuses public economics contracts, and exposes no internal/SQL/provider/service-map contract.
- Build and BuildWithBilling converge through buildCommon and exactly one BuildHost, preserving existing Host/Manager ownership and reload. Ordinary Options and stock startup remain non-money.
- Credit results bind exact store/account; quotes bind requested call/store and frozen tariff/policy identities; exposure handles bind store/call/quote. Invalid identities stop before the next port. Degraded credit maps to unavailable and prevents provider open.
- Canonical admission now carries trusted scope/account with wire parity; the external live-host fixture asserts the credited customer.
- Mandatory terminal scope reaches session Background close, regular and independent/post-open legs, abort and call closure. Known frozen scope takes precedence over conflicting live principal/tenant without merging fields. Scope-free envelopes stop before the public sink.
- Terminal ACKs bind exact store/envelope identity with one sink invocation. Terminal DTOs retain applicable account/A-leg/submission/workload/coverage and positive authoritative B-leg sequence.
- Envelope correlation and observation lineage reject foreign applicable call, B-leg, A-leg, submission, sequence and parent references. Scope agreement covers known principal, tenant, organization, workspace, project, department and cost-center IDs. TenantID is a scope.Value. Non-request evidence exemptions are constrained by actual request attribution.
- The final missing subject-sequence comparison is now present in checkLineageAgreement: subject sequence 9 versus top-level/correlation 3 returns ErrInvalidTerminal; exact 3/3/3 succeeds. The permanent regression reproduces the prior independent probe.
- External own-go.mod/public-only integration uses custom submission/credit and synthetic widget rating, host-delivered terminal processing, same-host failed/successful reload, retained recorded snapshot evidence, owned-worker shutdown/reverse close, borrowed non-ownership and repeated Close.

No blocking findings remain within Phase 15.

## Task and requirement mapping

| Task | Requirements | Evidence/result |
|---|---|---|
| 15.1 minimal typed public binding | 15.1, 15.3, 15.4 | Binding validation, typed ports/DTOs, public import closure and all terminal identity regressions pass |
| 15.2 existing Host integration | 14.1, 15.3-15.5 | Single BuildHost architecture gates, non-money stock facade, shared credit/admission/terminal ports and frozen terminal scope pass |
| 15.3 external/lifecycle certification | 7.1, 8.1, 8.5, 15.2-15.4, 18.3 | Public separate module, customer/widget rating, exact credited scope, reload, terminal worker and ownership tests pass |

The external cross-generation test proves sequential old/new host and usage-snapshot identities and preservation of already-recorded old evidence. It does not itself hold a request in flight across publication; approval does not claim that stronger evidence from that individual test.

## Mechanical verification

Fresh final re-review passed, all exit 0:
- `go test -count=1 ./pkg/lipsdk/billing ./pkg/lipruntime/... ./internal/core/runtime/... ./internal/infra/billingbinding/... ./internal/infra/runtimebundle/...`
- External module `go test -count=1 ./...`
- External module `go test -count=3 -run 'TestSameHostFailedReloadPreservesActiveGeneration|TestCrossGenerationFrozenSnapshots' ./...`
- External module `go run .`: `external_billing_binding: ok`
- `go test -count=1 -run 'TestBillingBinding|TestBuildWithBilling|TestFacadeConverges|TestExternalBillingModule|TestPhase14|TestPhase1PublicOptions' ./internal/archtest`
- `go vet ./pkg/lipsdk/billing ./pkg/lipruntime ./internal/infra/billingbinding ./internal/infra/runtimebundle ./internal/core/runtime`
- Explicit verbose runs of TestTerminalEnvelopeRequiresSubjectSequenceAgreement, TestTerminalEnvelopeRejectsForeignCorrelation, TestTerminalEnvelopeRejectsForeignObservationTenant and TestTerminalEnvelopeRequiresCustomerScope.
- Focused TestWithTerminalScope, session Background close and call-closure scope regressions.
- `git diff --check`, gofmt over modified/new Go files, and placeholder/private-key/token-pattern scan: clean.

The immediately preceding cumulative review also passed the broader required `./pkg/lipsdk/...` command. The final change is limited to billing sequence validation and its regression, so unrelated SDK packages were not repeated. Broad QA/architecture baselines and race were not rerun; controller-documented unrelated baselines and Windows cgo limitations remain outside this approval's focused evidence.

Historical RED provenance: executor reports supplied by the controller record initial missing APIs and behavioral failures for degraded credit, canonical scope, scope-free terminal handoff and observation mismatch acceptance. This reviewer independently reproduced foreign correlation call, foreign observation tenant, and subject sequence drift before fixes, then verified their permanent regressions after fixes. Temporary reviewer probes were removed.
