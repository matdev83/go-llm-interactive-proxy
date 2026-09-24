package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	maxAttemptAccumulatedUsage    = 1024
	maxAttemptUsageDedupeKeyBytes = 4096
)

func (a *attemptSession) rememberUsageEvidenceOnce(ev lipapi.Event) bool {
	return a.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleStream)
}

// rememberUsageEvidenceOnceAs owns the bounded per-attempt evidence set. The
// legacy internalUsageKeys map remains keyed by DedupeKey for compatibility;
// usageEvidence retains the event and acquisition role so terminal capture can
// preserve source separation. A changed payload under one source key is
// retained as a visible conflict, never merged into a later B-leg.
func (a *attemptSession) rememberUsageEvidenceOnceAs(ev lipapi.Event, role string) bool {
	if a == nil {
		return false
	}
	if len(ev.Accounting.DedupeKey) > maxAttemptUsageDedupeKeyBytes {
		return false
	}
	key := strings.TrimSpace(ev.Accounting.DedupeKey)
	if key == "" {
		return false
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.internalUsageKeys == nil {
		a.internalUsageKeys = make(map[string]struct{})
	}
	if a.usageEvidence == nil {
		a.usageEvidence = make(map[string]capturedBillingEvidence)
	}
	if prior, exists := a.usageEvidence[key]; exists {
		if eventEvidenceFingerprint(prior.event) != eventEvidenceFingerprint(ev) {
			a.usageConflicts = appendBoundedEvidenceConflict(a.usageConflicts, billing.EvidenceConflict{
				Identity:     key + "\x00revision:1",
				ExistingHash: eventEvidenceFingerprint(prior.event),
				IncomingHash: eventEvidenceFingerprint(ev),
			})
		}
		return false
	}
	if len(a.internalUsageKeys) >= maxAttemptAccumulatedUsage {
		return false
	}
	owned := cloneBillingEvidenceEvent(ev)
	a.internalUsageKeys[key] = struct{}{}
	a.accumulatedUsage = append(a.accumulatedUsage, owned)
	a.usageEvidence[key] = capturedBillingEvidence{event: owned, role: strings.TrimSpace(role)}
	a.usageEvidenceOrder = append(a.usageEvidenceOrder, key)
	return true
}

func (a *attemptSession) recordUsageEvidence(ev lipapi.Event) {
	if a == nil || ev.Kind == "" {
		return
	}
	if !a.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleSideband) {
		return
	}
	a.observeAccountingUsage(ev)
}

// billingEvidenceSnapshot returns caller-owned source-tagged evidence and
// drains the attempt accumulator. Draining is terminal-owner scoped: callers
// must invoke it only after all backend sideband drain passes have completed.
func (a *attemptSession) billingEvidenceSnapshot() []capturedBillingEvidence {
	out, _ := a.billingEvidenceDrain()
	return out
}

