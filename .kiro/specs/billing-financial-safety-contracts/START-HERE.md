# Start here — execution contract

**Feature:** billing-financial-safety-contracts. **Baseline:** b560dbff3a06dc44a324aa15a0245ccaed5f76cb. **Delivery:** specification only; no billing implementation or production deployment has been executed by this archive.
> **Repository delivery note:** The PR keeps the canonical spec files readable and ships the approved revision-2 `execution/` packet tree losslessly as `execution-packets.tar.xz`. Before executing any task, run `python .kiro/specs/billing-financial-safety-contracts/tools/materialize_execution.py`. The materializer verifies the archive hash, recreates the revision-2 packet base byte-for-byte, and the generated directory is git-ignored. Revision 3 is a narrow tracked overlay: read [revision-3-architecture.md](revision-3-architecture.md) after this file and before every packet. For task IDs named there, the revision-3 override has precedence over conflicting packet wording. Do not plan or implement from the packed archive directly.


## Read and execute one packet

Read this file, `revision-3-architecture.md`, the selected `execution/Tnn.md`, the repository's root/local AGENTS rules, and only that packet's named source owners/direct compiler consumers. Each packet embeds its exact requirement criteria, relevant approved design sections, interface contracts when needed, and literal acceptance scenarios. Do not load all 113 packets into one agent context. The original review is reference evidence, not an additional planning assignment.

Follow `execution/order.md` exactly for the default serial sequence. IDs are stable workstream IDs, not an instruction to run in numeric order; some funding tasks depend on earlier-produced schema/fence units numbered in another workstream. Dependencies in the manifest are mandatory. A predecessor is satisfied only by its committed deliverables and verified receipt, not an agent message saying “done.” The materialized task manifest remains the revision-2 base manifest and no parallel group is pre-authorized. Revision 3 changes only the narrow architecture/evidence instructions listed in its overlay; follow the same serial dependency sequence. A dispatcher may later isolate connector-module work only after shared prerequisites, without shared-file edits; dependency waves are not permission to parallelize.

Each task targets <=250k active context tokens; checkpoint at 400k and stop before 600k, below the 1M maximum. Never compact and continue. When actual context counters are unavailable, cap cumulative tool/source transcript at 800k UTF-8 bytes; this is an operational proxy, not an assertion about tokenizer ratios. Save full logs outside model context and read bounded failure windows. Respect each packet's changed-Go-file maximum and the repository 100-Go-file/PR limit. A packet that cannot finish within its bound ends BLOCKED or checkpointed, not expanded into a repository-wide refactor.

## Fixed decisions — do not reopen

All new strict paid work uses `all-attributable-pessimistic/v1`. Reserve valid models.dev maximum output even with a smaller client cap. Fallback to a client cap is explicitly enabled only when the catalog bound is missing and the adapter proves enforcement. Full cache miss includes the most expensive permitted full-input cache creation treatment. Every declared paid attempt must be funded before first root dispatch; additional work gets a funded extension. No arbitrary static “safe ceiling,” hidden SDK retry, heuristic-only token proof, or missing-usage-as-zero shortcut is allowed.

Money is not token-stream orchestration. Keep pure quote/funding before paid dispatch and durable post-usage workers; no token-time rating/journal I/O. The narrow steering change in1.1 authorizes repeated use of the same funding authority for additional paid work, not an independent money system. Preserve canonical protocol adapters, non-money stock host, all existing immutable ledgers and #698 advisories.

A rejected fresh unaffordable root does not cancel its funded siblings. A required continuation/child extension that cannot be funded, or a true bound breach, freezes and cancels the whole same-account running set. A financial fence is linearized at grant authorization; bounded local/distributed cancellation plus retained provider-tail funding are required. Do not promise instantaneous physical remote cancellation.

## Scope and completion discipline

Implement the packet's ordered steps, starting with the specified failing boundary case. A test may already pass; retain the independent trace and explain that fact rather than inventing RED. Never use the production quoter/rater as the expected-money oracle. Passing an unmatched `go test -run` or skipped topology is not evidence. Run the supplied task gate wrapper and save its JSON result, then run affected existing focused tests.

All task outputs and tests are mandatory. No research/discovery/architecture-choice tasks are assigned to implementers. Reading the named code to apply a fixed change is implementation work; it is not permission to choose a new design. If an actual API/schema difference conflicts with the approved contract, stop with a concise BLOCKED receipt identifying exact source/symbol/required change. Do not silently widen scope, weaken a check, create an optimistic fallback, or mark the parent work complete.

Each packet finishes with a durable receipt containing its task ID, base/head commit, changed paths, actual commands/exit codes/test names/skips, scenario observations, remaining obligations, and PASS/BLOCKED status. Do not invent a success receipt at spec-authoring time. The supplied receipt example deliberately says NOT_RUN. A fresh verifier uses the same packet and checks code plus evidence, not the implementer's prose.

## Release is not task completion

All 113 tasks, 147 criteria, 112 acceptance scenarios and required topology/family gates must close at an exact candidate SHA before the strict profile can be activated. The final three packets measure, validate the complete coordinate universe, run the mandatory factored-conformance witnesses and fault topologies, and seal evidence; they do not authorize live production database modification. Historical completed calls retain their original policy. Live activation or financial correction requires a separately authorized operator action. No safety ceiling, dead-letter abandonment or blanket rejection of required native-supported modalities counts as a final repair.

Run `python .kiro/specs/billing-financial-safety-contracts/tools/verify_spec.py` to check this archive's internal metadata/traceability. That validates the specification package only, not the proxy's financial correctness.


## Revision 2: full interface and mixed-modality coverage

Read coverage/README.md and coverage/contracts.md when the selected packet references D17-D24. The complete universe has five frontend contributions, ten builtin backends and 34 connector modules at the pinned baseline. The 901120 base modality-set obligations are expanded by actual profile, operation, carrier and independent delivery/transport; they are **not** claims of tests already run.

Tasks 11.x-15.x are prerequisites inserted into execution/order.md; do not finish 10.3 first and treat these as optional follow-ups. Native-supported finite-priced mixtures require positive billing tests. A missing proxy implementation cannot be passed as a native incompatibility. Existing unbounded or natively impossible operations remain explicit limitations, not fictional support.

Each connector packet's gate uses its own module working directory. The wrapper records the root code SHA and actual module command. Keep unknown schema/profile/transport changes blocked until their obligations are updated; do not perform unassigned external research or invent native semantics.


## Revision 3: architecture simplification

Revision 3 keeps the complete financial and multimodal scope but removes two avoidable sources of complexity: separate final allocation/grant-consumption transactions and per-coordinate full-stack repetition. See `revision-3-architecture.md` for the normative atomic authorization contract, factored conformance model, hexagonal placement rule, CORDIS lifetime table, task overrides, and simplification acceptance gates.
