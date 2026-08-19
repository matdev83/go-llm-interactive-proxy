package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// persistCancellationBilling settles non-money usage-authority reservations for a
// canceled attempt. Monetary cancellation work belongs exclusively to post-usage
// current call/leg rating after terminal handoff.
//
// Evidence recovery is first: a mid-stream EventUsageDelta is enough, otherwise
// FinalizeBilling may recover provider evidence (shared with call-leg usage via finalizeOnce).
// Authoritative evidence reconciles an already-settled reservation (requirement
// 7.6, 8.4-8.6); without it the path only settles Cancellation and never
// re-opens a prior Partial/Final. One tail then applies advisory usage and
// request settle/release. Settlement uses a non-canceled context so post-output
// accounting completes after client cancellation (requirement 11.7).
func (s *retryRecvStream) persistCancellationBilling(ctx context.Context, attempt *attemptSession, reason string) {
	if s == nil {
		return
	}
	if attempt == nil {
		return
	}
	if attempt.accounting.usageObserved || s.finalizeBillingAfterCancel(ctx, attempt, reason) {
		s.reconcileOrSettleCancellationAuthorityForAttempt(ctx, attempt)
	} else {
		s.settleCancellationAuthorityForAttempt(ctx, attempt)
	}
	s.finishCancellationAuthorityForAttempt(ctx, attempt)
}

// finishCancellationAuthority is the single cancellation tail: advisory usage
// apply, then request-authority settle or unused-hold release.
func (s *retryRecvStream) finishCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession) {
	if s == nil || attempt == nil {
		return
	}
	if s.executor != nil {
		ctx = s.facts.projectContext(ctx, s.executor.Log)
	}
	attempt.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindCancellation, s.operatorUsageForFinalize())
	if s.isCommitted() {
		_ = s.settleRequestAuthorityWithFrontendEgress(ctx, s.usageEvidenceOrEmpty())
		return
	}
	if s.executor != nil {
		_ = s.executor.releaseRequestAuthority(ctx)
	}
}

// reconcileOrSettleCancellationAuthority routes the cancellation settlement based
// on whether the reservation is already settled. When already settled AND
// authoritative usage is available (the caller guarantees usageObserved or
// finalizeBilling succeeded), it calls ReconcileAuthoritative to adjust the prior
// estimated settlement with the authoritative usage event. When not yet settled,
// it falls back to settleCancellationAuthority which settles as a Cancellation.
func (s *retryRecvStream) reconcileOrSettleCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession) {
	if s == nil || attempt == nil {
		return
	}
	if attempt.authority.Settled() {
		attempt.authority.ReconcileAuthoritative(ctx, s.operatorUsageForFinalize())
		return
	}
	s.settleCancellationAuthorityForAttempt(ctx, attempt)
}

// settleCancellationAuthority settles the usage-authority reservation for a canceled
// attempt with the observed usage as a Cancellation. It is a no-op when the
// reservation is already settled (preventing a double settle of a strict
// reservation, e.g. after a prior partial/final settle). The losing-fallback
// (ReleaseKindLosing when the settle fails) now lives inside the authorityLifecycle
// owner's Settle, mirroring the finalizeResponseFinishedAuthority path. It passes
// a non-canceled context to Settle so cancellation of the client request does not
// abort the post-output settlement (requirement 11.7).
func (s *retryRecvStream) settleCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession) {
	if s == nil || attempt == nil || attempt.authority.Settled() {
		return
	}
	usageEv := s.operatorUsageForFinalize()
	attempt.authority.Settle(ctx, authorityapp.SettlementKindCancellation, usageEv, true)
	s.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeCanceled, metering.SurfacedNo, usageEv)
}

func (s *retryRecvStream) finalizeBillingAfterCancel(ctx context.Context, attempt *attemptSession, reason string) bool {
	if s == nil || s.executor == nil {
		return false
	}
	if attempt == nil {
		return false
	}
	ev, ok := s.facts.billingCallState.finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(s.facts.traceID),
		ALegID:  strings.TrimSpace(s.facts.aLegID),
		BLegID:  strings.TrimSpace(attempt.bleg.BLegID),
		Backend: strings.TrimSpace(attempt.cand.Primary.Backend),
		Model:   strings.TrimSpace(attempt.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	}, func(cctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
		return s.executor.callFinalizeBilling(cctx, in)
	})
	if !ok {
		return false
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingFinalizeTimeout)
	defer cancel()
	attempt.accounting.observeUsage(ev)
	s.rememberClientEvent(ev)
	if recErr := s.beforeEmitClientFacing(persistCtx, ev); recErr != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session billing finalizer marker", "error", recErr)
	}
	s.emitUsageForAttempt(persistCtx, attempt, ev)
	return true
}