// billingEvidenceDrain returns source-tagged evidence and conflicts while
// clearing every attempt-owned accumulator. A second terminal callback sees
// no state from this B-leg and therefore cannot bind stale evidence to a
// replacement B-leg.
func (a *attemptSession) billingEvidenceDrain() ([]capturedBillingEvidence, []billing.EvidenceConflict) {
	if a == nil {
		return nil, nil
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	out := make([]capturedBillingEvidence, 0, len(a.usageEvidenceOrder))
	for _, key := range a.usageEvidenceOrder {
		if evidence, ok := a.usageEvidence[key]; ok {
			out = append(out, capturedBillingEvidence{event: cloneBillingEvidenceEvent(evidence.event), role: evidence.role})
		}
	}
	conflicts := append([]billing.EvidenceConflict(nil), a.usageConflicts...)
	a.internalUsageKeys = nil
	a.accumulatedUsage = nil
	a.usageEvidence = nil
	a.usageEvidenceOrder = nil
	a.usageConflicts = nil
	return out, conflicts
}

func (a *attemptSession) billingEvidenceConflictsSnapshot() []billing.EvidenceConflict {
	if a == nil {
		return nil
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return append([]billing.EvidenceConflict(nil), a.usageConflicts...)
}

// rememberEconomicEvidenceOnce owns validated connector V2 observations until
// the B-leg terminal owner builds its durable record. Exact source replays are
// ignored; a changed payload under the same identity remains a bounded visible
// conflict. Coverage metadata remains attached until the terminal handoff.
func (a *attemptSession) rememberEconomicEvidenceOnce(evidence execbackend.EconomicEvidence) {
	if a == nil {
		return
	}
	canonical, err := evidence.Observation.Canonical()
	if err != nil {
		return
	}
	if evidence.Coverage == "" {
		evidence.Coverage = "complete"
	}
	evidence.Observation = canonical
	identity := canonical.IdentityKey()
	hash := canonical.Fingerprint() + "\x00" + evidence.Coverage + "\x00" + evidence.CoverageReason
	if identity == "" || hash == "" {
		return
	}
	a.economicMu.Lock()
	defer a.economicMu.Unlock()
	// Keep the map allocation below the queue admission. A checkpoint
	// rejection must leave no dedupe marker behind, so a later replay can
	// retry after bounded capacity is released.
	fingerprints := a.economicObservationHashes[identity]
	if fingerprints != nil {
		if _, exists := fingerprints[hash]; exists {
			return
		}
	}
	if len(fingerprints) != 0 {
		prior := ""
		for candidate := range fingerprints {
			if prior == "" || candidate < prior {
				prior = candidate
			}
		}
		priorEvidence := fingerprints[prior]
		a.economicConflicts = appendBoundedEvidenceConflict(a.economicConflicts, billing.EvidenceConflict{
			Identity:               identity,
			ExistingHash:           prior,
			IncomingHash:           hash,
			ExistingCoverage:       billing.EconomicEvidenceCoverage(priorEvidence.Coverage),
			ExistingCoverageReason: priorEvidence.CoverageReason,
			IncomingCoverage:       billing.EconomicEvidenceCoverage(evidence.Coverage),
			IncomingCoverageReason: evidence.CoverageReason,
		})
		fingerprints[hash] = evidence
		return
	}
	if len(a.economicObservations) >= billing.MaxCallLegEvidenceObservations {
		return
	}
	// Queue admission precedes the terminal evidence dedupe marker. If the
	// bounded checkpoint state is exhausted, this observation remains
	// retryable rather than being irreversibly accepted and suppressed on
	// replay. A nil sink reports Ignored, preserving the legacy terminal-only
	// path.
	if a.queueEconomicCheckpoint(canonical) == economicCheckpointRejected {
		return
	}
	if a.economicObservationHashes == nil {
		a.economicObservationHashes = make(map[string]map[string]execbackend.EconomicEvidence)
	}
	fingerprints = make(map[string]execbackend.EconomicEvidence)
	a.economicObservationHashes[identity] = fingerprints
	fingerprints[hash] = evidence
	a.economicObservations = append(a.economicObservations, evidence)
	// Keep the durable checkpoint queue separate from terminal billing
	// evidence. The queue only persists immutable V2 observations; it never
	// invokes valuation, authority, or money mutation paths.
}

func (a *attemptSession) rememberEconomicObservationOnce(observation metering.Observation) {
	a.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation})
}

func (a *attemptSession) economicEvidenceDrain() ([]execbackend.EconomicEvidence, []billing.EvidenceConflict) {
	if a == nil {
		return nil, nil
	}
	a.economicMu.Lock()
	defer a.economicMu.Unlock()
	observations := make([]execbackend.EconomicEvidence, len(a.economicObservations))
	for i, evidence := range a.economicObservations {
		observations[i] = evidence
		observations[i].Observation = evidence.Observation.Clone()
	}
	conflicts := append([]billing.EvidenceConflict(nil), a.economicConflicts...)
	a.economicObservations = nil
	a.economicObservationHashes = nil
	a.economicConflicts = nil
	return observations, conflicts
}

