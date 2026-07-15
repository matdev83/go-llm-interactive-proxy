package runtime

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// authorityLifecycle owns one admission result's synchronized settle/release
// lifecycle and submits complete reservation-set mutations.
type authorityLifecycle struct {
	svc UsageAuthorityService
	log *slog.Logger
	// attemptCoord settles/releases coordinator-admitted holds through owning providers.
	attemptCoord *authoritycoord.AttemptCoordinator
	// Pointers keep the value-shaped lifecycle copyable for existing stream and
	// race-owner structs without copying sync/atomic noCopy values. The owner
	// created by newAuthorityLifecycle always initializes them.
	control          *authorityLifecycleControl
	backendAttempted *atomic.Bool
	outputCommitted  *atomic.Bool
}

type authorityReservationState struct {
	reservationID  string
	ruleID         string
	reservedAmount domain.Amount
	reservationKey domain.ReservationKey
}

type settlementAuthorityState struct {
	UsageByUnit              map[domain.AmountUnit]domain.AuthorityLevel
	Cost                     domain.AuthorityLevel
	AuthoritativeCostPresent bool
}

type authorityTerminalState uint8

const (
	authorityTerminalOpen authorityTerminalState = iota
	authorityTerminalSettled
	authorityTerminalReleased
)

type authorityLifecycleControl struct {
	mu        sync.Mutex
	state     attemptAuthorityState
	cand      routing.AttemptCandidate
	terminal  authorityTerminalState
	authority settlementAuthorityState
}

const defaultAuthorityCleanupTimeout = 2 * time.Second

func cleanupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultAuthorityCleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// newAuthorityLifecycle builds an owner over the supplied authority service,
// logger, reservation state, and attempt candidate. The owner is inactive until
// IsActive reports true (correlated + reserved admission result).
func newAuthorityLifecycle(svc UsageAuthorityService, log *slog.Logger, state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	lifecycle := authorityLifecycle{
		svc:              svc,
		log:              log,
		control:          &authorityLifecycleControl{state: state, cand: cand, authority: newSettlementAuthorityState()},
		backendAttempted: &atomic.Bool{},
		outputCommitted:  &atomic.Bool{},
	}
	// Most lifecycle owners are created from an opened attempt, so the default
	// is true. The pre-open cleanup owner in tryPlanOpenOnce explicitly resets
	// this until the actual backend Open call begins.
	lifecycle.backendAttempted.Store(true)
	return lifecycle
}

// newAttemptAuthorityLifecycle builds a lifecycle owner wired to both the
// built-in usage-authority service and the attempt coordinator (when present).
func (e *Executor) newAttemptAuthorityLifecycle(state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	if e == nil {
		return newAuthorityLifecycle(nil, nil, state, cand)
	}
	lifecycle := newAuthorityLifecycle(e.authorityService(), e.Log, state, cand)
	lifecycle.attemptCoord = e.AttemptCoordinator
	return lifecycle
}

func (l *authorityLifecycle) settledLoad() bool {
	if l == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	return l.control.terminal != authorityTerminalOpen
}

func newSettlementAuthorityState() settlementAuthorityState {
	return settlementAuthorityState{
		UsageByUnit: make(map[domain.AmountUnit]domain.AuthorityLevel),
		Cost:        domain.AuthorityLevelEstimated,
	}
}

func usageAuthorityForUnit(value settlementAuthorityState, unit domain.AmountUnit) domain.AuthorityLevel {
	if authority := value.UsageByUnit[unit]; authority != "" {
		return authority
	}
	return domain.AuthorityLevelEstimated
}

func (l *authorityLifecycle) markOutputCommitted() {
	if l != nil && l.outputCommitted != nil {
		l.outputCommitted.Store(true)
	}
}

// IsActive reports whether this lifecycle owns a correlated live reservation.
func (l *authorityLifecycle) IsActive() bool {
	if l == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	return l.isActiveLocked()
}

func (l *authorityLifecycle) isActiveLocked() bool {
	return l.control.state.admissionInput.Correlation.TraceID != "" &&
		l.control.state.admissionResult.Reserved
}

// Settled reports whether the reservation has been settled or released.
func (l *authorityLifecycle) Settled() bool {
	return l.settledLoad()
}

