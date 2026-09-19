package billing

import (
	"fmt"
	"math/big"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.3B/R3 deep retention validation. Every nested result field is
// validated through the existing bounded domain validators, and every retained
// reference is bound to the parent result store and economic subject before a
// payload can be canonicalized or persisted.

func validateRetentionNested(r ReconciliationRetentionResult) error {
	parent := r.Subject
	if err := validateRetentionObservationRefs(r.ObservationRefs, parent.StoreID); err != nil {
		return err
	}
	if err := validateRetentionCoverageRefs(r.CoverageRefs, parent.StoreID); err != nil {
		return err
	}
	if err := validateRetentionQuantityComparison(r.Quantity, parent); err != nil {
		return err
	}
	if err := validateRetentionMonetaryComparison(r.Monetary, parent); err != nil {
		return err
	}
	if err := validateRetentionAggregate(r.Aggregate, r.Quantity, r.Monetary, parent); err != nil {
		return err
	}
	return nil
}

func validateRetentionObservationRefs(refs []metering.ObservationRef, storeID string) error {
	if len(refs) > MaxReconciliationRetentionObservationRefs {
		return fmt.Errorf("%w: observation refs exceed %d", ErrInvalidReconciliationRetention, MaxReconciliationRetentionObservationRefs)
	}
	seen := make(map[string]string, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: observation ref %d: %v", ErrInvalidReconciliationRetention, i, err)
		}
		if ref.StoreID != storeID {
			return fmt.Errorf("%w: observation ref %d store %q does not match result store %q", ErrInvalidReconciliationRetention, i, ref.StoreID, storeID)
		}
		// One store/observation/revision is one identity: the payload hash is
		// not part of the identity, so a differing hash is a conflict and an
		// identical hash is a duplicate; neither may be retained twice.
		key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + fmt.Sprint(ref.Revision)
		if priorHash, exists := seen[key]; exists {
			if priorHash != ref.PayloadHash {
				return fmt.Errorf("%w: observation ref %d conflicts with source identity %q", ErrInvalidReconciliationRetention, i, ref.ObservationID)
			}
			return fmt.Errorf("%w: duplicate observation ref %d", ErrInvalidReconciliationRetention, i)
		}
		seen[key] = ref.PayloadHash
	}
	return nil
}

func validateRetentionCoverageRefs(refs []metering.ChargeCoverageRef, storeID string) error {
	seen := make(map[string]string, len(refs))
	for i, edge := range refs {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("%w: coverage ref %d: %v", ErrInvalidReconciliationRetention, i, err)
		}
		if edge.Ref.StoreID != storeID {
			return fmt.Errorf("%w: coverage ref %d store %q does not match result store %q", ErrInvalidReconciliationRetention, i, edge.Ref.StoreID, storeID)
		}
		// The referenced charge item is the identity; contradictory relations
		// conflict and identical edges are duplicates.
		key := edge.Ref.StoreID + "\x00" + edge.Ref.ObservationID + "\x00" +
			fmt.Sprint(edge.Ref.Revision) + "\x00" + edge.Ref.ChargeItemID
		if priorRelation, exists := seen[key]; exists {
			if priorRelation != string(edge.Relation) {
				return fmt.Errorf("%w: contradictory coverage relation for %q", ErrInvalidReconciliationRetention, edge.Ref.ChargeItemID)
			}
			return fmt.Errorf("%w: duplicate coverage ref %d", ErrInvalidReconciliationRetention, i)
		}
		seen[key] = string(edge.Relation)
	}
	return nil
}