func (a *attemptSession) finalizeBillingResult(ctx context.Context, state *billingCallState, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, bool) {
	if a == nil || state == nil {
		return execbackend.BillingFinalizationResult{}, false
	}
	if a.finalizeBillingV2 != nil {
		return state.finalizeOnceWithEvidence(ctx, in, a.finalizeBillingV2)
	}
	if a.finalizeBilling == nil {
		return execbackend.BillingFinalizationResult{}, false
	}
	ev, ok := state.finalizeOnce(ctx, in, a.finalizeBilling)
	return execbackend.BillingFinalizationResult{Usage: ev}, ok
}

func (a *attemptSession) aggregatedUsageEvidence() lipapi.Event {
	if a == nil {
		return lipapi.Event{}
	}
	a.usageMu.Lock()
	if len(a.accumulatedUsage) == 0 {
		a.usageMu.Unlock()
		return lipapi.Event{}
	}
	events := append([]lipapi.Event(nil), a.accumulatedUsage...)
	a.usageMu.Unlock()
	return authorityUsageEvent(events)
}

func (a *attemptSession) drainStreamUsageEvidence(inner lipapi.ManagedEventStream) {
	if a == nil || inner == nil {
		return
	}
	source, ok := inner.(lipapi.UsageEvidenceSource)
	economicSource, economicOK := inner.(execbackend.EconomicEvidenceSource)
	if economicOK {
		_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_economic", func() error {
			for _, evidence := range economicSource.DrainEconomicEvidenceRecords() {
				a.rememberEconomicEvidenceOnce(evidence)
			}
			return nil
		})
	}
	if !ok {
		if !economicOK {
			if observationSource, observationOK := inner.(metering.ObservationSource); observationOK {
				_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_economic", func() error {
					for _, observation := range observationSource.DrainEconomicObservations() {
						a.rememberEconomicObservationOnce(observation)
					}
					return nil
				})
			}
		}
		return
	}
	_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_usage", func() error {
		for _, ev := range source.DrainUsageEvidence() {
			if ev.Kind != lipapi.EventUsageDelta {
				continue
			}
			if a.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleSideband) {
				a.observeAccountingUsage(ev)
			}
		}
		return nil
	})
	if !economicOK {
		if observationSource, observationOK := inner.(metering.ObservationSource); observationOK {
			_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_economic", func() error {
				for _, observation := range observationSource.DrainEconomicObservations() {
					a.rememberEconomicObservationOnce(observation)
				}
				return nil
			})
		}
	}
}

// usageOrAccumulated returns primary when it carries token or cost presence,
// otherwise the best attempt-owned accumulated evidence, else an empty shell.
func (a *attemptSession) usageOrAccumulated(primary lipapi.Event) lipapi.Event {
	if primary.Kind != "" && (primary.UsagePresence.Any() || primary.CostPresent) {
		return primary
	}
	if acc := a.aggregatedUsageEvidence(); acc.Kind != "" {
		return acc
	}
	return emptyOperatorUsageShell()
}

// augmentBillingUsage merges attempt-owned accumulated evidence into a terminal
// billing stream event: full substitution when the stream event lacks presence,
// otherwise provider-cost backfill only. Accumulated host-only money fills the
// V1 compatibility projection carrier; source-separated V2 observations retain
// each source independently, so no source record is destroyed by the backfill.
// Task 18.2 (Migration Strategy step 8): this host-only V1 draining carrier is
// intentionally retained for in-flight V1 recovery; it is not a live V2 money
// path. Operator migration: V2 terminal observations own new economics.
func (a *attemptSession) augmentBillingUsage(streamEv, fallbackPrimary lipapi.Event) lipapi.Event {
	if streamEv.Kind == "" {
		streamEv = fallbackPrimary
	}
	if accumulatedEv := a.aggregatedUsageEvidence(); accumulatedEv.Kind != "" {
		if streamEv.Kind == "" || (!streamEv.UsagePresence.Any() && !streamEv.CostPresent) {
			streamEv = accumulatedEv
		} else if !streamEv.CostPresent && accumulatedEv.CostPresent {
			streamEv.CostPresent, streamEv.CostNanoUnits, streamEv.Currency = true, accumulatedEv.CostNanoUnits, accumulatedEv.Currency
		}
	}
	if streamEv.Kind == "" {
		return emptyOperatorUsageShell()
	}
	return streamEv
}