func (l *authorityLifecycle) stateSnapshot() attemptAuthorityState {
	if l == nil || l.control == nil {
		return attemptAuthorityState{}
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	return l.control.state
}

func (l *authorityLifecycle) reservationStates() []authorityReservationState {
	if l == nil {
		return nil
	}
	state := l.control.state
	reservations := state.admissionResult.Reservations
	if len(reservations) == 0 {
		reservations = []authorityapp.AdmissionReservation{{
			ReservationID:  state.admissionResult.ReservationID,
			RuleID:         attemptAuthorityRuleID(state),
			ReservedAmount: state.admissionResult.ReservedAmount,
		}}
	}
	out := make([]authorityReservationState, 0, len(reservations))
	for _, reservation := range reservations {
		ruleID := strings.TrimSpace(reservation.RuleID)
		if ruleID == "" {
			ruleID = attemptAuthorityRuleID(state)
		}
		reservedAmount := reservation.ReservedAmount
		if reservedAmount.Unit == "" {
			reservedAmount = state.admissionResult.ReservedAmount
		}
		reservationKey := state.admissionInput.ReservationKey
		reservationKey.RuleID = ruleID
		out = append(out, authorityReservationState{
			reservationID:  strings.TrimSpace(reservation.ReservationID),
			ruleID:         ruleID,
			reservedAmount: reservedAmount,
			reservationKey: reservationKey,
		})
	}
	return out
}

// authorityForSettlement selects the authority level from the usage metadata,
// never from a non-zero token count alone (requirement 8.4-8.6). Only an event
// explicitly marked authoritative on the provider-billable plane is
// authoritative; local client-visible reconstruction remains estimated across
// final, partial, and cancellation paths.
func authorityForSettlement(_ authorityapp.SettlementKind, usageEv lipapi.Event) domain.AuthorityLevel {
	if eventCarriesAuthoritativeProviderUsage(usageEv) {
		return domain.AuthorityLevelAuthoritative
	}
	if eventCarriesUnavailableUsage(usageEv) {
		return domain.AuthorityLevelUnavailable
	}
	return domain.AuthorityLevelEstimated
}

func measurementAuthorityForEvent(ev lipapi.Event) authorityapp.MeasurementAuthority {
	usage := authorityForSettlement(authorityapp.SettlementKindFinal, ev)
	cost := domain.AuthorityLevelEstimated
	switch strings.TrimSpace(ev.CostSource) {
	case string(lipapi.UsageSourceUnavailable):
		cost = domain.AuthorityLevelUnavailable
	case string(lipapi.UsageSourceProviderReported), string(lipapi.UsageSourceProviderCountAPI):
		// A provider source marker without an actual cost field is not an
		// authoritative monetary measurement. Token authority can still be
		// authoritative, but the money reservation remains estimated until a
		// provider-reported/count-API cost is present.
		if costEventPresent(ev) {
			cost = domain.AuthorityLevelAuthoritative
		}
	}
	return authorityapp.MeasurementAuthority{
		Usage:                    usage,
		Cost:                     cost,
		AuthoritativeCostPresent: cost == domain.AuthorityLevelAuthoritative && costEventPresent(ev),
	}
}

func measurementAuthorityForUnit(ev lipapi.Event, unit domain.AmountUnit) authorityapp.MeasurementAuthority {
	authority := measurementAuthorityForEvent(ev)
	if unit != domain.AmountUnitMoneyNano && !attemptAuthorityEventHasUsageForUnit(ev, unit) {
		authority.Usage = domain.AuthorityLevelEstimated
	}
	return authority
}

func authorityRank(value domain.AuthorityLevel) int {
	switch value {
	case domain.AuthorityLevelAuthoritative:
		return 3
	case domain.AuthorityLevelEstimated:
		return 2
	default:
		return 0
	}
}

func measurementAuthorityNeedsUpgradeForAmount(prior settlementAuthorityState, incoming authorityapp.MeasurementAuthority, amount domain.Amount) bool {
	if amount.Unit == domain.AmountUnitMoneyNano {
		if !incoming.AuthoritativeCostPresent || incoming.Cost != domain.AuthorityLevelAuthoritative {
			return false
		}
		return authorityRank(incoming.Cost) > authorityRank(prior.Cost) ||
			(prior.Cost == domain.AuthorityLevelAuthoritative && !prior.AuthoritativeCostPresent)
	}
	return authorityRank(incoming.Usage) > authorityRank(usageAuthorityForUnit(prior, amount.Unit))
}

func mergeMeasurementAuthorityForAmount(prior settlementAuthorityState, incoming authorityapp.MeasurementAuthority, amount domain.Amount) settlementAuthorityState {
	if amount.Unit == domain.AmountUnitMoneyNano {
		if incoming.Cost == domain.AuthorityLevelAuthoritative && incoming.AuthoritativeCostPresent &&
			(authorityRank(incoming.Cost) > authorityRank(prior.Cost) || !prior.AuthoritativeCostPresent) {
			prior.Cost = incoming.Cost
		}
		prior.AuthoritativeCostPresent = prior.AuthoritativeCostPresent || incoming.AuthoritativeCostPresent
		return prior
	}
	if prior.UsageByUnit == nil {
		prior.UsageByUnit = make(map[domain.AmountUnit]domain.AuthorityLevel)
	}
	if authorityRank(incoming.Usage) > authorityRank(usageAuthorityForUnit(prior, amount.Unit)) {
		prior.UsageByUnit[amount.Unit] = incoming.Usage
	}
	return prior
}

func eventCarriesAuthoritativeProviderUsage(ev lipapi.Event) bool {
	if authoritativeProviderAccounting(ev.Accounting) {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if authoritativeProviderAccounting(scope.Accounting) {
			return true
		}
	}
	return false
}

func authoritativeProviderAccounting(accounting lipapi.UsageAccountingMetadata) bool {
	if accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		return false
	}
	if accounting.Plane != lipapi.UsagePlaneProviderBillable {
		return false
	}
	return accounting.Source == lipapi.UsageSourceProviderReported ||
		accounting.Source == lipapi.UsageSourceProviderCountAPI
}

