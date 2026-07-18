package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// persistCancellationBilling records the final usage/cost evidence for a canceled
// attempt and settles the matching usage authority reservation. When a backend
// EventUsageDelta was already observed mid-stream (accounting.usageObserved), the
// observed usage is settled directly as a Cancellation — no estimated billing marker
// is needed. Otherwise it first attempts the backend's FinalizeBilling hook (when
// present) to recover authoritative usage; on failure it records an estimated
// billing marker so accounting still has evidence. When the reservation is already
// settled AND authoritative usage is available (usageObserved or finalizeBilling
// succeeded), it calls ReconcileAuthoritative to adjust the prior estimated
// settlement instead of the no-op settleCancellationAuthority (requirement 7.6,
// 8.4-8.6). If not already settled, the existing settleCancellationAuthority path
// runs. Every settlement path uses a non-canceled context so post-output
// accounting completes after client cancellation (requirement 11.7).
func (s *retryRecvStream) persistCancellationBilling(ctx context.Context, reason string) {
	if s == nil {
		return
	}
	if s.accounting.usageObserved {
		s.reconcileOrSettleCancellationAuthority(ctx)
		s.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindCancellation, authorityUsageEvent(tokenAccountingUsageEvents(s.seenEvents)))
		if s.isCommitted() {
			s.settleRequestAuthorityWithFrontendEgress(ctx, s.usageEvidenceOrEmpty())
		} else if s.executor != nil {
			s.executor.releaseRequestAuthority(ctx)
		}
		return
	}
	if s.finalizeBillingAfterCancel(ctx, reason) {
		s.reconcileOrSettleCancellationAuthority(ctx)
		s.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindCancellation, authorityUsageEvent(tokenAccountingUsageEvents(s.seenEvents)))
		if s.isCommitted() {
			s.settleRequestAuthorityWithFrontendEgress(ctx, s.usageEvidenceOrEmpty())
		} else if s.executor != nil {
			s.executor.releaseRequestAuthority(ctx)
		}
		return
	}
	s.recordCancellationBillingMarker(ctx, reason)
	s.settleCancellationAuthority(ctx)
	s.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindCancellation, authorityUsageEvent(tokenAccountingUsageEvents(s.seenEvents)))
	if s.isCommitted() {
		s.settleRequestAuthorityWithFrontendEgress(ctx, s.usageEvidenceOrEmpty())
	} else if s.executor != nil {
		s.executor.releaseRequestAuthority(ctx)
	}
}

// reconcileOrSettleCancellationAuthority routes the cancellation settlement based
// on whether the reservation is already settled. When already settled AND
// authoritative usage is available (the caller guarantees usageObserved or
// finalizeBilling succeeded), it calls ReconcileAuthoritative to adjust the prior
// estimated settlement with the authoritative usage event. When not yet settled,
// it falls back to settleCancellationAuthority which settles as a Cancellation.
func (s *retryRecvStream) reconcileOrSettleCancellationAuthority(ctx context.Context) {
	if s.authority.Settled() {
		usageEv := authorityUsageEvent(tokenAccountingUsageEvents(s.seenEvents))
		s.authority.ReconcileAuthoritative(ctx, usageEv)
		return
	}
	s.settleCancellationAuthority(ctx)
}

// settleCancellationAuthority settles the usage-authority reservation for a canceled
// attempt with the observed usage as a Cancellation. It is a no-op when the
// reservation is already settled (preventing a double settle of a strict
// reservation, e.g. after a prior partial/final settle). The losing-fallback
// (ReleaseKindLosing when the settle fails) now lives inside the authorityLifecycle
// owner's Settle, mirroring the finalizeResponseFinishedAuthority path. It passes
// a non-canceled context to Settle so cancellation of the client request does not
// abort the post-output settlement (requirement 11.7).
func (s *retryRecvStream) settleCancellationAuthority(ctx context.Context) {
	if s == nil || s.authority.Settled() {
		return
	}
	usageEv := authorityUsageEvent(tokenAccountingUsageEvents(s.seenEvents))
	s.authority.Settle(ctx, authorityapp.SettlementKindCancellation, usageEv, true)
	s.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeCanceled, metering.SurfacedNo, usageEv)
}

