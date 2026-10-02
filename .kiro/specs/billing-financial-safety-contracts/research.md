# Research, Brownfield Gap Analysis, and Design Decisions

## Summary

Feature: billing-financial-safety-contracts. Classification: **brownfield refinement / complex integration**. Specification authoring date: 2 October 2026. Authoritative reviewed and rechecked repository main: `b560dbff3a06dc44a324aa15a0245ccaed5f76cb`. Scope includes #620/#659/#666 and the non-monetary overlap work #692/#694/#698. The attached adversarial report is copied unchanged to `reference/source-review.md`.

The author performed the requirements gap analysis and design validation before task generation. Executors receive implementation tasks, not research or architecture-choice assignments. Source-based failure traces from the review remain source-based; this specification does not claim to have executed the proxy, provider billing, DB stress, repository Kiro parser, or live migration tests. The included validator checks artifact consistency, task graph and coverage only.

## Provenance and source priority

The current user instructions and eleven contracts take precedence over earlier winner-only specifications. The review supplies F01-F14 code-flow findings. Repository steering/templates define existing ownership and validation rules except the explicitly repaired once-per-root restriction. External primary documentation establishes provider/database semantics, not user-specific deployment configuration.

The uploaded SKILL(1).md full-spec orchestrator was read. Its referenced `references/01-orchestration.md` through `08-templates.md` were not part of the upload and were not available as a bundled skill resource; they are not claimed as read. The self-contained orchestrator and the actual project-local `.kiro/settings/templates/specs/` templates were used instead. No cc-sdd installation is assumed.

## Actual repository discovery

Rechecked root AGENTS.md and .kiro/AGENTS.md, steering product/tech/structure/testing, template init/requirements/research/tasks and the design template's architecture/boundary sections. These establish core-provider separation, canonical streaming, explicit injection, non-money public Options, SQLite/PostgreSQL parity, no post-output transparent retry, a100 modified-Go-file delivery cap, and mandatory non-skipped external topology gates.

Rechecked `pkg/lipsdk/billing/binding.go` and `internal/infra/billingbinding/adapter.go`: the existing external monetary binding is v1, with credit/quote/admission/terminal/lifecycle ports and once-per-route-plan admission. A merely non-nil v1 binding cannot warrant per-dispatch funding and account control. Rechecked existing economic queue owners/tables through `economic_job_queue_store.go`, `economic_posting_intent.go`, and `economic_health.go`; this design extends that queue instead of creating a new broker. Rechecked backend inventory: the OpenResponses-compatible backend is `internal/plugins/backends/openresponsescompat`, while the frontend is `internal/plugins/frontends/openresponses`. Task paths use that actual distinction.

The source report's guard/estimator/terminal/settlement findings were used at the same pinned SHA; status fields and CI claims were not reinterpreted as proof. The source remains available through immutable path references below. A complete repository clone was not available in the working runtime; no local build or independently executed production reproduction is claimed.

## Brownfield gap findings and fixed disposition

