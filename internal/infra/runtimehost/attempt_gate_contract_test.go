package runtimehost

import (
	"context"

	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// Task 6.1 defines test-only AttemptGate contract vocabulary for Task 6.2.
// Types are unexported and Contract-suffixed so Task 6.2 can introduce an
// internal concrete owner without declaration collisions. This file must not
// back a second ownership state machine; production gate fields stay on
// Coordinator until Task 6.2 extracts them.

// attemptGateContract is the future exclusive owner of reload admission,
// coalescing, active-attempt cancellation, shutdown rejection, idle wait, and
// atomic pending-follow-up claim (requirements 6.1, 6.5-6.9).
type attemptGateContract interface {
	// TryStart admits one attempt or returns an immutable busy/reject outcome
	// with no live lease. On admissionAdmittedContract, registration of
	// busy/active state and the completion lease become visible in one lock
	// transition (req 6.5, 6.6): busy is never observable without an armed
	// completion signal.
	TryStart(ctx context.Context, trigger sdkreload.Trigger) attemptAdmissionOutcomeContract

	// BeginShutdown atomically clears pending follow-up work, rejects future
	// TryStart admissions, and cancels any active lease context (req 6.1, 6.7).
	// Final Complete/abandon of the canceled lease still wakes idle waiters.
	BeginShutdown()

	// WaitForIdle waits on completion notifications until no attempt is active.
	// It must not poll, sleep, or use periodic timers (req 6.5). Cancellation
	// or deadline of ctx cancels only this wait — never the active lease.
	WaitForIdle(ctx context.Context) error
}

// attemptLeaseContract is one admitted attempt token. The gate owns internal
// cancellation; consumers see only the host-owned attempt context and a single
// atomic completion/abandon transition (no separate Cancel authority).
type attemptLeaseContract interface {
	// Context is the host-owned attempt context. Trigger-caller cancel/deadline
	// after admission must not cancel this context; context values may remain
	// available. Gate/host shutdown cancels it.
	Context() context.Context

	// Complete atomically finishes or advances the active lease exactly once:
	// either final release (wake idle waiters) or claim exactly one pending
	// HUP follow-up without an idle/admission gap (req 6.7, 6.8).
	Complete() attemptFinishOutcomeContract
}

// attemptAdmissionOutcomeContract is the immutable TryStart result. Busy and
// coalesced metadata live on the value — no mutable snapshot/getter API.
type attemptAdmissionOutcomeContract struct {
	Kind             attemptAdmissionKindContract
	Lease            attemptLeaseContract // non-nil only for admissionAdmittedContract
	CoalescedSignals int64                // safe count for pending/coalesced HUP busy results
	Category         sdkreload.ResultCategory
	ReasonCategory   string
}

// attemptAdmissionKindContract classifies TryStart without a second state machine.
type attemptAdmissionKindContract int

const (
	// admissionAdmittedContract: lease armed; attempt observable as active.
	admissionAdmittedContract attemptAdmissionKindContract = iota
	// admissionBusyAPIContract: API while active — canonical busy, no queue (req 6.9).
	admissionBusyAPIContract
	// admissionPendingHUPContract: first SIGHUP while active sets at most one pending follow-up (req 6.8).
	admissionPendingHUPContract
	// admissionCoalescedHUPContract: additional SIGHUP increments coalesced; no extra queued work (req 6.8).
	admissionCoalescedHUPContract
	// admissionRejectedShutdownContract: shutting down; no lease (req 6.1).
	admissionRejectedShutdownContract
)

// attemptFinishOutcomeContract is the immutable Complete() result distinguishing
// final idle release from atomic pending-follow-up claim.
type attemptFinishOutcomeContract struct {
	Kind             attemptFinishKindContract
	FollowUpLease    attemptLeaseContract // non-nil only for finishFollowUpClaimedContract
	CoalescedSignals int64                // count claimed with the follow-up (gate resets after claim)
}

// attemptFinishKindContract classifies the atomic completion transition.
type attemptFinishKindContract int

const (
	// finishReleasedIdleContract: final active lease released; idle waiters wake.
	finishReleasedIdleContract attemptFinishKindContract = iota
	// finishFollowUpClaimedContract: claimed exactly one pending HUP, cleared/reset
	// its coalesced count, returned FollowUpLease; gate stays non-idle with no
	// admission window between release and follow-up arm.
	finishFollowUpClaimedContract
	// finishAlreadyCompletedContract: concurrent/sequential duplicate Complete —
	// panic-free no-op; advances/finishes at most once.
	finishAlreadyCompletedContract
)

// attemptGateSemanticEvidenceKind labels how a semantic row is locked in Task 6.1.
type attemptGateSemanticEvidenceKind string

const (
	evidenceCharacterization attemptGateSemanticEvidenceKind = "characterization"
	evidenceASTRED           attemptGateSemanticEvidenceKind = "ast_red"
	evidenceTask62Contract   attemptGateSemanticEvidenceKind = "task62_contract"
)

// attemptGateContractSemantics documents exact Task 6.2 behavior. Evidence must
// be characterization, AST RED, or an explicit future Task 6.2 contract case —
// prose alone is not coverage.
var attemptGateContractSemantics = []struct {
	Name      string
	Rule      string
	Evidence  []attemptGateSemanticEvidenceKind
	CoveredBy string
}{
	{
		Name: "atomic_arm",
		Rule: "TryStart(admissionAdmittedContract) makes attempt registration and completion lease visible in one lock transition; busy is never observable without an armed completion signal.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceASTRED,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_ArchitectureREDInventory;TestAttemptGate_BusyBeforeArmedREDWindow",
	},
	{
		Name: "api_busy_no_queue",
		Rule: "API trigger while active returns admissionBusyAPIContract with canonical ResultBusy/StageBusy metadata, CoalescedSignals=0, no live lease, and does not set pending follow-up.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_BusyAPIConflictNoPending",
	},
	{
		Name: "sighup_pending_once",
		Rule: "First SIGHUP while active returns admissionPendingHUPContract with CoalescedSignals=0 on the outcome, sets at most one pending follow-up, and issues no live lease / second attempt.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_CoalesceBoundedPending",
	},
	{
		Name: "sighup_coalesce_count",
		Rule: "Additional SIGHUPs while pending already set return admissionCoalescedHUPContract carrying the incremented CoalescedSignals on the immutable outcome; no further queued work.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_CoalesceBoundedPending",
	},
	{
		Name: "finish_claim_pending_followup",
		Rule: "Complete() either (a) finishReleasedIdleContract: releases the final lease and wakes idle waiters, or (b) finishFollowUpClaimedContract: atomically claims exactly one pending HUP, clears/resets coalesced, returns FollowUpLease+CoalescedSignals, and keeps the gate non-idle with no admission window.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceASTRED,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_ArchitectureREDInventory;attemptFinishOutcomeContract",
	},
	{
		Name: "shutdown_atomic",
		Rule: "BeginShutdown clears pending follow-up, rejects future starts with admissionRejectedShutdownContract (no live lease), cancels the active lease context, and allows final Complete/abandon to wake WaitForIdle waiters.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_ShutdownRejectsAndCancels;TestAttemptGate_ShutdownCancelsIdleWaiters",
	},
	{
		Name: "finish_exactly_once",
		Rule: "Complete is panic-free under concurrent duplicate calls, advances/finishes at most once (finishAlreadyCompletedContract on duplicates), and wakes every WaitForIdle waiter on the releasing transition.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceASTRED,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_ExactFinishSequentialDuplicate;TestAttemptGate_ArchitectureREDInventory",
	},
	{
		Name: "caller_cancel_detach",
		Rule: "After admission, trigger-caller cancel/deadline must not cancel or leak the host-owned active attempt/follow-up slot (context values may remain). Gate/host shutdown cancels the active lease. WaitForIdle ctx cancel/deadline cancels only that wait. Non-admitted outcomes return no live lease.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceASTRED,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_CallerCancelDetachmentEvidence;TestCoordinator_HostTimeoutIndependentOfClientCancel",
	},
	{
		Name: "idle_wait_cases",
		Rule: "WaitForIdle covers already-idle, active→finish (blocked waiters awakened), concurrent waiters, canceled context, already-expired deadline (DeadlineExceeded), and shutdown+finish wake without polling.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceASTRED,
			evidenceTask62Contract,
		},
		CoveredBy: "TestAttemptGate_IdleAlreadyIdle;TestAttemptGate_IdleAfterActiveFinish;TestAttemptGate_IdleWaitersWake;TestAttemptGate_IdleCanceledContext;TestAttemptGate_IdleDeadlineExceeded;TestAttemptGate_ShutdownCancelsIdleWaiters;TestAttemptGate_NoPollPolicyInConcurrencySuite",
	},
	{
		Name: "interleave_one_complete_vs_wait_shutdown",
		Rule: "Race coverage for Task 6.1 exercises one completion racing with WaitForIdle and BeginShutdown (plus busy/coalesce hammer while still busy). Concurrent duplicate Complete remains source-level AST RED until Task 6.2.",
		Evidence: []attemptGateSemanticEvidenceKind{
			evidenceCharacterization,
			evidenceASTRED,
		},
		CoveredBy: "TestAttemptGate_InterleavingsOneCompleteVsWaitShutdown;TestAttemptGate_ArchitectureREDInventory",
	},
}