func validateRetentionQuantityComparison(in *ComponentQuantityComparison, subject metering.SubjectRef) error {
	if in == nil {
		return nil
	}
	if !reconciliationFindingStatusKnown(in.Status) {
		return fmt.Errorf("%w: unknown quantity comparison status %q", ErrInvalidReconciliationRetention, in.Status)
	}
	if !in.Reason.IsKnown() {
		return fmt.Errorf("%w: unknown quantity comparison reason %q", ErrInvalidReconciliationRetention, in.Reason)
	}
	if (in.Complete || in.Status == ReconciliationStatusMatched || in.Status == ReconciliationStatusDiscrepant) && in.Reason != ReconciliationReasonNone {
		return fmt.Errorf("%w: complete/comparable quantity comparison cannot carry a reason", ErrInvalidReconciliationRetention)
	}
	if len(in.Items) > MaxReconciliationItems {
		return fmt.Errorf("%w: quantity items exceed %d", ErrInvalidReconciliationRetention, MaxReconciliationItems)
	}
	seenItems := make(map[string]struct{}, len(in.Items))
	for i := range in.Items {
		item := in.Items[i]
		key, err := item.Key.Normalize()
		if err != nil {
			return fmt.Errorf("%w: quantity item %d key: %v", ErrInvalidReconciliationRetention, i, err)
		}
		itemKey := key.CanonicalKey()
		if _, exists := seenItems[itemKey]; exists {
			return fmt.Errorf("%w: duplicate quantity item %d", ErrInvalidReconciliationRetention, i)
		}
		seenItems[itemKey] = struct{}{}
		if !reconciliationFindingStatusKnown(item.Status) {
			return fmt.Errorf("%w: quantity item %d unknown status %q", ErrInvalidReconciliationRetention, i, item.Status)
		}
		if !item.Reason.IsKnown() {
			return fmt.Errorf("%w: quantity item %d unknown reason %q", ErrInvalidReconciliationRetention, i, item.Reason)
		}
		if err := validateRetentionQuantityEvidence(item.Local, key, subject.StoreID, i, "local"); err != nil {
			return err
		}
		if err := validateRetentionQuantityEvidence(item.Provider, key, subject.StoreID, i, "provider"); err != nil {
			return err
		}
		if (item.SignedDelta == nil) != (item.AbsoluteDelta == nil) {
			return fmt.Errorf("%w: quantity item %d must carry both or neither delta", ErrInvalidReconciliationRetention, i)
		}
		for name, delta := range map[string]*metering.Decimal{"signed": item.SignedDelta, "absolute": item.AbsoluteDelta} {
			if delta == nil {
				continue
			}
			if err := delta.Validate(); err != nil {
				return fmt.Errorf("%w: quantity item %d %s delta: %v", ErrInvalidReconciliationRetention, i, name, err)
			}
		}
		switch item.Status {
		case ReconciliationStatusMatched, ReconciliationStatusDiscrepant:
			// The live comparator emits exactly one source per side for a
			// comparable pair and computes both deltas from those two values.
			// Retention rederives the same authoritative arithmetic and rejects
			// a forged delta, a missing value or an ambiguous source set.
			if len(item.Local) != 1 || len(item.Provider) != 1 {
				return fmt.Errorf("%w: quantity item %d comparable status %q must carry exactly one local and one provider source, got %d local and %d provider", ErrInvalidReconciliationRetention, i, item.Status, len(item.Local), len(item.Provider))
			}
			if item.Local[0].Value == nil || item.Provider[0].Value == nil {
				return fmt.Errorf("%w: quantity item %d comparable status %q requires both exact evidence values", ErrInvalidReconciliationRetention, i, item.Status)
			}
			signed, absolute, comparison, err := reconciliationExactDelta(*item.Local[0].Value, *item.Provider[0].Value)
			if err != nil {
				return fmt.Errorf("%w: quantity item %d exact provider-local delta: %v", ErrInvalidReconciliationRetention, i, err)
			}
			if item.SignedDelta == nil || item.AbsoluteDelta == nil {
				return fmt.Errorf("%w: quantity item %d comparable status %q lacks signed/absolute deltas", ErrInvalidReconciliationRetention, i, item.Status)
			}
			if !item.SignedDelta.Equal(signed) || !item.AbsoluteDelta.Equal(absolute) {
				return fmt.Errorf("%w: quantity item %d retained deltas do not match the exact provider-local evidence difference", ErrInvalidReconciliationRetention, i)
			}
			derived := ReconciliationStatusMatched
			if comparison != 0 {
				derived = ReconciliationStatusDiscrepant
			}
			if item.Status != derived {
				return fmt.Errorf("%w: quantity item %d status %q does not match the exact delta sign", ErrInvalidReconciliationRetention, i, item.Status)
			}
			if item.Reason != ReconciliationReasonNone {
				return fmt.Errorf("%w: quantity item %d comparable status %q cannot carry a source reason", ErrInvalidReconciliationRetention, i, item.Status)
			}
		case ReconciliationStatusMissingLocal:
			if len(item.Local) != 0 || len(item.Provider) == 0 || item.SignedDelta != nil {
				return fmt.Errorf("%w: quantity item %d missing_local shape is invalid", ErrInvalidReconciliationRetention, i)
			}
		case ReconciliationStatusMissingProvider:
			if len(item.Provider) != 0 || len(item.Local) == 0 || item.SignedDelta != nil {
				return fmt.Errorf("%w: quantity item %d missing_provider shape is invalid", ErrInvalidReconciliationRetention, i)
			}
		default:
			if item.SignedDelta != nil {
				return fmt.Errorf("%w: quantity item %d non-comparable status %q carries deltas", ErrInvalidReconciliationRetention, i, item.Status)
			}
		}
	}
	derivedStatus, derivedComplete := summarizeComponentQuantityComparison(in.Items)
	if in.Status != derivedStatus || in.Complete != derivedComplete {
		return fmt.Errorf("%w: quantity comparison status/complete (%s/%v) does not match its items (%s/%v)", ErrInvalidReconciliationRetention, in.Status, in.Complete, derivedStatus, derivedComplete)
	}
	return nil
}

func validateRetentionQuantityEvidence(evidence []ReconciliationQuantityEvidence, parentKey metering.ComponentKey, storeID string, itemIndex int, side string) error {
	if len(evidence) > MaxReconciliationRetentionObservationRefs {
		return fmt.Errorf("%w: quantity item %d %s evidence exceeds %d", ErrInvalidReconciliationRetention, itemIndex, side, MaxReconciliationRetentionObservationRefs)
	}
	seen := make(map[string]string, len(evidence))
	for j := range evidence {
		entry := evidence[j]
		key, err := entry.Key.Normalize()
		if err != nil {
			return fmt.Errorf("%w: quantity item %d %s evidence %d key: %v", ErrInvalidReconciliationRetention, itemIndex, side, j, err)
		}
		if !key.Equal(parentKey) {
			return fmt.Errorf("%w: quantity item %d %s evidence %d component key does not match the parent item", ErrInvalidReconciliationRetention, itemIndex, side, j)
		}
		if err := entry.Observation.Validate(); err != nil {
			return fmt.Errorf("%w: quantity item %d %s evidence %d ref: %v", ErrInvalidReconciliationRetention, itemIndex, side, j, err)
		}
		if entry.Observation.StoreID != storeID {
			return fmt.Errorf("%w: quantity item %d %s evidence %d store %q does not match result store %q", ErrInvalidReconciliationRetention, itemIndex, side, j, entry.Observation.StoreID, storeID)
		}
		if !reconciliationQualityKnown(entry.Quality) {
			return fmt.Errorf("%w: quantity item %d %s evidence %d unknown quality %q", ErrInvalidReconciliationRetention, itemIndex, side, j, entry.Quality)
		}
		if entry.MethodRef != "" && !validEconomicIdentity(entry.MethodRef, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%w: quantity item %d %s evidence %d method ref is not a bounded identity", ErrInvalidReconciliationRetention, itemIndex, side, j)
		}
		if entry.Value != nil {
			if err := entry.Value.Validate(); err != nil {
				return fmt.Errorf("%w: quantity item %d %s evidence %d value: %v", ErrInvalidReconciliationRetention, itemIndex, side, j, err)
			}
		}
		// One observation revision is one identity regardless of payload hash.
		refKey := entry.Observation.StoreID + "\x00" + entry.Observation.ObservationID + "\x00" +
			fmt.Sprint(entry.Observation.Revision)
		if priorHash, exists := seen[refKey]; exists {
			if priorHash != entry.Observation.PayloadHash {
				return fmt.Errorf("%w: quantity item %d %s evidence %d conflicts for observation %q", ErrInvalidReconciliationRetention, itemIndex, side, j, entry.Observation.ObservationID)
			}
			return fmt.Errorf("%w: quantity item %d %s evidence %d duplicates observation %q", ErrInvalidReconciliationRetention, itemIndex, side, j, entry.Observation.ObservationID)
		}
		seen[refKey] = entry.Observation.PayloadHash
	}
	return nil
}