func (s *retryRecvStream) emitUsage(ctx context.Context, ev lipapi.Event) {
	if s == nil {
		return
	}
	s.emitUsageForAttempt(ctx, s.attempt.snapshot(), ev)
}

func (s *retryRecvStream) emitUsageForAttempt(ctx context.Context, attempt *attemptSession, ev lipapi.Event) {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil || ev.Kind != lipapi.EventUsageDelta {
		return
	}
	obs := s.executor.RuntimeSnapshot.UsageObserver()
	if obs == nil {
		return
	}
	if attempt == nil {
		return
	}
	principalID := ""
	scopeView := scopeFromCtx(ctx)
	if scopeView.PrincipalID.IsKnown() {
		principalID = strings.TrimSpace(scopeView.PrincipalID.String())
	}
	model := ""
	if attempt.cand.Primary.Model != "" {
		model = attempt.cand.Primary.Model
	}
	if err := obs.OnUsage(ctx, usage.Event{
		TraceID:          strings.TrimSpace(s.facts.traceID),
		ALegID:           strings.TrimSpace(s.facts.aLegID),
		BLegID:           strings.TrimSpace(attempt.bleg.BLegID),
		PrincipalID:      strings.TrimSpace(principalID),
		SessionID:        strings.TrimSpace(s.facts.baseline.Session.CorrelationID()),
		AttemptSeq:       int(attempt.bleg.Seq),
		BackendID:        strings.TrimSpace(attempt.cand.Primary.Backend),
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
	s.attempt.require().accounting.observeClientEvent(s.now(), ev)
	if s.recovery != nil && s.recovery.recoverPolicy != nil {
		s.recovery.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	pm, _ := s.recvHookMeta()
	out, err := s.emitClientFacingObserved(ctx, ev, pm)
	if err != nil {
		return lipapi.Event{}, err
	}
	s.emitUsage(ctx, out)
	return out, nil
}

func (s *retryRecvStream) finalizeTokenAccounting(ctx context.Context, attempt *attemptSession, finish lipapi.Event) (lipapi.Event, bool, error) {
	if s == nil || s.executor == nil {
		return lipapi.Event{}, false, nil
	}
	if attempt == nil {
		return lipapi.Event{}, false, nil
	}
	if s.executor.StreamUsage == nil {
		s.lastAuthorityUsage = lipapi.Event{}
		attempt.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	events := append(s.seenEventsCopy(), finish)
	result, err := s.executor.StreamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    strings.TrimSpace(attempt.cand.Primary.Backend),
		Model:      strings.TrimSpace(attempt.cand.Primary.Model),
		Call:       s.facts.baseline,
		OutputText: s.releasedOutputText(),
		Events:     events,
	})
	if err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "token accounting stream reconstruction", "error", err)
	}
	if len(result.Events) == 0 {
		s.lastAuthorityUsage = lipapi.Event{}
		attempt.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	authorityEv := authorityUsageEvent(result.Events)
	clientUsageEv := mergeUsageEventsForClient(result.Events, tokenAccountingHasProviderUsage(s.seenEventsCopy()))
	// Strip any residual monetary fields: protocol usage is a read-side projection
	// only. Customer/operator money is owned exclusively by sealed current-record rating.
	clientUsageEv.CostNanoUnits = 0
	clientUsageEv.Currency = ""
	clientUsageEv.CostSource = ""
	clientUsageEv.CostPresent = false
	s.lastAuthorityUsage = authorityEv
	s.lastCustomerUsage = customerPlaneUsageEvent(clientUsageEv)
	// The legacy token ledger is intentionally not written here. Client-visible
	// usage remains a protocol/read-side projection; monetary settlement is owned
	// by the sealed current-record post-usage processor.
	attempt.authority.Settle(ctx, authorityapp.SettlementKindFinal, authorityEv, false)
	return clientUsageEv, true, nil
}

// finalizeResponseFinishedAuthority is the single authority-finalization chokepoint for
// response_finished completion paths. It runs token-accounting finalization, which settles
// the usage-authority reservation via the authorityLifecycle owner (the owner folds the
// losing-fallback release into Settle, so a failed settle releases ReleaseKindLosing and
// marks the lifecycle settled). Idempotent via the turn terminal's request-level
// accounting-finalized claim (which gates
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
	attempt := s.attempt.snapshot()
	if attempt == nil || s.terminal == nil {
		return lipapi.Event{}, false, nil
	}
	if s.terminal.accountingFinalized() && s.terminal.requestTerminal().Owner().State().IsTerminal() {
		// The winning completion path already emitted/requeued the synthesized
		// usage event. A later drain observation must not emit it again.
		return lipapi.Event{}, false, nil
	}
	var usageEv lipapi.Event
	var ok bool
	var err error
	effects := func(cctx context.Context) error {
		if !s.terminal.claimAccountingFinalization() {
			return nil
		}
		usageEv, ok, err = s.finalizeTokenAccounting(cctx, attempt, ev)
		if err != nil {
			s.terminal.unclaimAccountingFinalization()
			return err
		}
		authorityEv := s.lastAuthorityUsage
		if authorityEv.Kind == "" {
			authorityEv = usageEv
		}
		attempt.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindFinal, authorityEv)
		s.emitBackendEgressMeteringFactForAttempt(cctx, attempt, metering.AttemptOutcomeWinner, metering.SurfacedYes, authorityEv)
		if s.isInterleavedThinker {
			return nil
		}
		return s.settleRequestAuthorityWithFrontendEgress(cctx, authorityEv)
	}
	var r terminal.Result
	if s.isInterleavedThinker {
		r = attempt.terminalizeSnapshot(ctx, sdkterminal.CommandNormalFinish, s.accumulatorSnapshot(), func(cctx context.Context, _ terminal.Outcome) error {
			err := effects(cctx)
			s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandNormalFinish)
			return err
		})
	} else {
		r = s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandNormalFinish, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ terminal.Outcome) error {
			return effects(cctx)
		}, func(cctx context.Context, _ terminal.Outcome) error {
			s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandNormalFinish)
			s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandNormalFinish)
			return nil
		})
	}
	if !r.Won {
		// Another exit path already terminalized; surface cancel/error consistently.
		return lipapi.Event{}, false, terminalLossError(r)
	}
	if r.Err != nil {
		return usageEv, ok, r.Err
	}
	return usageEv, ok, err
}

