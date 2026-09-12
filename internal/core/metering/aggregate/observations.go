package aggregate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

var (
	// ErrAmbiguousCumulative means two non-additive snapshots have the same
	// declared stream position and no revision ordering can select one.
	ErrAmbiguousCumulative = errors.New("metering/aggregate: ambiguous cumulative observation")
	// ErrIdentityConflict is the reducer-facing spelling of the shared replay
	// identity error. It remains one authority, owned by replay.
	ErrIdentityConflict = replay.ErrIdentityConflict
)

// Scope is the complete source/subject/charge scope of one reduced measure.
// A component key is not sufficient identity: local and provider evidence,
// distinct boundaries/perspectives, accounts, charge events, streams and
// subjects remain separate.
type Scope struct {
	StoreID     string
	TenantID    string
	AccountKey  string
	Origin      string
	Acquisition string
	Perspective metering.EconomicPerspective
	Boundary    metering.Boundary
	Lifecycle   metering.LifecycleScope
	Subject     metering.SubjectRef
	StreamID    string
	ChargeScope string
}

// Key returns a delimiter-safe deterministic scope key.
func (s Scope) Key() string {
	return lengthPrefixed(
		s.StoreID,
		s.TenantID,
		s.AccountKey,
		s.Origin,
		s.Acquisition,
		string(s.Perspective),
		string(s.Boundary),
		string(s.Lifecycle),
		s.SubjectIdentity(),
		s.StreamID,
		s.ChargeScope,
	)
}

func (s Scope) SubjectIdentity() string {
	bytes, _ := json.Marshal(s.Subject)
	return string(bytes)
}

// ReducedMeasure is one exact component value after source-scoped reduction.
// Value is always present; absent/unknown input measures are represented by
// Snapshot.Complete and the retained canonical Observations instead.
type ReducedMeasure struct {
	Scope             Scope
	Key               metering.ComponentKey
	Value             metering.Decimal
	Quality           string
	LastSequence      uint64
	LastRevision      uint64
	LastObservationID string
}

// ReducedCharge retains each reported charge item without recomputing or
// combining monetary amounts. Coverage completeness is exposed separately.
type ReducedCharge struct {
	Scope         Scope
	ObservationID string
	Revision      uint64
	Charge        metering.ReportedCharge
}

// SnapshotV2 is the deterministic reduction result for canonical V2
// observations. It is source/subject/charge scoped and keeps canonical input
// observations for audit/replay rather than replacing them with a scalar map.
type SnapshotV2 struct {
	Measures          []ReducedMeasure
	Charges           []ReducedCharge
	Observations      []metering.Observation
	PendingCoverage   []metering.ChargeCoverageRef
	PendingSupersedes []metering.ObservationRef
	Unavailable       []string
	Complete          bool
	Payable           bool
	Replayed          int
	LastSequence      uint64
}

