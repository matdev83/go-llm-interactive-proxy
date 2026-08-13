package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// BillingLegObserver receives one recorded LUR. Panics from observers are
// isolated by the runtime and never alter stream behavior.
type BillingLegObserver interface {
	ObserveBillingLeg(context.Context, billing.LegUsageRecord)
}

// BillingLegObserverFunc adapts a function to BillingLegObserver.
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

	now := s.now()
	started := s.accounting.requestStartedAt
	if started.IsZero() {
		started = now
	}
	surfaced := billing.SurfacedNo
	if command == sdkterminal.CommandNormalFinish || s.isCommitted() {
		surfaced = billing.SurfacedYes
	}
	streamEv := s.billingEvidenceFallback()
	s.executor.billingTurns().record(ctx, billingLegRecord(billingLegDraft{
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
	}))
}

// finalizeBillingEvidence returns authoritative finalize evidence when the backend
// supports FinalizeBilling. Unlike finalizeBillingAfterCancel, this path is
// observational only: it never emits client usage or mutates stream accounting.
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

// billingEvidenceFallback prefers reconstructed authority usage, then the last
// EventUsageDelta in seen events. Cumulative providers emit running totals;
// summing them would double-count LUR quantities. This path is billing-only and
// must not change mergeUsageEventsForClient (client protocol aggregation).
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

// mergeStreamCostOntoLUR keeps finalize token/provenance while copying any
// stream-observed monetary cost (including authoritative zero) onto the LUR
// when FinalizeBilling has no money fields (Req 3.4). Stream Authority is
// copied with Cost so present-zero remains authoritative for rating (Req 3.6).
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
	if s == nil || s.executor == nil || s.executor.BillingTerminalHandoff == nil || !isBillingTurnTerminalCommand(command) {
		return
	}
	s.billingHandoffMu.Lock()
	defer s.billingHandoffMu.Unlock()
	if s.billingHandoffSuccess {
		return
	}

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()

	if !s.billingIdentityStamped {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing TUR handoff skipped: identity not stamped at admission")
		}
		return
	}
	accountID := strings.TrimSpace(s.billingAccountID)
	authID := strings.TrimSpace(s.billingAuthorizationID)
	if accountID == "" || authID == "" {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing TUR handoff skipped: stamped identity incomplete")
		}
		return
	}

	job := billingHandoffRetryJob{
		stream:          s,
		command:         command,
		accountID:       accountID,
		authorizationID: authID,
		aLegID:          strings.TrimSpace(s.aLegID),
		sessionID:       strings.TrimSpace(s.baseline.Session.AuthoritativeSessionID),
		customerPricing: s.billingCustomerPricing,
		chargePolicy:    s.billingChargePolicy,
		upstreamOpened:  true,
	}
	if s.executor.billingTurns().sealTurn(ctx, job) {
		s.billingHandoffSuccess = true
	}
}

var errBillingHandoffNoEvidence = fmt.Errorf("runtime: billing handoff has no B-leg evidence")

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

func (e *Executor) recordParallelBillingLeg(ctx context.Context, leg *parallelLeg, usage lipapi.Event, command sdkterminal.Command, committed bool) {
	if e == nil || leg == nil || (e.BillingTerminalHandoff == nil && e.BillingLegObserver == nil) {
		return
	}
	if leg.startedAt.IsZero() {
		// Never-opened legs have no provider work and must not enter TUR/LUR
		// evidence: Seal rejects zero timestamps, and fabricating start=finish
		// would invent a billed interval.
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
	e.billingTurns().record(ctx, billingLegRecord(billingLegDraft{
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
	}))
}