func eventCarriesUnavailableUsage(ev lipapi.Event) bool {
	if ev.Accounting.Authority == lipapi.UsageAuthorityUnavailable || ev.Accounting.Source == lipapi.UsageSourceUnavailable {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if scope.Accounting.Authority == lipapi.UsageAuthorityUnavailable || scope.Accounting.Source == lipapi.UsageSourceUnavailable {
			return true
		}
	}
	return false
}

func (l *authorityLifecycle) settlementInput(kind authorityapp.SettlementKind, usageEv lipapi.Event, clientCanceled bool, reservations []authorityReservationState) authorityapp.SettleInput {
	state := l.control.state
	measurementAuthority := measurementAuthorityForEvent(usageEv)
	input := authorityapp.SettleInput{
		Correlation:          state.admissionInput.Correlation,
		Scope:                state.admissionInput.Scope,
		Kind:                 kind,
		Authority:            measurementAuthority.Usage,
		MeasurementAuthority: measurementAuthority,
		Stage:                feature.StageIDAttemptLifecycle,
		BackendAttempted:     l.backendAttempted != nil && l.backendAttempted.Load(),
		OutputCommitted:      l.outputCommitted != nil && l.outputCommitted.Load(),
		ClientCanceled:       clientCanceled,
		FinalUsagePresent:    usageEventPresent(usageEv),
		FinalCostPresent:     costEventPresent(usageEv),
		// Pin settle to the admission-time snapshot (requirement 11.4).
		BoundVersion:       state.admissionResult.BoundVersion,
		BoundRatingVersion: state.admissionResult.BoundRatingVersion,
	}
	input.Reservations = make([]authorityapp.SettlementDescriptor, 0, len(reservations))
	for _, reservation := range reservations {
		descriptorAuthority := measurementAuthorityForUnit(usageEv, reservation.reservedAmount.Unit)
		input.Reservations = append(input.Reservations, authorityapp.SettlementDescriptor{
			Reservation: authorityapp.ReservationDescriptor{
				RuleID:         reservation.ruleID,
				Unit:           reservation.reservedAmount.Unit,
				Currency:       reservation.reservedAmount.Currency,
				Dimensions:     state.admissionInput.Dimensions,
				ReservationKey: reservation.reservationKey,
				ReservationID:  reservation.reservationID,
				Amount:         reservation.reservedAmount,
			},
			FinalUsage:           attemptAuthorityUsageAmount(usageEv, l.finalUsageEstimate(reservation)),
			FinalCost:            attemptAuthorityCostAmount(usageEv, l.estimatedCostCurrency(reservation)),
			EstimatedUsage:       reservation.reservedAmount,
			EstimatedCost:        l.estimatedCost(reservation),
			Authority:            descriptorAuthority.ForUnit(reservation.reservedAmount.Unit),
			MeasurementAuthority: descriptorAuthority,
		})
	}
	if len(input.Reservations) > 0 {
		first := input.Reservations[0]
		input.Authority = first.Authority
		input.MeasurementAuthority = first.MeasurementAuthority
		input.ReservationKey = first.Reservation.ReservationKey
		input.ReservationID = first.Reservation.ReservationID
		input.RuleID = first.Reservation.RuleID
		input.FinalUsage = first.FinalUsage
		input.FinalCost = first.FinalCost
		input.ReservedUsage = first.Reservation.Amount
		input.EstimatedUsage = first.EstimatedUsage
		input.EstimatedCost = first.EstimatedCost
	}
	return input
}

func usageEventPresent(ev lipapi.Event) bool {
	return ev.Kind == lipapi.EventUsageDelta
}

func costEventPresent(ev lipapi.Event) bool {
	return ev.Kind == lipapi.EventUsageDelta && ev.CostPresent
}

func (l *authorityLifecycle) estimatedCost(reservation authorityReservationState) domain.Amount {
	estimatedCost := l.control.state.admissionInput.Spend
	if reservation.reservedAmount.IsMoney() {
		estimatedCost = reservation.reservedAmount
	}
	return estimatedCost
}

func (l *authorityLifecycle) estimatedCostCurrency(reservation authorityReservationState) string {
	currency := strings.TrimSpace(l.estimatedCost(reservation).Currency)
	if currency == "" {
		currency = strings.TrimSpace(l.control.state.admissionInput.Spend.Currency)
	}
	return currency
}

