package archtest

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Budgets are non-test line counts (approximate architectural mass).
// Raise only when intentionally growing a layer; see docs/architecture-guardrails.md.
var lineBudgets = []struct {
	dir string
	max int
}{
	// internal/core was raised from 32000 to 33000 for the admission-policy-decision-core
	// spec, which adds the shared policy decision vocabulary, legality/evidence/timeout
	// infrastructure, and stage-runner error taxonomy integration to the core layer.
	// Raised from 33000 to 33500 for the control-plane-persistence-query-event-ledger
	// spec Phase 1, which adds the core control-plane validation, status state, and
	// store/clock/identity ports under internal/core/controlplane.
	// Raised from 33500 to 35000 for the control-plane-persistence-query-event-ledger
	// spec Phase 3, which adds the core scope flattener, event normalizer, recorder
	// service, query service, and retention controller under internal/core/controlplane.
	// Raised from 35000 to 38000 for the usage-quota-rate-budget-authority spec,
	// which adds the usageauthority bounded context under internal/core.
	// Raised from 38000 to 38500 to accommodate the full implementation of the
	// usage-quota-rate-budget-authority spec, including runtime admission/settlement
	// integration and the usageauthority bounded context under internal/core.
	// Raised from 38500 to 38800 to accommodate the authority reservation leak-fix
	// additions to internal/core/runtime (recv-loop/handler/interleaved/parallel
	// leak-path hardening and the executor recv-handler settlement chokepoint).
	// Raised from 38800 to 38900 to accommodate the usage-quota-rate-budget-authority
	// spec A1/A2 domain & contract foundation: the Credential authority dimension
	// (Dimensions/DimensionsMatcher/Key/Matches, config matcher, store scope
	// snapshot + ScopeDimensionsMatch, runtime scope projection with safe-label
	// filtering), the authority-unavailable rule outcome for authoritative-only
	// rules under estimated evidence, and the spend-cap clamp EffectiveMax/
	// RequestedMax fields; plus the in-progress multi-rule admission/settlement
	// and recv-loop leak-fix work on the same branch.
	// Raised from 39700 to 40000 to accommodate the medium-findings follow-up:
	// lifecycle-aware settlement/release evidence projection, the accompanying
	// app/runtime test assertions, and the related spec sync cleanup on this
	// branch.
	// Raised from 40000 to 40500 for the remediation pass: app-owned atomic
	// reservation descriptors, per-unit settlement, durable lifecycle evidence,
	// and the accompanying boundary tests.
	// Raised from 40500 to 41000 for the decision-authority remediation: bounded
	// evaluation, explicit usage provenance, compensation, reconciliation, and
	// per-rule unreserved usage facts across runtime and durable stores.
	// Raised from 41000 to 41200 for live spend-cap capacity refinement in the
	// application layer and durable configuration reconciliation in the authority
	// store, including their regression coverage and orchestration helpers.
	// Raised from 41200 to 41250 for rule-attributed multi-rule reservation
	// recovery and independently bounded detached authority cleanup contexts.
	// Raised from 41250 to 41300 for scope-isolated spend-cap lookup and a fresh
	// fallback-release cleanup deadline after failed settlement.
	// Raised from 41300 to 41500 for the coordinated authority stabilization:
	// independent token/cost authority state, typed durable capacity replay,
	// targeted PostgreSQL proof seams, and the differential adapter coverage.
	// Raised from 41500 to 41700 for review remediation: CostPresent monetary
	// presence, unenforceable spend-cap candidate exclusion, multi-scope
	// settlement aggregation, and zero-remaining output budget denial.
	// Raised from 41700 to 42200 to accommodate internal/core/codexcatalog, the
	// auto-discovered Codex model catalog (parser, fallback snapshot, discovery,
	// binary resolver) shared by the openai-codex and codex app-server connectors.
	// Raised from 42200 to 42500 for dual-plane economics Phase 2 correctness:
	// token-total inclusion/presence, checked money arithmetic with optional
	// rate presence, and unknown-output preflight policy resolution.
	// Raised from 42500 to 43500 for Phase 4 metering checkpoints (capture,
	// widening, egress fact drafts) plus runtime hook wiring.
	// Raised from 43500 to 44000 for Phase 5 aggregate + reconcile packages.
	// Raised from 44000 to 45000 for Phase 6 authority coordinators + adapters.
	// Raised from 45000 to 45500 for Phase 7 usage-authority dual-plane kernel.
	// Raised from 45500 to 47000 for Phase 8 concurrencyauthority domain/app + runtime lease ownership.
	// Raised from 47000 to 47500 for Phase 9 snapshotgen RuntimeGeneration publisher.
	// Raised from 47500 to 48000 for Phase 11.2 control-plane query bounds validation.
	// Raised from 48000 to 48100 for Phase 12 rem: settle/release/query stage metrics (16.5).
	// Raised from 48100 to 49400 for injected-rater OutputLimitQuoter clamp path
	// plus dual-plane remediations on this branch (no silent catalog fallback).
	// Raised from 49400 to 49600 for FE/BE deferred ingress counting + BE freeze
	// before attempt authorization (reqs 2.1, 2.2, 4.1, 5.1).
	// Raised from 49600 to 49750 for explicit PostgreSQL connection/schema modes
	// and validation; this is typed configuration, not runtime control-flow growth.
	// Raised from 49750 to 49950 for live model-registry inventory/runtime growth
	// (issue #146): BackendModel discovery diagnostics and refresh seams in
	// internal/core/modelregistry. The OpenAI-compatible GET /v1/models HTTP DTO
	// stays in internal/stdhttp (not core).
	// Raised from 49950 to 50050 for issue #146 access-mode gate comments on
	// CodexModelCatalogConfig (single_user + enabled-consumer discovery).
	// Raised from 50050 to 50300 for issue #146/#149 model-registry perf and
	// inventory completion: publish-time OpenAI list JSON, content fingerprint,
	// parallel inventory fetch, and refresh overlap guard in modelregistry.
	// Raised from 50300 to 50700 for issue #147 identity foundation: pure
	// identity policy model, defaults/validation/merge, and AcceptClientUserAgent.
	// Raised for issue #147 OpenRouter attribution: AcceptClientAppURL/AppTitle.
	// Raised from 50700 to 50900 for issue #147 gap closure: SpecBundleIdentityScenarios.
	// Raised from 50900 to 51300 for protocol-neutral internal/core/jsonshape
	// (encoding/json.Decoder.Token size/shape preflight shared by frontend
	// jsonguard). Measured post-change non-test total is 51185; cap keeps ~115
	// lines of headroom.
	// Raised from 51300 to 53900 for issue #152 tool-call repair (toolcallrepair +
	// assembler/finalization). Measured post-change non-test total is 53816; cap
	// keeps ~80 lines of headroom.
	// Raised for issue #151 secretsguard catalog/matcher/source + quarantine
	// adapters + runtime barrier (merged with #152/#jsonshape on main).
	// Measured post-merge non-test total is 56295; cap keeps ~105 lines of headroom.
	// Raised from 56400 to 57550 for dual-plane-economics production-readiness
	// Phase 1: customer evidence accumulator, final-backend clamp preview,
	// correlation/presence ingress facts, and control-plane metering projection.
	// Accidental duplication was removed first (~148 lines); remaining growth is
	// approved core orchestration. Measured post-simplify non-test total is
	// 57447; cap keeps ~103 lines of headroom.
	// Raised from 57550 to 57650 for Phase 1 follow-up: operator usage retention on
	// incurred loser/swallowed paths plus metering/plane extraction of pure
	// dual-plane helpers.
	// Raised from 57650 to 58300 for dual-plane Phase 2.3 authority coordinator
	// posture/compensation, settlement concurrency state, and req 4.3 hold
	// validation (measured ~58197 non-test lines).
	// Raised from 56400 (main lineage) for reasoning-output-preservation Phase 2.3:
	// RunCandidateAttemptTransformStage + openPlannedCandidate wiring / post-hook rederive.
	// Raised from 56900 for Phase 2.4 final_stream_observation runner + recv/gate lifecycle.
	// Raised from 57200 for Phase 2.4 repair: race-safe session claim, CompletionGateChainResult,
	// central emitClientFacingObserved (recoverDrain/synthesized usage), parallel excluded nil-guard.
	// Raised from 57250 to 57380 for Phase 2.5 safe generic stage telemetry
	// (bounded label collapse, count/byte helpers, generic-port inventory posture).
	// Raised to 57400 for early Recv ctx-cancel remediation (nil-inner Cancelled
	// taxonomy + swallowed release; nil-executor-safe cancel path).
	// Combined dual-plane Phase 1 + reasoning-preservation on merge into main.
	// Measured post-merge non-test total is 58740; cap keeps ~110 lines of headroom.
	// Phase 2 + reasoning-preservation combined after merge into main: 59500
	// (main 58850 + Phase 2 +650); post-merge measured 59256.
	// Raised from 59500 to 59800 for dual-plane Phase 3 durable metering journal:
	// ingress checkpoint producers, control-plane metering usage bridge/projection,
	// and reconstruction seams (retarget onto main Phase 1+2 + reasoning; measured
	// 59629). Cap keeps ~171 lines of headroom. Prefer further decomposition over
	// another raise.
	// Raised for dual-plane Phase 4 terminal ownership and terminal-work domain:
	// stream terminal session wiring, terminal owner CAS, WorkRecord/SameIntentReplay,
	// claim-lease transitions, Phase 4.4 processor app, and Phase 4.5 durable
	// settle/release recovery + query/metrics/readiness. Combined with main
	// reasoning-output-preservation on merge into Phase 4: measured 62647;
	// cap 62750 keeps ~103 lines of headroom.
	// Raised from 62750 to 63750 for dual-plane Phase 5 executable generations
	// (contribution compile/publish/bind/lifetime, generation-owned
	// RequestCoordinator/AttemptCoordinator/max-active limiter + rater binding,
	// control-plane executable readiness) merged onto main Phase 1–4 + reasoning.
	// Post-merge measured non-test total is 63659; cap keeps ~91 lines of headroom.
	// Raised from 63750 to 64550 for dual-plane Phase 6 atomic lease-set concurrency
	// (AcquireSet/RenewSet/ReleaseSet, heartbeat fail-closed cancel, durable
	// ReleaseLeaseSet terminal work, QuerySets readiness) merged onto main
	// Phase 1–5 + reasoning. Post-merge measured non-test total is 64452;
	// cap keeps ~98 lines of headroom. Prefer further decomposition over another raise.
	// Raised from 64550 to 64800 for the cursor-sdk backend follow-up merged onto
	// the above: operator usage evidence wiring into operator settle/egress
	// (lastAuthorityUsage → seenEvents → empty UsageDelta shell precedence;
	// req 1.5 / 2.9), AttemptCoordinator.Preview clamp-preview fallback for
	// nil-coordinator UsageAuthority-only deployments, and execbackend.Backend.Close.
	// Post-merge measured non-test total is 64713; cap keeps ~87 lines of headroom.
	// Raised from 64800 to 65300 for the versioned runtime reload policy package:
	// explicit top-level/startup-override inventory, typed section comparators,
	// safe bounded restart-required reporting, and mixed-change rejection.
	// Raised from 65300 to 65800 for the strict effective configuration pipeline:
	// bounded core-compatible loading, exactly-one-document decode, defaults and
	// fixed override materialization, and private/public effective identities
	// (requirements 1.7, 2.1-2.10, 3.6-3.8, 14.3). The measured total is 65411;
	// cap keeps roughly 389 lines of headroom.
	// Raised from 65800 to 65850 for versioned-runtime-reload task 3.2 final wiring:
	// optional backend Start/Stop/idle/preflight fields on execbackend.Backend
	// (measured non-test total 65818; ~32 lines headroom).
	// Raised from 65850 to 66250 for versioned-runtime-reload task 3.5:
	// immutable per-request model registry/catalog BoundView APIs, routing
	// native-model resolver context, and coherent diagnostics helpers
	// (measured non-test total 66202; ~48 lines headroom).
	// Raised from 66250 to 66700 for task 3.5 repair: multi-state native
	// binding, monotonic allowlists, recv-bound model views, modelview
	// aggregate identity (measured non-test total 66624; ~76 lines headroom).
	// Raised from 66700 to 66950 for task 3.6: runtime-generation pin tracker,
	// request-context pin prepare helper, and BoundVersions runtime identity
	// accessors for durable terminal/async ownership (req 5.3, 10.3, 10.7;
	// measured non-test total 66847; ~103 lines headroom).
	// Raised from 66950 to 67400 for task 3.6 pass-2: runtime instance ID,
	// AppendIntent reconciliation, WorkID executable pending, aux stream pin,
	// and synchronized terminal-done callbacks (measured 67287; ~113 headroom).
	// Raised from 67400 to 67900 for task 3.6 remediation A: adoption
	// reservation/MarkTerminal state machine, AppendIntentOutcome seam, and
	// AmbiguousAppendHandoff wiring (measured 67702; ~198 headroom).
	// Raised from 67900 to 68500 for task 3.6 remediation B: process-owned
	// AmbiguousAppendReconciler worker and IntentService handoff (measured
	// 68303; ~197 headroom).
	// Raised from 68500 to 68700 for versioned-runtime-reload task 4.4+4.5:
	// generation-scoped CircuitBreakerState/CircuitBreakerPolicy split
	// (internal/core/policy) and route-selector backend-ID enumeration for
	// default-route/alias validation (internal/core/routing) (measured
	// 68608; ~92 headroom).
	// Raised from 68700 to 68850 for versioned-runtime-reload task 5.1:
	// configreload trigger/result vocabulary and load-failure mapping used by
	// the serialized reload coordinator (measured 68780; ~70 headroom).
	// Raised from 68850 to 69350 for versioned-runtime-reload task 5.4:
	// secret-safe reload sanitizers, bounded status history ring, and protected
	// reload diagnostics DTOs (measured 69194; ~156 headroom).
	{"internal/core", 69350},
	{"internal/pluginreg", 4500},
	// Raised from 3500 to 3800 for versioned-runtime-reload task 3.3: App-less
	// generation request-plane composer (ComposeRequestPlane) without listener
	// bind (measured non-test total 3668; ~132 lines headroom).
	// Raised from 3800 to 3950 for versioned-runtime-reload task 3.4: stable
	// generation-host runner (RunWithGenerationHost) behind one http.Server
	// (measured non-test total 3801; ~149 lines headroom).
	// Raised from 3950 to 4000 for versioned-runtime-reload task 4.3: frontend/
	// feature generation route-conflict isolation on ComposeRequestPlane mounts
	// (measured non-test total 3970; ~30 lines headroom).
	// Raised from 4000 to 4650 for versioned-runtime-reload task 5.3: process-owned
	// management HTTP adapter (admin/configreload listener, auth, browser guard,
	// DTOs, and lifecycle) outside the swappable request plane
	// (measured non-test total 4587; ~63 lines headroom).
	// Raised from 4650 to 4750 for versioned-runtime-reload task 5.6: generation
	// host shutdown ordering (coordinator BeginShutdown + management close).
	// Ratcheted to the exact runtime-architecture-convergence task 3.2 total:
	// StandardHTTPInput focused projection groups, typed-nil adapter boundaries,
	// and defensive clones (measured non-test total 4793; zero headroom).
	// Raised from 4793 to 4897 for task 3.4: cycle-neutral internal/stdhttp/contract
	// package (StandardHTTPInput/HTTP*Input groups + shared clone helpers moved out
	// of http_input.go so runtimebundle can build the focused input without
	// importing root stdhttp) plus the canonical ComposeStandardHTTP composer
	// (measured non-test total 4897; zero headroom).
	// Ratcheted from 4897 to 4811 for Task 3.5: deleted ComposeRequestPlane and
	// standardHTTPInputFromRequestPlane (measured 4811; zero headroom).
	// Ratcheted from 4811 to 4522 for Task 4.2: deleted NewStandardHandler,
	// RunWithRuntime, standardHTTPInputFromBuilt, releaseBuiltResources, and
	// runClosers (measured 4522; zero headroom).
	// Contracted from 4522 to 4509 for Task 7.4: shutdownGenerationHost and
	// closeProcessServices deleted; the serve adapter now depends on one
	// focused GenerationServeHost seam with a shared post-Host input-failure
	// cleanup path and single HTTPHandler resolution (measured 4509; zero headroom).
	{"internal/stdhttp", 4301},
	// Raised from 4650 to 4800 for dynamic snapshot SnapshotController refresh
	// lifecycle (requirements 11.3, 11.6, 11.7).
	// Raised from 4800 to 5200 for build-local PostgreSQL pool ownership,
	// centralized schema lifecycle, and the associated runtime readiness and
	// integration seams. The current 4929-line package remains below this cap
	// with meaningful headroom for follow-up changes; keep new growth in focused
	// files and do not use this budget to excuse Build orchestrator re-bloat.
	// Raised from 5200 for issue #151 Phase 3 secret-guard source binding.
	// Raised from 5300 for issue #151 composition collapse + bootstrap uniqueness.
	// Raised from 5350 after merging main (tool-call repair + secrets-guard wiring).
	// Measured post-merge non-test total is 5365; cap keeps ~85 lines of headroom.
	// Raised from 5450 for dual-plane Phase 2.2 descriptor-bound registrations:
	// production authority_coord consumes Request/Attempt/Concurrency/Rater
	// registrations with stable IDs (no production-request-%d generation).
	// Measured non-test total is 5562; cap 5580 keeps ~18 lines of headroom.
	// Raised from 5580 to 5690 after merging reasoning-output-preservation into
	// Phase 2 (post-merge measured 5586; ~100 lines headroom).
	// Raised for dual-plane Phase 4.4–4.5 terminal-work processor ownership
	// (ProductionOptions + Built + buildTerminalWorkRuntime, tick/renew defaults,
	// TerminalWorkReadiness, IntentService injection, RequestRegistrations →
	// AuthorityRequestEffectProvider merge, ProcessDue metrics observer). Combined
	// with main reasoning wiring on merge into Phase 4: measured 6121;
	// cap 6170 keeps ~49 lines of headroom.
	// Raised from 6170 to 6400 for Phase 5 executable generation compile/publish/
	// readiness plus Phase 5 remediation (provider-removal validation + terminal
	// pending-drain binding at composition root). Post-merge measured non-test
	// total is 6353; cap keeps ~47 lines of headroom.
	// Raised from 6400 to 6525 for Phase 6 lease-set QuerySets readiness, startup
	// uncertain-set reconcile, and settle-release pending counts at composition
	// root. Post-merge measured non-test total is 6477; cap keeps ~48 lines of
	// headroom.
	// Raised from 6525 to 7020 for versioned-runtime-reload task 2.3: explicit
	// ProcessServices construction, candidate compilation inputs, and Build
	// compatibility wrapper (measured non-test total 6967; ~53 lines headroom).
	// Raised from 7020 to 7600 for versioned-runtime-reload task 2.4: hoist
	// process-capacity / shared mutable continuity (A-leg, affinity/health views,
	// extension state, accounting/metering stores, BackendStateIdentity keys).
	// Raised from 7600 to 8200 for versioned-runtime-reload task 3.2: candidate
	// ResourceLedger, backend instance wrapper, and lifecycle adaption
	// (measured non-test total ~8100; ~100 lines headroom).
	// Raised from 8200 to 8350 for task 3.2 final production wiring: feature
	// lifecycle classify/adapt, backend optional hooks, catalog PhaseQuiesce,
	// and generation-owned HTTP idle cleanup (measured 8238; ~112 lines headroom).
	// Raised from 8350 to 9100 for versioned-runtime-reload task 3.3: complete
	// generation compilation, immutable GenerationBundle, RequestPlane view, and
	// config freeze (measured non-test total 8982; ~118 lines headroom).
	// Raised from 9100 to 9350 for versioned-runtime-reload task 3.4: initial
	// generation bootstrap host (ProcessServices + CompileGeneration + Manager
	// publish of generation 1) without Built (measured non-test total 9211;
	// ~139 lines headroom).
	// Raised from 9350 to 9550 for task 3.6: generation-local terminal provider
	// resolver + GenerationBundle provider snapshot (measured 9417; ~133 headroom).
	// Raised from 9550 to 9650 for versioned-runtime-reload task 4.4+4.5:
	// candidate-vs-process Classify wiring, default-route/alias-vs-backend-set
	// validation, and generation-scoped health-policy wiring in
	// candidateRoutingViews (measured 9578; ~72 headroom).
	// Raised from 9650 to 9700 for versioned-runtime-reload task 5.1:
	// GenerationBundle.BackendFactoryKindCounts for LiveFactoryKinds admission
	// (measured 9666; ~34 headroom).
	// Raised from 9700 to 9950 for versioned-runtime-reload tasks 5.5–5.6:
	// shared ReloadHost composition (fixed source, effective loader, generation
	// compiler adapter, coordinator bind) and ActiveSource bootstrap retention
	// used by lipruntime and lipstd (measured non-test total ~9849; ~100 headroom).
	// Raised from 9950 to 10101 for runtime-architecture-convergence Task 3.3:
	// GenerationRuntime contract, cohesive private groups, singular ledger
	// ownership transfer from CandidateRuntime, and race-safe Quiesce/Close state
	// machine replacing dual generationOwner+Once wrappers; candidate lifecycle
	// extracted to candidate_lifecycle.go (measured 10101; zero headroom).
	// Raised from 10101 to 10196 for Task 3.4: CompileGeneration now builds the
	// focused StandardHTTPInput directly from the contract package (no
	// RequestPlane construction on the canonical path), GenerationCompiler
	// widens its return via the one explicit candidateCompilerAdapter, and the
	// transitional NewCompatRequestPlane constructor keeps ComposeRequestPlane
	// compatibility tests independent of CompileGeneration (measured 10196;
	// zero headroom).
	// Raised from 10196 to 10206 for Task 3.4 repair: CompileGeneration deep-
	// clones canonical frozen config before lending it to the injected
	// HandlerComposer so composition cannot mutate the immutable generation
	// source used for publication (measured 10206; zero headroom).
	// Ratcheted from 10206 to 9969 for Task 3.5: deleted broad RequestPlane
	// aggregate/getter wall and NewCompatRequestPlane; HandlerComposer +
	// FrozenRoutingView moved to handler_composer.go (measured 9969; zero headroom).
	// Task 4.1: exact measured post-deletion of Candidate terminal-work detour
	// (candidate_terminal_work.go + terminal_work.go shared-helper extraction).
	// Ratcheted from 9928 to 9603 for Task 4.2: deleted built.go, build.go,
	// candidate Closers field, ResourceLedger.LegacyClosers, and the Built-only
	// terminal-work methods (measured 9603; zero headroom).
	// Task 5.1: exact measured size after per-invocation bootstrapEffectiveLoader
	// parameter + unexported buildHost RED seam (no package-global loader hook).
	// Task 5.2: exact measured after BuildHost repair — shared hostBuildProbe
	// seam (no test-only ownership engine) and EnforceMultiUserCLIGate; temporary
	// raise records serve/public Build migration off BuildBootstrap+AttachReloadHost
	// (measured 9996; zero headroom).
	// Task 5.3: contracted to 9991 after deleting BootstrapInspect, serve-mode App
	// construction, and duplicate feature-surface merge, and adding InspectRoutes/
	// InspectInventory (measured 9991; zero headroom). Task 5.5 deletes remaining
	// BuildBootstrap/AttachReloadHost dual path.
	// Raised from 9991 to 10200 for task 5.4: true unpublished ValidateDistribution
	// (validate_distribution.go) plus omitSoleAlreadyClosed so mixed
	// ErrAlreadyClosed cleanup joins keep sibling failures (measured 10200;
	// zero headroom). Same effective loader/ProcessServices/CompileGeneration/
	// composer path as serve/reload, with no Manager, generation ID, active
	// pointer, or listener.
	// Ratcheted from 10200 to 10020 for Task 5.5: deleted BuildBootstrap,
	// BootstrapResult, BootstrapMode/BootstrapServe/BootstrapUnspecified,
	// AttachReloadHost, and the LoadBootstrapEffective no-source wrapper;
	// moved installRegistryAndRegistrations/initProcessTracing/shutdownTracing
	// into composition_root.go; refactored publishInitialGeneration to return
	// explicit (*ProcessServices, *runtimehost.Manager, *runtimehost.Generation,
	// error) instead of projecting through BootstrapResult. The
	// process_services.go / process_services_types.go split is critical-file
	// organization only (neutral to this recursive package total). check-config
	// uses ValidateDistribution and serve/public Build use BuildHost exclusively
	// (measured 10020; zero headroom).
	// Ratcheted from 10020 to 10163 for Task 7.2: ResourceLedger is the sole
	// generation-resource phase owner (mutex/cond/retryable close); Candidate
	// transfer-vs-lifecycle exclusive claim + terminal start blocking;
	// GenerationBundle wrapper guards and buildBackends releaseOnce deleted.
	// Ratcheted from 10163 to 10162 for Task 7.3: host_build.go no longer
	// imports runtimehost directly (ShutdownDetached call sites simplified;
	// measured 10162; zero headroom).
	// Raised from 10162 then re-measured for Task 7.4: Host absorbed the
	// duplicated stdhttp/CLI shutdown orchestration as the sole process
	// shutdown coordinator (shared-attempt Close admission, per-phase
	// idempotency, Host reload/status/source delegation, consolidated
	// pre-Host rollback). Attempt-3 removed closeAttemptWaitHook. stdhttp and
	// cmd/lipstd contract in the same change, so the net production delta
	// across the three trees vs Task 7.3 is +78 (measured 10286; zero headroom).
	// Task 8.1: Host facade query methods (Ready/snapshot/readiness/metering)
	// moved onto Host so pkg/lipruntime stays at/below the b264155f pre-task
	// recursive non-test total (measured 10451; zero headroom).
	// Task 8.2: dropped deprecated ProductionOptions fields (measured 10419).
	// Task 8.4: comment-only contraction after legacy-adapter wording removal (10418).
	// Task 9.2: candidate_compile helpers moved to candidate_options.go; recursive
	// package-tree total unchanged (measured 10418; zero headroom).
	{"internal/infra/runtimebundle", 9468},
	// Task 5.5: exact-measured cmd/lipstd after dual-bootstrap deletion
	// (measured 913; zero headroom).
	// Contracted from 913 to 880 for Task 7.4: tracing_shutdown.go deleted and
	// the tracingDeferred serve boundary removed (measured 880; zero headroom).
	{"cmd/lipstd", 880},
	// Task 8.1: recursive public facade package exact-measured after host seam
	// split (measured 819; at/under the b264155f pre-task 820 ceiling).
	// Task 8.2: legacy_options quarantine adapter (measured 829).
	// Task 8.4: legacy adapter + deprecated Options fields deleted (measured 648).
	// PR A: restored HostCapabilities public contract (measured 536).
	{"pkg/lipruntime", 536},
}

func TestLineComplexityBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range lineBudgets {
		t.Run(b.dir, func(t *testing.T) {
			t.Parallel()
			n, err := countNonTestGoLines(filepath.Join(root, b.dir))
			if err != nil {
				t.Fatal(err)
			}
			if n > b.max {
				t.Fatalf("%s: %d non-test lines exceeds budget %d (see docs/architecture-guardrails.md)", b.dir, n, b.max)
			}
		})
	}
}

// criticalFileBudgets locks single-file gravity wells from silently re-bloating.
// These complement the tree-level lineBudgets above. Budgets are non-test line counts
// measured with the same bufio.Scanner methodology as countNonTestGoLines.
// Rationale and values are maintained in CriticalFileBudgets so make arch-report
// reports the same hotspot list.
var criticalFileBudgets = CriticalFileBudgets

func TestCriticalFileLineBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range criticalFileBudgets {
		t.Run(strings.ReplaceAll(b.Path, "/", "_"), func(t *testing.T) {
			t.Parallel()
			n, err := countFileLines(filepath.Join(root, b.Path))
			if err != nil {
				t.Fatalf("%s: %v", b.Path, err)
			}
			if criticalFileExceedsBudget(n, b.Max) {
				t.Fatalf("%s: %d non-test lines exceeds critical-file budget %d (see docs/architecture-guardrails.md)", b.Path, n, b.Max)
			}
			t.Logf("%s: %d/%d lines", b.Path, n, b.Max)
		})
	}
}

