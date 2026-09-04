package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
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
func (t *turnTerminal) persistCancellationBilling(ctx context.Context, attempt *attemptSession, reason string, request requestTerminalFacts, p *responsePipeline) {
	ctx = request.toRecvTurnFacts(ctx).projectContext(ctx, nil)
	if t == nil || attempt == nil {
		return
	}
	if attempt.accounting.usageObserved || t.finalizeBillingAfterCancel(ctx, attempt, reason, request, p) {
		t.reconcileOrSettleCancellationAuthorityForAttempt(ctx, attempt, p)
	} else {
		t.settleCancellationAuthorityForAttempt(ctx, attempt, p)
	}
	t.finishCancellationAuthorityForAttempt(ctx, attempt, request, p)
}

func (t *turnTerminal) finishCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession, request requestTerminalFacts, p *responsePipeline) {
	if t == nil || attempt == nil {
		return
	}
	attempt.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindCancellation, p.operatorUsageForFinalize())
	t.settleOrReleaseRequestAuthority(ctx, p, request)
}

// reconcileOrSettleCancellationAuthority routes the cancellation settlement based
// on whether the reservation is already settled. When already settled AND
// authoritative usage is available (the caller guarantees usageObserved or
// finalizeBilling succeeded), it calls ReconcileAuthoritative to adjust the prior
// estimated settlement with the authoritative usage event. When not yet settled,
// it routes to settleCancellationAuthority which settles as a Cancellation.
func (t *turnTerminal) reconcileOrSettleCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil {
		return
	}
	if attempt.authority.Settled() {
		attempt.authority.ReconcileAuthoritative(ctx, p.operatorUsageForFinalize())
		return
	}
	t.settleCancellationAuthorityForAttempt(ctx, attempt, p)
}

// settleCancellationAuthority settles the usage-authority reservation for a canceled
// attempt with the observed usage as a Cancellation. It is a no-op when the
// reservation is already settled (preventing a double settle of a strict
// reservation, e.g. after a prior partial/final settle). The losing-attempt
// release (ReleaseKindLosing when the settle fails) now lives inside the authorityLifecycle
// owner's Settle, mirroring the finalizeResponseFinishedAuthority path. It passes
// a non-canceled context to Settle so cancellation of the client request does not
// abort the post-output settlement (requirement 11.7).
func (t *turnTerminal) settleCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || attempt.authority.Settled() {
		return
	}
	usageEv := p.operatorUsageForFinalize()
	attempt.authority.Settle(ctx, authorityapp.SettlementKindCancellation, usageEv, true)
	t.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeCanceled, metering.SurfacedNo, usageEv)
}

func (t *turnTerminal) finalizeBillingAfterCancel(ctx context.Context, attempt *attemptSession, reason string, request requestTerminalFacts, p *responsePipeline) bool {
	if t == nil || t.finalizeBilling == nil {
		return false
	}
	if attempt == nil {
		return false
	}
	billingState := request.billingState
	if billingState == nil {
		billingState = attempt.billingCallState
	}
	if billingState == nil {
		return false
	}
	traceID := strings.TrimSpace(request.traceID)
	if traceID == "" {
		traceID = strings.TrimSpace(attempt.traceID)
	}
	aLegID := strings.TrimSpace(request.aLegID)
	if aLegID == "" {
		aLegID = strings.TrimSpace(attempt.bleg.ALegID)
	}
	ev, ok := billingState.finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		TraceID: traceID,
		ALegID:  aLegID,
		BLegID:  strings.TrimSpace(attempt.bleg.BLegID),
		Backend: strings.TrimSpace(attempt.cand.Primary.Backend),
		Model:   strings.TrimSpace(attempt.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	}, func(cctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
		return t.finalizeBilling(cctx, in)
	})
	if !ok {
		return false
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingFinalizeTimeout)
	defer cancel()
	attempt.observeAccountingUsage(ev)
	p.rememberClientEvent(ev)
	recording := p.recordClientFacingTerminal(persistCtx, request, attempt, ev, t.committed())
	if recording.err != nil && p.log != nil {
		p.log.DebugContext(persistCtx, "secure_session billing finalizer marker", "error", recording.err)
	}
	p.emitUsageTerminal(persistCtx, request, attempt, ev)
	return true
}