func validateRetentionMonetaryComparison(in *MonetaryDiscrepancyComparison, subject metering.SubjectRef) error {
	if in == nil {
		return nil
	}
	if err := in.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: monetary subject: %v", ErrInvalidReconciliationRetention, err)
	}
	if !sameSubject(in.Subject, subject) {
		return fmt.Errorf("%w: monetary subject does not match retention subject", ErrInvalidReconciliationRetention)
	}
	if !in.Status.IsKnown() {
		return fmt.Errorf("%w: unknown monetary comparison status %q", ErrInvalidReconciliationRetention, in.Status)
	}
	if !in.Reason.IsKnown() {
		return fmt.Errorf("%w: unknown monetary comparison reason %q", ErrInvalidReconciliationRetention, in.Reason)
	}
	if len(in.Rows) > MaxMonetaryDiscrepancyRows {
		return fmt.Errorf("%w: monetary rows exceed %d", ErrInvalidReconciliationRetention, MaxMonetaryDiscrepancyRows)
	}
	seenRoles := make(map[MonetaryDiscrepancyRole]struct{}, len(in.Valuations))
	for i := range in.Valuations {
		evidence := in.Valuations[i]
		if !monetaryRoleKnown(evidence.Role) {
			return fmt.Errorf("%w: monetary valuation %d unknown role %q", ErrInvalidReconciliationRetention, i, evidence.Role)
		}
		if _, exists := seenRoles[evidence.Role]; exists {
			return fmt.Errorf("%w: duplicate monetary valuation role %q", ErrInvalidReconciliationRetention, evidence.Role)
		}
		seenRoles[evidence.Role] = struct{}{}
		if err := evidence.Valuation.Validate(); err != nil {
			return fmt.Errorf("%w: monetary valuation %d: %v", ErrInvalidReconciliationRetention, i, err)
		}
		if !sameSubject(evidence.Valuation.Subject, subject) {
			return fmt.Errorf("%w: monetary valuation %d subject does not match retention subject", ErrInvalidReconciliationRetention, i)
		}
		if err := validateRetentionObservationRefs(evidence.Valuation.InputObservations, subject.StoreID); err != nil {
			return fmt.Errorf("%w: monetary valuation %d input refs: %v", ErrInvalidReconciliationRetention, i, err)
		}
	}
	seenCurrencies := make(map[string]struct{}, len(in.Rows))
	for i := range in.Rows {
		row := in.Rows[i]
		currency, err := economics.NormalizeCurrency(row.Currency)
		if err != nil {
			return fmt.Errorf("%w: monetary row %d currency: %v", ErrInvalidReconciliationRetention, i, err)
		}
		if row.Currency != currency {
			return fmt.Errorf("%w: monetary row %d currency %q is not normalized", ErrInvalidReconciliationRetention, i, row.Currency)
		}
		if _, exists := seenCurrencies[currency]; exists {
			return fmt.Errorf("%w: duplicate monetary row %d currency %q", ErrInvalidReconciliationRetention, i, currency)
		}
		seenCurrencies[currency] = struct{}{}
		for name, term := range map[string]MonetaryDiscrepancyTerm{
			"metering": in.Rows[i].MeteringCostEffect, "residual": in.Rows[i].ReportedPriceResidual, "end_to_end": in.Rows[i].EndToEndCostDelta,
		} {
			if err := validateRetentionMonetaryTerm(term, currency); err != nil {
				return fmt.Errorf("%w: monetary row %d %s term: %w", ErrInvalidReconciliationRetention, i, name, err)
			}
		}
	}
	if err := validateRetentionQuantityComparison(in.Quantity, subject); err != nil {
		return err
	}
	// The overall rollup is derived from the validated terms and quantity
	// integration; a stored label that disagrees is rejected rather than trusted.
	derivedStatus, derivedReason := monetaryDiscrepancyRollup(in)
	if in.Status != derivedStatus || in.Reason != derivedReason {
		return fmt.Errorf("%w: monetary comparison status/reason (%s/%s) does not match its terms (%s/%s)", ErrInvalidReconciliationRetention, in.Status, in.Reason, derivedStatus, derivedReason)
	}
	// The terms themselves are rederived from the retained E/Q/P valuations with
	// the authoritative producer, so a copied-but-forged amount, status, reason or
	// cause can never survive retention.
	return validateRetentionMonetaryRederivation(in)
}