func TestStandardBundlePackagesHaveNoInitFunctions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "pluginreg"),
		filepath.Join(root, "internal", "standardplugins"),
		filepath.Join(root, "cmd", "lipstd"),
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				if hasInitFunc(path) {
					t.Fatalf("forbid init() in standard bundle path (explicit InstallStandardBundleOn/validation from composition root): %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTestsMustNotRegisterStandardBundleInInit(t *testing.T) {
	t.Parallel()
	initDecl := "func init" + "("
	regStd := "RegisterStandard" + "Bundle()"
	root := repoRoot(t)
	var bad []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.Contains(s, initDecl) && strings.Contains(s, regStd) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("forbid init-time standard bundle registration in tests; install factories on a fresh registry from tests/helpers explicitly:\n%s", strings.Join(bad, "\n"))
	}
}

func TestRuntimebundleDoesNotSelectPluginregDefault(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimebundle")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if refsPluginregDefaultSelector(t, path, src) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("runtimebundle must not reference pluginreg.Default (pass *pluginreg.Registry via BuildOptions); offending files:\n%s", strings.Join(bad, "\n"))
	}
}

func TestCompositionLayersDoNotRegisterStandardBundle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "stdhttp"),
	}
	// composition_root.go is the single allowed call site for InstallStandardBundleOn
	// inside the runtimebundle composition root (installRegistryAndRegistrations,
	// shared by BuildHost/ValidateDistribution/Inspect entrypoints).
	// All other runtimebundle and stdhttp files must not install the standard bundle.
	allowedFile := filepath.Join(root, "internal", "infra", "runtimebundle", "composition_root.go")
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			var bad []string
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				if path == allowedFile {
					return nil // composition_root.go is the composition-root startup path
				}
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if callsStandardBundleInstall(t, path, src) {
					bad = append(bad, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) != 0 {
				t.Fatalf("%s: forbid standard bundle installation in composition layer (only composition_root.go may install; install in cmd/lipstd or tests, pass registry in): %s", dir, strings.Join(bad, "\n"))
			}
		})
	}
}