| Finding | Current source-derived gap | Source owner/symbol | Target design | Primary implementation tasks | Preservation rule |
|---|---|---|---|---|---|
| F01 | Default winner-only customer selection is incompatible with all-attributable charging. | [internal/core/billing/retail_selector.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/retail_selector.go); [internal/core/billing/rating.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/rating.go)<br>ResolveRetailSelectionPolicy / selectRetailLegInfos / rateCustomerCharge | D01 D03 D04 D09 D10 | 1.1 2.4 6.1 6.2 8.4 | Preserve legacy frozen policies; activate the new selection only with funded execution. |
| F02 | Post-Open TTFT failure can be labelled NeverStarted and omitted. | [internal/core/runtime/executor_open_attempt.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/executor_open_attempt.go); [internal/core/runtime/attempt_session.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/attempt_session.go); [internal/core/runtime/billing_leg.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/billing_leg.go)<br>openInvoked/backendAttempted / rollback / billingLegRecord | D06 D07 D10 | 4.2 6.1 6.5 | Separate dispatch uncertainty from transport outcome; never label a possible paid send free. |
| F03 | Inner thinker normal finish is incorrectly equated with external visibility. | [internal/core/runtime/interleaved_stream.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/interleaved_stream.go); [internal/core/runtime/executor_settlement.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/executor_settlement.go); [internal/core/billing/retail_selector.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/retail_selector.go)<br>recvThinker / finalizeResponseFinishedAuthority / selectSurfacedWinner | D04 D10 | 4.5 6.1 9.7 | Keep outer closure ownership and record internal-only thinker delivery explicitly. |
| F04 | Stock composition selects scalar input pricing, client-min maxima and unproved static ceilings. | [internal/infra/runtimebundle/billing_compose.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/runtimebundle/billing_compose.go); [internal/infra/billingadmission/adapter.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingadmission/adapter.go); [internal/core/billing/estimate.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/estimate.go)<br>ComposeBilling / Quote / EstimateMaxCustomerCharge / ceilingOrError | D02 D03 D14 | 2.1 2.2 2.3 2.6 4.1 | Reuse rich tariff/rater; make conservative quote and output-source policy explicit. |
| F05 | Quote precedes final transformations; additional attempts can reuse an unrelated bound. | [internal/core/runtime/billing_admission.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/billing_admission.go); [internal/core/runtime/recovery_controller.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/recovery_controller.go); [internal/core/runtime/interleaved_open.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/interleaved_open.go)<br>billingRoutePlanInput / newReplacementOpener / openInterleavedExecutorContinuation | D03 D04 D06 D14 | 2.4 2.5 3.5 4.2 4.3 4.4 4.5 4.6 | Keep core orchestration; add a managed final prepared-payload funding boundary. |
| F06 | An overrun can consume sibling funding and insufficient collection suppresses posting. | [internal/core/billing/account.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/account.go); [internal/infra/billingstore/call_settlement.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/call_settlement.go)<br>ApplyBalanceDelta / ApplyCallBillingResult | D02 D05 D09 | 1.2 3.4 6.3 7.4 | Keep financial journal machinery; separate recognized debt from available cash and new admission. |
| F07 | No wired account-wide monetary breaker/cancellation fanout. | [internal/infra/runtimebundle/billing_compose.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/runtimebundle/billing_compose.go); [internal/core/billing/call_post_usage_worker.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/call_post_usage_worker.go); [internal/core/runtime/turn_terminal.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/turn_terminal.go)<br>ComposeBilling / ProcessOnce / cancelALeg | D06 D08 D14 | 7.1 7.2 7.3 7.4 4.1 | Reuse lifecycle owners and add epoch/outbox/control leases, not per-token money queries. |
| F08 | Terminal CAS and bounded first enqueue can lose the only billing handoff. | [internal/core/runtime/billing_call_closure.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/billing_call_closure.go); [internal/core/runtime/billing_leg.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/billing_leg.go); [internal/core/runtime/stream_terminal.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/stream_terminal.go); [internal/infra/billingspool/spool.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingspool/spool.go)<br>handoffBillingTurn / appendIndependentCallLegStrict / Terminalize / append | D05 D06 D07 D11 | 3.5 3.6 4.7 6.5 9.6 | Keep spool; create durable obligation/capacity before dispatch and independent financial retry ownership. |
| F09 | Non-money authority/egress error can short-circuit monetary closure. | [internal/core/runtime/executor_settlement.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/executor_settlement.go)<br>finalizeResponseFinishedAuthority / settleRequestAuthorityWithFrontendEgress | D07 | 4.7 6.5 9.6 | Retain both independent work states; errors.Join alone does not close the first-enqueue hole. |
| F10 | 20-attempt limit moves valid work outside the regular worker claim set. | [internal/infra/billingstore/call_usage_store.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/call_usage_store.go)<br>completeCallClaimMaxAttempts / RetryCompleteCall / ClaimCompleteCalls | D07 D11 | 3.7 6.5 9.2 9.6 | Preserve durable rows and bounded per-invocation retries; remove lifetime exhaustion for transients. |
| F11 | Legacy scalar failed-leg absent quantities can be skipped as no charge. | [internal/core/billing/rating.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/rating.go)<br>acceptedCustomerLegs / rateCustomerCharge / chargeLeg | D09 D10 D11 | 6.1 6.2 6.5 | Preserve old policy but replace unknown-as-zero completion with explicit evidence-pending state. |
| F12 | ExposureInsufficient can become500 and no useful affordable-token detail survives. | [internal/core/billing/exposure.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/exposure.go); [internal/plugins/frontends/execerr/execerr.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/plugins/frontends/execerr/execerr.go); [internal/infra/billingbinding/adapter.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingbinding/adapter.go)<br>EvaluateAdmit / ClassifyExecute / Admit | D12 | 2.7 8.1 8.2 | Create transaction-bound public-safe DTO and preserve typed cause through external bindings. |
| F13 | Per-store FOR UPDATE and O(open exposures) admission work amplify DB pressure. | [internal/infra/billingstore/cutover_serialization_store.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/cutover_serialization_store.go); [internal/infra/billingstore/exposure_store.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/exposure_store.go)<br>loadAccountingCutoverLocked / ensureAndLockAccountingCutoverTx / AdmitExposure | D05 D11 D13 | 3.2 3.3 3.4 3.7 8.3 10.1 | Use shared work/exclusive transition locks, O(1) projections, bounded priority lanes; keep dual-engine parity. |
| F14 | Blanket V2 native-mapper guard blocks text alongside unsupported native units. | [internal/infra/runtimebundle/build_executor.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/runtimebundle/build_executor.go); [internal/standardplugins/custom_backends.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/standardplugins/custom_backends.go); [internal/infra/billingadmission/adapter.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingadmission/adapter.go)<br>bindUnsupportedV2NativeUsage / UsesOpenAINativeUsageMapper / guardUnsupportedV2NativeUsage | D03 D10 D14 | 1.3 5.1 5.2 5.3 5.4 5.5 5.6 8.4 9.7 | Retain fail-closed safety; replace blanket ban only with actual request-specific family proofs and positive text tests. |