func (s *retryRecvStream) recordCancellationBillingMarker(ctx context.Context, reason string) {
	if s == nil || s.accounting.usageObserved {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"billing_basis": "estimated_after_a_leg_cancellation",
		"reason":        strings.TrimSpace(reason),
	})
	ev := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		CostSource:   accounting.CostSourceEstimated,
		RawUsageJSON: string(raw),
	}
	persistCtx := context.WithoutCancel(ctx)
	if err := s.beforeEmitClientFacing(persistCtx, ev); err != nil && s.executor != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session cancellation billing marker", "error", err)
	}
	s.emitUsage(persistCtx, ev)
}

func (s *retryRecvStream) finalizeBillingAfterCancel(ctx context.Context, reason string) bool {
	if s == nil || s.executor == nil || s.executor.Backends == nil {
		return false
	}
	be, ok := s.executor.Backends[strings.TrimSpace(s.cand.Primary.Backend)]
	if !ok || be.FinalizeBilling == nil {
		return false
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	ev, err := be.FinalizeBilling(persistCtx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(s.traceID),
		ALegID:  strings.TrimSpace(s.aLegID),
		BLegID:  strings.TrimSpace(s.bleg.BLegID),
		Backend: strings.TrimSpace(s.cand.Primary.Backend),
		Model:   strings.TrimSpace(s.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing finalizer after cancellation", "error", err)
		}
		return false
	}
	if ev.Kind != lipapi.EventUsageDelta {
		return false
	}
	s.accounting.observeUsage(ev)
	s.rememberClientEvent(ev)
	if recErr := s.beforeEmitClientFacing(persistCtx, ev); recErr != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session billing finalizer marker", "error", recErr)
	}
	s.emitUsage(persistCtx, ev)
	return true
}

func (s *retryRecvStream) emitUsage(ctx context.Context, ev lipapi.Event) {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil || ev.Kind != lipapi.EventUsageDelta {
		return
	}
	obs := s.executor.RuntimeSnapshot.UsageObserver()
	if obs == nil {
		return
	}
	principalID := ""
	scopeView := scopeFromCtx(ctx)
	if scopeView.PrincipalID.IsKnown() {
		principalID = strings.TrimSpace(scopeView.PrincipalID.String())
	}
	model := ""
	if s.cand.Primary.Model != "" {
		model = s.cand.Primary.Model
	}
	if err := obs.OnUsage(ctx, usage.Event{
		TraceID:          strings.TrimSpace(s.traceID),
		ALegID:           strings.TrimSpace(s.aLegID),
		BLegID:           strings.TrimSpace(s.bleg.BLegID),
		PrincipalID:      strings.TrimSpace(principalID),
		SessionID:        strings.TrimSpace(s.baseline.Session.CorrelationID()),
		AttemptSeq:       int(s.bleg.Seq),
		BackendID:        strings.TrimSpace(s.cand.Primary.Backend),
		Model:            strings.TrimSpace(model),
		Scope:            scopeView.Clone(),
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         strings.TrimSpace(ev.Currency),
		CostSource:       strings.TrimSpace(ev.CostSource),
		RawUsageJSON:     strings.TrimSpace(ev.RawUsageJSON),
		RecordedAt:       s.executor.now(),
	}); err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "usage observer error", "error", err)
	}
}

func (s *retryRecvStream) emitSynthesizedUsage(ctx context.Context, ev lipapi.Event) (lipapi.Event, error) {
	s.accounting.observeClientEvent(s.now(), ev)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	s.rememberClientEvent(ev)
	pm, _ := s.recvHookMeta()
	out, err := s.emitClientFacingObserved(ctx, ev, pm)
	if err != nil {
		return lipapi.Event{}, err
	}
	s.emitUsage(ctx, out)
	return out, nil
}