func TestWiringRootsHaveNoPackageLevelPluginRegistryOrSyncOnce(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "stdhttp"),
		filepath.Join(root, "cmd", "lipstd"),
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			var badReg, badOnce []string
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				pr, once := packageLevelRegistryVarOrSyncOnce(t, path, src)
				if pr {
					badReg = append(badReg, path)
				}
				if once {
					badOnce = append(badOnce, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(badReg) != 0 {
				t.Fatalf("%s: forbid package-level *pluginreg.Registry / NewRegistry vars (registry owned by composition root, threaded as parameters): %s", dir, strings.Join(badReg, "\n"))
			}
			if len(badOnce) != 0 {
				t.Fatalf("%s: forbid package-level sync.Once (no lazy standard-bundle or registry singletons in wiring): %s", dir, strings.Join(badOnce, "\n"))
			}
		})
	}
}

func TestCompositionRootDoesNotPairSyncOnceWithStandardBundleInstall(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "cmd", "lipstd")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fileReferencesSyncOnce(t, path, src) && callsStandardBundleInstall(t, path, src) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("cmd/lipstd: forbid sync.Once + standard bundle install in the same file (no lazy registration); offending files:\n%s", strings.Join(bad, "\n"))
	}
}

func refsPluginregDefaultSelector(t *testing.T, filename string, src []byte) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		xid, ok := sel.X.(*ast.Ident)
		if !ok || xid.Name != "pluginreg" {
			return true
		}
		if sel.Sel == nil || sel.Sel.Name != "Default" {
			return true
		}
		found = true
		return false
	})
	return found
}