// validateRetentionMonetaryRederivation re-runs DecomposeMonetaryDiscrepancies
// over the retained E/Q/P valuations and optional quantity comparison, then
// requires every retained row and term to match the recomputed Q-E/P-Q/P-E
// output. Exact amounts are compared as rationals, so a decimal and a rational
// spelling of the same exact value are equivalent; representation differences
// that preserve the exact value are not rejected.
func validateRetentionMonetaryRederivation(in *MonetaryDiscrepancyComparison) error {
	input := MonetaryDiscrepancyInput{QuantityComparison: in.Quantity}
	input.Valuations = make([]economics.Valuation, 0, len(in.Valuations))
	for i := range in.Valuations {
		input.Valuations = append(input.Valuations, in.Valuations[i].Valuation)
	}
	expected, err := DecomposeMonetaryDiscrepancies(input)
	if err != nil {
		return fmt.Errorf("%w: monetary result cannot be rederived from its retained valuations: %v", ErrInvalidReconciliationRetention, err)
	}
	if expected.Status != in.Status || expected.Reason != in.Reason {
		return fmt.Errorf("%w: monetary comparison status/reason (%s/%s) does not match the rederived decomposition (%s/%s)", ErrInvalidReconciliationRetention, in.Status, in.Reason, expected.Status, expected.Reason)
	}
	if len(expected.Rows) != len(in.Rows) {
		return fmt.Errorf("%w: monetary rows (%d) do not match the rederived decomposition (%d)", ErrInvalidReconciliationRetention, len(in.Rows), len(expected.Rows))
	}
	rederived := make(map[string]MonetaryDiscrepancyRow, len(expected.Rows))
	for _, row := range expected.Rows {
		rederived[row.Currency] = row
	}
	for i := range in.Rows {
		row := in.Rows[i]
		want, ok := rederived[row.Currency]
		if !ok {
			return fmt.Errorf("%w: monetary row %d currency %q is not present in the rederived decomposition", ErrInvalidReconciliationRetention, i, row.Currency)
		}
		terms := map[string][2]MonetaryDiscrepancyTerm{
			"metering_cost_effect":    {row.MeteringCostEffect, want.MeteringCostEffect},
			"reported_price_residual": {row.ReportedPriceResidual, want.ReportedPriceResidual},
			"end_to_end_cost_delta":   {row.EndToEndCostDelta, want.EndToEndCostDelta},
		}
		for name, pair := range terms {
			if !monetaryTermMatches(pair[0], pair[1]) {
				return fmt.Errorf("%w: monetary row %d %s does not match the rederived Q-E/P-Q/P-E decomposition", ErrInvalidReconciliationRetention, i, name)
			}
		}
	}
	return nil
}

// monetaryTermMatches compares two term outcomes for exact semantic equality:
// status, reason and cause must match, and amounts must carry the same currency
// and the same exact rational value.
func monetaryTermMatches(got, want MonetaryDiscrepancyTerm) bool {
	if got.Status != want.Status || got.Reason != want.Reason || got.Cause != want.Cause {
		return false
	}
	if (got.Amount == nil) != (want.Amount == nil) {
		return false
	}
	if got.Amount == nil {
		return true
	}
	if got.Amount.Currency != want.Amount.Currency {
		return false
	}
	gotValue, err := got.Amount.Rat()
	if err != nil {
		return false
	}
	wantValue, err := want.Amount.Rat()
	if err != nil {
		return false
	}
	return gotValue.Cmp(wantValue) == 0
}

func validateRetentionMonetaryTerm(term MonetaryDiscrepancyTerm, currency string) error {
	switch term.Status {
	case MonetaryTermComplete, MonetaryTermPartial, MonetaryTermIncomparable, MonetaryTermMissing:
	default:
		return fmt.Errorf("unknown status %q", term.Status)
	}
	if !term.Reason.IsKnown() {
		return fmt.Errorf("unknown reason %q", term.Reason)
	}
	switch term.Cause {
	case MonetaryCauseNone, MonetaryCauseQuantityDifference, MonetaryCauseSuspectedPricingDifference:
	default:
		return fmt.Errorf("unknown cause %q", term.Cause)
	}
	if term.Amount != nil {
		if err := term.Amount.Validate(); err != nil {
			return fmt.Errorf("amount: %w", err)
		}
		if term.Amount.Currency != currency {
			return fmt.Errorf("amount unit %q does not match row currency %q", term.Amount.Currency, currency)
		}
	}
	switch term.Status {
	case MonetaryTermComplete:
		if term.Amount == nil {
			return fmt.Errorf("complete term requires an amount")
		}
		if term.Reason != MonetaryReasonNone {
			return fmt.Errorf("complete term cannot carry a reason")
		}
	case MonetaryTermPartial:
		if term.Reason == MonetaryReasonNone {
			return fmt.Errorf("partial term requires an explicit reason")
		}
	case MonetaryTermMissing, MonetaryTermIncomparable:
		if term.Amount != nil {
			return fmt.Errorf("%s term cannot carry an amount", term.Status)
		}
		if term.Reason == MonetaryReasonNone {
			return fmt.Errorf("%s term requires an explicit reason", term.Status)
		}
		if term.Cause != MonetaryCauseNone {
			return fmt.Errorf("%s term cannot carry a cause", term.Status)
		}
	}
	return nil
}