const (
	billingEvidenceRoleStream    = "stream"
	billingEvidenceRoleSideband  = "sideband"
	billingEvidenceRoleFinalizer = "finalizer"
	billingObservationMappingV1  = "lipapi.event.accounting.v1"
)

// capturedBillingEvidence keeps the acquisition path separate until terminal
// record construction. The legacy lipapi.Event remains an input boundary only;
// no provider wire value is parsed here.
type capturedBillingEvidence struct {
	event lipapi.Event
	role  string
}

// billingObservationInput is a terminal conversion input. Sequence is local
// ordering only and never replaces the concrete B-leg subject.
type billingObservationInput struct {
	event    lipapi.Event
	role     string
	sequence uint64
}

// cloneBillingEvidenceEvent keeps only the canonical accounting carriers that
// the V1-to-V2 mapping is allowed to consume. In particular, raw provider
// bodies, reasoning/media payloads and other mutable Event interiors never
// enter the attempt-owned terminal accumulator.
func cloneBillingEvidenceEvent(ev lipapi.Event) lipapi.Event {
	return lipapi.Event{
		Kind:             ev.Kind,
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		UsagePresence:    ev.UsagePresence,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         ev.Currency,
		CostSource:       ev.CostSource,
		CostPresent:      ev.CostPresent,
		Accounting:       ev.Accounting,
		UsageScopes:      append([]lipapi.ScopedUsageDelta(nil), ev.UsageScopes...),
	}
}

func observationsFromBillingEvidence(draft billingLegDraft, captured []capturedBillingEvidence) ([]metering.Observation, []billing.EvidenceConflict) {
	inputs := make([]billingObservationInput, 0, len(captured)+2)
	capturedKeys := make(map[string]struct{}, len(captured))
	for i, evidence := range captured {
		if directEvidenceCoveredByEconomicObservation(draft, evidence.event) {
			continue
		}
		inputs = append(inputs, billingObservationInput{event: evidence.event, role: evidence.role, sequence: uint64(i + 1)})
		if key := strings.TrimSpace(evidence.event.Accounting.DedupeKey); key != "" {
			capturedKeys[key] = struct{}{}
		}
	}
	// Keep direct stream/finalizer envelopes in the collection even when an
	// adapter did not expose an attempt accumulator. Once an accumulator or
	// negotiated V2 source exists, its source events are authoritative and the
	// stream field is only a derived V1 fallback; adding that aggregate would
	// manufacture a duplicate against the provider source. Exact source
	// identity replay is removed below; changed payloads are surfaced as
	// conflicts.
	appendDirectEvidence := func(event lipapi.Event, role string) {
		if event.Kind == "" || directEvidenceCoveredByEconomicObservation(draft, event) {
			return
		}
		key := strings.TrimSpace(event.Accounting.DedupeKey)
		if role == billingEvidenceRoleStream && key != "" && hasCapturedEvidenceKey(capturedKeys, key) {
			return
		}
		inputs = append(inputs, billingObservationInput{event: event, role: role, sequence: uint64(len(inputs) + 1)})
	}
	appendDirectEvidence(draft.stream, billingEvidenceRoleStream)
	appendDirectEvidence(draft.finalize, billingEvidenceRoleFinalizer)

	observations := make([]metering.Observation, 0, len(inputs))
	conflicts := make([]billing.EvidenceConflict, 0)
	// lipapi's DedupeKey is the only source-event identity available at this
	// neutral boundary. Revision is fixed to one because Event has no revision
	// member; a later corrected source must arrive through the V2 observation
	// contract rather than being guessed here.
	seenSourceEvents := make(map[string]string, len(inputs))
	for _, input := range inputs {
		observation, ok := billingObservationFromEvent(draft, input)
		if !ok {
			continue
		}
		identity := strings.TrimSpace(input.event.Accounting.DedupeKey) + "\x00revision:1"
		hash := eventEvidenceFingerprint(input.event)
		if prior, exists := seenSourceEvents[identity]; exists {
			if prior != hash {
				conflicts = appendBoundedEvidenceConflict(conflicts, billing.EvidenceConflict{
					Identity: identity, ExistingHash: prior, IncomingHash: hash,
				})
			}
			continue
		}
		seenSourceEvents[identity] = hash
		if len(observations) >= billing.MaxCallLegEvidenceObservations {
			// The attempt accumulator is intentionally bounded. Preserve the
			// V1 scalar fallback for any dropped terminal source while keeping
			// the immutable V2 collection within the record contract.
			continue
		}
		observations = append(observations, observation)
	}
	return observations, conflicts
}