func (t *turnTerminal) finalizeTokenAccounting(ctx context.Context, attempt *attemptSession, finish lipapi.Event, request requestTerminalFacts, p *responsePipeline) (lipapi.Event, bool, error) {
	if t == nil || p == nil {
		return lipapi.Event{}, false, nil
	}
	if attempt == nil {
		return lipapi.Event{}, false, nil
	}
	if p.streamUsage == nil {
		p.setLastAuthorityUsage(lipapi.Event{})
		attempt.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	events := append(p.seenEventsCopy(), finish)
	result, err := p.streamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    strings.TrimSpace(attempt.cand.Primary.Backend),
		Model:      strings.TrimSpace(attempt.cand.Primary.Model),
		Call:       request.call,
		OutputText: p.releasedOutputText(),
		Events:     events,
	})
	if err != nil && p.log != nil {
		p.log.DebugContext(ctx, "token accounting stream reconstruction", "error", err)
	}
	if len(result.Events) == 0 {
		p.setLastAuthorityUsage(lipapi.Event{})
		attempt.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	authorityEv := authorityUsageEvent(result.Events)
	clientUsageEv := mergeUsageEventsForClient(result.Events, tokenAccountingHasProviderUsage(p.seenEventsCopy()))
	// Strip any residual monetary fields: protocol usage is a read-side projection
	// only. Customer/operator money is owned exclusively by sealed current-record rating.
	clientUsageEv.CostNanoUnits = 0
	clientUsageEv.Currency = ""
	clientUsageEv.CostSource = ""
	clientUsageEv.CostPresent = false
	p.setLastAuthorityUsage(authorityEv)
	p.setLastCustomerUsage(customerPlaneUsageEvent(clientUsageEv))
	// The legacy token ledger is intentionally not written here. Client-visible
	// usage remains a protocol/read-side projection; monetary settlement is owned
	// by the sealed current-record post-usage processor.
	attempt.authority.Settle(ctx, authorityapp.SettlementKindFinal, authorityEv, false)
	return clientUsageEv, true, nil
}

