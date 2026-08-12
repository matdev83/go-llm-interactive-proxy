package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// BillingShadowObserver receives one recorded LUR. Panics from observers are
// isolated by the runtime and never alter stream behavior.
type BillingShadowObserver interface {
	ObserveBillingShadow(context.Context, billing.LegUsageRecord)
}

// BillingShadowObserverFunc adapts a function to BillingShadowObserver.
type BillingShadowObserverFunc func(context.Context, billing.LegUsageRecord)

func (f BillingShadowObserverFunc) ObserveBillingShadow(ctx context.Context, record billing.LegUsageRecord) {
	if f != nil {
		f(ctx, record)
	}
}

func (s *retryRecvStream) observeBillingShadow(ctx context.Context, command sdkterminal.Command) {
	if s == nil || s.executor == nil || !s.executor.billingTurns().enabled() {
		return
	}
	blegID := strings.TrimSpace(s.bleg.BLegID)
	if blegID == "" {
		blegID = billingSyntheticBLegID(s.bleg.Seq)
	}
	s.billingShadowMu.Lock()
	if s.billingShadowSeen == nil {
		s.billingShadowSeen = make(map[string]struct{})
	}
	if s.billingShadowInflight == nil {
		s.billingShadowInflight = make(map[string]struct{})
	}
	if _, seen := s.billingShadowSeen[blegID]; seen {
		s.billingShadowMu.Unlock()
		return
	}
	if _, inflight := s.billingShadowInflight[blegID]; inflight {
		s.billingShadowMu.Unlock()
		return
	}
	s.billingShadowInflight[blegID] = struct{}{}
	s.billingShadowMu.Unlock()

	defer func() {
		s.billingShadowMu.Lock()
		delete(s.billingShadowInflight, blegID)
		s.billingShadowMu.Unlock()
	}()

	now := s.now()
	started := s.accounting.requestStartedAt
	if started.IsZero() {
		started = now
	}
	surfaced := billing.SurfacedNo
	if command == sdkterminal.CommandNormalFinish || s.isCommitted() {
		surfaced = billing.SurfacedYes
	}
	backend := strings.TrimSpace(s.cand.Primary.Backend)
	if backend == "" {
		backend = "unknown"
	}
	model := strings.TrimSpace(s.cand.Primary.Model)
	if model == "" {
		model = "unknown"
	}
	var operatorRateRef billing.VersionRef
	if s.executor.BillingIdentity.OperatorRateRef != nil {
		operatorRateRef = s.executor.BillingIdentity.OperatorRateRef(ctx, backend, model)
	}
	streamEv := s.billingEvidenceFallback()
	finalizeEv := s.shadowBillingEvidence(ctx, "shadow_observe")
	record := billing.LegUsageRecord{
		ALegID:          strings.TrimSpace(s.aLegID),
		BLegID:          blegID,
		Seq:             s.bleg.Seq,
		BackendID:       backend,
		ProviderID:      billingProviderID(s.cand.Primary),
		ModelID:         model,
		OperatorRateRef: operatorRateRef,
		StartedAt:       started,
		FinishedAt:      now,
		Outcome:         legOutcomeFromCommand(command),
		Surfaced:        surfaced,
		Evidence:        mergeStreamCostOntoLUR(finalBillingEvidenceFromEvent(finalizeEv), finalBillingEvidenceFromEvent(streamEv)),
	}

	s.billingShadowMu.Lock()
	if _, seen := s.billingShadowSeen[blegID]; seen {
		s.billingShadowMu.Unlock()
		return
	}
	s.billingShadowSeen[blegID] = struct{}{}
	s.billingShadowMu.Unlock()
	s.executor.billingTurns().record(ctx, record)
}

// shadowBillingEvidence returns authoritative finalize evidence when the backend
// supports FinalizeBilling. Unlike finalizeBillingAfterCancel, this path is
// observational only: it never emits client usage or mutates stream accounting.
// Cost/CostPresent are not copied here; mergeStreamCostOntoLUR applies them on
// the LUR after tokens and provenance are taken from FinalizeBilling.
func (s *retryRecvStream) shadowBillingEvidence(ctx context.Context, reason string) lipapi.Event {
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

// lastUsageDeltaOrShell returns the last EventUsageDelta, else an empty shell.
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

	accountID, authID, customerPricing, chargePolicy := s.billingHandoffIdentity(persistCtx)
	if accountID == "" {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing TUR handoff skipped: account identity unavailable")
		}
		return
	}
	aLegID := strings.TrimSpace(s.aLegID)
	if authID == "" {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing TUR handoff skipped: authorization identity unavailable")
		}
		return
	}

	job := billingHandoffRetryJob{
		stream:          s,
		command:         command,
		accountID:       accountID,
		authorizationID: authID,
		aLegID:          aLegID,
		sessionID:       strings.TrimSpace(s.baseline.Session.AuthoritativeSessionID),
		customerPricing: customerPricing,
		chargePolicy:    chargePolicy,
		upstreamOpened:  true,
	}
	if s.executor.billingTurns().sealTurn(ctx, job) {
		s.billingHandoffSuccess = true
	}
}