func validateRetentionAggregate(in *ReconciliationAggregate, quantity *ComponentQuantityComparison, monetary *MonetaryDiscrepancyComparison, subject metering.SubjectRef) error {
	if in == nil {
		return nil
	}
	if !validEconomicIdentity(in.Policy.ID, metering.MaxSchemaIDBytes) || !validEconomicIdentity(in.Policy.Version, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: aggregate policy ref is required", ErrInvalidReconciliationRetention)
	}
	if len(in.Findings) > MaxReconciliationAggregateFindings {
		return fmt.Errorf("%w: aggregate findings exceed %d", ErrInvalidReconciliationRetention, MaxReconciliationAggregateFindings)
	}
	seenFindings := make(map[string]struct{}, len(in.Findings))
	for i := range in.Findings {
		finding := in.Findings[i]
		if err := validateReconciliationFinding(finding); err != nil {
			return fmt.Errorf("%w: aggregate finding %d: %w", ErrInvalidReconciliationRetention, i, err)
		}
		if err := validateRetentionFindingClassification(finding, in.Policy); err != nil {
			return fmt.Errorf("%w: aggregate finding %d: %w", ErrInvalidReconciliationRetention, i, err)
		}
		for j, ref := range finding.SourceObservationRefs {
			if ref.StoreID != subject.StoreID {
				return fmt.Errorf("%w: aggregate finding %d ref %d store %q does not match result store %q", ErrInvalidReconciliationRetention, i, j, ref.StoreID, subject.StoreID)
			}
		}
		key := finding.Scope + "\x00" + finding.Currency + "\x00" + finding.Unit + "\x00" + finding.ID
		if _, exists := seenFindings[key]; exists {
			return fmt.Errorf("%w: duplicate aggregate finding %d", ErrInvalidReconciliationRetention, i)
		}
		seenFindings[key] = struct{}{}
	}
	if err := validateRetentionAggregateSourceBinding(in, quantity, monetary); err != nil {
		return err
	}
	if len(in.Rows) > MaxReconciliationAggregateRows {
		return fmt.Errorf("%w: aggregate rows exceed %d", ErrInvalidReconciliationRetention, MaxReconciliationAggregateRows)
	}
	seenRows := make(map[string]struct{}, len(in.Rows))
	for i := range in.Rows {
		row := in.Rows[i]
		if err := validateRetentionAggregateRow(row); err != nil {
			return fmt.Errorf("%w: aggregate row %d: %w", ErrInvalidReconciliationRetention, i, err)
		}
		key := row.Scope + "\x00" + row.Currency + "\x00" + row.Unit
		if _, exists := seenRows[key]; exists {
			return fmt.Errorf("%w: duplicate aggregate row %d", ErrInvalidReconciliationRetention, i)
		}
		seenRows[key] = struct{}{}
	}
	expectedRows, err := buildReconciliationAggregateRows(in.Findings)
	if err != nil {
		return fmt.Errorf("%w: aggregate rows cannot be rederived from the retained findings: %v", ErrInvalidReconciliationRetention, err)
	}
	return validateRetentionRederivedAggregateRows(in.Rows, expectedRows)
}