## Requirements repaired during gap analysis

The author made six explicit repairs before freezing requirements. First, catalog-first reservation is not `min(client,model)`; the transmitted cap and reserved cap are separate. Second, a denial of an independent unadmitted root is not a kill switch for funded siblings. Third, “never dropped” means durable ownership, infinite transient retry lifetime and active semantic reconciliation, not inventing a charge from missing evidence. Fourth, debt recognition is breach containment, not permission to overspend normally. Fifth, customer funding must cover supplier liability and tariff combinations without an unimplemented subsidy escape hatch. Sixth, external v1 billing bindings and unaware legacy database writers must be fenced, not treated as complete simply because all old ports are present.

Each repair propagated into EARS requirements, the approved design, task packets and the original 66-scenario matrix, expanded to 112 scenarios in revision 2. The account-formula correction avoids subtracting this request's full maximum twice when deriving an affordable output allowance.

## Design synthesis and alternatives resolved

| Question | Rejected approach | Fixed decision and reason |
|---|---|---|
| Repair breadth | Another generic billing-engine rewrite | Preserve existing rater, canonical model, journals, IDs and process ownership; repair their integration contracts. |
| Conservative funding | One scalar quote at root plus optimistic runtime transforms | Finite declared root envelope plus final prepared-request validation and atomic extension before any extra paid side effect. |
| Streaming performance | Per-token balance reads/debits | No stream-time rating or financial I/O; full reservation and process-owned account-cancel control. |
| Failed requests | Classify timeout as not-started or bill only winner | Separate dispatch liability, outcome and visibility; all-attributable economic charge identity. |
| First enqueue | Retry a terminal callback whose CAS already fired | Durable pre-dispatch obligation/capacity plus independent replayable financial completion. |
| Collection deficit | Truncate bill or consume sibling funds | Full obligation recognition, sibling-protected collection, debt/freeze and separate accounting from admission. |
| Retry exhaustion | 20 tries then an unclaimed reconcile_required row | Infinite transient lifetime; bounded per-attempt resources; semantic cases retain owner and requeue trigger. |
| Native support | Delete blanket guard or keep all native text blocked | Compile request-specific unit/enforcement proofs; positive existing text families, explicit rejection of unbounded extra shapes. |
| PostgreSQL contention | Remove locks, use FOR KEY SHARE, or hold locks during provider calls | Shared normal marker lock, exclusive transition marker lock, account-local serialization and no network inside tx. |
| New connectors | Require authors to remember to mark unsafe code | Derive inventory from actual registrations/manifests, mandatory managed send and fail-closed absent certificate. |
| Legacy cutover | Add a field old executables ignore | Quiesce old writers plus persistence guards rejecting old-shaped strict exposure/account/settlement writes. |
| Verification | Existing CI/green unit counts or estimator-as-oracle | Literal money fixtures, independent provider send log, real DB/process fault points, nonempty/non-skipped gate and negative mutations. |