func (l *authorityLifecycle) finalUsageEstimate(reservation authorityReservationState) domain.Amount {
	estimate := reservation.reservedAmount
	if estimate.Unit == domain.AmountUnitMoneyNano && strings.TrimSpace(estimate.Currency) == "" {
		estimate.Currency = strings.TrimSpace(l.control.state.admissionInput.Request.Currency)
	}
	return estimate
}

// Settle atomically finalizes the reservation set with retryable compensation.
func (l *authorityLifecycle) Settle(ctx context.Context, kind authorityapp.SettlementKind, usageEv lipapi.Event, clientCanceled bool) bool {
	if l == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	if !l.isActiveLocked() {
		return false
	}
	if l.control.terminal == authorityTerminalReleased {
		return false
	}
	if l.control.terminal == authorityTerminalSettled {
		return l.reconcileAuthoritativeLocked(ctx, usageEv)
	}
	if l.control.state.viaCoordinator {
		return l.settleViaCoordinatorLocked(ctx, kind, usageEv, clientCanceled)
	}
	if l.svc == nil {
		return false
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	reservations := l.reservationStates()
	result, err := l.svc.Settle(cleanupCtx, l.settlementInput(kind, usageEv, clientCanceled, reservations))
	cancel()
	if result.Applied {
		l.control.terminal = authorityTerminalSettled
		for _, reservation := range reservations {
			incoming := measurementAuthorityForUnit(usageEv, reservation.reservedAmount.Unit)
			l.control.authority = mergeMeasurementAuthorityForAmount(l.control.authority, incoming, reservation.reservedAmount)
		}
		if err != nil && l.log != nil {
			l.log.DebugContext(ctx, "usage authority settle evidence failed after commit", "error", err, "candidate_key", l.control.cand.Key)
		}
		return true
	}
	if err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority settle failed", "error", err, "candidate_key", l.control.cand.Key)
	}
	// A pre-output settlement failure may safely release the complete set. Once
	// client output is committed, release would hide a real accounting failure;
	// retain the reservation and let degraded/unavailable evidence describe it.
	if l.outputCommitted == nil || !l.outputCommitted.Load() {
		releaseCtx, releaseCancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
		defer releaseCancel()
		if l.releaseReservationSet(releaseCtx, authorityapp.ReleaseKindLosing, reservations) {
			l.control.terminal = authorityTerminalReleased
		}
	}
	return false
}

// usageAuthorityAttemptProviderID is the stable AttemptCoordinator slot ID used
// by BuildAuthorityCoordinators for the built-in usage-authority adapter.
const usageAuthorityAttemptProviderID = "usage-authority-attempt"