// validateRetentionAggregateSourceBinding proves every retained aggregate
// finding is exactly one authoritative producer projection of the retained
// quantity/monetary comparisons. The producer scope is the finding's own
// retained scope, because both producer functions copy their scope argument
// into every finding they emit. Candidates are generated once per distinct
// scope and none are trusted: zero or multiple exact matches fail closed, and
// findings retained without either source comparison are rejected rather than
// having their provenance inferred.
func validateRetentionAggregateSourceBinding(aggregate *ReconciliationAggregate, quantity *ComponentQuantityComparison, monetary *MonetaryDiscrepancyComparison) error {
	if len(aggregate.Findings) == 0 {
		return nil
	}
	if quantity == nil && monetary == nil {
		return fmt.Errorf("%w: aggregate findings cannot be bound without a retained quantity or monetary comparison", ErrInvalidReconciliationRetention)
	}
	candidatesByScope := make(map[string]map[string][]ReconciliationFinding)
	candidatesForScope := func(scope string) (map[string][]ReconciliationFinding, error) {
		if cached, ok := candidatesByScope[scope]; ok {
			return cached, nil
		}
		byKey := make(map[string][]ReconciliationFinding)
		appendProducer := func(findings []ReconciliationFinding) {
			for _, finding := range findings {
				key := reconciliationSourceBindingKey(finding)
				byKey[key] = append(byKey[key], finding)
			}
		}
		if quantity != nil {
			findings, err := ReconciliationFindingsFromQuantityComparison(scope, *quantity)
			if err != nil {
				return nil, fmt.Errorf("quantity source findings: %v", err)
			}
			appendProducer(findings)
		}
		if monetary != nil {
			findings, err := ReconciliationFindingsFromMonetaryComparison(scope, *monetary)
			if err != nil {
				return nil, fmt.Errorf("monetary source findings: %v", err)
			}
			appendProducer(findings)
		}
		candidatesByScope[scope] = byKey
		return byKey, nil
	}
	for i := range aggregate.Findings {
		finding := aggregate.Findings[i]
		byKey, err := candidatesForScope(finding.Scope)
		if err != nil {
			return fmt.Errorf("%w: aggregate finding %d: %v", ErrInvalidReconciliationRetention, i, err)
		}
		matches := 0
		for _, candidate := range byKey[reconciliationSourceBindingKey(finding)] {
			if reconciliationFindingSourceEqual(finding, candidate) {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%w: aggregate finding %d (%q) matches %d authoritative producer findings for scope %q, want exactly 1", ErrInvalidReconciliationRetention, i, finding.ID, matches, finding.Scope)
		}
	}
	return nil
}

func reconciliationSourceBindingKey(finding ReconciliationFinding) string {
	return finding.Scope + "\x00" + finding.Currency + "\x00" + finding.Unit + "\x00" + finding.ID
}

// reconciliationFindingSourceEqual compares the full producer-facing source
// identity of two findings: target identity, source Status/Reason, exact
// expected/reported amounts (a decimal and a rational spelling of one exact
// value are equal), quality labels, canonicalized source refs and canonicalized
// valuation ids. The evaluated tolerance layer is deliberately excluded because
// it is a derived projection validated separately.
func reconciliationFindingSourceEqual(left, right ReconciliationFinding) bool {
	if left.ID != right.ID || left.Scope != right.Scope || left.Direction != right.Direction ||
		left.Unit != right.Unit || left.Currency != right.Currency || left.Component != right.Component ||
		left.SchemaID != right.SchemaID || left.Context != right.Context ||
		left.Status != right.Status || left.Reason != right.Reason ||
		left.LocalQuality != right.LocalQuality || left.ProviderQuality != right.ProviderQuality {
		return false
	}
	if !monetaryExactAmountsEqual(left.Expected, right.Expected) || !monetaryExactAmountsEqual(left.Reported, right.Reported) {
		return false
	}
	return sameRetentionObservationRefValues(left.SourceObservationRefs, right.SourceObservationRefs) && sameRetentionIDs(left.ValuationIDs, right.ValuationIDs)
}

func sameRetentionObservationRefValues(left, right []metering.ObservationRef) bool {
	leftCanonical := canonicalReconciliationObservationRefs(left)
	rightCanonical := canonicalReconciliationObservationRefs(right)
	if len(leftCanonical) != len(rightCanonical) {
		return false
	}
	for i := range leftCanonical {
		if !leftCanonical[i].Equal(rightCanonical[i]) {
			return false
		}
	}
	return true
}

// validateRetentionFindingClassification validates the deliberate finding
// and evaluation reason vocabulary and rederives the evaluated classification
// plus the nested tolerance evaluation from the retained finding and the
// aggregate policy reference. The policy's rule set is not retained, so rule
// selection and limit provenance cannot be verified here; every field
// derivable from the retained finding, evaluation and limits is.
func validateRetentionFindingClassification(finding ReconciliationFinding, policy VersionRef) error {
	if !reconciliationFindingReasonKnown(finding.Reason) {
		return fmt.Errorf("unknown finding reason %q", finding.Reason)
	}
	if !reconciliationFindingStatusKnown(finding.EvaluatedStatus) {
		return fmt.Errorf("unknown evaluated status %q", finding.EvaluatedStatus)
	}
	if !reconciliationFindingReasonKnown(finding.EvaluationReason) {
		return fmt.Errorf("unknown evaluation reason %q", finding.EvaluationReason)
	}
	switch finding.Status {
	case ReconciliationStatusMatched, ReconciliationStatusDiscrepant, ReconciliationStatusWithinTolerance:
		if finding.Expected == nil || finding.Reported == nil {
			if finding.Evaluation != nil {
				return fmt.Errorf("finding without both exact amounts cannot carry a tolerance evaluation")
			}
			if finding.EvaluatedStatus != ReconciliationStatusPartial || finding.EvaluationReason != ReconciliationReasonTolerancePolicyMissing {
				return fmt.Errorf("finding without both exact amounts must be evaluated partial/tolerance_policy_missing")
			}
			return nil
		}
		if finding.Evaluation == nil {
			return fmt.Errorf("comparable finding with both exact amounts requires a tolerance evaluation")
		}
		return validateRetentionToleranceEvaluation(finding, policy)
	default:
		if finding.Evaluation != nil {
			return fmt.Errorf("non-comparable finding cannot carry a tolerance evaluation")
		}
		if finding.EvaluatedStatus != finding.Status || finding.EvaluationReason != finding.Reason {
			return fmt.Errorf("non-comparable finding evaluated status/reason must equal its status/reason")
		}
		return nil
	}
}

// validateRetentionToleranceEvaluation recomputes the exact tolerance
// arithmetic and classification from the retained finding amounts and the
// retained exact limits, then requires every nested evaluation field to match.
// Exact values are compared as rationals, so a decimal and a rational spelling
// of the same exact value are equivalent.
func validateRetentionToleranceEvaluation(finding ReconciliationFinding, policy VersionRef) error {
	evaluation := finding.Evaluation
	if evaluation.PolicyID != policy.ID || evaluation.PolicyVersion != policy.Version {
		return fmt.Errorf("evaluation policy ref does not match the retained aggregate policy")
	}
	target := ReconciliationToleranceTarget{
		Unit: finding.Unit, Currency: finding.Currency,
		Component: finding.Component, SchemaID: finding.SchemaID, Context: finding.Context,
	}
	if evaluation.Target != target {
		return fmt.Errorf("evaluation target does not match its finding")
	}
	if !monetaryExactAmountsEqual(evaluation.Expected, finding.Expected) || !monetaryExactAmountsEqual(evaluation.Reported, finding.Reported) {
		return fmt.Errorf("evaluation expected/reported amounts do not match its finding")
	}
	expectedRat, err := finding.Expected.Rat()
	if err != nil {
		return fmt.Errorf("finding expected amount: %v", err)
	}
	reportedRat, err := finding.Reported.Rat()
	if err != nil {
		return fmt.Errorf("finding reported amount: %v", err)
	}
	unit := finding.Expected.Currency
	signedRat := new(big.Rat).Sub(reportedRat, expectedRat)
	absoluteRat := new(big.Rat).Abs(signedRat)
	signed, err := toleranceExactAmount(unit, signedRat)
	if err != nil {
		return err
	}
	absolute, err := toleranceExactAmount(unit, absoluteRat)
	if err != nil {
		return err
	}
	if !monetaryExactAmountsEqual(evaluation.SignedDelta, &signed) || !monetaryExactAmountsEqual(evaluation.AbsoluteDelta, &absolute) {
		return fmt.Errorf("evaluation signed/absolute delta does not match the retained exact amounts")
	}
	if evaluation.ZeroDenominator != (expectedRat.Sign() == 0) {
		return fmt.Errorf("evaluation zero-denominator flag does not match the retained expected amount")
	}
	if evaluation.RuleID == "" {
		if evaluation.AbsoluteLimit != nil || evaluation.RelativeLimit != nil {
			return fmt.Errorf("evaluation without a matching rule cannot retain limits")
		}
		if evaluation.Threshold != nil || evaluation.RelativeDifference != nil || evaluation.RelativePresent || evaluation.WithinTolerance {
			return fmt.Errorf("evaluation without a matching rule cannot retain a tolerance classification")
		}
		if evaluation.Status != ReconciliationStatusPartial || evaluation.Reason != ReconciliationReasonTolerancePolicyMissing {
			return fmt.Errorf("evaluation without a matching rule must be partial/tolerance_policy_missing")
		}
		return nil
	}
	if !validEconomicIdentity(evaluation.RuleID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("evaluation rule id is not a bounded identity")
	}
	if evaluation.AbsoluteLimit == nil && evaluation.RelativeLimit == nil {
		return fmt.Errorf("matching evaluation requires at least one exact limit")
	}
	if err := validateToleranceLimit("absolute limit", evaluation.AbsoluteLimit); err != nil {
		return err
	}
	if err := validateToleranceLimit("relative limit", evaluation.RelativeLimit); err != nil {
		return err
	}
	var threshold *big.Rat
	if evaluation.AbsoluteLimit != nil {
		threshold, err = evaluation.AbsoluteLimit.ToRat()
		if err != nil {
			return fmt.Errorf("absolute limit: %v", err)
		}
	}
	if expectedRat.Sign() != 0 && evaluation.RelativeLimit != nil {
		relativeLimit, err := evaluation.RelativeLimit.ToRat()
		if err != nil {
			return fmt.Errorf("relative limit: %v", err)
		}
		relativeComponent := new(big.Rat).Mul(relativeLimit, new(big.Rat).Abs(expectedRat))
		if threshold == nil || relativeComponent.Cmp(threshold) > 0 {
			threshold = relativeComponent
		}
	}
	if threshold == nil {
		if evaluation.Threshold != nil || evaluation.RelativeDifference != nil || evaluation.RelativePresent || evaluation.WithinTolerance {
			return fmt.Errorf("relative-only zero-denominator evaluation cannot retain a threshold or relative difference")
		}
		if evaluation.Status != ReconciliationStatusPartial || evaluation.Reason != ReconciliationReasonZeroDenominator {
			return fmt.Errorf("relative-only zero-denominator evaluation must be partial/zero_denominator")
		}
		return nil
	}
	thresholdAmount, err := toleranceExactAmount(unit, threshold)
	if err != nil {
		return err
	}
	if !monetaryExactAmountsEqual(evaluation.Threshold, &thresholdAmount) {
		return fmt.Errorf("evaluation threshold does not match the retained limits and expected amount")
	}
	if expectedRat.Sign() != 0 {
		relativeRat := new(big.Rat).Quo(absoluteRat, new(big.Rat).Abs(expectedRat))
		relativeAmount, err := toleranceExactAmount(unit, relativeRat)
		if err != nil {
			return err
		}
		if !evaluation.RelativePresent || !monetaryExactAmountsEqual(evaluation.RelativeDifference, &relativeAmount) {
			return fmt.Errorf("evaluation relative difference does not match the retained exact amounts")
		}
	} else if evaluation.RelativePresent || evaluation.RelativeDifference != nil {
		return fmt.Errorf("zero expected amount cannot retain a relative difference")
	}
	derivedStatus, derivedReason, derivedWithin := retainedEvaluationClassification(signedRat, absoluteRat, threshold, expectedRat, finding)
	if evaluation.Status != derivedStatus || evaluation.Reason != derivedReason || evaluation.WithinTolerance != derivedWithin {
		return fmt.Errorf("evaluation status/reason/within-tolerance does not match the rederived classification")
	}
	if finding.EvaluatedStatus != evaluation.Status || finding.EvaluationReason != evaluation.Reason {
		return fmt.Errorf("finding evaluated status/reason does not match its evaluation")
	}
	return nil
}

// retainedEvaluationClassification mirrors the authoritative producer
// classification: zero signed delta is matched, a difference within the exact
// threshold is within_tolerance, anything else is discrepant. A zero expected
// amount retains the zero_denominator reason, and an estimated exact match is
// downgraded to within_tolerance/estimated_not_exact.
func retainedEvaluationClassification(signed, absolute, threshold, expected *big.Rat, finding ReconciliationFinding) (ReconciliationComparisonStatus, ReconciliationComparisonReason, bool) {
	status := ReconciliationStatusDiscrepant
	within := false
	switch {
	case signed.Sign() == 0:
		status, within = ReconciliationStatusMatched, true
	case absolute.Cmp(threshold) <= 0:
		status, within = ReconciliationStatusWithinTolerance, true
	}
	reason := ReconciliationReasonNone
	if expected.Sign() == 0 {
		reason = ReconciliationReasonZeroDenominator
	}
	if status == ReconciliationStatusMatched && reconciliationFindingEstimated(finding) {
		status, reason, within = ReconciliationStatusWithinTolerance, ReconciliationReasonEstimatedNotExact, true
	}
	return status, reason, within
}

func monetaryExactAmountsEqual(left, right *MonetaryExactAmount) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	if left == nil {
		return true
	}
	if left.Currency != right.Currency {
		return false
	}
	leftRat, err := left.Rat()
	if err != nil {
		return false
	}
	rightRat, err := right.Rat()
	if err != nil {
		return false
	}
	return leftRat.Cmp(rightRat) == 0
}