## Brownfield design validation and repairs

Author validation checked the ownership and economic invariants across the entire target flow, not an isolated “component done” list. The resulting design keeps canonical/provider SDK boundaries and no-transparent-post-output-retry, and explicitly changes only the old one-root-admission assumption. The new prepared-request contract is additive to ordinary Open, so non-money clients and independent connector modules do not require a global interface rewrite.

The transaction algebra was checked for normal settlement, partial evidence, per-charge recognition, debt, refunds, postpaid floors and sibling protection. Recognition and collection use separate journal operations; debt is not also debited below the postpaid floor. Known independent amounts may post without waiting for root finality, but unresolved residual funding remains. Provider COGS is independent of customer cash collection. Supplier tariff dominance is not proved by comparing unrelated aggregate maxima.

The dispatch/freezing race was checked at the financial authorization commit: no grant can be newly authorized after freeze. A grant authorized before freeze can be in flight and remains fully reserved. The design does not pretend a DB transaction and remote network write are atomic. Callback/polling/lease timings are selected operational bounds exercised by the test harness, not claims that a paused OS or remote provider can be physically cancelled instantly.

The storage boundary was checked at local-capacity commit, central admission, grant commit, actual provider acceptance, first terminal append, central evidence enqueue, journal commit and worker ACK. Each edge has a durable owner or an explicitly retained ambiguous state. Lost exact evidence is not guessed; permanent source unavailability remains pending/encumbered and visible. A byte reservation cannot defeat destruction of every durable replica; the supported durability assumptions are explicit.

The task graph was checked for hidden prerequisites, cycles, orphaned criteria, wrong family paths and accidental parallel shared-file edits. Revision 2 disables the former four-family parallel block because shared media contracts changed. All work uses the supplied deterministic serial topological sequence; each connector packet remains module-local. Packet scope/context limits are deliberately far below the requested 1M context; an execution checkpoint is not an architecture-research task.

**Specification verdict:** implementation-ready after the included structural/coverage validator passes. **Product verdict:** not yet implemented or released by this archive. All actual gates remain mandatory and unchecked. The repository-specific Go Kiro checker must run in task 1.1; it was not run during archive generation.

## Residual dependencies with fixed behavior, not research tasks

Provider data and configured prices must be supplied by the existing catalog/profile inputs; the spec does not invent live prices or model maxima. Missing or unenforceable facts cause a defined pre-dispatch rejection. Native text family positive tests are mandatory; unsupported extra shapes are explicitly refused. A provider violating its certified bounds triggers visible breach containment and cannot be reported as a passing normal no-overspend case.

Exact billing of a permanently missing provider receipt requires valid later evidence or an authorized reconciliation input. Until then the obligation remains active and funded, with no silent waiver. Human authorization is needed for live activation or historical financial repair; execution tasks do not manufacture it. Required database topology availability is an execution prerequisite, and its absence fails the gate rather than creating a false pass.

## Primary external references and how they affect this design

These are semantic sources consulted by the author, not tasks to re-research during implementation. Default tests use synthetic frozen fixtures. No current dollar tariff from these pages is embedded as a production constant.

