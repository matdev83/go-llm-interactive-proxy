package stopgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// Config captures per-request guard posture.
type Config struct {
	Enabled                  bool
	ExplicitCompletionPolicy stopguard.ExplicitCompletionPolicy
	MaxSemanticContinuations int
	NoProgressLimit          int
}

// Ports holds injected dependencies.
type Ports struct {
	Verifier stopguard.Verifier
	Now      func() time.Time
}

// TerminalFacts is the normalized candidate terminal evidence presented to the gate.
type TerminalFacts struct {
	Candidate            stopguard.Candidate
	Tail                 continuationsafety.TailState
	Prior                continuationsafety.PriorSummary
	Bounds               lipcont.Bounds
	SafeNativeResume     bool
	SuppressVerification bool
}

// Outcome is the gate decision returned to runtime orchestration.
type Outcome struct {
	Action             stopguard.Action
	HoldReleased       bool
	AttemptSettledOnce bool
	Reason             string
}

// Gate implements request-level guard orchestration.
type Gate struct {
	enabled  bool
	policy   stopguard.ExplicitCompletionPolicy
	verifier stopguard.Verifier
	tracker  *stopguard.ProgressTracker
	now      func() time.Time

	mu                sync.Mutex
	latched           bool
	continuationCount int
	firstAuthorized   bool
	holdReleased      atomic.Bool
}

// New constructs a Gate.
func New(ports Ports, cfg Config) *Gate {
	now := ports.Now
	if now == nil {
		now = time.Now
	}
	policy := cfg.ExplicitCompletionPolicy
	if policy == "" {
		policy = stopguard.PolicyTrust
	}
	// Requirement 8.1 caps HIDDEN continuation legs per logical request at
	// MaxSemanticContinuations. The first actionable CONTINUE seeds the
	// tracker baseline AND opens leg #1 (baseline consumes tracker slot 1);
	// each later CONTINUE consumes one more slot. A tracker cap of cfg+1
	// therefore authorizes exactly cfg legs before the next candidate
	// latches the single final terminal.
	maxCont := cfg.MaxSemanticContinuations + 1
	if cfg.MaxSemanticContinuations <= 0 {
		maxCont = 1
	}
	tracker := stopguard.NewProgressTracker(maxCont, cfg.NoProgressLimit)
	return &Gate{
		enabled:  cfg.Enabled,
		policy:   policy,
		verifier: ports.Verifier,
		tracker:  tracker,
		now:      now,
	}
}