// finalizeResponseFinishedAuthority is the single authority-finalization chokepoint for
// response_finished completion paths. It runs token-accounting finalization, which settles
// the usage-authority reservation via the authorityLifecycle owner (the owner folds the
// losing-attempt release into Settle, so a failed settle releases ReleaseKindLosing and
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
func (t *turnTerminal) finalizeResponseFinishedAuthority(ctx context.Context, ev lipapi.Event, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, continuation ...func(context.Context, terminaldecision.ContinuationIntent) (bool, error)) (lipapi.Event, bool, error) {
	if attempt == nil || t == nil || p == nil {
		return lipapi.Event{}, false, nil
	}
	if t.accountingFinalized() && t.requestTerminal().Owner().State().IsTerminal() {
		return lipapi.Event{}, false, nil
	}
	snapshot := p.accumulatorSnapshot()
	decision := t.sharedTerminalDecision(ctx, t.terminalDecisionProvider, t.terminalDecisionInput(sdkterminal.CommandNormalFinish, request, attempt, p, snapshot))
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		if len(continuation) == 0 || continuation[0] == nil || decision.Decision.Continue == nil {
			// Callers without a receive transaction preserve the provisional
			// behavior until they can supply the generic publication boundary.
			return lipapi.Event{}, false, nil
		}
		published, err := continuation[0](ctx, *decision.Decision.Continue)
		if published {
			if err != nil {
				return lipapi.Event{}, false, fmt.Errorf("%w: %v", errTerminalDecisionContinuationPublished, err)
			}
			return lipapi.Event{}, false, errTerminalDecisionContinuationPublished
		}
		return lipapi.Event{}, false, err
	}
	terminalCommand := sdkterminal.CommandNormalFinish
	terminalIntent := IntentSuccess
	if decision.Decision.Kind == terminaldecision.DecisionSurfaceFailure {
		terminalCommand = sdkterminal.CommandPartialError
		terminalIntent = IntentSurfacedFailure
	}
	// Token accounting and observe must happen inside the attempt terminal winner
	// so concurrent losers wait for the winner's effects via streamTerminal.
	var preparedUsageEv lipapi.Event
	var preparedAuthorityEv lipapi.Event
	var preparedOK bool
	var preparedErr error
	evidence := attemptEvidence{
		Command:        terminalCommand,
		LegOutcome:     billing.LegOutcomeWinner,
		Usage:          lipapi.Event{},
		ObsOutcome:     response.OutcomeSuccessReleased,
		TraceID:        request.traceID,
		ALegID:         request.aLegID,
		Snapshot:       &snapshot,
		RecordOutcome:  lipapi.AttemptSuccess,
		StartedAt:      attempt.accountingStartedAt(),
		StreamFallback: p.billingEvidenceFallback(),
		BillingState:   request.billingState,
		BillingCallID:  request.billingCallID,
		Committed:      t.committed(),
		ObserveEvent:   &ev,
		AuthorityPrepare: func(cctx context.Context) (lipapi.Event, lipapi.Event, bool, error) {
			if !t.claimAccountingFinalization() {
				return lipapi.Event{}, lipapi.Event{}, false, nil
			}
			usageEv, ok, err := t.finalizeTokenAccounting(cctx, attempt, ev, request, p)
			if err != nil {
				t.unclaimAccountingFinalization()
				return lipapi.Event{}, lipapi.Event{}, false, err
			}
			authorityEv := p.lastAuthorityUsageSnapshot()
			if authorityEv.Kind == "" {
				authorityEv = usageEv
			}
			preparedUsageEv = usageEv
			preparedAuthorityEv = authorityEv
			preparedOK = ok
			// Do not settle request authority here; it will be done in the request winner
			return usageEv, authorityEv, ok, nil
		},
	}
	if terminalIntent == IntentSurfacedFailure {
		evidence.LegOutcome = billing.LegOutcomeFailed
		evidence.ObsOutcome = response.OutcomeFailed
		evidence.RecordOutcome = lipapi.AttemptSurfacedFailure
	}
	resOuter := attempt.TerminalizeAttempt(ctx, terminalIntent, evidence)
	if !resOuter.Result.Won {
		return lipapi.Event{}, false, terminalLossError(resOuter.Result)
	}
	if resOuter.Result.Err != nil {
		return preparedUsageEv, preparedOK, resOuter.Result.Err
	}
	// Use the prepared authorityEv for request settlement; if Prepare didn't run (loser), use evidence.Usage
	authorityEv := preparedAuthorityEv
	if authorityEv.Kind == "" {
		authorityEv = preparedUsageEv
	}
	// Also need to handle the case where Prepare error was already propagated
	if preparedErr != nil {
		return preparedUsageEv, preparedOK, preparedErr
	}
	// Request authority settlement for non-thinker is now handled inside the attempt terminal winner via typed seams;
	// for thinker the attempt-only path keeps request open, so we still need to handle request-side effects here if needed.
	// However billing leg is now owned by the attempt terminal winner, so we only handoff the call closure here.
	var r terminal.Result
	if t.isInterleavedThinker() {
		r = terminal.Result{Won: true, Outcome: terminal.Outcome{Command: terminalCommand}, State: sdkterminal.StateReleased}
	} else {
		r = t.claimRequestTerminal(ctx, terminalCommand, snapshot, func(cctx context.Context, _ terminal.Outcome) error {
			if err := t.settleRequestAuthorityWithFrontendEgress(cctx, authorityEv, request, p); err != nil {
				return err
			}
			t.handoffBillingTurn(cctx, request, terminalCommand)
			return nil
		})
	}
	if !r.Won {
		// Another exit path already terminalized; surface cancel/error consistently.
		return lipapi.Event{}, false, terminalLossError(r)
	}
	if r.Err != nil {
		return preparedUsageEv, preparedOK, r.Err
	}
	return preparedUsageEv, preparedOK, nil
}

