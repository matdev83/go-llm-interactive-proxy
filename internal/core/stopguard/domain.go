package stopguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Cause is a provider-neutral classification of why a backend attempt reached a
// possible terminal boundary. Values derive from already-normalized canonical
// facts, never from provider-name checks.
type Cause string

const (
	CauseNormalEnd              Cause = "normal_end"
	CauseEmptyNormalEnd         Cause = "empty_normal_end"
	CauseProviderPause          Cause = "provider_pause"
	CauseOutputLimit            Cause = "output_limit"
	CauseTransportEOFPreCommit  Cause = "transport_eof_precommit"
	CauseTransportEOFPostCommit Cause = "transport_eof_postcommit"
	CauseIdlePreCommit          Cause = "idle_precommit"
	CauseIdlePostCommit         Cause = "idle_postcommit"
	CausePartialToolCall        Cause = "partial_tool_call"
	CauseRefusalOrFilter        Cause = "refusal_or_filter"
	CauseClientCancel           Cause = "client_cancel"
	CauseUnknownTerminal        Cause = "unknown_terminal"
)

// VerdictKind enumerates the conservative verifier outcome space. Only
// VerdictContinue is continuation-authorizing.
type VerdictKind string

const (
	VerdictAllowStop VerdictKind = "allow_stop"
	VerdictContinue  VerdictKind = "continue"
	VerdictNeedsUser VerdictKind = "needs_user"
	VerdictBlocked   VerdictKind = "blocked"
	VerdictUncertain VerdictKind = "uncertain"
)

// Bounded field sizes for verifier-derived text propagated into recovery.
const (
	MaxReasonBytes             = 512
	MaxRemainingObjectiveBytes = 512
)

// Verdict is the structured result of a semantic completion check.
type Verdict struct {
	Kind               VerdictKind
	Reason             string
	RemainingObjective string
}

// ContinuationRef opaquely identifies the retained continuation lineage used to
// build evidence. The runtime fills it from existing continuation records;
// stopguard never dereferences it.
type ContinuationRef struct {
	ContinuationID string
}

// ToolCompletionState summarizes canonical tool/action progress relevant to
// safe continuation decisions.
type ToolCompletionState struct {
	CompletedToolResults      int
	PendingToolCallID         string
	HasIncompleteArguments    bool
	HasUnsupportedOpaqueState bool
}

// Evidence is the bounded projection of canonical state handed to a verifier.
// It reuses canonical message/item types rather than introducing a second
// transcript representation.
type Evidence struct {
	Cause               Cause
	UserObjective       []lipapi.Message
	RecentTrajectory    []lipapi.Item
	CandidateAssistant  []lipapi.Item
	ToolState           ToolCompletionState
	OutputCommitted     bool
	ExplicitCompletion  bool
	ContinuationLineage ContinuationRef
	RecoveryAttempt     int
}

// Verifier is the consumer-owned semantic completion boundary.
type Verifier interface {
	Verify(ctx context.Context, evidence Evidence) (Verdict, error)
}

// VerifierFunc adapts a function to the Verifier contract.
type VerifierFunc func(ctx context.Context, evidence Evidence) (Verdict, error)

// Verify implements Verifier.
func (f VerifierFunc) Verify(ctx context.Context, evidence Evidence) (Verdict, error) {
	return f(ctx, evidence)
}

// Action is the bounded decision vocabulary the guard hands back to runtime
// orchestration. It deliberately excludes any second retry-controller concept.
type Action string

const (
	ActionForwardTerminal           Action = "forward_terminal"
	ActionDelegatePreOutputRecovery Action = "delegate_preoutput_recovery"
	ActionContinueLeg               Action = "continue_leg"
	ActionSurfaceFailure            Action = "surface_failure"
)

// ExplicitCompletionPolicy mirrors the configured treatment of a normalized
// frontend explicit completion fact. It is defined here so core policy stays
// independent of the config package.
type ExplicitCompletionPolicy string

const (
	PolicyTrust  ExplicitCompletionPolicy = "trust"
	PolicyVerify ExplicitCompletionPolicy = "verify"
)

// Candidate is the runtime-provided classification of one terminal candidate
// plus the safety facts proven outside this package (stream recovery, native
// resume capability, canonical continuation safety).
type Candidate struct {
	Cause                     Cause
	OutputCommitted           bool
	ExplicitCompletion        bool
	EmptyRetryEligible        bool
	SafeNativeResume          bool
	SafeCanonicalContinuation bool
}

// Decision communicates either an immediate action or, when Verify is true,
// a request for the runtime to run its verifier and call DecideWithVerdict.
// When Verify is true, Action is unspecified and must be ignored.
type Decision struct {
	Action Action
	Verify bool
}