// validateRetentionRederivedAggregateRows requires every retained row to match
// the row rebuilt from the retained findings by the authoritative aggregation.
func validateRetentionRederivedAggregateRows(retained, expected []ReconciliationAggregateRow) error {
	if len(retained) != len(expected) {
		return fmt.Errorf("%w: aggregate rows (%d) do not match the rederived projection (%d)", ErrInvalidReconciliationRetention, len(retained), len(expected))
	}
	expectedByKey := make(map[reconciliationAggregateKey]ReconciliationAggregateRow, len(expected))
	for _, row := range expected {
		expectedByKey[reconciliationAggregateKey{Scope: row.Scope, Currency: row.Currency, Unit: row.Unit}] = row
	}
	for i := range retained {
		row := retained[i]
		want, ok := expectedByKey[reconciliationAggregateKey{Scope: row.Scope, Currency: row.Currency, Unit: row.Unit}]
		if !ok {
			return fmt.Errorf("%w: aggregate row %d (%q/%q/%q) is not derived from the retained findings", ErrInvalidReconciliationRetention, i, row.Scope, row.Currency, row.Unit)
		}
		if err := validateRetentionRederivedAggregateRow(row, want); err != nil {
			return fmt.Errorf("%w: aggregate row %d: %w", ErrInvalidReconciliationRetention, i, err)
		}
	}
	return nil
}