// settleRequestAuthorityWithFrontendEgress emits the frontend-egress fact for the
// delivered/committed customer usage and passes that fact into request settlement
// for non-money quota/lease coordination (4.2). Monetary rating is exclusively a
// post-usage current-record concern and is never attached here. Durable-pending and
// durable-intent-rejected errors are returned so stream terminal effects fail
// truthfully (Phase 4.5 / D9).
func (t *turnTerminal) settleRequestAuthorityWithFrontendEgress(ctx context.Context, usageEv lipapi.Event, request requestTerminalFacts, p *responsePipeline) error {
	if t == nil {
		return nil
	}
	if !p.markCustomerSettled() {
		return nil
	}
	customerEv := p.resolveCustomerUsageForTerminal(ctx, usageEv, request)
	var egressFacts []metering.Fact
	var fact metering.Fact
	var persisted bool
	if t.emitFrontendEgress != nil {
		fact, persisted = t.emitFrontendEgressMeteringFact(ctx, request.traceID, customerEv)
	}
	if persisted {
		egressFacts = []metering.Fact{fact}
	} else if t.meteringRecorderPresent {
		// Required settlement evidence was not persisted. Keep request authority
		// open so a later terminal/reconciliation attempt can retry the append.
		p.unmarkCustomerSettled()
		return fmt.Errorf("%w: frontend egress fact not persisted", terminalworkapp.ErrDurableIntentRejected)
	}
	if t.settleRequestAuthority == nil {
		return nil
	}
	// Monetary rating is exclusively a post-usage current-record concern. Runtime
	// settlement receives only the non-money authority/egress evidence.
	err := t.settleRequestAuthority(ctx, egressFacts)
	if request.requestAuth != nil && !request.requestAuth.Settled {
		// Provider settlement failed: keep customer once-only open for retry.
		p.unmarkCustomerSettled()
	}
	return err
}

// Customer FE quantities are reconstructed by responsePipeline from released
// accumulator content through StreamUsage.Reconstruct / CountOutput. Provider-
// preferring usageEv scopes are never imported; they only seed an empty shell
// when no customer evidence can be reconstructed.
// reconstructCustomerUsageForResponse reconstructs client-visible usage from
// released response evidence. It is a callback target for responsePipeline so
// provider/runtime adapters remain outside that owner.
func reconstructCustomerUsageForResponse(ctx context.Context, streamUsage *accountingstream.Reconstructor, log *slog.Logger, facts recvTurnFacts, attempt *attemptSession, text string, events []lipapi.Event) lipapi.Event {
	if streamUsage == nil {
		return lipapi.Event{}
	}
	call := facts.baseline
	var backend, model string
	if attempt != nil {
		backend = strings.TrimSpace(attempt.cand.Primary.Backend)
		model = strings.TrimSpace(attempt.cand.Primary.Model)
	}
	if holder := facts.metering; holder != nil && holder.FrontendIngress != nil && holder.FrontendIngress.Call.ID != "" {
		call = holder.FrontendIngress.Call
	}
	result, err := streamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend: backend, Model: model, Call: call, OutputText: text, Events: events,
	})
	if err != nil && log != nil {
		log.DebugContext(ctx, "customer stream usage reconstruction", "error", err)
	}
	out := customerPlaneUsageEvent(mergeUsageEventsForClient(result.Events, true))
	if out.Kind == "" {
		return lipapi.Event{}
	}
	return applyFrontendIngressInput(facts.metering, out)
}

func applyFrontendIngressInput(holder *checkpoint.RequestHolder, ev lipapi.Event) lipapi.Event {
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

func (t *turnTerminal) recordPartialTokenAccounting(ctx context.Context, attempt *attemptSession, reason string, err error, request requestTerminalFacts, p *responsePipeline) {
	if t == nil || attempt == nil {
		return
	}
	// Keep non-money attempt/request coordination only. Do not write the legacy
	// token ledger or settle monetary exposure from stream usage.
	usageEv := p.operatorUsageForFinalize()
	attempt.authority.Settle(ctx, authorityapp.SettlementKindPartial, usageEv, false)
	attempt.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindPartial, usageEv)
	t.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeFailed, metering.SurfacedYes, usageEv)
	if t.committed() {
		_ = t.settleRequestAuthorityWithFrontendEgress(ctx, p.usageEvidenceOrEmpty(), request, p)
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