func (s *retryRecvStream) finalizeTokenAccounting(ctx context.Context, finish lipapi.Event) (lipapi.Event, bool, error) {
	if s == nil || s.executor == nil {
		return lipapi.Event{}, false, nil
	}
	if s.executor.StreamUsage == nil {
		s.lastAuthorityUsage = lipapi.Event{}
		s.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	started := s.now()
	events := append([]lipapi.Event(nil), s.seenEvents...)
	events = append(events, finish)
	result, err := s.executor.StreamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    strings.TrimSpace(s.cand.Primary.Backend),
		Model:      strings.TrimSpace(s.cand.Primary.Model),
		Call:       s.baseline,
		OutputText: s.visibleText.String(),
		Events:     events,
	})
	if err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "token accounting stream reconstruction", "error", err)
	}
	if len(result.Events) == 0 {
		s.lastAuthorityUsage = lipapi.Event{}
		s.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	authorityEv := authorityUsageEvent(result.Events)
	clientUsageEv := mergeUsageEventsForClient(result.Events, tokenAccountingHasProviderUsage(s.seenEvents))
	s.lastAuthorityUsage = authorityEv
	duration := s.now().Sub(started)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	if err := s.recordTokenAccountingLedger(ctx, result.Events, "", "", duration); err != nil {
		if s.executor.LedgerWriteRequired {
			s.authority.Settle(ctx, authorityapp.SettlementKindFinal, authorityEv, false)
			return lipapi.Event{}, false, err
		}
	}
	s.authority.Settle(ctx, authorityapp.SettlementKindFinal, authorityEv, false)
	return clientUsageEv, true, nil
}

// finalizeResponseFinishedAuthority is the single authority-finalization chokepoint for
// response_finished completion paths. It runs token-accounting finalization, which settles
// the usage-authority reservation via the authorityLifecycle owner (the owner folds the
// losing-fallback release into Settle, so a failed settle releases ReleaseKindLosing and
// marks the lifecycle settled). Idempotent via tokenAccountingFinalized (which gates
// usage-delta re-queue, not authority idempotency — the owner owns that via settled). It
// does NOT mark the stream finished and does NOT queue the event — callers own
// emission/finish timing.
//
// After token-accounting finalization it also applies advisory usage (requirement 7.7) so
// advisory windows accumulate actual usage even when the request was not reserved. The
// advisory apply runs on a non-canceled context so post-output accounting completes after
// client cancellation, and is idempotent via the store source key (duplicate finalize calls
// are no-ops at the runtime guard and at the store).
func (s *retryRecvStream) finalizeResponseFinishedAuthority(ctx context.Context, ev lipapi.Event) (lipapi.Event, bool, error) {
	if s.tokenAccountingFinalized {
		return lipapi.Event{}, false, nil
	}
	usageEv, ok, err := s.finalizeTokenAccounting(ctx, ev)
	if err != nil {
		return lipapi.Event{}, false, err
	}
	s.tokenAccountingFinalized = true
	authorityEv := s.lastAuthorityUsage
	if authorityEv.Kind == "" {
		authorityEv = usageEv
	}
	s.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindFinal, authorityEv)
	s.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeWinner, metering.SurfacedYes, authorityEv)
	s.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv)
	return usageEv, ok, nil
}

