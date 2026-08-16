package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

type BillingLegObserver interface {
	ObserveBillingLeg(context.Context, billing.LegUsageRecord)
}
type BillingLegObserverFunc func(context.Context, billing.LegUsageRecord)

func (f BillingLegObserverFunc) ObserveBillingLeg(ctx context.Context, record billing.LegUsageRecord) {
	if f != nil {
		f(ctx, record)
	}
}

type billingLegDraft struct {
	aLegID          string
	bLegID          string
	seq             int
	primary         routing.Primary
	startedAt       time.Time
	finishedAt      time.Time
	command         sdkterminal.Command
	surfaced        billing.SurfacedState
	finalize        lipapi.Event
	stream          lipapi.Event
	operatorRateRef billing.VersionRef
}

func billingLegRecord(draft billingLegDraft) billing.LegUsageRecord {
	backend := strings.TrimSpace(draft.primary.Backend)
	if backend == "" {
		backend = "unknown"
	}
	model := strings.TrimSpace(draft.primary.Model)
	if model == "" {
		model = "unknown"
	}
	bLegID := strings.TrimSpace(draft.bLegID)
	if bLegID == "" {
		bLegID = billingSyntheticBLegID(draft.seq)
	}
	return billing.LegUsageRecord{
		ALegID:          strings.TrimSpace(draft.aLegID),
		BLegID:          bLegID,
		Seq:             draft.seq,
		BackendID:       backend,
		ProviderID:      billingProviderID(draft.primary),
		ModelID:         model,
		OperatorRateRef: draft.operatorRateRef,
		StartedAt:       draft.startedAt,
		FinishedAt:      draft.finishedAt,
		Outcome:         legOutcomeFromCommand(draft.command),
		Surfaced:        draft.surfaced,
		Evidence:        mergeStreamCostOntoLUR(finalBillingEvidenceFromEvent(draft.finalize), finalBillingEvidenceFromEvent(draft.stream)),
	}
}

func (e *Executor) operatorRateRef(ctx context.Context, primary routing.Primary) billing.VersionRef {
	if e == nil || e.BillingIdentity.OperatorRateRef == nil {
		return billing.VersionRef{}
	}
	backend := strings.TrimSpace(primary.Backend)
	model := strings.TrimSpace(primary.Model)
	return e.BillingIdentity.OperatorRateRef(ctx, backend, model)
}

func (s *retryRecvStream) recordBillingLeg(ctx context.Context, command sdkterminal.Command) {
	if s == nil || s.executor == nil || !s.executor.billingTurns().enabled() {
		return
	}
	blegID := strings.TrimSpace(s.bleg.BLegID)
	if blegID == "" {
		blegID = billingSyntheticBLegID(s.bleg.Seq)
	}
	s.billingLegMu.Lock()
	if s.billingLegRecorded == nil {
		s.billingLegRecorded = make(map[string]struct{})
	}
	if _, seen := s.billingLegRecorded[blegID]; seen {
		s.billingLegMu.Unlock()
		return
	}
	s.billingLegRecorded[blegID] = struct{}{}
	s.billingLegMu.Unlock()
	s.executor.billingTurns().noteAllocatedBLeg(s.billingCallID, blegID)
	now := s.now()
	started := s.accounting.requestStartedAt
	if started.IsZero() {
		started = now
	}
	s.executor.billingTurns().noteLegTimes(s.billingCallID, started, now)
	surfaced := billing.SurfacedNo
	if command == sdkterminal.CommandNormalFinish || s.isCommitted() {
		surfaced = billing.SurfacedYes
	}
	streamEv := s.billingEvidenceFallback()
	legRecord := billingLegRecord(billingLegDraft{
		aLegID:          s.aLegID,
		bLegID:          s.bleg.BLegID,
		seq:             s.bleg.Seq,
		primary:         s.cand.Primary,
		startedAt:       started,
		finishedAt:      now,
		command:         command,
		surfaced:        surfaced,
		finalize:        s.finalizeBillingEvidence(ctx, "record_leg"),
		stream:          streamEv,
		operatorRateRef: s.executor.operatorRateRef(ctx, s.cand.Primary),
	})
	s.executor.billingTurns().observe(ctx, legRecord)
	s.executor.appendIndependentCallLeg(ctx, s.billingCallID, legRecord)
}

func (s *retryRecvStream) finalizeBillingEvidence(ctx context.Context, reason string) lipapi.Event {
	fallback := s.billingEvidenceFallback()
	if s == nil || s.executor == nil {
		return fallback
	}
	ev, ok := s.executor.billingTurns().finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(s.traceID),
		ALegID:  strings.TrimSpace(s.aLegID),
		BLegID:  strings.TrimSpace(s.bleg.BLegID),
		Backend: strings.TrimSpace(s.cand.Primary.Backend),
		Model:   strings.TrimSpace(s.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	})
	if !ok {
		return fallback
	}
	return ev
}