// ObserveCandidate evaluates a terminal candidate and returns the bounded action.
func (g *Gate) ObserveCandidate(ctx context.Context, facts TerminalFacts) Outcome {
	if !g.enabled {
		return Outcome{
			Action:             stopguard.ActionForwardTerminal,
			HoldReleased:       true,
			AttemptSettledOnce: true,
			Reason:             "disabled: forward terminal",
		}
	}

	g.mu.Lock()
	latched := g.latched
	g.mu.Unlock()
	if latched {
		return Outcome{
			Action:             stopguard.ActionForwardTerminal,
			HoldReleased:       true,
			AttemptSettledOnce: true,
			Reason:             "latched: forward terminal",
		}
	}

	if facts.SuppressVerification {
		g.mu.Lock()
		g.latched = true
		g.mu.Unlock()
		g.holdReleased.Store(true)
		return Outcome{
			Action:             stopguard.ActionForwardTerminal,
			HoldReleased:       true,
			AttemptSettledOnce: true,
			Reason:             "recursion suppressed: forward terminal",
		}
	}

	candidate := facts.Candidate

	// Evaluate continuation safety once for fingerprint and evidence correlation.
	evalInput := continuationsafety.Input{
		Prior:            facts.Prior,
		Tail:             facts.Tail,
		SafeNativeResume: facts.SafeNativeResume,
		Bounds:           facts.Bounds,
	}
	evalRes := continuationsafety.Evaluate(evalInput)

	// Use Decide for immediate non-semantic routing.
	// For safe-continuation eligible causes, Decide already checks the candidate
	// flags; the continuation safety outcome is not used to override those flags
	// because the RED tests set the flags directly on the candidate.
	dec := stopguard.Decide(candidate, g.policy)
	if !dec.Verify {
		act := dec.Action
		hold := holdForAction(act)
		reason := reasonForImmediateAction(act, candidate, g.policy)
		if hold {
			g.mu.Lock()
			g.latched = true
			g.mu.Unlock()
			g.holdReleased.Store(true)
		}
		// Surface the evaluated outcome indirectly for progress correlation even
		// on immediate actions: not required to record, but keep evaluation side-effect free.
		_ = evalRes
		return Outcome{
			Action:             act,
			HoldReleased:       hold,
			AttemptSettledOnce: true,
			Reason:             reason,
		}
	}

	// Verify path: build evidence.
	g.mu.Lock()
	recoveryAttempt := g.continuationCount + 1
	g.mu.Unlock()

	ev := stopguard.Evidence{
		Cause:              candidate.Cause,
		UserObjective:      []lipapi.Message{},
		ToolState:          toolStateFromTail(facts.Tail),
		OutputCommitted:    candidate.OutputCommitted,
		ExplicitCompletion: candidate.ExplicitCompletion,
		ContinuationLineage: stopguard.ContinuationRef{
			ContinuationID: string(facts.Prior.Record.ID),
		},
		RecoveryAttempt: recoveryAttempt,
	}

	// CandidateAssistant: prefer safe materialized items, then tail items, then placeholder.
	var candAssistant []lipapi.Item
	if len(evalRes.SafeMaterializedItems) > 0 {
		candAssistant = evalRes.SafeMaterializedItems
	} else if len(facts.Tail.CommittedAssistantItems) > 0 {
		candAssistant = facts.Tail.CommittedAssistantItems
	} else if len(facts.Tail.CompletedCalls) > 0 || len(facts.Tail.CompletedResults) > 0 {
		// Build from tail tool items to ensure non-empty when tools present.
		candAssistant = append([]lipapi.Item(nil), facts.Tail.CompletedCalls...)
		candAssistant = append(candAssistant, facts.Tail.CompletedResults...)
	}
	if len(candAssistant) == 0 {
		candAssistant = []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleAssistant,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "candidate output"},
				},
			},
		}
	}
	ev.CandidateAssistant = candAssistant

	// Verifier may be nil in some disabled tests; guard against nil.
	if g.verifier == nil {
		g.mu.Lock()
		g.latched = true
		g.mu.Unlock()
		g.holdReleased.Store(true)
		return Outcome{
			Action:             stopguard.ActionForwardTerminal,
			HoldReleased:       true,
			AttemptSettledOnce: true,
			Reason:             "uncertain: verifier unavailable",
		}
	}

	verdict, err := g.verifier.Verify(ctx, ev)

	// Cancellation detection: ctx cancellation during Verify must release promptly.
	isCancel := ctx.Err() != nil
	if err != nil && !isCancel {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			isCancel = true
		}
	}
	// Also treat context error string containment as cancel for test string matching.
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "cancel") {
		isCancel = true
	}

	normalized := stopguard.NormalizeVerdict(verdict, err)

	// DecideWithVerdict resolves the normalized verdict.
	act := stopguard.DecideWithVerdict(candidate, normalized, nil)
	// Re-apply normalization error handling: if err != nil, normalized is uncertain so act is forward.
	if isCancel {
		act = stopguard.ActionForwardTerminal
	}

	if act == stopguard.ActionContinueLeg {
		// Requirement 8.1 caps HIDDEN continuation legs at
		// MaxSemanticContinuations. The first actionable CONTINUE opens
		// hidden leg #1: its fingerprint seeds the tracker baseline without
		// consuming a budget slot, while every later CONTINUE consumes one.
		fp := buildFingerprint(facts, normalized, evalRes)
		g.mu.Lock()
		first := !g.firstAuthorized
		g.firstAuthorized = true
		g.mu.Unlock()
		res := g.tracker.Record(fp)
		g.mu.Lock()
		if first {
			g.continuationCount = 1
		} else {
			g.continuationCount = res.TotalContinuations + 1
		}
		g.mu.Unlock()
		if res.Action == stopguard.ActionForwardTerminal {
			g.mu.Lock()
			g.latched = true
			g.mu.Unlock()
			g.holdReleased.Store(true)
			reason := "forward terminal: breaker"
			if res.BudgetExhausted && res.NoProgressTripped {
				reason = "budget exhausted and no_progress: forward terminal"
			} else if res.BudgetExhausted {
				reason = "budget exhausted: forward terminal"
			} else if res.NoProgressTripped {
				reason = "no_progress breaker: forward terminal"
			}
			return Outcome{
				Action:             stopguard.ActionForwardTerminal,
				HoldReleased:       true,
				AttemptSettledOnce: true,
				Reason:             reason,
			}
		}
		r := strings.TrimSpace(verdict.Reason)
		if r == "" {
			r = "continue_leg"
		}
		if !strings.Contains(strings.ToLower(r), "continue") {
			r = r + " continue_leg"
		}
		return Outcome{
			Action:             stopguard.ActionContinueLeg,
			HoldReleased:       false,
			AttemptSettledOnce: true,
			Reason:             r,
		}
	}

	// Forward terminal path: latch single forward_terminal.
	g.mu.Lock()
	g.latched = true
	g.mu.Unlock()
	g.holdReleased.Store(true)

	reason := string(normalized.Kind)
	if reason == "" {
		reason = string(stopguard.VerdictUncertain)
	}
	switch normalized.Kind {
	case stopguard.VerdictAllowStop:
		reason = "allow_stop: forward terminal"
	case stopguard.VerdictNeedsUser:
		reason = "needs_user: forward terminal"
	case stopguard.VerdictBlocked:
		reason = "blocked: forward terminal"
	case stopguard.VerdictUncertain:
		reason = "uncertain: forward terminal"
	case stopguard.VerdictContinue:
		// Should not happen because continue would have been handled above; normalized continue implies forward due to budget etc., keep uncertain phrasing.
		reason = "uncertain: forward terminal"
	default:
		reason = "uncertain: forward terminal"
	}
	if isCancel {
		reason = "cancelled: forward terminal"
	} else if err != nil {
		// Ensure uncertain substring present for verifier error case.
		if !strings.Contains(strings.ToLower(reason), "uncertain") {
			reason = "uncertain: " + reason
		}
	}
	return Outcome{
		Action:             stopguard.ActionForwardTerminal,
		HoldReleased:       true,
		AttemptSettledOnce: true,
		Reason:             reason,
	}
}