// settleRequestAuthorityWithFrontendEgress emits the frontend-egress fact for the
// delivered/committed usage and passes that fact into request settlement (4.2).
// When EconomicsRater is attached, customer FE-egress quantities are rated and
// forwarded on RequestSettlement.Rated (requirements 6.1, 4.2).
func (s *retryRecvStream) settleRequestAuthorityWithFrontendEgress(ctx context.Context, usageEv lipapi.Event) {
	if s == nil {
		return
	}
	var facts []metering.Fact
	fact, persisted := s.emitFrontendEgressMeteringFact(ctx, usageEv)
	if persisted {
		facts = []metering.Fact{fact}
	} else if s.executor != nil && s.executor.MeteringRecorder != nil {
		// Required settlement evidence was not persisted. Keep request authority
		// open so a later terminal/reconciliation attempt can retry the append.
		return
	}
	var rated []economics.RatingResult
	if s.executor != nil && s.executor.EconomicsRater != nil {
		qs := usageEventRatingQuantities(usageEv)
		if len(facts) > 0 && len(facts[0].Quantities) > 0 {
			qs = facts[0].Quantities
		}
		if res, err := s.executor.rateMonetaryExposure(ctx, economics.RatingRequest{
			Perspective: metering.PerspectiveCustomer,
			Quantities:  qs,
			At:          s.executor.now(),
		}); err == nil {
			rated = []economics.RatingResult{res}
		}
		// Rating failure after committed output must not erase the fact or block
		// settle retry; settle still runs with facts (15.5).
	}
	if s.executor != nil {
		s.executor.settleRequestAuthority(ctx, facts, rated...)
	}
}

func mergeUsageEvents(events []lipapi.Event) lipapi.Event {
	return mergeUsageEventsForClient(events, false)
}

func authorityUsageEvent(events []lipapi.Event) lipapi.Event {
	authoritative := authoritativeProviderUsageEvents(events)
	if len(authoritative) > 0 {
		return mergeUsageEventsForClient(authoritative, false)
	}
	return mergeUsageEvents(events)
}

// authoritativeProviderUsageEvents keeps only provider scopes whose metadata
// proves billable authority. Costs are retained only when the event-level
// metadata makes their scope unambiguous; a mixed scoped event otherwise
// contributes token counters but not an ambiguous provider cost.
func authoritativeProviderUsageEvents(events []lipapi.Event) []lipapi.Event {
	out := make([]lipapi.Event, 0, len(events))
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if len(ev.UsageScopes) == 0 {
			if authoritativeProviderAccounting(ev.Accounting) {
				out = append(out, ev)
			}
			continue
		}
		filtered := ev
		filtered.UsageScopes = nil
		explicitScopeMetadata := false
		for _, scope := range ev.UsageScopes {
			if authoritativeProviderAccounting(scope.Accounting) {
				filtered.UsageScopes = append(filtered.UsageScopes, scope)
				continue
			}
			if scope.Accounting != (lipapi.UsageAccountingMetadata{}) {
				explicitScopeMetadata = true
			}
		}
		// A provider may put one event-level accounting record around otherwise
		// unannotated scopes. Accept those scopes only when none carries an
		// explicit conflicting classification; never let a local/client scope
		// ride along with an authoritative provider scope.
		if len(filtered.UsageScopes) == 0 && authoritativeProviderAccounting(ev.Accounting) && !explicitScopeMetadata {
			for _, scope := range ev.UsageScopes {
				scope.Accounting = ev.Accounting
				scope.UsagePresence = scope.UsagePresence.Union(ev.UsagePresence)
				filtered.UsageScopes = append(filtered.UsageScopes, scope)
			}
		}
		if len(filtered.UsageScopes) == 0 {
			continue
		}
		if !authoritativeProviderAccounting(ev.Accounting) {
			filtered.CostNanoUnits = 0
			filtered.Currency = ""
			filtered.CostSource = ""
			filtered.CostPresent = false
		}
		out = append(out, filtered)
	}
	return out
}