func (s *retryRecvStream) billingEvidenceFallback() lipapi.Event {
	if s == nil {
		return emptyOperatorUsageShell()
	}
	if s.lastAuthorityUsage.Kind != "" {
		return s.lastAuthorityUsage
	}
	return lastUsageDeltaOrShell(s.seenEventsCopy())
}

func lastUsageDeltaOrShell(events []lipapi.Event) lipapi.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == lipapi.EventUsageDelta {
			return events[i]
		}
	}
	return emptyOperatorUsageShell()
}

func mergeStreamCostOntoLUR(finalize, stream billing.FinalBillingEvidence) billing.FinalBillingEvidence {
	if finalize.Cost.Present {
		return finalize
	}
	if !stream.Cost.Present {
		return finalize
	}
	finalize.Cost = stream.Cost
	if stream.Authority != "" {
		finalize.Authority = stream.Authority
	}
	return finalize
}

func isBillingTurnTerminalCommand(command sdkterminal.Command) bool {
	switch command {
	case sdkterminal.CommandNormalFinish, sdkterminal.CommandEOF, sdkterminal.CommandCancel,
		sdkterminal.CommandClose, sdkterminal.CommandTimeout, sdkterminal.CommandPartialError,
		sdkterminal.CommandFrontendEncoderFailure:
		return true
	default:
		return false
	}
}

func isCallClosureTerminalCommand(command sdkterminal.Command) bool {
	return command.AllowsScope(sdkterminal.ScopeRequest)
}

func legOutcomeFromCommand(command sdkterminal.Command) billing.LegOutcome {
	switch command {
	case sdkterminal.CommandNormalFinish:
		return billing.LegOutcomeWinner
	case sdkterminal.CommandSwallowedAttempt:
		return billing.LegOutcomeSwallowed
	case sdkterminal.CommandParallelLoser:
		return billing.LegOutcomeLoser
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return billing.LegOutcomeCanceled
	default:
		return billing.LegOutcomeFailed
	}
}

func turnOutcomeFromCommand(command sdkterminal.Command) billing.TurnOutcome {
	switch command {
	case sdkterminal.CommandNormalFinish, sdkterminal.CommandEOF:
		return billing.TurnOutcomeCompleted
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return billing.TurnOutcomeCanceled
	default:
		return billing.TurnOutcomeFailed
	}
}

func (s *retryRecvStream) handoffBillingTurn(ctx context.Context, command sdkterminal.Command) {
	if s == nil || s.executor == nil || !isCallClosureTerminalCommand(command) {
		return
	}
	s.billingCallClosureMu.Lock()
	defer s.billingCallClosureMu.Unlock()
	s.appendCallClosureLocked(ctx, command)
}

func billingSyntheticBLegID(seq int) string {
	return fmt.Sprintf("seq_%d", seq)
}

func billingProviderID(primary routing.Primary) string {
	if provider := primary.TrimmedParam("provider"); provider != "" {
		return provider
	}
	backend := strings.TrimSpace(primary.Backend)
	if backend == "" {
		return "unknown"
	}
	return backend
}

func finalBillingEvidenceFromEvent(ev lipapi.Event) billing.FinalBillingEvidence {
	return billing.FinalBillingEvidence{
		InputTokens:      billing.Quantity{Value: int64(ev.InputTokens), Present: ev.UsagePresence.InputTokens},
		OutputTokens:     billing.Quantity{Value: int64(ev.OutputTokens), Present: ev.UsagePresence.OutputTokens},
		CacheReadTokens:  billing.Quantity{Value: int64(ev.CacheReadTokens), Present: ev.UsagePresence.CacheReadTokens},
		CacheWriteTokens: billing.Quantity{Value: int64(ev.CacheWriteTokens), Present: ev.UsagePresence.CacheWriteTokens},
		ReasoningTokens:  billing.Quantity{Value: int64(ev.ReasoningTokens), Present: ev.UsagePresence.ReasoningTokens},
		TotalTokens:      billing.Quantity{Value: int64(ev.TotalTokens), Present: ev.UsagePresence.TotalTokens},
		Cost:             billing.MoneyEvidence{NanoUnits: ev.CostNanoUnits, Currency: strings.TrimSpace(ev.Currency), Present: ev.CostPresent},
		Source:           billing.EvidenceSource(ev.Accounting.Source),
		Authority:        billing.EvidenceAuthority(ev.Accounting.Authority),
		DedupeKey:        strings.TrimSpace(ev.Accounting.DedupeKey),
	}
}

func (e *Executor) appendIndependentCallLeg(ctx context.Context, callID billing.BillingCallID, leg billing.LegUsageRecord) {
	if e == nil || e.CallLegUsageAppender == nil {
		return
	}
	independent := billing.CallLegUsageRecord{CallID: callID, ALegID: leg.ALegID, BLegID: leg.BLegID, BackendID: leg.BackendID, ProviderID: leg.ProviderID, ModelID: leg.ModelID, StartedAt: leg.StartedAt, FinishedAt: leg.FinishedAt, Outcome: leg.Outcome, Surfaced: leg.Surfaced, Evidence: leg.Evidence, OperatorRateRef: leg.OperatorRateRef}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	if err := e.CallLegUsageAppender.AppendCallLegUsage(persistCtx, independent); err != nil {
		e.logBillingUsageAppendFailure(persistCtx, "billing_call_leg_append_critical", "billing call-leg append failed", err)
	}
}