- models.dev: https://models.dev/ — source of versioned model metadata and output/input limits; a catalog fact must be bound to the resolved provider/model and preserved by content identity. It does not by itself prove that a custom endpoint enforces a limit.
- Anthropic prompt caching: https://platform.claude.com/docs/en/build-with-claude/prompt-caching — cache creation and TTL price treatment differ from ordinary input. D03 distinguishes inclusive versus surcharge rules and max permitted TTL.
- OpenAI token-counting guide: https://developers.openai.com/api/docs/guides/token-counting — text/output token and reasoning limitations inform D03/D10; local text estimates are not blanket multimodal proofs.
- OpenAI predicted outputs: https://developers.openai.com/api/docs/guides/predicted-outputs — prediction work needs explicit economic treatment and cannot be enabled solely by an ordinary output cap.
- Gemini generateContent API: https://ai.google.dev/api/generate-content — native prompt/cached/candidate/thought counters and candidate multiplicity inform D10, with per-model enforcement required.
- PostgreSQL explicit locking: https://www.postgresql.org/docs/17/explicit-locking.html — shared versus exclusive row-lock conflicts motivate D13; transaction pooling excludes session-pinned lock/state assumptions.
- SQLite transactions: https://www.sqlite.org/lang_transaction.html — single-writer behavior and transaction acquisition motivate bounded writer ownership, short transactions and explicit topology restrictions.

## Immutable repository references

The following source links are fixed to the reviewed SHA. The detailed source report contains the function-specific current-state argument. New symbols in design/interfaces/task outputs are proposed additions, not falsely reported existing APIs.

- [AGENTS.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/AGENTS.md)
- [.kiro/AGENTS.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/AGENTS.md)
- [.kiro/steering/product.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/steering/product.md)
- [.kiro/steering/tech.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/steering/tech.md)
- [.kiro/steering/structure.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/steering/structure.md)
- [.kiro/steering/testing.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/steering/testing.md)
- [.kiro/settings/templates/specs/init.json](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/settings/templates/specs/init.json)
- [.kiro/settings/templates/specs/requirements.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/settings/templates/specs/requirements.md)
- [.kiro/settings/templates/specs/design.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/settings/templates/specs/design.md)
- [.kiro/settings/templates/specs/tasks.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/settings/templates/specs/tasks.md)
- [.kiro/settings/templates/specs/research.md](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/settings/templates/specs/research.md)
- [pkg/lipsdk/billing/binding.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/pkg/lipsdk/billing/binding.go)
- [internal/infra/billingbinding/adapter.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingbinding/adapter.go)
- [internal/infra/runtimebundle/billing_compose.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/runtimebundle/billing_compose.go)
- [internal/infra/billingadmission/adapter.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingadmission/adapter.go)
- [internal/core/billing/estimate.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/estimate.go)
- [internal/core/billing/exposure.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/exposure.go)
- [internal/core/billing/retail_selector.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/retail_selector.go)
- [internal/core/billing/rating.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/billing/rating.go)
- [internal/core/runtime/executor_open_attempt.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/executor_open_attempt.go)
- [internal/core/runtime/interleaved_open.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/interleaved_open.go)
- [internal/core/runtime/executor_settlement.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/executor_settlement.go)
- [internal/core/runtime/billing_call_closure.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/billing_call_closure.go)
- [internal/core/runtime/stream_terminal.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/core/runtime/stream_terminal.go)
- [internal/infra/billingspool/spool.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingspool/spool.go)
- [internal/infra/billingstore/call_settlement.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/call_settlement.go)
- [internal/infra/billingstore/call_usage_store.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/call_usage_store.go)
- [internal/infra/billingstore/cutover_serialization_store.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/cutover_serialization_store.go)
- [internal/infra/billingstore/economic_job_queue_store.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/economic_job_queue_store.go)
- [internal/infra/billingstore/economic_health.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/infra/billingstore/economic_health.go)
- [internal/standardplugins/custom_backends.go](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/standardplugins/custom_backends.go)
- [internal/plugins/backends/openresponsescompat](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/plugins/backends/openresponsescompat)
- [.kiro/specs/archive/billing-uncertain-component-overlap/spec.json](https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/.kiro/specs/archive/billing-uncertain-component-overlap/spec.json)