func callsStandardBundleInstall(t *testing.T, filename string, src []byte) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		if name == "InstallStandardBundleOn" {
			found = true
			return false
		}
		return true
	})
	return found
}

func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if f.Sel != nil {
			return f.Sel.Name
		}
	}
	return ""
}

func packageLevelRegistryVarOrSyncOnce(t *testing.T, filename string, src []byte) (badRegVar bool, badPkgOnce bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil && isStarPluginregRegistry(vs.Type) {
				badRegVar = true
			}
			for _, v := range vs.Values {
				if isPluginregNewRegistryCall(v) {
					badRegVar = true
				}
			}
			if vs.Type != nil && isSyncOnceType(vs.Type) {
				badPkgOnce = true
			}
		}
	}
	return badRegVar, badPkgOnce
}

func isStarPluginregRegistry(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	if !ok || xid.Name != "pluginreg" {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == "Registry"
}

func isPluginregNewRegistryCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if callName(call.Fun) != "NewRegistry" {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	return ok && xid.Name == "pluginreg"
}

func isSyncOnceType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xid, ok := sel.X.(*ast.Ident)
	if !ok || xid.Name != "sync" {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == "Once"
}

func fileReferencesSyncOnce(t *testing.T, filename string, src []byte) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		xid, ok := sel.X.(*ast.Ident)
		if !ok || xid.Name != "sync" {
			return true
		}
		if sel.Sel != nil && sel.Sel.Name == "Once" {
			found = true
			return false
		}
		return true
	})
	return found
}

