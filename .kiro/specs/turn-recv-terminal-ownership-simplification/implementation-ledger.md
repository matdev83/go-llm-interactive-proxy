# Implementation Ledger

This ledger records implementation commits and direct verification evidence. Overall spec completion remains false until the implementation is merged and verified on `main`.

| Task | Status | Commit | Verification evidence |
|---|---|---|---|
| 1.1 Ownership/coupling baseline | Complete | `2bb1c4c2` | Deterministic AST baseline: 89 fields, 12 synchronization primitives, 91 receiver methods, 34 domain-package fan-out, 39 Executor-reachable methods, and 62 state-copy assignments. Opt-in final-topology gate is RED for the intended flattened-ownership findings. |
| 1.2 Recv/Close/failure/terminal characterization | Complete | `2bb1c4c2` | Exact event/error/terminal ordering, blocked Recv versus Close, cancel/timeout/EOF/panic behavior, loser claims, and detached bounded cleanup characterized; 100 repeated scheduling runs passed. |
| 1.3 Response/usage/billing/security/observer characterization | Complete | `2bb1c4c2` | Existing runtime characterization matrix plus new sequential-replacement evidence test pins sideband usage, two correctly attributed B-leg records, one call closure, observer output, and ordering; uncached runtime package passed. |
| 1.4 Replacement pinning/interleaved characterization | Complete | `2bb1c4c2` | Existing interleaved/reload suite plus new continuation test pins exec/session/metering/authority/security/routing/model/billing facts across refresh; uncached and 100 repeated focused runs passed. |
| 2.1 Immutable receive-turn facts | Complete | `4528016b` | `recvTurnFacts` is the sole construction-time authority for cloned request, pinned context/model views, route/security identity, and stable economic owner references. Focused facts/ratchet tests, uncached runtime, runtime vet, and 100 repeated Recv/Close/domain/pinning runs passed. The opt-in target improved from 89 to 67 direct façade fields and from 62 to 40 state-copy assignments; remaining target debt is assigned to later owner tasks. |
| 2.2 Current-B-leg attempt owner | Complete | `296205ce` | One private `attemptSession` now owns the managed stream, B-leg/candidate identity, authority lifecycle, and fresh ScopeAttempt terminal behind a short-lock `attemptSlot`. Direct façade attempt fields/reset plumbing and transitional forwarding methods were deleted. Uncached runtime, runtime vet, focused repeated Recv/Close/replacement/domain tests, and diff checks passed; the opt-in target improved to 62 fields, 11 direct sync primitives, 86 receiver methods, and 35 state-copy assignments. |
| 2.3 Attempt-local accounting/tool/prompt-cache state | Complete | `362f13b1` | `attemptSession` now owns provider usage accounting/dedupe, the per-B-leg tool assembler, backend-derived prompt-cache source/controller, and final stream-observation session. A production-path replacement regression proves old state is finalized/discarded and replacement state derives from the new stream/backend; 20 repeated runs, uncached runtime, runtime vet, formatting, and diff checks passed. The opt-in target improved to 55 direct fields and 29 state-copy assignments. Windows race execution remains unavailable because ThreadSanitizer fails allocation with error 87. |
| 2.4 Coherent current-attempt snapshot/swap | Pending | — | — |
| 3.1 TurnTerminal and commitment authority | Pending | — | — |
| 3.2 Request authority/metering/billing closure | Pending | — | — |
| 3.3 Explicit A-leg/interleaved ownership | Pending | — | — |
| 3.4 Consolidated terminal entry points | Pending | — | — |
| 4.1 Recovery controller/opener adapter | Pending | — | — |
| 4.2 Replacement precedence/retirement | Pending | — | — |
| 4.3 Response event/evidence pipeline | Pending | — | — |
| 4.4 Tool/security/logical observations | Pending | — | — |
| 5.1 Small EventStream facade | Pending | — | — |
| 5.2 Ownership/dependency/deletion ratchets | Pending | — | — |
| 5.3 Repository regression gates | Pending | — | — |
| 5.4 Final simplification review/handoff | Pending | — | — |

## Preflight

- Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-turn-recv-terminal-ownership-simplification`
- Branch: `feat/turn-recv-terminal-ownership-simplification`
- Initial HEAD and `origin/main`: `d606571735701ad6f767bd35b939597b4f8ad44f`
- Initial state: clean; branch ahead/behind `origin/main`: `0/0`; no rebase required.
- Accepted requirements, design, tasks, brownfield analysis, and validation were rechecked against the current indexed runtime ownership/call graph before approval.