func (s *retryRecvStream) billingHandoffIdentity(ctx context.Context) (accountID, authID string, customerPricing, chargePolicy billing.VersionRef) {
	if s.billingIdentityStamped {
		return strings.TrimSpace(s.billingAccountID), strings.TrimSpace(s.billingAuthorizationID), s.billingCustomerPricing, s.billingChargePolicy
	}
	if s.executor.BillingIdentity.AccountID != nil {
		accountID = strings.TrimSpace(s.executor.BillingIdentity.AccountID(ctx, s.baseline))
	}
	if accountID == "" {
		if views, ok := s.viewsFor(ctx); ok {
			accountID = strings.TrimSpace(views.Scope.PrincipalID.String())
		}
	}
	authID = strings.TrimSpace(s.aLegID)
	if s.executor.BillingIdentity.AuthorizationID != nil {
		authID = strings.TrimSpace(s.executor.BillingIdentity.AuthorizationID(ctx, s.baseline, s.aLegID))
	}
	return accountID, authID, s.resolveCustomerPricingRef(ctx), s.resolveChargePolicyRef(ctx)
}

func (s *retryRecvStream) resolveCustomerPricingRef(ctx context.Context) billing.VersionRef {
	if s == nil || s.executor == nil || s.executor.BillingIdentity.CustomerPricingRef == nil {
		return billing.VersionRef{}
	}
	return s.executor.BillingIdentity.CustomerPricingRef(ctx, s.baseline)
}

func (s *retryRecvStream) resolveChargePolicyRef(ctx context.Context) billing.VersionRef {
	if s == nil || s.executor == nil || s.executor.BillingIdentity.ChargePolicyRef == nil {
		return billing.VersionRef{}
	}
	return s.executor.BillingIdentity.ChargePolicyRef(ctx, s.baseline)
}

func (s *retryRecvStream) persistBillingTurnLocked(ctx context.Context, job billingHandoffRetryJob) error {
	if s == nil || s.executor == nil {
		return fmt.Errorf("runtime: billing handoff unavailable")
	}
	return s.executor.billingTurns().persist(ctx, job)
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

func (e *Executor) recordParallelBillingShadow(ctx context.Context, leg *parallelLeg, usage lipapi.Event, command sdkterminal.Command, committed bool) {
	if e == nil || leg == nil || (e.BillingTerminalHandoff == nil && e.BillingShadowObserver == nil) {
		return
	}
	if leg.startedAt.IsZero() {
		// Never-opened legs have no provider work and must not enter TUR/LUR
		// evidence: Seal rejects zero timestamps, and fabricating start=finish
		// would invent a billed interval.
		return
	}
	now := e.now()
	surfaced := billing.SurfacedNo
	if committed {
		surfaced = billing.SurfacedYes
	}
	backend := strings.TrimSpace(leg.cand.Primary.Backend)
	if backend == "" {
		backend = "unknown"
	}
	model := strings.TrimSpace(leg.cand.Primary.Model)
	if model == "" {
		model = "unknown"
	}
	var operatorRateRef billing.VersionRef
	if e.BillingIdentity.OperatorRateRef != nil {
		operatorRateRef = e.BillingIdentity.OperatorRateRef(ctx, backend, model)
	}
	fallback := lastUsageDeltaOrShell([]lipapi.Event{usage})
	finalizeEv, ok := e.billingTurns().finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		ALegID:  strings.TrimSpace(leg.bleg.ALegID),
		BLegID:  strings.TrimSpace(leg.bleg.BLegID),
		Backend: backend,
		Model:   model,
		Reason:  "parallel_loser_shadow",
	})
	if !ok {
		finalizeEv = fallback
	}
	blegID := strings.TrimSpace(leg.bleg.BLegID)
	if blegID == "" {
		blegID = billingSyntheticBLegID(leg.bleg.Seq)
	}
	e.addBillingEvidence(ctx, billing.LegUsageRecord{
		ALegID: strings.TrimSpace(leg.bleg.ALegID), BLegID: blegID, Seq: leg.bleg.Seq,
		BackendID: backend, ProviderID: billingProviderID(leg.cand.Primary), ModelID: model, OperatorRateRef: operatorRateRef,
		StartedAt: leg.startedAt, FinishedAt: now,
		Outcome: legOutcomeFromCommand(command), Surfaced: surfaced,
		Evidence: mergeStreamCostOntoLUR(finalBillingEvidenceFromEvent(finalizeEv), finalBillingEvidenceFromEvent(fallback)),
	})
}