// directEvidenceCoveredByEconomicObservation prevents a canonical provider
// usage fallback from becoming a second V2 observation when the same source
// event already arrived through the negotiated host-only V2 seam. Matching is
// deliberately limited to the same provider source key and trusted B-leg/store
// subject; unrelated local/finalizer evidence remains eligible for mapping.
func directEvidenceCoveredByEconomicObservation(draft billingLegDraft, event lipapi.Event) bool {
	if event.Kind != lipapi.EventUsageDelta || event.Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		return false
	}
	key := strings.TrimSpace(event.Accounting.DedupeKey)
	if key == "" || len(draft.economicObservations) == 0 {
		return false
	}
	for _, evidence := range draft.economicObservations {
		observation, err := evidence.Observation.Canonical()
		if err != nil || strings.TrimSpace(observation.SourceEventKey) != key {
			continue
		}
		if observation.Subject.Kind != metering.SubjectBLeg {
			continue
		}
		if storeID := strings.TrimSpace(draft.storeID); storeID != "" && observation.Subject.StoreID != storeID {
			continue
		}
		if bLegID := strings.TrimSpace(draft.bLegID); bLegID != "" && observation.Subject.BLegID != bLegID {
			continue
		}
		return true
	}
	return false
}

func hasCapturedEvidenceKey(keys map[string]struct{}, key string) bool {
	_, ok := keys[strings.TrimSpace(key)]
	return ok
}

func appendBoundedEvidenceConflict(conflicts []billing.EvidenceConflict, conflict billing.EvidenceConflict) []billing.EvidenceConflict {
	if slices.Contains(conflicts, conflict) {
		return conflicts
	}
	if len(conflicts) >= billing.MaxCallLegEvidenceConflicts {
		return conflicts
	}
	return append(conflicts, conflict)
}