func mergeUsageEventsForClient(events []lipapi.Event, skipProviderBillable bool) lipapi.Event {
	out := lipapi.Event{UsageScopes: []lipapi.ScopedUsageDelta{}}
	found := false
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		included := false
		if len(ev.UsageScopes) > 0 {
			for _, scope := range ev.UsageScopes {
				if skipProviderBillable && scope.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
					continue
				}
				out.UsageScopes = append(out.UsageScopes, scope)
				included = true
			}
		} else {
			if skipProviderBillable && ev.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
				continue
			}
			out.UsageScopes = append(out.UsageScopes, lipapi.ScopedUsageDelta{
				InputTokens:      ev.InputTokens,
				OutputTokens:     ev.OutputTokens,
				CacheReadTokens:  ev.CacheReadTokens,
				CacheWriteTokens: ev.CacheWriteTokens,
				ReasoningTokens:  ev.ReasoningTokens,
				TotalTokens:      ev.TotalTokens,
				UsagePresence:    ev.UsagePresence,
				Accounting:       ev.Accounting,
			})
			included = true
		}
		if !included {
			continue
		}
		found = true
		out.CostNanoUnits += ev.CostNanoUnits
		out.CostPresent = out.CostPresent || ev.CostPresent
		if ev.Currency != "" {
			out.Currency = ev.Currency
		}
		if ev.CostSource != "" {
			out.CostSource = ev.CostSource
		}
		if ev.RawUsageJSON != "" {
			out.RawUsageJSON = ev.RawUsageJSON
		}
	}
	if len(out.UsageScopes) > 0 {
		projectAggregatedUsageCounters(&out)
	}
	if !found {
		return lipapi.Event{}
	}
	out.Kind = lipapi.EventUsageDelta
	return out
}

// projectAggregatedUsageCounters copies per-unit totals from every included
// scope onto the event top-level fields used by settlement. Presence is the
// union across scopes so a unit reported only in a later scope still settles.
func projectAggregatedUsageCounters(out *lipapi.Event) {
	if out == nil || len(out.UsageScopes) == 0 {
		return
	}
	var (
		input, output, cacheRead, cacheWrite, reasoning, total int
		presence                                               lipapi.UsagePresence
		accounting                                             lipapi.UsageAccountingMetadata
		haveAccounting                                         bool
	)
	for _, scope := range out.UsageScopes {
		input += scope.InputTokens
		output += scope.OutputTokens
		cacheRead += scope.CacheReadTokens
		cacheWrite += scope.CacheWriteTokens
		reasoning += scope.ReasoningTokens
		total += scope.TotalTokens
		presence = presence.Union(scope.UsagePresence)
		if !haveAccounting || (!authoritativeProviderAccounting(accounting) && authoritativeProviderAccounting(scope.Accounting)) {
			accounting = scope.Accounting
			haveAccounting = true
		}
	}
	out.InputTokens = input
	out.OutputTokens = output
	out.CacheReadTokens = cacheRead
	out.CacheWriteTokens = cacheWrite
	out.ReasoningTokens = reasoning
	out.TotalTokens = total
	out.UsagePresence = presence
	out.Accounting = accounting
}

func tokenAccountingHasProviderUsage(events []lipapi.Event) bool {
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if ev.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
			return true
		}
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
				return true
			}
		}
	}
	return false
}

func (s *retryRecvStream) recordTokenAccountingLedger(ctx context.Context, events []lipapi.Event, unavailableReason, failureReason string, duration time.Duration) error {
	if s == nil || s.executor == nil || s.executor.Ledger == nil {
		return nil
	}
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		scopes := ev.UsageScopes
		if len(scopes) == 0 {
			scopes = []lipapi.ScopedUsageDelta{{
				InputTokens:      ev.InputTokens,
				OutputTokens:     ev.OutputTokens,
				CacheReadTokens:  ev.CacheReadTokens,
				CacheWriteTokens: ev.CacheWriteTokens,
				ReasoningTokens:  ev.ReasoningTokens,
				TotalTokens:      ev.TotalTokens,
				UsagePresence:    ev.UsagePresence,
				Accounting:       ev.Accounting,
			}}
		}
		for _, scope := range scopes {
			if scope.Accounting.Plane == lipapi.UsagePlaneUnknown {
				continue
			}
			record := accountingledger.Record{
				RequestID:         strings.TrimSpace(s.baseline.ID),
				AttemptID:         strings.TrimSpace(s.bleg.BLegID),
				Backend:           strings.TrimSpace(s.cand.Primary.Backend),
				Model:             strings.TrimSpace(s.cand.Primary.Model),
				Plane:             scope.Accounting.Plane,
				InputTokens:       scope.InputTokens,
				OutputTokens:      scope.OutputTokens,
				CacheReadTokens:   scope.CacheReadTokens,
				CacheWriteTokens:  scope.CacheWriteTokens,
				ReasoningTokens:   scope.ReasoningTokens,
				TotalTokens:       scope.TotalTokens,
				Metadata:          scope.Accounting,
				CreatedAt:         s.now(),
				UnavailableReason: unavailableReason,
				FailureReason:     failureReason,
			}
			if record.RequestID == "" {
				record.RequestID = strings.TrimSpace(s.traceID)
			}
			if err := s.executor.Ledger.Record(ctx, record); err != nil {
				if s.executor.Log != nil {
					s.executor.Log.DebugContext(ctx, "token accounting ledger record", "error", err)
				}
				s.recordTokenAccountingObservation(ctx, record, err, duration)
				return err
			}
			s.recordTokenAccountingObservation(ctx, record, nil, duration)
		}
	}
	return nil
}

