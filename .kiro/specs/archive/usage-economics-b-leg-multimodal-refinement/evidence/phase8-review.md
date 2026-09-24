# Phase 8 review

- Verdict: APPROVED after three consolidated repair passes.
- Scope: parent tasks 8.1–8.5.
- Boundary: real upstream provider evidence producers, executable V1 bridging, host-only V2 evidence, account-window gauges, auxiliary-path disposition, and the closed producer census. Rating, revision workers, settlement, reports, refinement retail policy, and release cutover remain out of scope.

## Requirement review

- Anthropic-family producers preserve typed presence, cache lifetime, server-tool, reasoning/output, provider identity, interrupted-stream, and cumulative late-usage semantics without destructive replacement or double counting.
- OpenAI/OpenResponses-compatible producers distinguish estimated, provider-quantity, and provider-charge evidence; preserve media direction/native units; and do not fabricate missing terminal evidence or provider cost.
- Gemini/Vertex producers preserve modality, direction, native unit/quality, cache, reasoning, grounded-tool, and explicit-zero semantics without deriving storage or price-sheet charges.
- Codex request usage remains separate from account-window gauges. Pool, account, window, reset, and concurrent/out-of-order snapshot identity is preserved without turning utilization into a request debit.
- The 69-row producer/consumer census is closed: every relevant producer and auxiliary path is certified, losslessly bridged, or explicitly marked unsupported for advanced evidence with a bounded reason.
- Negotiated V2 is authoritative over matching V1 evidence only. Independent auxiliary evidence remains additive; negotiated executable V1 producers project canonical durable keys locally; legacy hosts retain canonical V1 capture.
- Implicit A/B/A corrections remain distinct bounded revisions, exact replay is suppressed, and conflicting payloads at an already accepted explicit provider revision fail closed.

## Review findings and repairs

- Anthropic split input/output snapshots initially reused one source identity destructively; family producers now merge presence-aware cumulative snapshots and flush a single terminal/interrupt sideband record.
- Provider evidence replay initially treated a source identity as permanently seen, losing A/B/A corrections; bounded last-fingerprint state now suppresses only replay while preserving corrections and immutable explicit revisions.
- Gemini emitted duplicate service context; the duplicate mapping was removed and validation fixtures cover the corrected observation.
- Canonical V1 and sideband/V2 evidence could both survive and double count; authority selection now suppresses only matching fallback evidence and coalesces cumulative provider snapshots by stable source identity.
- Full architecture review exposed stale public ABI/content-free allowlists and a direct raw-stream read outside the attempt owner. The ABI baseline was regenerated through its prescribed scanner, the narrow allowlist was updated, and the stream capability query moved behind `attemptSession` ownership.

## Fresh root verification

- Root metering, runtime, connector adapter, provider-family, stream wrapper, backendplugin SDK, QA, and Phase 8 architecture suites — PASS.
- Six changed connector modules (`codex`, `commandcode-anthropic`, `cursorsdk`, `gitlabduo`, `minimexoauth`, and `vertex`) with `GOWORK=off` — PASS.
- `git diff --check` — PASS.
- Changed Go files: 85, below the 100-file gate.

The complete architecture suite retains documented pre-existing failures: forbidden legacy `Rater`, request/attempt baseline drift, malformed ignored import path, and the aggregate line ceiling. The line ceiling was already exceeded at checkpoint `5c306fe8` (103331 versus 98581); Phase 8 adds 777 lines and does not weaken the ceiling. Windows race testing remains unavailable because `cgo.exe` exits during the race build. No Phase 8 requirement-blocking finding remains.