func validateRetentionRederivedAggregateRow(got, want ReconciliationAggregateRow) error {
	for name, pair := range map[string][2]*MonetaryExactAmount{
		"gross absolute":      {got.GrossAbsoluteDiscrepancy, want.GrossAbsoluteDiscrepancy},
		"discrepant absolute": {got.DiscrepantAbsoluteDiscrepancy, want.DiscrepantAbsoluteDiscrepancy},
		"net signed":          {got.NetSignedDiscrepancy, want.NetSignedDiscrepancy},
	} {
		if !monetaryExactAmountsEqual(pair[0], pair[1]) {
			return fmt.Errorf("%s discrepancy does not match the rederived projection", name)
		}
	}
	if got.AffectedCount != want.AffectedCount || got.EstimatedAffectedCount != want.EstimatedAffectedCount {
		return fmt.Errorf("affected counts do not match the rederived projection")
	}
	if !sameRetentionStatusCounts(got.StatusCounts, want.StatusCounts) {
		return fmt.Errorf("status counts do not match the rederived projection")
	}
	for name, pair := range map[string][2][]string{
		"missing":      {got.MissingIDs, want.MissingIDs},
		"incomparable": {got.IncomparableIDs, want.IncomparableIDs},
		"conflict":     {got.ConflictIDs, want.ConflictIDs},
	} {
		if !sameRetentionIDs(pair[0], pair[1]) {
			return fmt.Errorf("%s ids do not match the rederived projection", name)
		}
	}
	return nil
}

func sameRetentionStatusCounts(left, right []ReconciliationStatusCount) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[ReconciliationComparisonStatus]int, len(left))
	for _, entry := range left {
		counts[entry.Status] += entry.Count
	}
	for _, entry := range right {
		if counts[entry.Status] != entry.Count {
			return false
		}
	}
	return true
}

func sameRetentionIDs(left, right []string) bool {
	leftCanonical := canonicalReconciliationStrings(left)
	rightCanonical := canonicalReconciliationStrings(right)
	if len(leftCanonical) != len(rightCanonical) {
		return false
	}
	for i := range leftCanonical {
		if leftCanonical[i] != rightCanonical[i] {
			return false
		}
	}
	return true
}

func validateRetentionAggregateRow(row ReconciliationAggregateRow) error {
	if row.Scope != "" && !validEconomicIdentity(row.Scope, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("row scope is not a bounded identity")
	}
	unitKey := row.Currency
	if unitKey == "" {
		unitKey = row.Unit
	}
	if !validEconomicIdentity(unitKey, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("row currency or unit key is required")
	}
	if row.Currency != "" {
		currency, err := economics.NormalizeCurrency(row.Currency)
		if err != nil || currency != row.Currency {
			return fmt.Errorf("row currency %q is not normalized", row.Currency)
		}
	}
	if row.Unit != "" && !validEconomicIdentity(row.Unit, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("row unit is not a bounded identity")
	}
	for name, amount := range map[string]*MonetaryExactAmount{
		"gross absolute":      row.GrossAbsoluteDiscrepancy,
		"discrepant absolute": row.DiscrepantAbsoluteDiscrepancy,
		"net signed":          row.NetSignedDiscrepancy,
	} {
		if amount == nil {
			return fmt.Errorf("%s discrepancy is required", name)
		}
		if err := amount.Validate(); err != nil {
			return fmt.Errorf("%s discrepancy: %w", name, err)
		}
		if amount.Currency != unitKey {
			return fmt.Errorf("%s discrepancy unit %q does not match row unit %q", name, amount.Currency, unitKey)
		}
	}
	if row.AffectedCount < 0 || row.EstimatedAffectedCount < 0 || row.EstimatedAffectedCount > row.AffectedCount {
		return fmt.Errorf("row affected counts are invalid")
	}
	seenStatus := make(map[ReconciliationComparisonStatus]struct{}, len(row.StatusCounts))
	for _, count := range row.StatusCounts {
		if !reconciliationFindingStatusKnown(count.Status) {
			return fmt.Errorf("unknown status count %q", count.Status)
		}
		if count.Count <= 0 {
			return fmt.Errorf("status count %q must be positive", count.Status)
		}
		if _, exists := seenStatus[count.Status]; exists {
			return fmt.Errorf("duplicate status count %q", count.Status)
		}
		seenStatus[count.Status] = struct{}{}
	}
	for name, ids := range map[string][]string{
		"missing": row.MissingIDs, "incomparable": row.IncomparableIDs, "conflict": row.ConflictIDs,
	} {
		if len(ids) > MaxReconciliationAggregateFindings {
			return fmt.Errorf("%s ids exceed the finding bound", name)
		}
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if !validEconomicIdentity(id, metering.MaxSchemaIDBytes) {
				return fmt.Errorf("%s id is not a bounded identity", name)
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("duplicate %s id %q", name, id)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func monetaryRoleKnown(role MonetaryDiscrepancyRole) bool {
	switch role {
	case MonetaryRoleE, MonetaryRoleQ, MonetaryRoleP:
		return true
	default:
		return false
	}
}
