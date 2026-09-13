package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	callID            billing.BillingCallID
	aLegID            string
	storeID           string
	bLegID            string
	seq               int
	primary           routing.Primary
	startedAt         time.Time
	finishedAt        time.Time
	command           sdkterminal.Command
	outcome           billing.LegOutcome
	surfaced          billing.SurfacedState
	finalize          lipapi.Event
	stream            lipapi.Event
	evidenceEvents    []capturedBillingEvidence
	evidenceConflicts []billing.EvidenceConflict
	operatorRateRef   billing.VersionRef
	workload          billing.WorkloadIdentity
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
	// Evidence is captured source-by-source for V2. The scalar V1 value below
	// is retained only as a compatibility projection for old stores/readers;
	// it is deliberately not used as the terminal economic source of truth.
	evidence := projectV1BillingEvidence(finalBillingEvidenceFromEvent(draft.finalize), finalBillingEvidenceFromEvent(draft.stream))
	evidence = normalizeBillingEvidenceIdentity(evidence, draft.callID, bLegID)
	observations, conflicts := observationsFromBillingEvidence(draft, draft.evidenceEvents)
	outcome := draft.outcome
	if outcome == "" {
		outcome = legOutcomeFromCommand(draft.command)
	}
	record := billing.CallLegUsageRecord{
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
		Outcome:         outcome,
		Surfaced:        draft.surfaced,
		Evidence:        evidence,
		Workload:        draft.workload,
	}
	if len(observations) != 0 || len(conflicts) != 0 {
		record.EvidenceVersion = billing.EvidenceFormatVersionV2
		record.EvidenceProjection = billing.EvidenceProjectionV1
		record.Observations = observations
		record.EvidenceConflicts = appendBoundedEvidenceConflicts(conflicts, draft.evidenceConflicts)
	}
	return record
}

func appendBoundedEvidenceConflicts(existing, incoming []billing.EvidenceConflict) []billing.EvidenceConflict {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make([]billing.EvidenceConflict, 0, min(billing.MaxCallLegEvidenceConflicts, len(existing)+len(incoming)))
	for _, conflict := range existing {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= billing.MaxCallLegEvidenceConflicts {
			return out
		}
	}
	for _, conflict := range incoming {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= billing.MaxCallLegEvidenceConflicts {
			break
		}
	}
	return out
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
		if fallback, err := billing.DedupeKeyForBLeg(callID, bLegID); err == nil {
			evidence.DedupeKey = fallback
		}
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

// recordBillingLegForAttempt records exactly one leg for the explicitly
// snapshotted B-leg owner. Terminal callbacks may run after the attempt slot
// has been replaced; never re-read the current slot here.
func (t *turnTerminal) recordBillingLegForAttempt(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, evidence attemptTerminalEvidence, command sdkterminal.Command, streamEv lipapi.Event, committed bool, billingState *billingCallState) {
	if t == nil || t.billingEnabled == nil || !t.billingEnabled() || t.operatorRateRef == nil || t.billingWorkload == nil {
		return
	}
	if attempt == nil || !attempt.claimBillingLegRecord() {
		return
	}
	blegID := strings.TrimSpace(evidence.bleg.BLegID)
	if blegID == "" {
		blegID = billingSyntheticBLegID(evidence.bleg.Seq)
	}
	if evidence.bleg.Seq > 0 && billingState != nil {
		billingState.noteAllocatedBLeg(blegID, evidence.bleg.Seq)
	}
	now := t.nowTime()
	started := evidence.startedAt
	if started.IsZero() {
		started = now
	}
	if billingState != nil {
		billingState.noteLegTimes(started, now)
	}
	surfaced := billing.SurfacedNo
	if command == sdkterminal.CommandNormalFinish || committed {
		surfaced = billing.SurfacedYes
	}
	streamEv = attempt.augmentBillingUsage(streamEv, lipapi.Event{})
	evidenceEvents, evidenceConflicts := attempt.billingEvidenceDrain()
	workloadCtx := request.toRecvTurnFacts(ctx).projectContext(ctx, nil)
	legRecord := billingLegRecord(billingLegDraft{
		callID:            request.billingCallID,
		aLegID:            request.aLegID,
		storeID:           request.storeID,
		bLegID:            evidence.bleg.BLegID,
		seq:               evidence.bleg.Seq,
		primary:           evidence.candidate.Primary,
		startedAt:         started,
		finishedAt:        now,
		command:           command,
		surfaced:          surfaced,
		finalize:          t.finalizeBillingEvidence(ctx, request, evidence, billingState, "record_leg", streamEv),
		stream:            streamEv,
		evidenceEvents:    evidenceEvents,
		evidenceConflicts: evidenceConflicts,
		operatorRateRef:   t.operatorRateRef(workloadCtx, evidence.candidate.Primary),
		workload:          t.billingWorkload(workloadCtx, request.aLegID),
	})
	if t.observeBillingLeg != nil {
		t.observeBillingLeg(ctx, legRecord)
	}
	if t.appendBillingLegStrict != nil {
		if err := t.appendBillingLegStrict(ctx, request.billingCallID, legRecord); err != nil {
			if t.logBillingAppendFailure != nil {
				t.logBillingAppendFailure(ctx, "billing_call_leg_append_critical", "billing call-leg append failed", err)
			}
		}
	} else if t.appendBillingLeg != nil {
		t.appendBillingLeg(ctx, request.billingCallID, legRecord)
	}
}

func (t *turnTerminal) finalizeBillingEvidence(ctx context.Context, request requestTerminalFacts, evidence attemptTerminalEvidence, billingState *billingCallState, reason string, fallback lipapi.Event) lipapi.Event {
	if t == nil || t.finalizeBilling == nil {
		return fallback
	}
	if billingState == nil {
		return fallback
	}
	ev, ok := billingState.finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(request.traceID),
		ALegID:  strings.TrimSpace(request.aLegID),
		BLegID:  strings.TrimSpace(evidence.bleg.BLegID),
		Backend: strings.TrimSpace(evidence.candidate.Primary.Backend),
		Model:   strings.TrimSpace(evidence.candidate.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	}, func(cctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
		return t.finalizeBilling(cctx, in)
	})
	if !ok {
		return fallback
	}
	return ev
}