func (e *Executor) logBillingUsageAppendFailure(ctx context.Context, criticalMsg, warnMsg string, err error) {
	if e == nil || e.Log == nil || err == nil {
		return
	}
	if errors.Is(err, billing.ErrReplayConflict) {
		e.Log.DebugContext(ctx, warnMsg, "error", err)
		return
	}
	if errors.Is(err, billing.ErrUsageAppendOutboxEnqueue) {
		e.Log.ErrorContext(ctx, criticalMsg, "error", err)
		return
	}
	e.Log.WarnContext(ctx, warnMsg, "error", err)
}

func (e *Executor) appendIndependentTerminalLeg(ctx context.Context, callID billing.BillingCallID, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome billing.LegOutcome) {
	if e == nil || e.CallLegUsageAppender == nil {
		return
	}
	if started.IsZero() {
		started = finished
	}
	if finished.IsZero() {
		finished = started
	}
	backend := strings.TrimSpace(primary.Backend)
	if backend == "" {
		backend = "unknown"
	}
	model := strings.TrimSpace(primary.Model)
	if model == "" {
		model = "unknown"
	}
	leg := billing.LegUsageRecord{
		ALegID: strings.TrimSpace(aLegID), BLegID: strings.TrimSpace(bleg.BLegID), Seq: bleg.Seq,
		BackendID: backend, ProviderID: billingProviderID(primary), ModelID: model,
		StartedAt: started, FinishedAt: finished, Outcome: outcome, Surfaced: billing.SurfacedNo,
		Evidence:        billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
		OperatorRateRef: e.operatorRateRef(ctx, primary),
	}
	if err := callID.Validate(); err == nil {
		e.billingTurns().noteLegTimes(callID, started, finished)
	}
	e.billingTurns().observe(ctx, leg)
	e.appendIndependentCallLeg(ctx, callID, leg)
}

func (e *Executor) appendPostOpenTerminalLeg(ctx context.Context, callID billing.BillingCallID, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time) {
	if e == nil || strings.TrimSpace(bleg.BLegID) == "" {
		return
	}
	if err := callID.Validate(); err != nil {
		return
	}
	if started.IsZero() {
		started = e.now()
	}
	if finished.IsZero() {
		finished = e.now()
	}
	outcome := billing.LegOutcomeFailed
	if ctx.Err() != nil {
		outcome = billing.LegOutcomeCanceled
	}
	e.appendIndependentTerminalLeg(ctx, callID, aLegID, bleg, primary, started, finished, outcome)
}

func (e *Executor) recordParallelBillingLeg(ctx context.Context, leg *parallelLeg, usage lipapi.Event, command sdkterminal.Command, committed bool) {
	if e == nil || leg == nil || (e.BillingLegObserver == nil && e.CallLegUsageAppender == nil) {
		return
	}
	if leg.startedAt.IsZero() {
		e.appendIndependentTerminalLeg(ctx, leg.callID, leg.bleg.ALegID, leg.bleg, leg.cand.Primary, e.now(), e.now(), billing.LegOutcomeNeverStarted)
		return
	}
	surfaced := billing.SurfacedNo
	if committed {
		surfaced = billing.SurfacedYes
	}
	fallback := lastUsageDeltaOrShell([]lipapi.Event{usage})
	finalizeEv, ok := e.billingTurns().finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		ALegID:  strings.TrimSpace(leg.bleg.ALegID),
		BLegID:  strings.TrimSpace(leg.bleg.BLegID),
		Backend: strings.TrimSpace(leg.cand.Primary.Backend),
		Model:   strings.TrimSpace(leg.cand.Primary.Model),
		Reason:  "parallel_loser",
	})
	if !ok {
		finalizeEv = fallback
	}
	if err := leg.callID.Validate(); err == nil {
		e.billingTurns().noteLegTimes(leg.callID, leg.startedAt, e.now())
	}
	legRecord := billingLegRecord(billingLegDraft{
		aLegID:          leg.bleg.ALegID,
		bLegID:          leg.bleg.BLegID,
		seq:             leg.bleg.Seq,
		primary:         leg.cand.Primary,
		startedAt:       leg.startedAt,
		finishedAt:      e.now(),
		command:         command,
		surfaced:        surfaced,
		finalize:        finalizeEv,
		stream:          fallback,
		operatorRateRef: e.operatorRateRef(ctx, leg.cand.Primary),
	})
	e.billingTurns().observe(ctx, legRecord)
	if err := leg.callID.Validate(); err == nil {
		e.appendIndependentCallLeg(ctx, leg.callID, legRecord)
	}
}
