# Phase 6 review

- Verdict: APPROVED.
- Scope: parent tasks 6.1–6.4 and overlapping refinement tasks 3.1–3.3.
- Boundary: independent local provider-facing input/output measurement, customer-egress separation, honest unobservables, and bounded fast-path behavior. Connector ABI, provider economics parsing, generic rating/reconciliation, public host binding, and cutover remain out of scope.

## Requirement review

- Essential adapters inspect their final provider SDK parameter trees immediately before upstream execution. They do not re-label the canonical `Call` as final provider evidence and do not marshal a payload-sized accounting copy.
- Adapter tests prove decoded/transformed media and provider-visible fields can differ from canonical ingress while the ingress snapshot remains unchanged.
- Provider-origin output is accumulated before customer transforms; customer-egress output is accumulated afterward. Text estimates are calculated from the bounded cumulative byte count, making them invariant to arbitrary stream chunking.
- Prepared, attempted, and accepted states survive in the durable V2 input observation as schema-qualified request-state measures. Unknown acceptance remains explicitly unavailable.
- Cache disposition, hidden reasoning, provider tools, compute, and unsupported exact token units remain unavailable or method-labelled estimates; provider-origin evidence is not cloned into local provenance.
- Required metering declines unsupported large-body lanes before commitment. With accounting disabled, the new hot-path checks allocate zero and invoke no observer. Enabled summaries retain only bounded metadata.
- The no-transaction parallel-session fallback preserves its accumulated boundary state and trusted request, store, BillingCallID, B-leg, attempt, and scope lineage.

## Fresh root verification

- `go test -count=1 ./internal/core/metering/... ./internal/core/runtime/...` plus all five affected backend adapter trees — PASS.
- Focused no-money, large-body, static-disposition, post-commit, and frozen-facts architecture guards — PASS.
- `go vet` across all touched package trees — PASS.
- `git diff --check` — PASS.

The worker also reported `make parity-checks` passing with task-scoped temporary directories on `E:`. Windows race verification remains unavailable because the installed `cgo.exe` exits during build; no race result is claimed. No Phase 6 requirement-blocking finding remains.