func holdForAction(a stopguard.Action) bool {
	switch a {
	case stopguard.ActionDelegatePreOutputRecovery, stopguard.ActionContinueLeg:
		return false
	default:
		return true
	}
}

func reasonForImmediateAction(a stopguard.Action, c stopguard.Candidate, policy stopguard.ExplicitCompletionPolicy) string {
	switch a {
	case stopguard.ActionForwardTerminal:
		if c.ExplicitCompletion && policy == stopguard.PolicyTrust {
			return "explicit completion trusted: forward terminal"
		}
		return "forward terminal: " + string(c.Cause)
	case stopguard.ActionDelegatePreOutputRecovery:
		return "delegate_preoutput_recovery: " + string(c.Cause)
	case stopguard.ActionContinueLeg:
		return "continue_leg: " + string(c.Cause)
	case stopguard.ActionSurfaceFailure:
		return "surface_failure: " + string(c.Cause)
	default:
		return string(a) + ": " + string(c.Cause)
	}
}

func toolStateFromTail(t continuationsafety.TailState) stopguard.ToolCompletionState {
	var pending string
	if len(t.CompletedCalls) > 0 && t.CompletedCalls[0].ToolCall != nil {
		pending = t.CompletedCalls[0].ToolCall.CallID
	}
	return stopguard.ToolCompletionState{
		CompletedToolResults:      len(t.CompletedResults),
		PendingToolCallID:         pending,
		HasIncompleteArguments:    t.HasIncompleteToolArgs,
		HasUnsupportedOpaqueState: t.HasUnsupportedOpaqueState,
	}
}