Source-review SHA256: `f81632a8b2dec83025ce200a72e25c098a0dfff7166b78dc2df6aeb047e8ddca`. The attached skill is retained unchanged in reference/source-skill.md.


## Second-round brownfield review: interface and mixed-modality closure

**Basis:** actual source inventory and selected canonical, Gemini, native-usage, transport and Bedrock paths at the unchanged baseline; original specification artifacts; primary provider documentation. The 34 module directory inventory was checked, but every module codec was not re-audited in this authoring session. Module-local verification is specified explicitly rather than represented as completed. No repository implementation tests were run.

The original text-focused completion scope was insufficient for the current user instruction. Requirement12, D10, family packets, publication and final release dependencies have been repaired upstream. Added requirements17-24 and designsD17-D24 close the following gaps.

### R2-01: Text-only completion scope

Original requirement12.1, D10 and task5.6 permit completing a text-focused certificate while broadly denying media. Resolution: requirements17,19,23; tasks 11.1, 11.5, 5.6, 15.5. Source records: SRC03, SRC04.

### R2-02: Omitted native backend owners

The original four-family packets omit the distinct Bedrock and Alibaba contributions. Resolution: requirements17,21; tasks 13.1, 13.2. Source records: SRC01, SRC09.

### R2-03: Generic connector certification is insufficient

A common connector transport proof does not exercise each of34 module-native encoders, usage mappings or economic limits. Resolution: requirements17,23; tasks 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35. Source records: SRC10.

### R2-04: Input and output canonical media loss

Image/file references alone lack the joint output plan and candidate/media chunk identity needed for full mixed semantics. Resolution: requirements18,22; tasks 11.2, 11.3, 11.8. Source records: SRC03, SRC04.

### R2-05: Native economic options can be silently ignored

The Gemini decoder omits fileData and much native generation configuration in the inspected path. Resolution: requirements18,19; tasks 12.4, 5.4, 11.10. Source records: SRC05, EXT01.

### R2-06: Individual capabilities do not prove a mixture

An exact native output combination cannot be reconstructed from independent modality booleans. Resolution: requirements19,23; tasks 11.5, 15.1. Source records: EXT01.

### R2-07: Reference size/count estimates are not hard bounds

Media resource processing, mutable references and estimated counts need explicit bounded input proofs. Resolution: requirements18,19,20; tasks 11.4, 11.6. Source records: SRC03, EXT02, EXT03.

### R2-08: Mixed native rates need disjoint support

Modality/cache/reasoning/prediction marginals, inclusive totals and multiple candidates require exact scopes, not additive field sums. Resolution: requirements20,21; tasks 11.6, 11.7, 15.2. Source records: SRC06, EXT01, EXT04.

### R2-09: Media-only and failed output can lose economics

The proof must separate actual provider work from text visibility, chunks, assets and output collection. Resolution: requirements21,22; tasks 11.8, 15.3. Source records: SRC04.

### R2-10: Transport and reusable lifecycle axes are not closed

Independent upstream mode, WebSocket logical operations, compaction and async resource identity require separate obligations. Resolution: requirements17,22; tasks 11.9, 12.5, 15.3. Source records: SRC02, SRC07, SRC08.

### R2-11: Representative pair tests are not exhaustive

Original family-plus-sentinel gate cannot establish every pair and mixed signature; changing the denominator could hide failures. Resolution: requirements23,24; tasks 11.1, 15.1, 15.4, 15.5. Source records: SRC01, SRC10.

### R2-12: Finite fixtures need compositional tests and truthful claims

Signature enumeration alone cannot prove arbitrary order/count/nesting, every future schema, or an unbounded external operation. Resolution: requirements24; tasks 11.10, 15.2, 15.4, 15.5. Source records: SRC03, SRC04.

