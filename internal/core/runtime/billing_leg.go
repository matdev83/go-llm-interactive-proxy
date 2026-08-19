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
	ObserveBillingLeg(context.Context, billing.CallLegUsageRecord)
}
type BillingLegObserverFunc func(context.Context, billing.CallLegUsageRecord)

func (f BillingLegObserverFunc) ObserveBillingLeg(ctx context.Context, record billing.CallLegUsageRecord) {
	if f != nil {
		f(ctx, record)
	}
}

type billingLegDraft struct {
	callID          billing.BillingCallID
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

func billingLegRecord(draft billingLegDraft) billing.CallLegUsageRecord {
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
	evidence := mergeStreamCostOntoLeg(finalBillingEvidenceFromEvent(draft.finalize), finalBillingEvidenceFromEvent(draft.stream))
	evidence = normalizeBillingEvidenceIdentity(evidence, draft.callID, bLegID)
	return billing.CallLegUsageRecord{
		CallID:          draft.callID,
		ALegID:          strings.TrimSpace(draft.aLegID),
		BLegID:          bLegID,
		AttemptSeq:      draft.seq,
		BackendID:       backend,
		ProviderID:      billingProviderID(draft.primary),
		ModelID:         model,
		OperatorRateRef: draft.operatorRateRef,
		StartedAt:       draft.startedAt,
		FinishedAt:      draft.finishedAt,
		Outcome:         legOutcomeFromCommand(draft.command),
		Surfaced:        draft.surfaced,
		Evidence:        evidence,
	}
}

// normalizeBillingEvidenceIdentity keeps every independently accounted B-leg
// identifiable even when no provider usage event was observed (for example, a
// pre-output failover leg). Provider evidence remains authoritative when
// present; only missing identity/provenance fields receive explicit bounded
// fallback values.
func normalizeBillingEvidenceIdentity(evidence billing.FinalBillingEvidence, callID billing.BillingCallID, bLegID string) billing.FinalBillingEvidence {
	if evidence.Source == billing.EvidenceSourceUnknown {
		evidence.Source = billing.EvidenceSourceUnavailable
	}
	if evidence.Authority == billing.EvidenceAuthorityUnknown {
		evidence.Authority = billing.EvidenceAuthorityUnavailable
	}
	if strings.TrimSpace(evidence.DedupeKey) == "" {
		id := strings.TrimSpace(callID.String())
		if id == "" {
			id = "unknown-call"
		}
		bLegID = strings.TrimSpace(bLegID)
		if bLegID == "" {
			bLegID = "unknown-b-leg"
		}
		evidence.DedupeKey = "lip-b-leg:" + id + ":" + bLegID
	}
	return evidence
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
	if s == nil || s.executor == nil || !s.executor.billingEnabled() {
		return
	}
	s.ensureBillingCallState()
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
	if s.bleg.Seq > 0 {
		s.billingCallState.noteAllocatedBLeg(blegID, s.bleg.Seq)
	}
	now := s.now()
	started := s.accounting.requestStartedAt
	if started.IsZero() {
		started = now
	}
	s.billingCallState.noteLegTimes(started, now)
	surfaced := billing.SurfacedNo
	if command == sdkterminal.CommandNormalFinish || s.isCommitted() {
		surfaced = billing.SurfacedYes
	}
	streamEv := s.billingEvidenceFallback()
	legRecord := billingLegRecord(billingLegDraft{
		callID:          s.billingCallID,
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
	s.executor.observeBillingLeg(ctx, legRecord)
	s.executor.appendIndependentCallLeg(ctx, s.billingCallID, legRecord)
}

func (s *retryRecvStream) finalizeBillingEvidence(ctx context.Context, reason string) lipapi.Event {
	fallback := s.billingEvidenceFallback()
	if s == nil || s.executor == nil {
		return fallback
	}
	s.ensureBillingCallState()
	ev, ok := s.billingCallState.finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(s.traceID),
		ALegID:  strings.TrimSpace(s.aLegID),
		BLegID:  strings.TrimSpace(s.bleg.BLegID),
		Backend: strings.TrimSpace(s.cand.Primary.Backend),
		Model:   strings.TrimSpace(s.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	}, func(cctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
		return s.executor.callFinalizeBilling(cctx, in)
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

func mergeStreamCostOntoLeg(finalize, stream billing.FinalBillingEvidence) billing.FinalBillingEvidence {
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

func (e *Executor) appendIndependentCallLeg(ctx context.Context, callID billing.BillingCallID, leg billing.CallLegUsageRecord) {
	if !e.hasTerminalSink() {
		return
	}
	// AttemptSeq is the authoritative B2BUA financial fact; reject unknown
	// sequences rather than deriving order. Legacy NULL rows remain readable,
	// but order-dependent rating fails closed.
	if leg.AttemptSeq <= 0 {
		if e.Log != nil {
			e.Log.ErrorContext(ctx, "billing call-leg append rejected: attempt sequence missing", "error", fmt.Errorf("%w: attempt sequence for B-leg %q", billing.ErrInvalidRecord, leg.BLegID), "b_leg_id", leg.BLegID)
		}
		return
	}
	leg.CallID = callID
	independent := leg
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err := e.TerminalUsageSink.AppendLeg(persistCtx, independent)
	if err != nil {
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
	e.Log.WarnContext(ctx, warnMsg, "error", err)
}

func (e *Executor) appendIndependentTerminalLeg(ctx context.Context, state *billingCallState, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome billing.LegOutcome) {
	if !e.hasTerminalSink() {
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
	leg := billing.CallLegUsageRecord{
		ALegID: strings.TrimSpace(aLegID), BLegID: strings.TrimSpace(bleg.BLegID), AttemptSeq: bleg.Seq,
		BackendID: backend, ProviderID: billingProviderID(primary), ModelID: model,
		StartedAt: started, FinishedAt: finished, Outcome: outcome, Surfaced: billing.SurfacedNo,
		Evidence:        billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
		OperatorRateRef: e.operatorRateRef(ctx, primary),
	}
	var callID billing.BillingCallID
	if state != nil {
		callID = state.callID
		leg.CallID = callID
		state.noteLegTimes(started, finished)
	}
	leg.Evidence = normalizeBillingEvidenceIdentity(leg.Evidence, leg.CallID, leg.BLegID)
	e.observeBillingLeg(ctx, leg)
	if callID != "" {
		e.appendIndependentCallLeg(ctx, callID, leg)
	}
}

func (e *Executor) appendPostOpenTerminalLeg(ctx context.Context, state *billingCallState, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time) {
	if e == nil || strings.TrimSpace(bleg.BLegID) == "" {
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
	e.appendIndependentTerminalLeg(ctx, state, aLegID, bleg, primary, started, finished, outcome)
}

func (e *Executor) recordParallelBillingLeg(ctx context.Context, leg *parallelLeg, usage lipapi.Event, command sdkterminal.Command, committed bool) {
	if e == nil || leg == nil || (e.BillingLegObserver == nil && !e.hasTerminalSink()) {
		return
	}
	if leg.startedAt.IsZero() {
		e.appendIndependentTerminalLeg(ctx, leg.billingCallState, leg.bleg.ALegID, leg.bleg, leg.cand.Primary, e.now(), e.now(), billing.LegOutcomeNeverStarted)
		return
	}
	surfaced := billing.SurfacedNo
	if committed {
		surfaced = billing.SurfacedYes
	}
	fallback := lastUsageDeltaOrShell([]lipapi.Event{usage})
	finalizeEv, ok := leg.billingCallState.finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		ALegID:  strings.TrimSpace(leg.bleg.ALegID),
		BLegID:  strings.TrimSpace(leg.bleg.BLegID),
		Backend: strings.TrimSpace(leg.cand.Primary.Backend),
		Model:   strings.TrimSpace(leg.cand.Primary.Model),
		Reason:  "parallel_loser",
	}, func(cctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
		return e.callFinalizeBilling(cctx, in)
	})
	if !ok {
		finalizeEv = fallback
	}
	if leg.billingCallState != nil {
		leg.billingCallState.noteLegTimes(leg.startedAt, e.now())
	}
	var callID billing.BillingCallID
	if leg.billingCallState != nil {
		callID = leg.billingCallState.callID
	}
	legRecord := billingLegRecord(billingLegDraft{
		callID:          callID,
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
	e.observeBillingLeg(ctx, legRecord)
	if leg.billingCallState != nil {
		e.appendIndependentCallLeg(ctx, leg.billingCallState.callID, legRecord)
	}
}