// countNonTestGoLines delegates to the exported CountNonTestGoLines so
// guardrails, Task 4.4 exact budgets, and make arch-report share one walker.
func countNonTestGoLines(dir string) (int, error) {
	return CountNonTestGoLines(dir)
}

// countFileLines returns the number of lines in a single file. Used for per-file
// critical-file budgets and the architecture report.
func countFileLines(path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	sc := bufio.NewScanner(f)
	// Raise the per-line buffer so long generated/fixture-style lines are still
	// counted; budgets are approximate architectural mass, not exact SLOC.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

func hasInitFunc(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(b)
	// Naive but sufficient: registration must not hide in init().
	return strings.Contains(s, "func init(")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for range 12 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find go.mod above", wd)
	return ""
}

// referencesIdent reports whether any *ast.Ident in the file has the given name. It
// catches both field declarations (Field.Names) and selector/operand references, so a
// guardrail can forbid an identifier from a whole layer without regard to how it is used.
func referencesIdent(t *testing.T, filename string, src []byte, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestPolicyDiagnosticsEnabledNotReferencedFromFrontendOrStdhttp locks requirement 7.4:
// the privileged-diagnostics flag must be settable only from the composition root
// (runtimebundle.BuildOptions -> runtime.Executor), never from a frontend HTTP adapter
// or stdhttp request path. A client request must not be able to enable privileged
// policy decision evidence.
func TestPolicyDiagnosticsEnabledNotReferencedFromFrontendOrStdhttp(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "plugins", "frontends"),
		filepath.Join(root, "internal", "stdhttp"),
	}
	var bad []string
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if referencesIdent(t, path, src, "PolicyDiagnosticsEnabled") {
				bad = append(bad, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("PolicyDiagnosticsEnabled must not be referenced from frontend/stdhttp request paths (composition-root-only, requirement 7.4):\n%s", strings.Join(bad, "\n"))
	}
}

// TestFailureModeAndTimeoutBudgetNotClientSourced locks requirements 6.2 and 6.3: policy
// failure behavior and evaluation timeout budgets are sourced only from plugin
// interface methods and the frozen RequestRuntimeSnapshot (composition root), never
// from client request input decoded in a frontend adapter. An external client must not
// be able to set failure modes or timeout budgets.
func TestFailureModeAndTimeoutBudgetNotClientSourced(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "plugins", "frontends")
	forbidden := []string{"FailOpen", "FailClosed", "FailureMode", "TimeoutBudget", "TimeoutFor"}
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range forbidden {
			if referencesIdent(t, path, src, name) {
				bad = append(bad, path+" ("+name+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("frontend adapters must not reference failure-mode/timeout-budget identifiers (plugin/composition-root-only, requirements 6.2/6.3):\n%s", strings.Join(bad, "\n"))
	}
}