// settleRequestAuthorityWithFrontendEgress emits the frontend-egress fact for the
// delivered/committed customer usage and passes that fact into request settlement
// for non-money quota/lease coordination (4.2). Monetary rating is exclusively a
// post-usage current-record concern and is never attached here. Durable-pending and
// durable-intent-rejected errors are returned so stream terminal effects fail
// truthfully (Phase 4.5 / D9).
func (s *retryRecvStream) settleRequestAuthorityWithFrontendEgress(ctx context.Context, usageEv lipapi.Event) error {
	if s == nil {
		return nil
	}
	if s.executor != nil {
		ctx = s.facts.projectContext(ctx, s.executor.Log)
	}
	if s.customer != nil && !s.customer.MarkSettled() {
		return nil
	}
	customerEv := s.resolveCustomerUsage(ctx, usageEv)
	var facts []metering.Fact
	var fact metering.Fact
	var persisted bool
	if s.executor != nil {
		fact, persisted = s.executor.emitFrontendEgressMeteringFact(ctx, s.facts.traceID, customerEv)
	}
	if persisted {
		facts = []metering.Fact{fact}
	} else if s.executor != nil && s.executor.MeteringRecorder != nil {
		// Required settlement evidence was not persisted. Keep request authority
		// open so a later terminal/reconciliation attempt can retry the append.
		s.customer.unmarkSettled()
		return fmt.Errorf("%w: frontend egress fact not persisted", terminalworkapp.ErrDurableIntentRejected)
	}
	if s.executor == nil {
		return nil
	}
	// Monetary rating is exclusively a post-usage current-record concern. Runtime
	// settlement receives only the non-money authority/egress evidence.
	err := s.executor.settleRequestAuthority(ctx, facts)
	if s.executor.RequestCoordinator != nil {
		if st := requestAuthorityFrom(ctx); st != nil && !st.Settled {
			// Provider settlement failed: keep customer once-only open for retry.
			s.customer.unmarkSettled()
		}
	}
	return err
}