// ApplyObservations deduplicates exact source-event revisions, validates
// supersession/coverage graphs, then reduces each source-scoped stream in
// declared Sequence/Revision order. Graph validation deliberately occurs only
// after exact replay deduplication so a valid duplicate node is harmless.
func ApplyObservations(observations []metering.Observation) (SnapshotV2, error) {
	replayed, err := replay.Deduplicate(observations)
	if err != nil {
		return SnapshotV2{}, err
	}
	ordered := replay.Sort(replayed.Observations)
	if err := metering.ValidateCoverageGraph(ordered); err != nil {
		return SnapshotV2{}, err
	}
	if err := metering.ValidateSupersessionGraph(ordered); err != nil {
		return SnapshotV2{}, err
	}
	if err := rejectAmbiguousSnapshots(ordered); err != nil {
		return SnapshotV2{}, err
	}

	snapshot := SnapshotV2{
		Observations: make([]metering.Observation, len(ordered)),
		Complete:     true,
		Payable:      true,
		Replayed:     replayed.Replayed,
	}
	charges := make(map[string]ReducedCharge)
	for i, observation := range ordered {
		snapshot.Observations[i] = observation.Clone()
		if observation.Sequence > snapshot.LastSequence {
			snapshot.LastSequence = observation.Sequence
		}
		if observation.Authority == metering.AuthorityUnavailableClaim {
			snapshot.Unavailable = append(snapshot.Unavailable, observation.ID)
			snapshot.Complete = false
			snapshot.Payable = false
		}
		for _, measure := range observation.Measures {
			if measure.Value == nil || measure.Quality == metering.QualityUnknown || measure.Quality == metering.QualityUnavailable {
				snapshot.Complete = false
				snapshot.Payable = false
			}
		}
	}

	snapshot.PendingCoverage = pendingCoverage(ordered)
	snapshot.PendingSupersedes = pendingSupersession(ordered)
	if len(snapshot.PendingCoverage) != 0 || len(snapshot.PendingSupersedes) != 0 {
		snapshot.Complete = false
		snapshot.Payable = false
	}

	states := make(map[string]*reductionState)
	for _, observation := range ordered {
		scope := scopeFor(observation)
		key := scope.Key()
		state := states[key]
		if state == nil {
			state = &reductionState{scope: scope, values: make(map[string]ReducedMeasure)}
			states[key] = state
		}
		if err := state.apply(observation); err != nil {
			return SnapshotV2{}, err
		}
		for _, charge := range observation.Charges {
			applyCharge(charges, scope, observation, charge)
		}
	}
	for _, charge := range charges {
		snapshot.Charges = append(snapshot.Charges, charge)
	}

	for _, state := range states {
		for _, measure := range state.values {
			snapshot.Measures = append(snapshot.Measures, measure)
		}
	}
	sort.Slice(snapshot.Measures, func(i, j int) bool {
		if snapshot.Measures[i].Scope.Key() != snapshot.Measures[j].Scope.Key() {
			return snapshot.Measures[i].Scope.Key() < snapshot.Measures[j].Scope.Key()
		}
		return snapshot.Measures[i].Key.CanonicalKey() < snapshot.Measures[j].Key.CanonicalKey()
	})
	sort.Slice(snapshot.Charges, func(i, j int) bool {
		left := snapshot.Charges[i]
		right := snapshot.Charges[j]
		if left.Scope.Key() != right.Scope.Key() {
			return left.Scope.Key() < right.Scope.Key()
		}
		if left.ObservationID != right.ObservationID {
			return left.ObservationID < right.ObservationID
		}
		if left.Revision != right.Revision {
			return left.Revision < right.Revision
		}
		return left.Charge.ChargeItemID < right.Charge.ChargeItemID
	})
	slices.Sort(snapshot.Unavailable)
	return snapshot, nil
}

// applyCharge keeps the effective reported-charge view revision aware while
// retaining every immutable source observation in SnapshotV2. A correction or
// replacement that supersedes a prior observation and carries the same charge
// item replaces that effective item; a differently named item remains an
// explicit adjustment/auxiliary charge. Ordinary distinct source events are
// never collapsed merely because their item IDs or amounts match.
func applyCharge(dst map[string]ReducedCharge, scope Scope, observation metering.Observation, charge metering.ReportedCharge) {
	if (observation.Semantics == metering.SemanticsCorrection || observation.Semantics == metering.SemanticsReplacement) && !isChargeAdjustment(observation, charge) {
		for key, prior := range dst {
			if prior.Scope.Key() != scope.Key() || prior.Charge.ChargeItemID != charge.ChargeItemID {
				continue
			}
			if supersedesObservation(observation.Supersedes, prior.ObservationID, prior.Revision, prior.Scope.Subject.StoreID) {
				delete(dst, key)
			}
		}
	}
	effectiveKey := lengthPrefixed(scope.Key(), observation.ID, strconv.FormatUint(observation.Revision, 10), charge.ChargeItemID)
	dst[effectiveKey] = ReducedCharge{
		Scope: scope, ObservationID: observation.ID, Revision: observation.Revision, Charge: charge.Clone(),
	}
}

func isChargeAdjustment(observation metering.Observation, charge metering.ReportedCharge) bool {
	if observation.Semantics != metering.SemanticsCorrection {
		return charge.Kind == metering.ChargeKindAdjustment || charge.Kind == metering.ChargeKindCredit
	}
	if charge.Kind == metering.ChargeKindAdjustment || charge.Kind == metering.ChargeKindCredit || observation.MappingRef == metering.LegacyV1MappingRef {
		return true
	}
	if charge.Amount == nil {
		return false
	}
	value, err := charge.Amount.Normalize()
	return err == nil && strings.HasPrefix(value.Coefficient, "-")
}