func lastUsageDeltaOrShell(events []lipapi.Event) lipapi.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == lipapi.EventUsageDelta {
			return events[i]
		}
	}
	return emptyOperatorUsageShell()
}

// projectV1BillingEvidence keeps the old scalar contract readable while
// refusing to combine values from incompatible source/authority planes. V2
// source observations are always retained independently by billingLegRecord.
func projectV1BillingEvidence(finalize, stream billing.FinalBillingEvidence) billing.FinalBillingEvidence {
	if finalize.Cost.Present {
		return finalize
	}
	if !stream.Cost.Present {
		return finalize
	}
	// A provider-authoritative stream cost is a valid compatibility projection
	// when the finalizer only supplied non-economic provenance (the historical
	// FinalizeBilling ABI does this for token totals). This selects one V1
	// value; the V2 observations still retain the two source envelopes.
	if stream.Authority == billing.EvidenceAuthorityAuthoritative {
		finalize.Cost = stream.Cost
		finalize.Authority = stream.Authority
		return finalize
	}
	if !compatibleBillingEvidenceProjection(finalize, stream) {
		return finalize
	}
	finalize.Cost = stream.Cost
	return finalize
}

// mergeStreamCostOntoLeg is retained as a source-compatible helper for older
// internal callers. It now applies the explicitly labelled V1 projection.
func mergeStreamCostOntoLeg(finalize, stream billing.FinalBillingEvidence) billing.FinalBillingEvidence {
	return projectV1BillingEvidence(finalize, stream)
}

func compatibleBillingEvidenceProjection(finalize, stream billing.FinalBillingEvidence) bool {
	if finalize.Source == billing.EvidenceSourceUnknown || finalize.Authority == billing.EvidenceAuthorityUnknown {
		return false
	}
	if stream.Source == billing.EvidenceSourceUnknown || stream.Authority == billing.EvidenceAuthorityUnknown {
		return false
	}
	return finalize.Source == stream.Source && finalize.Authority == stream.Authority
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

func billingSyntheticBLegID(seq int) string {
	return "seq_" + strconv.Itoa(seq)
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
	if err := e.appendIndependentCallLegStrict(ctx, callID, leg); err != nil {
		e.logBillingUsageAppendFailure(ctx, "billing_call_leg_append_critical", "billing call-leg append failed", err)
	}
}

// appendIndependentCallLegStrict is the terminal durability seam. It returns
// sink failures to the owning terminal effect while retaining the existing
// validation diagnostics and void observer wrapper for optional callers.
func (e *Executor) appendIndependentCallLegStrict(ctx context.Context, callID billing.BillingCallID, leg billing.CallLegUsageRecord) error {
	if !e.hasTerminalSink() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// AttemptSeq is the authoritative B2BUA financial fact; reject unknown
	// sequences rather than deriving order. Legacy NULL rows remain readable,
	// but order-dependent rating fails closed.
	if leg.AttemptSeq <= 0 {
		err := fmt.Errorf("%w: attempt sequence for B-leg %q", billing.ErrInvalidRecord, leg.BLegID)
		if e.Log != nil {
			e.Log.ErrorContext(ctx, "billing call-leg append rejected: attempt sequence missing", "error", err, "b_leg_id", leg.BLegID)
		}
		return nil
	}
	leg.CallID = callID
	leg.Evidence = normalizeBillingEvidenceIdentity(leg.Evidence, callID, leg.BLegID)
	if err := billing.ValidateIndependentLeg(leg); err != nil {
		if e.Log != nil {
			e.Log.ErrorContext(ctx, "billing call-leg append rejected: invalid independent leg", "error", err, "b_leg_id", leg.BLegID)
		}
		return nil
	}
	independent := leg.Clone()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err := e.TerminalUsageSink.AppendLeg(persistCtx, independent)
	if err != nil {
		return fmt.Errorf("billing call-leg append: %w", err)
	}
	return nil
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
		Workload:        e.billingWorkloadIdentityForALeg(ctx, aLegID),
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
		storeID:         leg.storeID,
		bLegID:          leg.bleg.BLegID,
		seq:             leg.bleg.Seq,
		primary:         leg.cand.Primary,
		startedAt:       leg.startedAt,
		finishedAt:      e.now(),
		command:         command,
		surfaced:        surfaced,
		finalize:        finalizeEv,
		stream:          fallback,
		evidenceEvents:  []capturedBillingEvidence{{event: usage, role: billingEvidenceRoleStream}},
		operatorRateRef: e.operatorRateRef(ctx, leg.cand.Primary),
		workload:        e.billingWorkloadIdentityForALeg(ctx, leg.bleg.ALegID),
	})
	e.observeBillingLeg(ctx, legRecord)
	if leg.billingCallState != nil {
		e.appendIndependentCallLeg(ctx, leg.billingCallState.callID, legRecord)
	}
}