// resolveCustomerUsage builds customer FE quantities from released accumulator
// content through StreamUsage.Reconstruct / CountOutput. Provider-preferring
// usageEv scopes are never imported; they only seed an empty shell when no
// customer evidence can be reconstructed.
func (s *retryRecvStream) resolveCustomerUsage(ctx context.Context, usageEv lipapi.Event) lipapi.Event {
	if s == nil {
		return customerPlaneUsageEvent(usageEv)
	}
	if ev := s.customerUsageFromReleased(ctx); ev.Kind != "" {
		return ev
	}
	if s.lastCustomerUsage.Kind != "" {
		return s.lastCustomerUsage
	}
	out := customerPlaneUsageEvent(usageEv)
	if out.Kind == "" && usageEv.Kind != "" {
		return lipapi.Event{Kind: lipapi.EventUsageDelta}
	}
	return out
}

// customerUsageFromReleased reconstructs client-visible usage from FE-ingress
// call evidence and accumulator OutputText via the existing StreamUsage seam.
func (s *retryRecvStream) customerUsageFromReleased(ctx context.Context) lipapi.Event {
	if s == nil || s.executor == nil || s.executor.StreamUsage == nil {
		return lipapi.Event{}
	}
	call := s.facts.baseline
	var backend, model string
	if attempt := s.attempt.snapshot(); attempt != nil {
		backend = strings.TrimSpace(attempt.cand.Primary.Backend)
		model = strings.TrimSpace(attempt.cand.Primary.Model)
	}
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil {
		if holder.FrontendIngress.Call.ID != "" {
			call = holder.FrontendIngress.Call
		}
	}
	text := s.releasedOutputText()
	var events []lipapi.Event
	if s.customer != nil {
		events = s.customer.contentEvents()
	}
	result, err := s.executor.StreamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    backend,
		Model:      model,
		Call:       call,
		OutputText: text,
		Events:     events,
	})
	if err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "customer stream usage reconstruction", "error", err)
	}
	out := customerPlaneUsageEvent(mergeUsageEventsForClient(result.Events, true))
	if out.Kind == "" {
		return lipapi.Event{}
	}
	return applyFrontendIngressInput(ctx, out)
}

func applyFrontendIngressInput(ctx context.Context, ev lipapi.Event) lipapi.Event {
	holder := meteringHolderFrom(ctx)
	if holder == nil || holder.FrontendIngress == nil {
		return ev
	}
	in, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken)
	if !ok {
		return ev
	}
	ev.InputTokens = int(in)
	if len(ev.UsageScopes) > 0 {
		scopes := append([]lipapi.ScopedUsageDelta(nil), ev.UsageScopes...)
		for i := range scopes {
			if scopes[i].Accounting.Plane == lipapi.UsagePlaneClientVisible || scopes[i].Accounting.Plane == "" {
				scopes[i].InputTokens = int(in)
				scopes[i].TotalTokens = scopes[i].InputTokens + scopes[i].OutputTokens
			}
		}
		ev.UsageScopes = scopes
	}
	ev.TotalTokens = ev.InputTokens + ev.OutputTokens
	return ev
}

func (s *retryRecvStream) releasedOutputText() string {
	if s == nil {
		return ""
	}
	if s.customer != nil {
		text, _, _, _ := s.customer.Snapshot()
		return text
	}
	return s.visibleText.String()
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

func (s *retryRecvStream) recordPartialTokenAccounting(ctx context.Context, attempt *attemptSession, reason string, err error) {
	if s == nil || attempt == nil {
		return
	}
	// Keep non-money attempt/request coordination only. Do not write the legacy
	// token ledger or settle monetary exposure from stream usage.
	usageEv := s.operatorUsageForFinalize()
	attempt.authority.Settle(ctx, authorityapp.SettlementKindPartial, usageEv, false)
	attempt.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindPartial, usageEv)
	s.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeFailed, metering.SurfacedYes, usageEv)
	if s.isCommitted() {
		_ = s.settleRequestAuthorityWithFrontendEgress(ctx, s.usageEvidenceOrEmpty())
	}
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
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if ev.Kind == lipapi.EventResponseFinished {
		for _, seen := range s.seenEvents {
			if seen.Kind == lipapi.EventResponseFinished {
				return
			}
		}
	}
	if s.customer != nil {
		s.customer.ObserveReleased(ev)
	} else if ev.Kind == lipapi.EventTextDelta {
		s.visibleText.WriteString(ev.Delta)
	}
	if tool, ok := lipapi.ToolEventFromEvent(ev); ok {
		s.committedTools = append(s.committedTools, tool)
	}
	s.seenEvents = append(s.seenEvents, ev)
}

func (s *retryRecvStream) usageEventsSnapshot() []lipapi.Event {
	if s == nil {
		return nil
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return tokenAccountingUsageEvents(append([]lipapi.Event(nil), s.seenEvents...))
}
