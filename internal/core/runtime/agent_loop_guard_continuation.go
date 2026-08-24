package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/sdkadapter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func buildGuardTailState(p *responsePipeline, attempt *attemptSession) continuationsafety.TailState {
	tail := continuationsafety.TailState{}
	if attempt != nil && attempt.toolFinal != nil && attempt.toolFinal.active != nil && len(attempt.toolFinal.active) > 0 {
		tail.HasIncompleteToolArgs = true
	}
	// Derive committed tool pairs and opaque state from pipeline when available.
	if p != nil {
		events := p.seenEventsCopy()
		var textBuilder strings.Builder
		toolCalls := make(map[string]*lipapi.Item)
		var completedCalls []lipapi.Item
		var completedResults []lipapi.Item
		for _, ev := range events {
			switch ev.Kind {
			case lipapi.EventTextDelta:
				textBuilder.WriteString(ev.Delta)
			case lipapi.EventToolCallStarted:
				id := ev.ToolCallID
				if id == "" {
					continue
				}
				toolCalls[id] = &lipapi.Item{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: id, Name: ev.ToolName}}
			case lipapi.EventToolCallArgsDelta:
				// Incomplete args already handled via active map; no extra state needed.
			case lipapi.EventToolCallFinished:
				id := ev.ToolCallID
				if it, ok := toolCalls[id]; ok {
					it.Status = lipapi.ItemStatusCompleted
					completedCalls = append(completedCalls, *it)
					delete(toolCalls, id)
				} else {
					completedCalls = append(completedCalls, lipapi.Item{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: id}})
				}
			case lipapi.EventItem:
				if ev.Item != nil {
					switch ev.Item.Kind {
					case lipapi.ItemKindToolCall:
						if ev.Item.ToolCall != nil && ev.Item.ToolCall.CallID != "" {
							// Ordered-items tool call: preserve as completed if status indicates completed.
							it := *ev.Item
							if it.Status == "" {
								it.Status = lipapi.ItemStatusCompleted
							}
							if it.Status == lipapi.ItemStatusCompleted {
								completedCalls = append(completedCalls, it)
							} else {
								toolCalls[it.ToolCall.CallID] = &it
							}
						}
					case lipapi.ItemKindToolResult:
						if ev.Item.ToolResult != nil {
							it := *ev.Item
							if it.Status == "" {
								it.Status = lipapi.ItemStatusCompleted
							}
							completedResults = append(completedResults, it)
						}
					case lipapi.ItemKindMessage:
						for _, cp := range ev.Item.Content {
							textBuilder.WriteString(cp.Text)
						}
					}
				}
			case lipapi.EventReasoningOpaqueDelta, lipapi.EventReasoningPart:
				// Opaque thinking that cannot be resumed without native support.
				tail.HasUnsupportedOpaqueState = true
			}
		}
		if text := strings.TrimSpace(textBuilder.String()); text != "" {
			tail.CommittedAssistantItems = []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}}}}
		}
		if len(completedCalls) > 0 {
			tail.CompletedCalls = completedCalls
		}
		if len(completedResults) > 0 {
			tail.CompletedResults = completedResults
		}
		// Remaining active tool calls without finished are incomplete.
		if len(toolCalls) > 0 {
			tail.HasIncompleteToolArgs = true
		}
	}
	// Also reflect any remaining active assembler buffers as incomplete.
	if attempt != nil && attempt.toolFinal != nil && attempt.toolFinal.active != nil && len(attempt.toolFinal.active) > 0 {
		tail.HasIncompleteToolArgs = true
	}
	return tail
}

// hasExplicitCompletionFromTail reports whether tail contains a correlated
// completed explicit completion signal using the authoritative canonical
// lipapi.HasExplicitCompletion contract. Requires a completed ToolCall with a
// known explicit name AND a matching completed ToolResult with same CallID.
func hasExplicitCompletionFromTail(tail continuationsafety.TailState) bool {
	items := make([]lipapi.Item, 0, len(tail.CompletedCalls)+len(tail.CompletedResults))
	items = append(items, tail.CompletedCalls...)
	items = append(items, tail.CompletedResults...)
	return lipapi.HasExplicitCompletion(items)
}