### Decisions settled by this review

Exhaustive verification is a generated finite modality-set product plus all real interface/profile/operation/transport expansions; this does not authorize pairwise translators. Individual modality flags never imply a joint output set. Canonical media occurrence and field receipts carry exact input and output intent. Native cost vectors retain units and scopes; count estimates need a true upper bound; unknown cache/modality intersections are reconciled rather than guessed. Billing implementation gaps cannot be relabeled native incompatibility. All34 connector modules have separate scoped packets. The final release cannot be completed from text fixtures or a deny-all implementation.

### Provenance records

- **SRC01** — Five frontend and ten builtin backend contributions. Bedrock and Alibaba are distinct entries; compatible profiles expand from EmbeddedCatalog. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/standardplugins/standard_contributions.go
- **SRC02** — Actual route claims are config-derived; generic OpenResponses has optional WebSocket delivery. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/standardplugins/frontend_route_claims.go
- **SRC03** — Existing content kinds are text, image_ref, file_ref, tool_result, json and reasoning; generic files do not establish rich audio/video semantics. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/pkg/lipapi/parts.go
- **SRC04** — Existing complete assistant media output carriers are image/file references; output-specific chunk metadata needs a precise additive contract. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/pkg/lipapi/events.go
- **SRC05** — parsePart handles inlineData but not fileData; parseGenerationConfig reads temperature, topP and maxOutputTokens, not complete native output modality and candidate configuration. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/plugins/frontends/gemini/decode.go
- **SRC06** — Native totals/details and subset relationships exist, but source comments leave prediction subsets undeclared; usage-unit representation is not proof of full carrier or pricing support. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/plugins/backends/openaiusage/component_schema.go
- **SRC07** — Operation taxonomy includes the five inference operations and context.compaction. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/pkg/lipapi/invocation.go
- **SRC08** — Delivery and selected upstream transport are distinct values, but negotiation prefers matching modes; asymmetric combinations need explicit conformance. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/pkg/lipapi/transport.go
- **SRC09** — Bedrock prepares typed ConverseStreamInput with its own AWS client and codec. It is not covered merely by an OpenAI-compatible test. Source: https://github.com/matdev83/go-llm-interactive-proxy/blob/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/internal/plugins/backends/bedrock/invoke.go
- **SRC10** — Complete root connector directory inventory has34 modules. Individual module codecs were not all re-reviewed; their actual entrypoint certification is explicitly assigned, not represented as already proven. Source: https://github.com/matdev83/go-llm-interactive-proxy/tree/b560dbff3a06dc44a324aa15a0245ccaed5f76cb/connectors
- **EXT01** — Gemini describes exact valid output modality combinations and candidate-scoped limits, plus aggregate and modality-specific usage. This motivates joint predicates and complete field mapping. Source: https://ai.google.dev/api/generate-content
- **EXT02** — Anthropic describes token counts as estimates; a financial upper-bound proof must not equate provider origin with guaranteed exactness. Source: https://platform.claude.com/docs/en/build-with-claude/token-counting
- **EXT03** — PDF processing can include extracted text and page imagery, requiring provider-specific component semantics rather than filename or byte-count pricing. Source: https://platform.claude.com/docs/en/build-with-claude/pdf-support
- **EXT04** — Bedrock exposes input/output totals and cache read/write and TTL details; native evidence needs its own qualified schema. Source: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_TokenUsage.html
- **EXT05** — Final input and output quantities can include model processing beyond visible text; price/bound semantics must follow the concrete model contract. Source: https://developers.openai.com/api/docs/guides/token-counting
- **EXT06** — Chat API request and response shapes carry native input/output and usage options; unsupported fields cannot be silently ignored by a billing certificate. Source: https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create

### Validation scope

The archive validators check specification structure, traceability, deterministic coverage obligations, literal arithmetic and negative mutation behavior. They do not certify deployed provider semantics or run proxy integration tests. Final native descriptors must be tied to the actual implementation and model snapshot. Genuine native limitations remain explicit, not fictional support.