func supersedesObservation(refs []metering.ObservationRef, observationID string, revision uint64, storeID string) bool {
	for _, ref := range refs {
		if ref.StoreID == storeID && ref.ObservationID == observationID && ref.Revision == revision {
			return true
		}
	}
	return false
}

// ApplyFacts is the explicit one-way V1 bridge. Historical facts are lifted
// through the SDK's trusted reader and then use this same V2 reduction owner;
// Fact identity and old hashes are never rewritten.
func ApplyFacts(facts []metering.Fact) (SnapshotV2, error) {
	observations := make([]metering.Observation, 0, len(facts))
	for i, fact := range facts {
		observation, err := metering.ObservationFromFact(fact)
		if err != nil {
			return SnapshotV2{}, fmt.Errorf("metering/aggregate: V1 fact[%d]: %w", i, err)
		}
		observations = append(observations, observation)
	}
	return ApplyObservations(observations)
}

// ValueFor returns the reduced value for the same source scope and component
// key as observation. An invalid or absent value returns an empty string.
func (s SnapshotV2) ValueFor(observation metering.Observation, key metering.ComponentKey) string {
	normalized, err := key.Normalize()
	if err != nil {
		return ""
	}
	scopeKey := scopeFor(observation).Key()
	for _, measure := range s.Measures {
		if measure.Scope.Key() == scopeKey && measure.Key.Equal(normalized) {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

// ValueForObservationID is a narrow diagnostic lookup for bridge/TCK tests.
// The observation ID is not itself a reduction key; all identity axes remain
// retained in the matching reduced measure.
func (s SnapshotV2) ValueForObservationID(observationID, component string) string {
	for _, measure := range s.Measures {
		if measure.LastObservationID == observationID && measure.Key.Component == component {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

type reductionState struct {
	scope  Scope
	values map[string]ReducedMeasure
}

func (s *reductionState) apply(observation metering.Observation) error {
	for _, measure := range observation.Measures {
		if measure.Value == nil || measure.Quality == metering.QualityUnavailable || measure.Quality == metering.QualityNotApplicable {
			continue
		}
		key, err := measure.Key.Normalize()
		if err != nil {
			return err
		}
		value, err := measure.Value.Normalize()
		if err != nil {
			return err
		}
		canonicalKey := key.CanonicalKey()
		prior, exists := s.values[canonicalKey]
		switch observation.Semantics {
		case metering.SemanticsDelta, metering.SemanticsCorrection:
			if exists {
				value, err = addDecimal(prior.Value, value)
				if err != nil {
					return err
				}
			}
		case metering.SemanticsCumulative, metering.SemanticsGauge, metering.SemanticsReplacement:
			// Replacement is intentionally present-field only. A missing measure
			// never reaches this loop and therefore cannot erase an unrelated key.
		default:
			return fmt.Errorf("metering/aggregate: unsupported observation semantics %q", observation.Semantics)
		}
		s.values[canonicalKey] = ReducedMeasure{
			Scope: s.scope, Key: key, Value: value, Quality: measure.Quality,
			LastSequence: observation.Sequence, LastRevision: observation.Revision, LastObservationID: observation.ID,
		}
	}
	return nil
}

func scopeFor(observation metering.Observation) Scope {
	account := strings.TrimSpace(observation.Correlation.ProviderAccountKey)
	if account == "" {
		account = strings.TrimSpace(observation.Subject.ProviderAccountKey)
	}
	charge := strings.TrimSpace(observation.Correlation.ProviderChargeID)
	if charge == "" {
		charge = strings.TrimSpace(observation.Subject.ProviderChargeID)
	}
	if charge == "" && len(observation.Charges) == 1 {
		charge = strings.TrimSpace(observation.Charges[0].ChargeItemID)
	}
	return Scope{
		StoreID: observation.Correlation.StoreID, TenantID: observation.Correlation.TenantID,
		AccountKey: account, Origin: observation.Origin, Acquisition: observation.Acquisition,
		Perspective: observation.Perspective, Boundary: observation.Boundary, Lifecycle: observation.Lifecycle,
		Subject: observation.Subject.Clone(), StreamID: observation.StreamID, ChargeScope: charge,
	}
}

func rejectAmbiguousSnapshots(observations []metering.Observation) error {
	type position struct {
		scope    string
		sequence uint64
		revision uint64
	}
	positions := make(map[position][]metering.Observation)
	for _, observation := range observations {
		if observation.Semantics != metering.SemanticsCumulative && observation.Semantics != metering.SemanticsGauge {
			continue
		}
		key := position{scope: scopeFor(observation).Key(), sequence: observation.Sequence, revision: observation.Revision}
		positions[key] = append(positions[key], observation)
	}
	for _, samePosition := range positions {
		for i := 0; i < len(samePosition); i++ {
			for j := i + 1; j < len(samePosition); j++ {
				if overlappingMeasureKey(samePosition[i], samePosition[j]) {
					return fmt.Errorf("%w: sequence=%d revision=%d scope=%s", ErrAmbiguousCumulative,
						samePosition[i].Sequence, samePosition[i].Revision, scopeFor(samePosition[i]).Key())
				}
			}
		}
	}
	return nil
}

func overlappingMeasureKey(a, b metering.Observation) bool {
	keys := make(map[string]struct{}, len(a.Measures))
	for _, measure := range a.Measures {
		if measure.Value != nil {
			keys[measure.Key.CanonicalKey()] = struct{}{}
		}
	}
	for _, measure := range b.Measures {
		if measure.Value != nil {
			if _, ok := keys[measure.Key.CanonicalKey()]; ok {
				return true
			}
		}
	}
	return false
}

func pendingCoverage(observations []metering.Observation) []metering.ChargeCoverageRef {
	type node struct {
		store, observation string
		revision           uint64
		item               string
	}
	known := make(map[node]struct{})
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			known[node{observation.Subject.StoreID, observation.ID, observation.Revision, charge.ChargeItemID}] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	var pending []metering.ChargeCoverageRef
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			for _, coverage := range charge.Covers {
				ref := coverage.Ref
				if _, ok := known[node{ref.StoreID, ref.ObservationID, ref.Revision, ref.ChargeItemID}]; ok {
					continue
				}
				key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.ChargeItemID + "\x00" + string(coverage.Relation)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				pending = append(pending, coverage)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return coverageKey(pending[i]) < coverageKey(pending[j]) })
	return pending
}

func pendingSupersession(observations []metering.Observation) []metering.ObservationRef {
	type node struct {
		store, observation string
		revision           uint64
	}
	known := make(map[node]struct{}, len(observations))
	for _, observation := range observations {
		known[node{observation.Subject.StoreID, observation.ID, observation.Revision}] = struct{}{}
	}
	seen := make(map[string]struct{})
	var pending []metering.ObservationRef
	for _, observation := range observations {
		for _, ref := range observation.Supersedes {
			if _, ok := known[node{ref.StoreID, ref.ObservationID, ref.Revision}]; ok {
				continue
			}
			key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			pending = append(pending, ref)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return supersessionKey(pending[i]) < supersessionKey(pending[j]) })
	return pending
}

func coverageKey(ref metering.ChargeCoverageRef) string {
	return ref.Ref.StoreID + "\x00" + ref.Ref.ObservationID + "\x00" + strconv.FormatUint(ref.Ref.Revision, 10) + "\x00" + ref.Ref.ChargeItemID + "\x00" + string(ref.Relation)
}

func supersessionKey(ref metering.ObservationRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
}

func addDecimal(a, b metering.Decimal) (metering.Decimal, error) {
	left, err := a.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	right, err := b.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	leftInt, ok := new(big.Int).SetString(left.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: invalid left coefficient", ErrOverflow)
	}
	rightInt, ok := new(big.Int).SetString(right.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: invalid right coefficient", ErrOverflow)
	}
	scale := left.Scale
	if right.Scale > scale {
		scale = right.Scale
	}
	if scale > left.Scale {
		leftInt.Mul(leftInt, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-left.Scale)), nil))
	}
	if scale > right.Scale {
		rightInt.Mul(rightInt, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-right.Scale)), nil))
	}
	leftInt.Add(leftInt, rightInt)
	result, err := (metering.Decimal{Coefficient: leftInt.String(), Scale: scale}).Normalize()
	if err != nil {
		return metering.Decimal{}, fmt.Errorf("%w: %v", ErrOverflow, err)
	}
	return result, nil
}

func lengthPrefixed(values ...string) string {
	var b strings.Builder
	for _, value := range values {
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
	}
	return b.String()
}