func (l *authorityLifecycle) settleViaCoordinatorLocked(ctx context.Context, kind authorityapp.SettlementKind, usageEv lipapi.Event, clientCanceled bool) bool {
	if l.attemptCoord == nil {
		return false
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	if l.control.state.settledProviders == nil {
		l.control.state.settledProviders = make(map[string]struct{})
	}
	state := l.control.state
	outputCommitted := l.outputCommitted != nil && l.outputCommitted.Load()
	evidence := attemptSettlementEvidence(state, kind, usageEv, outputCommitted, clientCanceled)

	uaStack, externalStack := splitAttemptStackByProvider(state.stack, usageAuthorityAttemptProviderID)
	// Built-in usage authority keeps the rich reservation-set settle path so
	// FinalUsage/cost authority are preserved. External providers settle only
	// through AttemptCoordinator with their owning handles and full evidence.
	if l.svc != nil && len(uaStack.Handles()) > 0 && !providerSettled(state.settledProviders, usageAuthorityAttemptProviderID) {
		reservations := filterReservationsByHandles(l.reservationStates(), uaStack.Handles())
		result, err := l.svc.Settle(cleanupCtx, l.settlementInput(kind, usageEv, clientCanceled, reservations))
		if result.Applied {
			markProviderSettled(&l.control.state, usageAuthorityAttemptProviderID)
			for _, reservation := range reservations {
				incoming := measurementAuthorityForUnit(usageEv, reservation.reservedAmount.Unit)
				l.control.authority = mergeMeasurementAuthorityForAmount(l.control.authority, incoming, reservation.reservedAmount)
			}
			if err != nil && l.log != nil {
				l.log.DebugContext(ctx, "usage authority settle evidence failed after commit", "error", err, "candidate_key", l.control.cand.Key)
			}
		} else if err != nil && l.log != nil {
			l.log.DebugContext(ctx, "usage authority settle failed", "error", err, "candidate_key", l.control.cand.Key)
		}
	} else if l.svc == nil && len(uaStack.Handles()) > 0 {
		// No direct service: settle the built-in adapter via the coordinator too.
		externalStack = state.stack
	}

	for _, providerID := range uniqueStackProviderIDs(externalStack) {
		if providerSettled(l.control.state.settledProviders, providerID) {
			continue
		}
		if l.svc != nil && providerID == usageAuthorityAttemptProviderID {
			continue
		}
		sub := filterStackByProvider(externalStack, providerID)
		if len(sub.Handles()) == 0 {
			continue
		}
		settleIn := evidence
		settleIn.Handles = sub.Handles()
		if err := l.attemptCoord.Settle(cleanupCtx, sub, settleIn); err != nil {
			if l.log != nil {
				l.log.DebugContext(ctx, "attempt coordinator settle failed", "error", err, "provider_id", providerID, "candidate_key", l.control.cand.Key)
			}
			continue
		}
		markProviderSettled(&l.control.state, providerID)
	}

	unfinished := unfinishedStack(l.control.state.stack, l.control.state.settledProviders)
	if len(unfinished.Handles()) == 0 {
		l.control.terminal = authorityTerminalSettled
		return true
	}

	if outputCommitted {
		// Keep terminal open so only unfinished providers are retried (15.5).
		return false
	}

	// Before output is committed, release only providers that never settled.
	if l.releaseViaCoordinatorStackLocked(ctx, unfinished) {
		if len(l.control.state.settledProviders) > 0 {
			l.control.terminal = authorityTerminalSettled
			return true
		}
		l.control.terminal = authorityTerminalReleased
	}
	return false
}

func attemptSettlementEvidence(
	state attemptAuthorityState,
	kind authorityapp.SettlementKind,
	usageEv lipapi.Event,
	outputCommitted bool,
	clientCanceled bool,
) authority.AttemptSettlement {
	outcome, surfaced := mapAttemptSettlementPosture(kind, outputCommitted, clientCanceled)
	qs := quantitiesFromUsageEvent(usageEv)
	presence := metering.PresenceUnknown
	if len(qs) > 0 {
		presence = metering.PresencePresent
	}
	authorityLevel := metering.AuthorityEstimated
	if eventCarriesAuthoritativeProviderUsage(usageEv) {
		authorityLevel = metering.AuthorityAuthoritative
	}
	fact := metering.Fact{
		FactID:         "attempt-settle:" + strings.TrimSpace(state.attemptID),
		StreamID:       "be-egress:" + strings.TrimSpace(state.bLegID),
		Sequence:       1,
		Kind:           metering.FactKindCumulative,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Correlation:    metering.Correlation{RequestID: state.requestID, AttemptID: state.attemptID, BLegID: state.bLegID},
		Quantities:     qs,
		Money:          moneyFromUsageEvent(usageEv),
		Source:         metering.SourceObserved,
		Authority:      authorityLevel,
		Presence:       presence,
		AttemptOutcome: outcome,
		Surfaced:       surfaced,
		RecordedAt:     time.Now().UTC(),
	}
	var facts []metering.Fact
	if err := fact.Validate(); err == nil {
		facts = []metering.Fact{fact}
	}
	var rated []economics.RatingResult
	if usageEv.CostPresent {
		rated = []economics.RatingResult{{
			Money: economics.Money{
				NanoUnits: usageEv.CostNanoUnits,
				Currency:  strings.TrimSpace(usageEv.Currency),
				Present:   true,
			},
			Source:      strings.TrimSpace(usageEv.CostSource),
			Perspective: metering.PerspectiveOperator,
		}}
	}
	return authority.AttemptSettlement{
		RequestID:     state.requestID,
		AttemptID:     state.attemptID,
		BLegID:        state.bLegID,
		Facts:         facts,
		Rated:         rated,
		Outcome:       outcome,
		Surfaced:      surfaced,
		BoundVersions: append([]economics.PolicySnapshotRef(nil), state.boundVersions...),
	}
}

func mapAttemptSettlementPosture(kind authorityapp.SettlementKind, outputCommitted, clientCanceled bool) (metering.AttemptOutcome, metering.SurfacedState) {
	switch kind {
	case authorityapp.SettlementKindCancellation:
		if clientCanceled {
			return metering.AttemptOutcomeCanceled, metering.SurfacedNo
		}
		return metering.AttemptOutcomeCanceled, metering.SurfacedUnknown
	case authorityapp.SettlementKindLosing, authorityapp.SettlementKindSwallowed:
		return metering.AttemptOutcomeLoser, metering.SurfacedNo
	case authorityapp.SettlementKindUnavailable, authorityapp.SettlementKindPartial:
		return metering.AttemptOutcomeFailed, metering.SurfacedNo
	default:
		if outputCommitted {
			return metering.AttemptOutcomeWinner, metering.SurfacedYes
		}
		return metering.AttemptOutcomeUnknown, metering.SurfacedUnknown
	}
}

func providerSettled(settled map[string]struct{}, providerID string) bool {
	if settled == nil {
		return false
	}
	_, ok := settled[strings.TrimSpace(providerID)]
	return ok
}

func markProviderSettled(state *attemptAuthorityState, providerID string) {
	if state == nil {
		return
	}
	id := strings.TrimSpace(providerID)
	if id == "" {
		return
	}
	if state.settledProviders == nil {
		state.settledProviders = make(map[string]struct{})
	}
	state.settledProviders[id] = struct{}{}
}

func uniqueStackProviderIDs(stack authoritycoord.CompensationStack) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, e := range stack.Entries() {
		id := strings.TrimSpace(e.ProviderID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func filterStackByProvider(stack authoritycoord.CompensationStack, providerID string) authoritycoord.CompensationStack {
	want := strings.TrimSpace(providerID)
	var out authoritycoord.CompensationStack
	for _, e := range stack.Entries() {
		if strings.TrimSpace(e.ProviderID) == want {
			out.Push(e)
		}
	}
	return out
}

func unfinishedStack(stack authoritycoord.CompensationStack, settled map[string]struct{}) authoritycoord.CompensationStack {
	var out authoritycoord.CompensationStack
	for _, e := range stack.Entries() {
		if providerSettled(settled, e.ProviderID) {
			continue
		}
		out.Push(e)
	}
	return out
}

func splitAttemptStackByProvider(stack authoritycoord.CompensationStack, providerID string) (match, other authoritycoord.CompensationStack) {
	want := strings.TrimSpace(providerID)
	for _, e := range stack.Entries() {
		if strings.TrimSpace(e.ProviderID) == want {
			match.Push(e)
			continue
		}
		other.Push(e)
	}
	return match, other
}

func filterReservationsByHandles(reservations []authorityReservationState, handles []string) []authorityReservationState {
	if len(reservations) == 0 || len(handles) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		h = strings.TrimSpace(h)
		if h != "" {
			allow[h] = struct{}{}
		}
	}
	out := make([]authorityReservationState, 0, len(handles))
	for _, r := range reservations {
		if _, ok := allow[strings.TrimSpace(r.reservationID)]; ok {
			out = append(out, r)
		}
	}
	return out
}

// Release releases every reservation in the admission result once with the
// supplied kind. It marks settled only after the atomic release succeeds, so a
// failed cleanup can be retried. No-op when inactive or when svc is nil.
func (l *authorityLifecycle) Release(ctx context.Context, kind authorityapp.ReleaseKind) {
	if l == nil || l.control == nil {
		return
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	if !l.isActiveLocked() {
		return
	}
	if l.control.terminal != authorityTerminalOpen {
		return
	}
	if l.control.state.viaCoordinator {
		// Release never re-touches providers that already settled successfully.
		unfinished := unfinishedStack(l.control.state.stack, l.control.state.settledProviders)
		if len(unfinished.Handles()) == 0 {
			l.control.terminal = authorityTerminalSettled
			return
		}
		if l.releaseViaCoordinatorStackLocked(ctx, unfinished) {
			if len(l.control.state.settledProviders) > 0 {
				l.control.terminal = authorityTerminalSettled
			} else {
				l.control.terminal = authorityTerminalReleased
				l.control.authority = newSettlementAuthorityState()
			}
		}
		return
	}
	if l.svc == nil {
		return
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	if l.releaseReservationSet(cleanupCtx, kind, l.reservationStates()) {
		l.control.terminal = authorityTerminalReleased
		l.control.authority = newSettlementAuthorityState()
	}
}

func (l *authorityLifecycle) releaseViaCoordinatorLocked(ctx context.Context) bool {
	return l.releaseViaCoordinatorStackLocked(ctx, l.control.state.stack)
}

func (l *authorityLifecycle) releaseViaCoordinatorStackLocked(ctx context.Context, stack authoritycoord.CompensationStack) bool {
	if l.attemptCoord == nil {
		return false
	}
	if len(stack.Handles()) == 0 {
		return true
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	fails := l.attemptCoord.Release(cleanupCtx, stack)
	if len(fails) > 0 {
		if l.log != nil {
			l.log.DebugContext(ctx, "attempt coordinator release failures", "count", len(fails), "candidate_key", l.control.cand.Key)
		}
		return false
	}
	return true
}

func (l *authorityLifecycle) releaseReservationSet(ctx context.Context, kind authorityapp.ReleaseKind, reservations []authorityReservationState) bool {
	if l == nil || l.svc == nil || len(reservations) == 0 {
		return false
	}
	input := authorityapp.ReleaseInput{
		Correlation:      l.control.state.admissionInput.Correlation,
		Scope:            l.control.state.admissionInput.Scope,
		Kind:             kind,
		Authority:        domain.AuthorityLevelEstimated,
		Stage:            feature.StageIDAttemptLifecycle,
		BackendAttempted: l.backendAttempted != nil && l.backendAttempted.Load(),
		OutputCommitted:  l.outputCommitted != nil && l.outputCommitted.Load(),
		Reservations:     make([]authorityapp.ReleaseDescriptor, 0, len(reservations)),
	}
	for _, reservation := range reservations {
		input.Reservations = append(input.Reservations, authorityapp.ReleaseDescriptor{Reservation: authorityapp.ReservationDescriptor{
			RuleID:         reservation.ruleID,
			Unit:           reservation.reservedAmount.Unit,
			Currency:       reservation.reservedAmount.Currency,
			Dimensions:     l.control.state.admissionInput.Dimensions,
			ReservationKey: reservation.reservationKey,
			ReservationID:  reservation.reservationID,
			Amount:         reservation.reservedAmount,
		}})
	}
	if len(input.Reservations) > 0 {
		first := input.Reservations[0].Reservation
		input.ReservationKey = first.ReservationKey
		input.ReservationID = first.ReservationID
		input.RuleID = first.RuleID
		input.Amount = first.Amount
	}
	result, err := l.svc.Release(ctx, input)
	if err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority release failed", "error", err, "candidate_key", l.control.cand.Key)
	}
	return err == nil || result.Applied
}

// ReconcileAuthoritative adjusts a prior (typically estimated) settlement with
// later authoritative final usage (requirement 7.6, 8.4-8.6). It bypasses the
// settled guard — the prior settlement stays in evidence — and calls Settle
// once with the complete reservation set, marking each descriptor
// authoritative with a distinct source key (the ReservationID carries an "|authoritative"
// suffix so the app service's sourceEventKey produces a key the store has not
// seen before). The store finds the reservation by ReservationKey (unchanged),
// sees it is already settled, and applies an authoritative adjustment via
// authoritativeResettle instead of no-opping. Idempotent per authoritative
// source key: a replay produces the same source key and the store's settleBySrc
// catches it as a no-op. Returns true when at least one adjustment applied.
func (l *authorityLifecycle) ReconcileAuthoritative(ctx context.Context, usageEv lipapi.Event) bool {
	if l == nil || l.svc == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	if l.control.terminal != authorityTerminalSettled {
		return false
	}
	return l.reconcileAuthoritativeLocked(ctx, usageEv)
}

func (l *authorityLifecycle) reconcileAuthoritativeLocked(ctx context.Context, usageEv lipapi.Event) bool {
	prior := l.control.authority
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	reservations := l.reservationStates()
	input := l.settlementInput(authorityapp.SettlementKindFinal, usageEv, false, reservations)
	input.FinalUsagePresent = usageEventPresent(usageEv)
	input.FinalCostPresent = costEventPresent(usageEv)
	filtered := input.Reservations[:0]
	for i := range input.Reservations {
		reservation := input.Reservations[i]
		incoming := measurementAuthorityForUnit(usageEv, reservation.Reservation.Amount.Unit)
		if !measurementAuthorityNeedsUpgradeForAmount(prior, incoming, reservation.Reservation.Amount) {
			continue
		}
		if reservation.Reservation.Amount.Unit == domain.AmountUnitMoneyNano {
			reservation.Reservation.ReservationID += "|authoritative_cost"
		} else {
			// Preserve the established token-only reconciliation key. Monetary
			// reconciliation uses a distinct suffix because cost authority is
			// tracked independently from token authority.
			reservation.Reservation.ReservationID += "|authoritative"
		}
		filtered = append(filtered, reservation)
	}
	input.Reservations = filtered
	if len(input.Reservations) == 0 {
		return false
	}
	for i := range input.Reservations {
		input.Reservations[i].SourceKey = ""
		input.Reservations[i].Reservation.SourceKey = ""
	}
	if len(input.Reservations) > 0 {
		// Keep the legacy aggregate fields aligned with the descriptor set for
		// callers and test doubles that inspect the first reservation directly.
		first := input.Reservations[0]
		input.ReservationKey = first.Reservation.ReservationKey
		input.ReservationID = first.Reservation.ReservationID
		input.RuleID = first.Reservation.RuleID
		input.FinalUsage = first.FinalUsage
		input.FinalCost = first.FinalCost
		input.ReservedUsage = first.Reservation.Amount
		input.EstimatedUsage = first.EstimatedUsage
		input.EstimatedCost = first.EstimatedCost
		input.Authority = first.Authority
		input.MeasurementAuthority = first.MeasurementAuthority
	}
	result, err := l.svc.Settle(cleanupCtx, input)
	if err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority authoritative reconcile failed", "error", err, "candidate_key", l.control.cand.Key)
	}
	if result.Applied {
		for _, reservation := range input.Reservations {
			incoming := reservation.MeasurementAuthority
			l.control.authority = mergeMeasurementAuthorityForAmount(l.control.authority, incoming, reservation.Reservation.Amount)
		}
		if err != nil && l.log != nil {
			l.log.DebugContext(ctx, "usage authority authoritative reconcile evidence failed after commit", "error", err, "candidate_key", l.control.cand.Key)
		}
		return true
	}
	return false
}

// Reset replaces the reservation state for a replacement iteration (recv-phase
// failover or parallel winner handoff) and clears the settled guard so the new
// reservation can be settled/released independently.
func (l *authorityLifecycle) Reset(state attemptAuthorityState, cand routing.AttemptCandidate) {
	if l == nil {
		return
	}
	if l.control == nil {
		l.control = &authorityLifecycleControl{authority: newSettlementAuthorityState()}
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	l.control.state = state
	l.control.cand = cand
	if l.backendAttempted == nil {
		l.backendAttempted = &atomic.Bool{}
	}
	if l.outputCommitted == nil {
		l.outputCommitted = &atomic.Bool{}
	}
	l.control.terminal = authorityTerminalOpen
	l.control.authority = newSettlementAuthorityState()
	l.backendAttempted.Store(true)
	l.outputCommitted.Store(false)
}

// ApplyUnreservedUsage applies final or partial usage/cost to every matched rule
// that did not create a reservation. It is independent of reservation lifecycle
// state and is idempotent at the store fact/source boundary.
func (l *authorityLifecycle) ApplyUnreservedUsage(ctx context.Context, kind authorityapp.SettlementKind, usageEv lipapi.Event) bool {
	if l == nil || l.svc == nil {
		return false
	}
	if l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	ruleIDs := l.control.state.admissionResult.UnreservedRuleIDs
	if len(ruleIDs) == 0 {
		ruleIDs = l.control.state.admissionResult.AdvisoryRuleIDs
	}
	if len(ruleIDs) == 0 {
		return false
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	usagePresent := usageEventPresent(usageEv)
	usage := advisoryUsageBreakdown(usageEv)
	finalCost := attemptAuthorityCostAmount(usageEv, l.advisoryCostCurrency())
	measurementAuthority := measurementAuthorityForEvent(usageEv)
	if !usagePresent {
		// A final/cancellation path can legitimately have no usage event (for
		// example, reconstruction was unavailable). Keep unreserved windows
		// observable with the preflight estimate rather than silently dropping
		// token/request or estimated-spend accounting.
		usage = l.control.state.admissionInput.PreflightUsage
		finalCost = l.control.state.admissionInput.Spend
	} else if !costEventPresent(usageEv) {
		// A token-bearing provider event can be authoritative for usage while
		// carrying no provider cost. Advisory money windows still need the
		// configured estimate; otherwise they consume zero and bypass spend
		// accounting until a later cost event arrives.
		finalCost = l.control.state.admissionInput.Spend
	}
	cmd := authorityapp.ApplyUsageCommand{
		Correlation:          l.control.state.admissionInput.Correlation,
		Scope:                l.control.state.admissionInput.Scope,
		Dimensions:           l.control.state.admissionInput.Dimensions,
		RuleIDs:              append([]string(nil), ruleIDs...),
		Usage:                usage,
		RequestCount:         domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		FinalCost:            finalCost,
		UsagePresent:         usagePresent,
		CostPresent:          costEventPresent(usageEv),
		Authority:            measurementAuthority.Usage,
		MeasurementAuthority: measurementAuthority,
		Kind:                 kind,
		SourceKey:            l.advisorySourceKey(),
	}
	result, err := l.svc.ApplyUsage(cleanupCtx, cmd)
	if err != nil {
		if l.log != nil {
			l.log.DebugContext(ctx, "usage authority advisory apply failed", "error", err, "candidate_key", l.control.cand.Key)
		}
		return false
	}
	return result.Applied
}

// ApplyAdvisoryUsage preserves the legacy lifecycle call shape for callers that
// only have a final response event.
func (l *authorityLifecycle) ApplyAdvisoryUsage(ctx context.Context, usageEv lipapi.Event) bool {
	return l.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindFinal, usageEv)
}

// advisoryCostCurrency returns the currency fallback for advisory money rules,
// taken from the admission input's estimated spend currency.
func (l *authorityLifecycle) advisoryCostCurrency() string {
	if l == nil {
		return ""
	}
	return strings.TrimSpace(l.control.state.admissionInput.Spend.Currency)
}

// advisorySourceKey derives a deterministic idempotency key for advisory usage
// from the logical request trace ID and B-leg ID. Replays for the same logical
// request and B-leg converge on the same key so the store treats them as
// no-ops (requirement 7.8).
func (l *authorityLifecycle) advisorySourceKey() string {
	if l == nil {
		return ""
	}
	return strings.TrimSpace(l.control.state.admissionInput.Correlation.TraceID) + "|" +
		strings.TrimSpace(l.control.state.admissionInput.Correlation.BLegID) + "|advisory_usage"
}

// advisoryUsageBreakdown extracts the final per-unit token usage from a
// reconstructed usage event so the store can select the right amount per
// matched advisory rule unit (input/output/cache/reasoning/total tokens).
func advisoryUsageBreakdown(ev lipapi.Event) domain.PreflightUsage {
	totalPresent := attemptAuthorityEventHasUsageForUnit(ev, domain.AmountUnitTotalTokens)
	return domain.PreflightUsage{
		InputTokens:        int64(ev.InputTokens),
		OutputTokens:       int64(ev.OutputTokens),
		CacheReadTokens:    int64(ev.CacheReadTokens),
		CacheWriteTokens:   int64(ev.CacheWriteTokens),
		ReasoningTokens:    int64(ev.ReasoningTokens),
		TotalTokens:        int64(ev.TotalTokens),
		TotalTokensPresent: totalPresent,
	}
}