func (s *retryRecvStream) recordPartialTokenAccounting(ctx context.Context, reason string, err error) {
	s.recordPartialTokenAccountingLedger(ctx, reason, err)
	events := tokenAccountingUsageEvents(s.seenEvents)
	usageEv := authorityUsageEvent(events)
	s.authority.Settle(ctx, authorityapp.SettlementKindPartial, usageEv, false)
	s.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindPartial, usageEv)
	s.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeFailed, metering.SurfacedYes, usageEv)
	if s.isCommitted() {
		s.emitFrontendEgressMeteringFact(ctx, usageEv)
	}
}

func (s *retryRecvStream) recordPartialTokenAccountingLedger(ctx context.Context, reason string, err error) {
	if s == nil || s.executor == nil || s.executor.Ledger == nil {
		return
	}
	events := tokenAccountingUsageEvents(s.seenEvents)
	if len(events) == 0 {
		return
	}
	duration := s.now().Sub(s.accounting.requestStartedAt)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	_ = s.recordTokenAccountingLedger(ctx, events, reason, reason, duration)
}

func (s *retryRecvStream) recordTokenAccountingObservation(ctx context.Context, record accountingledger.Record, err error, duration time.Duration) {
	if s == nil || s.executor == nil || s.executor.TokenAccountingObservability == nil {
		return
	}
	obs, err := accountingobs.NewObservation(accountingobs.Input{
		Labels: accountingobs.Labels{
			Backend:   record.Backend,
			Model:     record.Model,
			Plane:     accountingobs.Plane(record.Plane),
			Source:    accountingobs.Source(record.Metadata.Source),
			Authority: accountingobs.Authority(record.Metadata.Authority),
		},
		Status:            observationStatus(record, err),
		UnavailableReason: record.UnavailableReason,
		Err:               err,
		Duration:          duration,
		OccurredAt:        record.CreatedAt,
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "token accounting observation", "error", err)
		}
		return
	}
	s.executor.TokenAccountingObservability.Record(obs)
}

func observationStatus(record accountingledger.Record, err error) accountingobs.Status {
	if err != nil || record.FailureReason != "" {
		return accountingobs.StatusUnavailable
	}
	return accountingobs.StatusSuccess
}

func tokenAccountingUsageEvents(events []lipapi.Event) []lipapi.Event {
	out := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			out = append(out, ev)
		}
	}
	return out
}

func (s *retryRecvStream) rememberClientEvent(ev lipapi.Event) {
	if s == nil {
		return
	}
	if ev.Kind == lipapi.EventResponseFinished {
		for _, seen := range s.seenEvents {
			if seen.Kind == lipapi.EventResponseFinished {
				return
			}
		}
	}
	if ev.Kind == lipapi.EventTextDelta {
		s.visibleText.WriteString(ev.Delta)
	}
	s.seenEvents = append(s.seenEvents, ev)
}