func buildGuardPrior(s *retryRecvStream) continuationsafety.PriorSummary {
	if s == nil || s.terminal == nil {
		return continuationsafety.PriorSummary{}
	}
	if s.terminal.guardPriorOK {
		return continuationsafety.PriorSummary{Record: s.terminal.guardPriorRecord}
	}
	baseline := s.facts.baseline
	tail := buildGuardTailState(s.responsePipeline, s.attempt.snapshot())
	var outputItems []lipapi.Item
	if len(tail.CommittedAssistantItems) > 0 {
		outputItems = lipcont.CloneItems(tail.CommittedAssistantItems)
	} else if len(tail.CompletedCalls) > 0 || len(tail.CompletedResults) > 0 {
		outputItems = append(lipcont.CloneItems(tail.CompletedCalls), lipcont.CloneItems(tail.CompletedResults)...)
	}
	idStr := "guard-init-" + s.facts.traceID
	if s.facts.traceID == "" {
		idStr = "guard-init-prior"
	}
	rec := lipcont.ContinuationRecord{
		ID:                lipcont.ResponseID(idStr),
		PreviousID:        "",
		InputItems:        lipcont.CloneItems(baseline.Items),
		OutputItems:       outputItems,
		ChainDepth:        0,
		MaterializedBytes: lipcont.EstimateItemsBytes(baseline.Items) + lipcont.EstimateItemsBytes(outputItems),
		Status:            lipcont.RecordStatusCompleted,
	}
	s.terminal.guardPriorRecord = rec
	s.terminal.guardPriorOK = true
	return continuationsafety.PriorSummary{Record: rec}
}

func honestPriorForEvaluate(t *turnTerminal, facts requestTerminalFacts, p *responsePipeline, attempt *attemptSession) continuationsafety.PriorSummary {
	if t != nil && t.guardPriorOK {
		return continuationsafety.PriorSummary{Record: t.guardPriorRecord}
	}
	tail := buildGuardTailState(p, attempt)
	var outputItems []lipapi.Item
	if len(tail.CommittedAssistantItems) > 0 {
		outputItems = lipcont.CloneItems(tail.CommittedAssistantItems)
	} else if len(tail.CompletedCalls) > 0 || len(tail.CompletedResults) > 0 {
		outputItems = append(lipcont.CloneItems(tail.CompletedCalls), lipcont.CloneItems(tail.CompletedResults)...)
	}
	traceID := facts.traceID
	if traceID == "" && attempt != nil {
		traceID = attempt.traceID
	}
	idStr := "guard-init-" + traceID
	if traceID == "" {
		idStr = "guard-init-prior"
	}
	rec := lipcont.ContinuationRecord{
		ID:                lipcont.ResponseID(idStr),
		PreviousID:        "",
		InputItems:        lipcont.CloneItems(facts.call.Items),
		OutputItems:       outputItems,
		ChainDepth:        0,
		MaterializedBytes: lipcont.EstimateItemsBytes(facts.call.Items) + lipcont.EstimateItemsBytes(outputItems),
		Status:            lipcont.RecordStatusCompleted,
	}
	// Preserve available lineage/requirements from call if exposed; otherwise leave zero.
	if t != nil {
		t.guardPriorRecord = rec
		t.guardPriorOK = true
	}
	return continuationsafety.PriorSummary{Record: rec}
}