func billingObservationFromEvent(draft billingLegDraft, input billingObservationInput) (metering.Observation, bool) {
	event := input.event
	if event.Kind != lipapi.EventUsageDelta {
		return metering.Observation{}, false
	}
	key := strings.TrimSpace(event.Accounting.DedupeKey)
	// A source identity is required by the V2 replay contract. A proxy-owned
	// synthetic V1 key remains available for legacy closure/readers, but it is
	// not presented as provider source identity in V2.
	if key == "" {
		return metering.Observation{}, false
	}
	storeID := strings.TrimSpace(draft.storeID)
	if storeID == "" {
		// V1 compatibility evidence has no trusted store scope. Do not emit a
		// V2 observation with a synthetic, empty, or inferred store identity.
		return metering.Observation{}, false
	}
	origin, acquisition, ok := billingObservationProvenance(event.Accounting.Source, input.role)
	if !ok {
		return metering.Observation{}, false
	}
	authority := billingObservationAuthority(event.Accounting.Authority)
	perspective := billingObservationPerspective(event.Accounting.Plane)
	sequence := input.sequence
	if sequence == 0 {
		sequence = 1
	}
	observedAt := draft.finishedAt
	if observedAt.IsZero() {
		observedAt = draft.startedAt
	}
	if observedAt.IsZero() {
		observedAt = time.Unix(0, 0).UTC()
	}
	observedAt = observedAt.UTC()

	callID := ""
	if err := draft.callID.Validate(); err == nil {
		callID = draft.callID.String()
	}
	bLegID := strings.TrimSpace(draft.bLegID)
	if bLegID == "" {
		bLegID = billingSyntheticBLegID(draft.seq)
	}
	aLegID := strings.TrimSpace(draft.aLegID)
	streamID := "b-leg:" + bLegID + ":" + strings.TrimSpace(input.role)
	if strings.TrimSuffix(streamID, ":") == "b-leg:"+bLegID+":" {
		streamID = "b-leg:" + bLegID + ":stream"
	}
	observation := metering.Observation{
		Version:        metering.ObservationVersionV2,
		SourceEventKey: key,
		Revision:       1,
		StreamID:       streamID,
		Sequence:       sequence,
		Origin:         origin,
		Acquisition:    acquisition,
		Authority:      authority,
		Perspective:    perspective,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       storeID,
			SubmissionID:  strings.TrimSpace(draft.submissionID),
			ALegID:        aLegID,
			BillingCallID: callID,
			BLegID:        bLegID,
			AttemptSeq:    positiveAttemptSeq(draft.seq),
		},
		Correlation: metering.CorrelationV2{
			StoreID:       storeID,
			SubmissionID:  strings.TrimSpace(draft.submissionID),
			CallID:        callID,
			BillingCallID: callID,
			ALegID:        aLegID,
			BLegID:        bLegID,
			AttemptSeq:    positiveAttemptSeq(draft.seq),
		},
		Semantics:  billingObservationSemantics(input.role),
		ObservedAt: observedAt,
		ReceivedAt: observedAt,
		MappingRef: billingObservationMappingV1 + ":" + strings.TrimSpace(input.role),
	}

	observation.Measures = billingObservationMeasures(event, authority)
	// Cost is the one monetary field allowed through this neutral conversion,
	// and only a provider-reported source has the authority to claim it. Local
	// estimators remain V1 compatibility evidence; treating them as provider
	// charges here would turn an estimate into operator COGS.
	if event.CostPresent && event.Accounting.Source == lipapi.UsageSourceProviderReported && strings.TrimSpace(event.Currency) != "" && event.CostNanoUnits >= 0 {
		amount := metering.DecimalFromNanoUnits(event.CostNanoUnits)
		observation.Charges = []metering.ReportedCharge{{
			ChargeItemID: "cost:" + key,
			Amount:       &amount,
			Currency:     strings.TrimSpace(event.Currency),
			Kind:         metering.ChargeKindAggregate,
		}}
	}
	if len(observation.Measures) == 0 && len(observation.Charges) == 0 && authority != metering.AuthorityUnavailableClaim {
		// An empty authoritative event is not a useful V2 observation and would
		// fail the SDK envelope validator. Preserve it through the V1 projection.
		return metering.Observation{}, false
	}
	seed := strings.Join([]string{
		callID, aLegID, bLegID, strings.TrimSpace(input.role), origin, acquisition, key, "1",
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	observation.ID = "lip-runtime-" + hex.EncodeToString(sum[:])
	if err := observation.Validate(); err != nil {
		return metering.Observation{}, false
	}
	return observation, true
}

func positiveAttemptSeq(seq int) uint64 {
	if seq <= 0 {
		return 0
	}
	return uint64(seq)
}

func billingObservationProvenance(source lipapi.UsageSource, role string) (origin, acquisition string, ok bool) {
	role = strings.TrimSpace(role)
	switch source {
	case lipapi.UsageSourceProviderReported:
		if role == billingEvidenceRoleFinalizer {
			return metering.OriginProvider, metering.AcquisitionProviderFinalizer, true
		}
		return metering.OriginProvider, metering.AcquisitionProviderResponse, true
	case lipapi.UsageSourceProviderCountAPI:
		return metering.OriginProvider, metering.AcquisitionProviderCountAPI, true
	case lipapi.UsageSourceLocalTokenizer:
		return metering.OriginLocal, metering.AcquisitionLocalTokenizer, true
	case lipapi.UsageSourceLocalEstimator:
		return metering.OriginLocal, metering.AcquisitionLocalEstimator, true
	case lipapi.UsageSourceUnavailable:
		return metering.OriginProvider, metering.AcquisitionProviderResponse, true
	default:
		// proxy_adjusted/policy_reserved have no approved V2 acquisition
		// mapping in the frozen SDK. Keep their V1 fields without inventing a
		// new provider-neutral source vocabulary.
		return "", "", false
	}
}

func billingObservationAuthority(authority lipapi.UsageAuthority) string {
	switch authority {
	case lipapi.UsageAuthorityAuthoritative, lipapi.UsageAuthorityDelegated:
		return metering.AuthorityObservedClaim
	case lipapi.UsageAuthorityEstimated, lipapi.UsageAuthorityAdvisory:
		return metering.AuthorityEstimatedClaim
	case lipapi.UsageAuthorityUnavailable:
		return metering.AuthorityUnavailableClaim
	default:
		return metering.AuthorityUnavailableClaim
	}
}

func billingObservationPerspective(plane lipapi.UsagePlane) metering.EconomicPerspective {
	switch plane {
	case lipapi.UsagePlaneClientVisible:
		return metering.PerspectiveCustomer
	case lipapi.UsagePlaneProviderBillable, lipapi.UsagePlaneProxyBillable:
		return metering.PerspectiveOperator
	default:
		return metering.PerspectiveNone
	}
}

func billingObservationSemantics(role string) string {
	if strings.TrimSpace(role) == billingEvidenceRoleFinalizer {
		return metering.SemanticsCumulative
	}
	return metering.SemanticsDelta
}

func billingObservationMeasures(event lipapi.Event, authority string) []metering.Measure {
	quality := metering.QualityObserved
	switch authority {
	case metering.AuthorityEstimatedClaim:
		quality = metering.QualityEstimated
	case metering.AuthorityUnavailableClaim:
		quality = metering.QualityUnknown
	}
	measures := make([]metering.Measure, 0, 6)
	appendMeasure := func(component string, direction metering.FlowDirection, value int, present bool) {
		if !present || value < 0 {
			return
		}
		decimal := metering.Decimal{Coefficient: strconv.Itoa(value)}
		measures = append(measures, metering.Measure{
			Key: metering.ComponentKey{
				Direction: direction, Component: component, Unit: metering.UnitToken,
				SchemaID: metering.DefaultInclusionSchemaID,
			},
			Value: &decimal, Quality: quality, MethodRef: billingObservationMappingV1,
		})
	}
	appendMeasure(metering.ComponentInputToken, metering.DirectionInput, event.InputTokens, event.UsagePresence.InputTokens)
	appendMeasure(metering.ComponentOutputToken, metering.DirectionOutput, event.OutputTokens, event.UsagePresence.OutputTokens)
	appendMeasure(metering.ComponentCacheReadInputToken, metering.DirectionInput, event.CacheReadTokens, event.UsagePresence.CacheReadTokens)
	appendMeasure(metering.ComponentCacheWriteInputToken, metering.DirectionInput, event.CacheWriteTokens, event.UsagePresence.CacheWriteTokens)
	appendMeasure(metering.ComponentReasoningOutputToken, metering.DirectionOutput, event.ReasoningTokens, event.UsagePresence.ReasoningTokens)
	appendMeasure(metering.ComponentTotalToken, metering.DirectionNone, event.TotalTokens, event.UsagePresence.TotalTokens)
	return measures
}

func eventEvidenceFingerprint(event lipapi.Event) string {
	payload, err := json.Marshal(cloneBillingEvidenceEvent(event))
	if err != nil {
		return fmt.Sprintf("invalid:%v", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