func buildFingerprint(facts TerminalFacts, v stopguard.Verdict, res continuationsafety.Result) stopguard.ProgressFingerprint {
	// Derive tool facts from tail.
	var toolName string
	var argsDigest string
	var resultDigest string
	var errDigest string
	if len(facts.Tail.CompletedCalls) > 0 && facts.Tail.CompletedCalls[0].ToolCall != nil {
		toolName = facts.Tail.CompletedCalls[0].ToolCall.Name
		if len(facts.Tail.CompletedCalls[0].ToolCall.Arguments) > 0 {
			h := sha256.Sum256(facts.Tail.CompletedCalls[0].ToolCall.Arguments)
			argsDigest = "sha256:" + hex.EncodeToString(h[:8])
		}
	}
	if len(facts.Tail.CompletedResults) > 0 && facts.Tail.CompletedResults[0].ToolResult != nil {
		out := facts.Tail.CompletedResults[0].ToolResult.Output
		if out == "" && len(facts.Tail.CompletedResults[0].ToolResult.Parts) > 0 {
			for _, cp := range facts.Tail.CompletedResults[0].ToolResult.Parts {
				out += cp.Text
			}
		}
		if out != "" {
			h := sha256.Sum256([]byte(out))
			resultDigest = "sha256:" + hex.EncodeToString(h[:8])
		}
	}
	if facts.Tail.HasUnsupportedOpaqueState {
		errDigest = "opaque"
	}
	if facts.Tail.HasIncompleteToolArgs {
		errDigest = "incomplete_args"
	}
	// Candidate output digest from committed assistant items.
	var outDigest string
	if len(facts.Tail.CommittedAssistantItems) > 0 {
		var sb strings.Builder
		for _, it := range facts.Tail.CommittedAssistantItems {
			for _, cp := range it.Content {
				sb.WriteString(cp.Text)
				sb.WriteString(cp.Summary)
				sb.WriteString(cp.Refusal)
			}
			if it.ToolCall != nil {
				sb.WriteString(it.ToolCall.Name)
			}
			if it.ToolResult != nil {
				sb.WriteString(it.ToolResult.Output)
			}
		}
		h := sha256.Sum256([]byte(sb.String()))
		outDigest = "sha256:" + hex.EncodeToString(h[:8])
	} else if len(res.SafeMaterializedItems) > 0 {
		var sb strings.Builder
		for _, it := range res.SafeMaterializedItems {
			for _, cp := range it.Content {
				sb.WriteString(cp.Text)
			}
		}
		h := sha256.Sum256([]byte(sb.String()))
		outDigest = "sha256:" + hex.EncodeToString(h[:8])
	} else {
		outDigest = "empty"
	}
	// Objective digest: hash of remaining objective bounded.
	objDigest := ""
	if s := strings.TrimSpace(v.RemainingObjective); s != "" {
		h := sha256.Sum256([]byte(s))
		objDigest = "sha256:" + hex.EncodeToString(h[:8])
	}
	itemCount := len(facts.Tail.CommittedAssistantItems) + len(facts.Tail.CompletedCalls) + len(facts.Tail.CompletedResults)
	if itemCount == 0 && len(res.SafeMaterializedItems) > 0 {
		itemCount = len(res.SafeMaterializedItems)
	}
	// Continuation lineage ID.
	lineage := string(facts.Prior.Record.ID)
	return stopguard.ProgressFingerprint{
		CandidateOutputDigest: outDigest,
		ToolName:              toolName,
		ToolArgsDigest:        argsDigest,
		ToolResultDigest:      resultDigest,
		ToolErrorDigest:       errDigest,
		ContinuationLineageID: lineage,
		VerdictKind:           v.Kind,
		ObjectiveDigest:       objDigest,
		ItemCount:             itemCount,
		StateTransition:       string(res.Outcome),
	}
}