func (t *turnTerminal) agentLoopGuardEvaluate(ctx context.Context, facts requestTerminalFacts, attempt *attemptSession, p *responsePipeline, ev lipapi.Event) stopgate.Outcome {
	if !t.isLoopGuardEnabled() {
		return stopgate.Outcome{Action: stopguard.ActionForwardTerminal, HoldReleased: true, Reason: "disabled"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	suppress := execctx.IsSuppressedPluginID(ctx, "agent_loop_guard")
	tail := buildGuardTailState(p, attempt)
	prior := honestPriorForEvaluate(t, facts, p, attempt)
	bounds := lipcont.DefaultBounds()
	candidateCause := stopguard.CauseNormalEnd
	if !t.committed() {
		candidateCause = stopguard.CauseEmptyNormalEnd
	}
	candidate := stopguard.Candidate{
		Cause:              candidateCause,
		OutputCommitted:    t.committed(),
		ExplicitCompletion: hasExplicitCompletionFromTail(tail),
	}
	tf := stopgate.TerminalFacts{
		Candidate:            candidate,
		Tail:                 tail,
		Prior:                prior,
		Bounds:               bounds,
		SafeNativeResume:     false,
		SuppressVerification: suppress,
		SupportsContinuation: t.supportsContinuation,
	}
	outcome := t.loopGuard.gate.ObserveCandidate(ctx, tf)
	t.emitGuardTelemetry(ctx, outcome)
	return outcome
}

func (t *turnTerminal) postOutputGuardOutcome(ctx context.Context, facts requestTerminalFacts, attempt *attemptSession, p *responsePipeline, cause stopguard.Cause, reason string) stopgate.Outcome {
	if !t.isLoopGuardEnabled() {
		return stopgate.Outcome{Action: stopguard.ActionForwardTerminal, HoldReleased: true, Reason: "disabled"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	suppress := execctx.IsSuppressedPluginID(ctx, "agent_loop_guard")
	tail := buildGuardTailState(p, attempt)
	prior := honestPriorForEvaluate(t, facts, p, attempt)
	bounds := lipcont.DefaultBounds()
	safetyInput := continuationsafety.Input{Prior: prior, Tail: tail, Bounds: bounds}
	safetyRes := continuationsafety.Evaluate(safetyInput)
	safe := safetyRes.Outcome == continuationsafety.OutcomeContinueSafe
	candidate := stopguard.Candidate{
		Cause:                     cause,
		OutputCommitted:           true,
		SafeCanonicalContinuation: safe,
		ExplicitCompletion:        hasExplicitCompletionFromTail(tail),
	}
	tf := stopgate.TerminalFacts{
		Candidate:            candidate,
		Tail:                 tail,
		Prior:                prior,
		Bounds:               bounds,
		SafeNativeResume:     false,
		SuppressVerification: suppress,
		SupportsContinuation: t.supportsContinuation,
	}
	outcome := t.loopGuard.gate.ObserveCandidate(ctx, tf)
	// Preserve original transport reason for diagnostics while keeping outcome's reason.
	if reason != "" && !strings.Contains(outcome.Reason, reason) {
		outcome.Reason = boundGuardReason(reason + " " + outcome.Reason)
	}
	t.emitGuardTelemetry(ctx, outcome)
	return outcome
}

func (t *turnTerminal) tryPostOutputContinuation(ctx context.Context, s *retryRecvStream, attempt *attemptSession, cause stopguard.Cause, reason string) bool {
	if s == nil || attempt == nil {
		return false
	}
	if ctx.Err() != nil || t.finished() {
		return false
	}
	outcome := t.postOutputGuardOutcome(ctx, s.facts.terminalFacts(), attempt, s.responsePipeline, cause, reason)
	if outcome.Action != stopguard.ActionContinueLeg || outcome.HoldReleased {
		return false
	}
	return t.tryGuardContinuation(ctx, s, attempt, outcome)
}

const postOutputInterruptionReason = "post_output_interruption"

func (t *turnTerminal) settlePostOutputInterruptedBAttempt(ctx context.Context, attempt *attemptSession, cause stopguard.Cause, detail string) {
	if attempt == nil {
		return
	}
	reason := postOutputInterruptionReason + ":" + string(cause)
	if strings.TrimSpace(detail) != "" {
		reason = boundGuardReason(reason + " " + strings.TrimSpace(detail))
	}
	attempt.TerminalizeAttempt(ctx, IntentSwallowedFailure, attemptEvidence{
		Command:       sdkterminal.CommandSwallowedAttempt,
		LegOutcome:    billing.LegOutcomeSwallowed,
		ObsOutcome:    response.OutcomeReplaced,
		RecordOutcome: lipapi.AttemptSwallowedFailure,
		RecordReason:  reason,
		TraceID:       attempt.traceID,
		ALegID:        attempt.bleg.ALegID,
		StartedAt:     attempt.accounting.requestStartedAt,
	})
}

func (t *turnTerminal) tryGuardContinuation(ctx context.Context, s *retryRecvStream, attempt *attemptSession, outcome stopgate.Outcome) bool {
	if t == nil || s == nil || s.responsePipeline == nil {
		return false
	}
	if outcome.Action != stopguard.ActionContinueLeg || outcome.HoldReleased {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	tail := buildGuardTailState(s.responsePipeline, attempt)
	prior := buildGuardPrior(s)
	bounds := lipcont.DefaultBounds()
	safetyInput := continuationsafety.Input{
		Prior:            prior,
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           bounds,
	}
	safetyRes := continuationsafety.Evaluate(safetyInput)
	if safetyRes.Outcome != continuationsafety.OutcomeContinueSafe {
		return false
	}
	instr := continuationsafety.BuildRecoveryInstruction(continuationsafety.RecoveryInput{
		Reason:             outcome.Reason,
		RemainingObjective: outcome.RemainingObjective,
		Attempt:            outcome.Attempt,
		MaxAttempts:        outcome.MaxAttempts,
	})
	if !strings.Contains(instr, "<automated-recovery>") {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	default:
	}

	// 1. Settle B1 attempt exactly once as swallowed failure
	res := attempt.TerminalizeAttempt(ctx, IntentSwallowedFailure, attemptEvidence{
		Command:       sdkterminal.CommandSwallowedAttempt,
		LegOutcome:    billing.LegOutcomeSwallowed,
		ObsOutcome:    response.OutcomeReplaced,
		RecordOutcome: lipapi.AttemptSwallowedFailure,
		RecordReason:  guardContinuationPendingReason,
		TraceID:       attempt.traceID,
		ALegID:        attempt.bleg.ALegID,
		StartedAt:     attempt.accounting.requestStartedAt,
	})
	if !res.Result.Won {
		return false
	}
	if ctx.Err() != nil || t.finished() || (t.hasALeg() && t.aLegErr() != nil) {
		_ = t.deactivateGuardOverlay(ctx, s.facts.aLegID)
		return false
	}

	// Update continuation record tracking
	t.guardPriorRecord.PreviousID = prior.Record.ID
	t.guardPriorRecord.ChainDepth = safetyRes.Facts.ChainDepth + 1
	t.guardPriorRecord.MaterializedBytes = safetyRes.Facts.MaterializedBytes
	if len(safetyRes.SafeMaterializedItems) > 0 {
		t.guardPriorRecord.OutputItems = lipcont.CloneItems(safetyRes.SafeMaterializedItems)
	}

	if s.recovery == nil || s.recovery.opener == nil {
		_ = t.deactivateGuardOverlay(ctx, s.facts.aLegID)
		return false
	}

	// 2. Put overlay commit via canonical sdkadapter.Writer / pkg/lipsdk/steering
	aLegID := s.facts.aLegID
	steeringStore := t.steeringStore
	if steeringStore == nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	resolver := func(rctx context.Context) (lipapi.Call, conversationview.Snapshot, error) {
		snap := s.facts.conversationSnapshot
		if snap.StateRevision == 0 && t.conversationReader != nil {
			if s, err := t.conversationReader.Snapshot(rctx, aLegID); err == nil {
				snap = s
			}
		}
		ingress := s.facts.ingressCall
		if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
			ingress = s.facts.baseline
		}
		return ingress, snap, nil
	}

	writer, err := sdkadapter.NewWriterWithObserver(steeringStore, aLegID, resolver, t.conversationObserver)
	if err != nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	putReq := steering.PutRequest{
		OverlayID: "alg-rec",
		Message: steering.Message{
			Role: lipapi.RoleDeveloper,
			Text: instr,
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	}

	_, putErr := writer.Put(ctx, putReq)
	if putErr != nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	// 3. Read / Freeze Snapshot N+1
	reader := t.conversationReader
	if reader == nil {
		if r, ok := conversationview.AsReader(steeringStore); ok {
			reader = r
		}
	}
	if reader == nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}
	snapN1, err := reader.Snapshot(ctx, aLegID)
	if err != nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	// 4. Project accepted ingress/continuation trajectory under Snapshot N+1
	ingress := s.facts.ingressCall
	if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
		ingress = s.facts.baseline
	}
	projectedBaseline, projEv, err := conversationview.Project(ingress, snapN1)
	if err != nil {
		if obs := t.conversationObserver; obs != nil {
			conversationview.SafeObserver(obs).OnProjectionFailure(conversationview.StageEarly)
		}
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}
	filteredBaseline, err := conversationview.FilterNeverBackend(ingress, snapN1)
	if err != nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	// 5. Carry Snapshot N+1 / provenance / filtered baseline into continuation leg facts
	newFacts := s.facts.clone()
	newFacts.baseline = projectedBaseline
	newFacts.conversationSnapshot = snapN1
	if projEv != nil {
		newFacts.conversationProvenance = projEv.Provenance
	}
	newFacts.conversationFilteredBaseline = filteredBaseline
	newFacts.ingressCall = s.facts.ingressCall

	if ctx.Err() != nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	// 6. Open continuation leg with newFacts (isRetryPath = false for normal semantic admission)
	openRes, openErr := t.openGuardContinuationLeg(ctx, s, attempt, newFacts)
	if openErr != nil || !openRes.opened || openRes.ready == nil {
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	ready := openRes.ready
	if ready.state != readyStatePrepared {
		if err := ready.Prepare(ctx, newFacts, s.responsePipeline, t.committed()); err != nil {
			ready.Dispose(ctx, err)
			_ = t.deactivateGuardOverlay(ctx, aLegID)
			return false
		}
	}
	if ctx.Err() != nil || t.finished() || (t.hasALeg() && t.aLegErr() != nil) {
		ready.Dispose(ctx, context.Canceled)
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}
	_, published := s.attempt.swapIfOpen(ready)
	if !published {
		ready.Dispose(ctx, context.Canceled)
		_ = t.deactivateGuardOverlay(ctx, aLegID)
		return false
	}

	// Successfully published continuation leg: update s.facts conversationview state while preserving ingress baseline
	s.facts.conversationSnapshot = snapN1
	if projEv != nil {
		s.facts.conversationProvenance = projEv.Provenance
	}
	s.facts.conversationFilteredBaseline = filteredBaseline

	return true
}

func (t *turnTerminal) openGuardContinuationLeg(ctx context.Context, s *retryRecvStream, priorAttempt *attemptSession, facts recvTurnFacts) (replacementOpenResult, error) {
	if s == nil || s.recovery == nil || s.recovery.opener == nil {
		return replacementOpenResult{}, context.Canceled
	}
	req := replacementOpenRequest{
		facts:       facts.terminalFacts(),
		pinnedFacts: facts,
		recovery:    s.recovery.openSnapshot(),
		prior:       priorAttemptOutcome{attempt: priorAttempt, retired: true},
		isRetryPath: false,
		interleaved: s.recovery.interleaved,
	}
	return s.recovery.openGuardContinuation(ctx, req)
}
