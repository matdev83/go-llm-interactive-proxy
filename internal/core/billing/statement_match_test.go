package billing

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.2 statement matching fixtures. The matcher consumes normalized
// imported statements (13.1) plus an explicitly eligible retained charge
// evidence set. Every assertion below rejects guessed per-request/B-leg
// allocation: matching is exact charge identity or complete account/period/SKU
// aggregate coverage only.

const (
	statementMatchStore   = "store-1"
	statementMatchTenant  = "tenant-1"
	statementMatchAccount = "provider-account"
	statementMatchID      = "statement-1"
	statementMatchPeriod  = "period-1"
)

type statementMatchLineSpec struct {
	ID              string
	Revision        uint64
	ChargeItemID    string
	Kind            metering.ChargeKind
	Component       *metering.ComponentKey
	Amount          string
	Currency        string
	Covers          []metering.ChargeCoverageRef
	Unmatched       bool
	UnmatchedReason string
}

type statementMatchStatementSpec struct {
	Store      string
	Tenant     string
	Account    string
	Statement  string
	Period     string
	Revision   uint64
	ReceivedAt time.Time
	Lines      []statementMatchLineSpec
	Scope      *TrustedStatementScope
}

func statementMatchSKU(unit string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction:  metering.DirectionInput,
		Component:  "match_test_metric",
		Unit:       unit,
		SchemaID:   "statement:match:test:v1",
		Dimensions: []metering.Dimension{{Name: "cache", Value: "uncached"}},
	}
}

func statementMatchSKURead() metering.ComponentKey {
	key := statementMatchSKU("token")
	key.Dimensions = []metering.Dimension{{Name: "cache", Value: "read"}}
	return key
}

func statementMatchRef(observation string, revision uint64, chargeItemID string) metering.ChargeRef {
	return metering.ChargeRef{
		StoreID:       statementMatchStore,
		ObservationID: observation,
		Revision:      revision,
		ChargeItemID:  chargeItemID,
	}
}

func statementMatchCovered(observation string, revision uint64, chargeItemID string) metering.ChargeCoverageRef {
	return metering.ChargeCoverageRef{
		Ref:      statementMatchRef(observation, revision, chargeItemID),
		Relation: metering.CoverageInclusive,
	}
}

func statementMatchComponentPointer(key metering.ComponentKey) *metering.ComponentKey {
	return &key
}

func statementMatchDecimal(t testing.TB, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	require.NoError(t, err)
	return &value
}

// statementMatchStatement builds and normalizes one imported statement
// revision. Matched lines reference a statement-line observation that carries
// exactly their normalized charge claim; unmatched lines retain the explicit
// unmatched outcome from 13.1.
func statementMatchStatement(t testing.TB, spec statementMatchStatementSpec) NormalizedStatement {
	t.Helper()

	if spec.Store == "" {
		spec.Store = statementMatchStore
	}
	if spec.Tenant == "" {
		spec.Tenant = statementMatchTenant
	}
	if spec.Account == "" {
		spec.Account = statementMatchAccount
	}
	if spec.Statement == "" {
		spec.Statement = statementMatchID
	}
	if spec.Period == "" {
		spec.Period = statementMatchPeriod
	}
	if spec.Revision == 0 {
		spec.Revision = 1
	}
	if spec.ReceivedAt.IsZero() {
		spec.ReceivedAt = time.Unix(1_700_000_000, 0).UTC()
	}
	require.NotEmpty(t, spec.Lines, "statement fixture requires at least one line")

	scope := TrustedStatementScope{
		StoreID:             spec.Store,
		TenantID:            spec.Tenant,
		PrincipalID:         "principal-1",
		ProviderAccountKeys: []string{spec.Account},
	}
	if spec.Scope != nil {
		scope = *spec.Scope
	}

	observations := make([]metering.Observation, 0, len(spec.Lines))
	lines := make([]economics.StatementLine, 0, len(spec.Lines))
	var envelopeSubject metering.SubjectRef
	for i, line := range spec.Lines {
		revision := line.Revision
		if revision == 0 {
			revision = 1
		}
		subject := metering.SubjectRef{
			Kind:               metering.SubjectStatementLine,
			StoreID:            spec.Store,
			TenantID:           spec.Tenant,
			ProviderAccountKey: spec.Account,
			StatementID:        spec.Statement,
			StatementLineID:    line.ID,
			PeriodID:           spec.Period,
		}
		if i == 0 {
			envelopeSubject = subject
		}
		if line.Unmatched || line.Kind == "" {
			lines = append(lines, economics.StatementLine{
				ID: line.ID, Revision: revision, Subject: subject,
				Outcome: economics.StatementLineUnmatched, UnmatchedReason: line.UnmatchedReason,
			})
			continue
		}

		charge := metering.ReportedCharge{
			ChargeItemID: line.ChargeItemID,
			Kind:         line.Kind,
			Component:    line.Component,
			Covers:       append([]metering.ChargeCoverageRef(nil), line.Covers...),
		}
		if line.Amount != "" {
			charge.Amount = statementMatchDecimal(t, line.Amount)
			charge.Currency = line.Currency
		}
		observation := metering.Observation{
			Version:        2,
			ID:             "statement-observation-" + line.ID,
			SourceEventKey: "statement-event-" + line.ID,
			Revision:       1,
			StreamID:       "statement-stream-1",
			Sequence:       uint64(i + 1),
			Origin:         metering.OriginStatement,
			Acquisition:    metering.AcquisitionStatementImporter,
			Authority:      metering.AuthorityVerifiedStatement,
			Perspective:    metering.PerspectiveOperator,
			Boundary:       metering.BoundaryBackendIngress,
			Lifecycle:      metering.LifecycleBackendAttempt,
			Subject:        subject,
			Correlation: metering.CorrelationV2{
				StoreID: spec.Store, TenantID: spec.Tenant,
				ProviderAccountKey: spec.Account, PeriodID: spec.Period,
			},
			Semantics:  metering.SemanticsDelta,
			ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
			ReceivedAt: spec.ReceivedAt,
			MappingRef: "statement:match:test:v1",
			Charges:    []metering.ReportedCharge{charge},
		}
		require.NoError(t, observation.Validate(), "statement observation fixture must validate")
		ref := metering.ObservationRef{
			StoreID:       observation.Subject.StoreID,
			ObservationID: observation.ID,
			Revision:      observation.Revision,
			PayloadHash:   observation.Fingerprint(),
		}
		observations = append(observations, observation)
		lines = append(lines, economics.StatementLine{
			ID: line.ID, Revision: revision, Subject: subject,
			Observation: ref, ChargeItemID: line.ChargeItemID, Outcome: economics.StatementLineMatched,
		})
	}

	batch := economics.StatementBatch{
		Version:            1,
		ProviderAccountKey: spec.Account,
		StatementID:        spec.Statement,
		Revision:           spec.Revision,
		PeriodID:           spec.Period,
		Subject:            envelopeSubject,
		Observations:       observations,
		Lines:              lines,
	}
	normalized, err := NormalizeStatement(scope, batch)
	require.NoError(t, err, "statement fixture must normalize")
	require.NoError(t, normalized.Validate())
	return normalized
}

type statementMatchEvidenceSpec struct {
	Store            string
	Observation      string
	Revision         uint64
	ChargeItemID     string
	Tenant           string
	Account          string
	Period           string
	Kind             metering.ChargeKind
	Component        *metering.ComponentKey
	Currency         string
	Covers           []metering.ChargeCoverageRef
	ReconciliationID string
	Ineligible       bool
}

func statementMatchEvidence(spec statementMatchEvidenceSpec) StatementChargeEvidence {
	if spec.Store == "" {
		spec.Store = statementMatchStore
	}
	if spec.Observation == "" {
		spec.Observation = "evidence-observation-" + spec.ChargeItemID
	}
	if spec.Revision == 0 {
		spec.Revision = 1
	}
	if spec.Tenant == "" {
		spec.Tenant = statementMatchTenant
	}
	if spec.Account == "" {
		spec.Account = statementMatchAccount
	}
	if spec.Period == "" {
		spec.Period = statementMatchPeriod
	}
	if spec.Kind == "" {
		spec.Kind = metering.ChargeKindComponent
	}
	if spec.Component == nil {
		component := statementMatchSKU("token")
		spec.Component = &component
	}
	if spec.Currency == "" {
		spec.Currency = "USD"
	}
	if spec.ReconciliationID == "" {
		spec.ReconciliationID = "reconciliation-" + spec.ChargeItemID
	}
	state := StatementEvidenceEligible
	if spec.Ineligible {
		state = StatementEvidenceIneligible
	}
	return StatementChargeEvidence{
		Ref: metering.ChargeRef{
			StoreID:       spec.Store,
			ObservationID: spec.Observation,
			Revision:      spec.Revision,
			ChargeItemID:  spec.ChargeItemID,
		},
		State:              state,
		ReconciliationID:   spec.ReconciliationID,
		TenantID:           spec.Tenant,
		ProviderAccountKey: spec.Account,
		PeriodID:           spec.Period,
		Kind:               spec.Kind,
		Component:          spec.Component,
		Currency:           spec.Currency,
		Covers:             append([]metering.ChargeCoverageRef(nil), spec.Covers...),
	}
}

func matchStatementSet(t *testing.T, statements []NormalizedStatement, evidence []StatementChargeEvidence) StatementMatchSet {
	t.Helper()
	set, err := MatchStatements(statements, evidence)
	require.NoError(t, err)
	return set
}

func TestStatementMatchExplicitChargeEvidence(t *testing.T) {
	statement := statementMatchStatement(t, statementMatchStatementSpec{
		Lines: []statementMatchLineSpec{{
			ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
			Component: statementMatchComponentPointer(statementMatchSKU("token")),
			Amount:    "1.25", Currency: "USD",
		}},
	})
	evidence := []StatementChargeEvidence{statementMatchEvidence(statementMatchEvidenceSpec{
		Observation: "evidence-observation-1", ChargeItemID: "charge-1",
		ReconciliationID: "reconciliation-1",
	})}

	set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
	require.Len(t, set.Results, 1)
	result := set.Results[0]
	require.Equal(t, statement.Identity.Key(), result.StatementKey)
	require.Equal(t, statement.Identity.StatementID, result.StatementID)
	require.Equal(t, statement.Identity.Revision, result.StatementRevision)
	require.Len(t, result.Lines, 1)
	require.Equal(t, StatementMatchStatusMatched, result.Lines[0].Status)
	require.Equal(t, StatementMatchReasonExplicitCharge, result.Lines[0].Reason)
	require.Equal(t, "line-1", result.Lines[0].LineID)
	require.NotEmpty(t, result.Lines[0].LinkKey)

	require.Len(t, result.Links, 1)
	link := result.Links[0]
	require.Equal(t, StatementMatchKindExplicitCharge, link.Kind)
	require.Equal(t, []string{"line-1"}, link.LineIDs)
	require.Equal(t, statement.Lines[0].Identity.Key(), link.LineKeys[0])
	require.Equal(t, []metering.ChargeRef{statementMatchRef("evidence-observation-1", 1, "charge-1")}, link.Charges)
	require.Equal(t, []string{"reconciliation-1"}, link.EvidenceIDs)
	require.Equal(t, link.Key(), result.Lines[0].LinkKey)
	require.NotEmpty(t, link.Key())
}

func TestStatementMatchCompleteAggregateSKUCoverage(t *testing.T) {
	component := statementMatchSKU("token")
	aggregateLine := statementMatchLineSpec{
		ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
		Component: statementMatchComponentPointer(component),
		Amount:    "3.75", Currency: "USD",
		Covers: []metering.ChargeCoverageRef{
			statementMatchCovered("evidence-observation-1", 1, "charge-1"),
			statementMatchCovered("evidence-observation-2", 1, "charge-2"),
			statementMatchCovered("evidence-observation-3", 1, "charge-3"),
		},
	}
	evidence := []StatementChargeEvidence{
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1", ReconciliationID: "reconciliation-1"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2", ReconciliationID: "reconciliation-2"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-3", ChargeItemID: "charge-3", ReconciliationID: "reconciliation-3"}),
	}

	t.Run("explicit coverage references", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{aggregateLine}})
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		require.Len(t, set.Results, 1)
		result := set.Results[0]
		require.Len(t, result.Lines, 1)
		require.Equal(t, StatementMatchStatusMatched, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonAggregateSKUCoverage, result.Lines[0].Reason)
		require.Len(t, result.Links, 1)
		link := result.Links[0]
		require.Equal(t, StatementMatchKindAggregateSKU, link.Kind)
		require.Len(t, link.Charges, 3, "many-charge aggregate coverage retains every covered charge")
		require.Equal(t, []string{"reconciliation-1", "reconciliation-2", "reconciliation-3"}, link.EvidenceIDs)
		require.Equal(t, []string{"agg-1"}, link.LineIDs)
	})

	t.Run("complete evidence set without coverage references", func(t *testing.T) {
		line := aggregateLine
		line.Covers = nil
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{line}})
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusMatched, result.Lines[0].Status)
		require.Len(t, result.Links, 1)
		require.Len(t, result.Links[0].Charges, 3)
	})
}

func TestStatementMatchRetainsUnmatchedAndAccountScopedLines(t *testing.T) {
	t.Run("missing explicit charge stays unmatched", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "line-1", ChargeItemID: "charge-missing", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(statementMatchSKU("token")),
				Amount:    "1.25", Currency: "USD",
			}},
		})
		set := matchStatementSet(t, []NormalizedStatement{statement}, nil)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusUnmatched, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonChargeNotFound, result.Lines[0].Reason)
		require.Empty(t, result.Links)
	})

	t.Run("imported unmatched line keeps its declared reason", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{
				{
					ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
					Component: statementMatchComponentPointer(statementMatchSKU("token")), Amount: "1.25", Currency: "USD",
				},
				{ID: "line-2", Unmatched: true, UnmatchedReason: "account-period aggregate"},
			},
		})
		evidence := []StatementChargeEvidence{statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})}
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		result := set.Results[0]
		require.Len(t, result.Lines, 2)
		require.Equal(t, StatementMatchReasonNoChargeLink, result.Lines[1].Reason)
		require.Equal(t, StatementMatchStatusUnmatched, result.Lines[1].Status)
		require.Equal(t, "account-period aggregate", result.Lines[1].UnmatchedReason)
	})

	t.Run("account total stays account-scoped", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "total-1", ChargeItemID: "account-total-1", Kind: metering.ChargeKindAggregate,
				Amount: "9.99", Currency: "USD",
			}},
		})
		evidence := []StatementChargeEvidence{
			statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"}),
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2"}),
		}
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusUnmatched, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonAccountScopedTotal, result.Lines[0].Reason)
		require.Empty(t, result.Links)
		require.Empty(t, result.Lines[0].LinkKey)
	})
}

func TestStatementMatchPartialAggregateIsNotMatched(t *testing.T) {
	component := statementMatchSKU("token")
	evidence := []StatementChargeEvidence{
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-3", ChargeItemID: "charge-3"}),
	}
	aggregateLine := statementMatchLineSpec{
		ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
		Component: statementMatchComponentPointer(component), Amount: "3.75", Currency: "USD",
	}

	t.Run("missing coverage reference is partial", func(t *testing.T) {
		line := aggregateLine
		line.Covers = []metering.ChargeCoverageRef{
			statementMatchCovered("evidence-observation-1", 1, "charge-1"),
			statementMatchCovered("evidence-observation-2", 1, "charge-2"),
		}
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{line}})
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusPartial, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonPartialSKUCoverage, result.Lines[0].Reason)
		require.Empty(t, result.Links)
	})

	t.Run("ineligible group member makes coverage partial", func(t *testing.T) {
		ineligible := evidence[2]
		ineligible.State = StatementEvidenceIneligible
		mixed := []StatementChargeEvidence{evidence[0], evidence[1], ineligible}
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{aggregateLine}})
		set := matchStatementSet(t, []NormalizedStatement{statement}, mixed)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusPartial, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonEvidenceIneligible, result.Lines[0].Reason)
		require.Empty(t, result.Links)
	})

	t.Run("dangling coverage reference is partial", func(t *testing.T) {
		line := aggregateLine
		line.Covers = []metering.ChargeCoverageRef{
			statementMatchCovered("evidence-observation-1", 1, "charge-1"),
			statementMatchCovered("evidence-observation-2", 1, "charge-2"),
			statementMatchCovered("evidence-observation-missing", 1, "charge-3"),
		}
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{line}})
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusPartial, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonDanglingCoverageRef, result.Lines[0].Reason)
	})
}

func TestStatementMatchNeverGuessesNearestChargeOrAllocation(t *testing.T) {
	// The account/period holds eligible charges with the same amount and
	// component; the statement line has no explicit charge link, so nothing
	// may be attached to it.
	statement := statementMatchStatement(t, statementMatchStatementSpec{
		Lines: []statementMatchLineSpec{{ID: "line-1", Unmatched: true, UnmatchedReason: "no explicit charge reference"}},
	})
	evidence := []StatementChargeEvidence{
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2"}),
	}
	set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
	result := set.Results[0]
	require.Equal(t, StatementMatchStatusUnmatched, result.Lines[0].Status)
	require.Empty(t, result.Links)
	require.Empty(t, result.Lines[0].LinkKey)
	require.Equal(t, "no explicit charge reference", result.Lines[0].UnmatchedReason)
}

func TestStatementMatchRejectsDuplicateParentChildInclusion(t *testing.T) {
	component := statementMatchSKU("token")
	parentRef := statementMatchRef("evidence-observation-parent", 1, "charge-parent")
	childRef := statementMatchRef("evidence-observation-child", 1, "charge-child")
	parentEvidence := statementMatchEvidence(statementMatchEvidenceSpec{
		Observation: "evidence-observation-parent", ChargeItemID: "charge-parent",
		Covers: []metering.ChargeCoverageRef{{Ref: childRef, Relation: metering.CoverageInclusive}},
	})
	childEvidence := statementMatchEvidence(statementMatchEvidenceSpec{
		Observation: "evidence-observation-child", ChargeItemID: "charge-child",
	})

	t.Run("one aggregate link cannot include parent and inclusive child", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
				Component: statementMatchComponentPointer(component), Amount: "5.00", Currency: "USD",
				Covers: []metering.ChargeCoverageRef{
					{Ref: parentRef, Relation: metering.CoverageInclusive},
					{Ref: childRef, Relation: metering.CoverageInclusive},
				},
			}},
		})
		set := matchStatementSet(t, []NormalizedStatement{statement}, []StatementChargeEvidence{parentEvidence, childEvidence})
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusConflict, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonDuplicateParentChild, result.Lines[0].Reason)
		require.Empty(t, result.Links)
	})

	t.Run("separate lines cannot cover a parent and its inclusive child", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{
				{
					ID: "line-parent", ChargeItemID: "charge-parent", Kind: metering.ChargeKindComponent,
					Component: statementMatchComponentPointer(component), Amount: "5.00", Currency: "USD",
				},
				{
					ID: "line-child", ChargeItemID: "charge-child", Kind: metering.ChargeKindComponent,
					Component: statementMatchComponentPointer(component), Amount: "1.00", Currency: "USD",
				},
			},
		})
		set := matchStatementSet(t, []NormalizedStatement{statement}, []StatementChargeEvidence{parentEvidence, childEvidence})
		result := set.Results[0]
		require.Len(t, result.Lines, 2)
		for _, line := range result.Lines {
			require.Equal(t, StatementMatchStatusConflict, line.Status)
			require.Equal(t, StatementMatchReasonDuplicateParentChild, line.Reason)
		}
		require.Empty(t, result.Links)
	})

	t.Run("direct overlap outranks internal parent-child inclusion deterministically", func(t *testing.T) {
		aggregate := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
				Component: statementMatchComponentPointer(component), Amount: "5.00", Currency: "USD",
				Covers: []metering.ChargeCoverageRef{
					{Ref: parentRef, Relation: metering.CoverageInclusive},
					{Ref: childRef, Relation: metering.CoverageInclusive},
				},
			}},
		})
		explicit := statementMatchStatement(t, statementMatchStatementSpec{
			Statement: "statement-2",
			Lines: []statementMatchLineSpec{{
				ID: "line-parent", ChargeItemID: "charge-parent", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(component), Amount: "5.00", Currency: "USD",
			}},
		})
		set := matchStatementSet(t, []NormalizedStatement{aggregate, explicit}, []StatementChargeEvidence{parentEvidence, childEvidence})
		require.Len(t, set.Results, 2)
		for _, result := range set.Results {
			require.Equal(t, StatementMatchStatusConflict, result.Lines[0].Status)
			require.Equal(t, StatementMatchReasonOverlappingCoverage, result.Lines[0].Reason,
				"a direct charge collision must outrank an internal parent-child duplicate")
		}
	})
}

func TestStatementMatchScopeAndClaimMismatchesAreIncomparable(t *testing.T) {
	baseClaim := statementMatchLineSpec{
		ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
		Component: statementMatchComponentPointer(statementMatchSKU("token")),
		Amount:    "1.25", Currency: "USD",
	}
	tests := []struct {
		name   string
		claim  statementMatchLineSpec
		evid   statementMatchEvidenceSpec
		status StatementMatchStatus
		reason StatementMatchReason
	}{
		{
			name: "store mismatch", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{Store: "store-2", ChargeItemID: "charge-1"},
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonStoreMismatch,
		},
		{
			name: "tenant mismatch", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{Tenant: "tenant-2", ChargeItemID: "charge-1"},
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonTenantMismatch,
		},
		{
			name: "account mismatch", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{Account: "other-account", ChargeItemID: "charge-1"},
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonAccountMismatch,
		},
		{
			name: "period mismatch", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{Period: "period-2", ChargeItemID: "charge-1"},
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonPeriodMismatch,
		},
		{
			name: "currency mismatch", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{Currency: "EUR", ChargeItemID: "charge-1"},
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonCurrencyMismatch,
		},
		{
			name: "unit mismatch", claim: baseClaim,
			evid: func() statementMatchEvidenceSpec {
				component := statementMatchSKU("second")
				return statementMatchEvidenceSpec{ChargeItemID: "charge-1", Component: &component}
			}(),
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonUnitMismatch,
		},
		{
			name: "sku mismatch", claim: baseClaim,
			evid: func() statementMatchEvidenceSpec {
				component := statementMatchSKURead()
				return statementMatchEvidenceSpec{ChargeItemID: "charge-1", Component: &component}
			}(),
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonSKUMismatch,
		},
		{
			name: "granularity mismatch", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{ChargeItemID: "charge-1", Kind: metering.ChargeKindAggregate},
			status: StatementMatchStatusIncomparable, reason: StatementMatchReasonGranularityMismatch,
		},
		{
			name: "ineligible evidence", claim: baseClaim,
			evid:   statementMatchEvidenceSpec{ChargeItemID: "charge-1", Ineligible: true},
			status: StatementMatchStatusUnmatched, reason: StatementMatchReasonEvidenceIneligible,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{test.claim}})
			set := matchStatementSet(t, []NormalizedStatement{statement}, []StatementChargeEvidence{statementMatchEvidence(test.evid)})
			result := set.Results[0]
			require.Equal(t, test.status, result.Lines[0].Status)
			require.Equal(t, test.reason, result.Lines[0].Reason)
			require.Empty(t, result.Links)
		})
	}
}

func TestStatementMatchRejectsOverlappingAndDuplicateCoverage(t *testing.T) {
	component := statementMatchSKU("token")

	t.Run("two explicit lines cannot cover the same charge", func(t *testing.T) {
		first := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
			}},
		})
		second := statementMatchStatement(t, statementMatchStatementSpec{
			Statement: "statement-2",
			Lines: []statementMatchLineSpec{{
				ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
			}},
		})
		evidence := []StatementChargeEvidence{statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})}
		set := matchStatementSet(t, []NormalizedStatement{first, second}, evidence)
		require.Len(t, set.Results, 2)
		for _, result := range set.Results {
			require.Equal(t, StatementMatchStatusConflict, result.Lines[0].Status)
			require.Equal(t, StatementMatchReasonOverlappingCoverage, result.Lines[0].Reason)
			require.Empty(t, result.Links)
		}
	})

	t.Run("two aggregate lines cannot cover the same eligible charges", func(t *testing.T) {
		aggregateLine := statementMatchLineSpec{
			ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
			Component: statementMatchComponentPointer(component), Amount: "2.50", Currency: "USD",
		}
		first := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{aggregateLine}})
		second := statementMatchStatement(t, statementMatchStatementSpec{
			Statement: "statement-2", Lines: []statementMatchLineSpec{aggregateLine},
		})
		evidence := []StatementChargeEvidence{
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1"}),
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2"}),
		}
		set := matchStatementSet(t, []NormalizedStatement{first, second}, evidence)
		for _, result := range set.Results {
			require.Equal(t, StatementMatchStatusConflict, result.Lines[0].Status)
			require.Equal(t, StatementMatchReasonOverlappingCoverage, result.Lines[0].Reason)
			require.Empty(t, result.Links)
		}
	})

	t.Run("duplicate evidence entries are rejected", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
			}},
		})
		duplicate := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
		_, err := MatchStatements([]NormalizedStatement{statement}, []StatementChargeEvidence{duplicate, duplicate})
		require.ErrorIs(t, err, ErrStatementMatchInvalid)
	})

	t.Run("duplicate statement revision is rejected", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
			}},
		})
		_, err := MatchStatements([]NormalizedStatement{statement, statement}, nil)
		require.ErrorIs(t, err, ErrStatementMatchInvalid)
	})
}

func TestStatementMatchIsDeterministicAcrossInputOrder(t *testing.T) {
	component := statementMatchSKU("token")
	statements := []NormalizedStatement{
		statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
				Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
			}},
		}),
		statementMatchStatement(t, statementMatchStatementSpec{
			Statement: "statement-2",
			Lines: []statementMatchLineSpec{{
				ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
				Component: statementMatchComponentPointer(component), Amount: "2.50", Currency: "USD",
			}},
		}),
	}
	evidence := []StatementChargeEvidence{
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-3", ChargeItemID: "charge-3"}),
	}

	canonical := matchStatementSet(t, statements, evidence)
	for shift := range len(statements) {
		for evidenceShift := range len(evidence) {
			rotatedStatements := append(append([]NormalizedStatement{}, statements[shift:]...), statements[:shift]...)
			rotatedEvidence := append(append([]StatementChargeEvidence{}, evidence[evidenceShift:]...), evidence[:evidenceShift]...)
			rotated := matchStatementSet(t, rotatedStatements, rotatedEvidence)
			require.Equal(t, canonical, rotated, "shift statements=%d evidence=%d", shift, evidenceShift)
		}
	}
	reversedEvidence := []StatementChargeEvidence{evidence[2], evidence[1], evidence[0]}
	require.Equal(t, canonical, matchStatementSet(t, statements, reversedEvidence))
}

func TestStatementMatchRetainsNoRequestOrAllocationLineage(t *testing.T) {
	assertNoGuessedAllocationFields(t, StatementChargeEvidence{})
	assertNoGuessedAllocationFields(t, StatementCoverageLink{})
	assertNoGuessedAllocationFields(t, StatementMatchLine{})
	assertNoGuessedAllocationFields(t, StatementMatchResult{})
	assertNoGuessedAllocationFields(t, StatementMatchSet{})
}

func assertNoGuessedAllocationFields(t *testing.T, value any) {
	t.Helper()
	typ := reflect.TypeOf(value)
	require.Equal(t, reflect.Struct, typ.Kind())
	banned := []string{
		"request", "aleg", "a_leg", "bleg", "b_leg", "billing_call", "billingcall",
		"attempt", "allocation", "call_id", "callid", "provider_request",
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.ToLower(field.Name)
		for _, token := range banned {
			assert.NotContains(t, name, token, "%s.%s must not carry guessed request lineage", typ.Name(), field.Name)
		}
	}
}

func TestStatementMatchRejectsAmbiguousRevisions(t *testing.T) {
	component := statementMatchSKU("token")
	line := statementMatchLineSpec{
		ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
		Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
	}

	t.Run("mixed statement revisions fail closed", func(t *testing.T) {
		first := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{line}})
		second := statementMatchStatement(t, statementMatchStatementSpec{Revision: 2, Lines: []statementMatchLineSpec{line}})
		_, err := MatchStatements([]NormalizedStatement{first, second}, nil)
		require.ErrorIs(t, err, ErrStatementMatchAmbiguous)
		var ambiguity *StatementMatchAmbiguityError
		require.ErrorAs(t, err, &ambiguity)
		require.Equal(t, []string{statementMatchID}, ambiguity.StatementIDs)
	})

	t.Run("multiple evidence revisions are ambiguous for explicit matching", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{line}})
		evidence := []StatementChargeEvidence{
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", Revision: 1, ChargeItemID: "charge-1"}),
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", Revision: 2, ChargeItemID: "charge-1", ReconciliationID: "reconciliation-2"}),
		}
		set := matchStatementSet(t, []NormalizedStatement{statement}, evidence)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusConflict, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonCrossRevisionAmbiguity, result.Lines[0].Reason)
		require.Empty(t, result.Links)
	})

	t.Run("multiple evidence revisions are ambiguous for aggregate coverage", func(t *testing.T) {
		aggregate := statementMatchStatement(t, statementMatchStatementSpec{
			Lines: []statementMatchLineSpec{{
				ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
				Component: statementMatchComponentPointer(component), Amount: "2.50", Currency: "USD",
			}},
		})
		evidence := []StatementChargeEvidence{
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", Revision: 1, ChargeItemID: "charge-1"}),
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", Revision: 2, ChargeItemID: "charge-1", ReconciliationID: "reconciliation-2"}),
		}
		set := matchStatementSet(t, []NormalizedStatement{aggregate}, evidence)
		result := set.Results[0]
		require.Equal(t, StatementMatchStatusConflict, result.Lines[0].Status)
		require.Equal(t, StatementMatchReasonCrossRevisionAmbiguity, result.Lines[0].Reason)
		require.Empty(t, result.Links)
	})
}

func TestStatementMatchRejectsMalformedAndOverboundInput(t *testing.T) {
	component := statementMatchSKU("token")
	validLine := statementMatchLineSpec{
		ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
		Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
	}

	t.Run("empty input is an empty match set", func(t *testing.T) {
		set, err := MatchStatements(nil, nil)
		require.NoError(t, err)
		require.Empty(t, set.Results)
	})

	t.Run("tampered statement is rejected", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{validLine}})
		tampered := statement.Clone()
		tampered.Lines[0].Line.ID = "line-2"
		_, err := MatchStatements([]NormalizedStatement{tampered}, nil)
		require.ErrorIs(t, err, ErrStatementMatchInvalid)
	})

	t.Run("malformed evidence is rejected", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{validLine}})
		tests := map[string]func() StatementChargeEvidence{
			"missing charge item": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.Ref.ChargeItemID = ""
				return item
			},
			"missing period": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.PeriodID = ""
				return item
			},
			"unknown state": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.State = "bogus"
				return item
			},
			"unknown kind": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.Kind = "bogus"
				return item
			},
			"zero revision": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.Ref.Revision = 0
				return item
			},
			"duplicate coverage edge": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				edge := statementMatchCovered("evidence-observation-2", 1, "charge-2")
				item.Covers = []metering.ChargeCoverageRef{edge, edge}
				return item
			},
			"self coverage edge": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.Covers = []metering.ChargeCoverageRef{statementMatchCovered(item.Ref.ObservationID, item.Ref.Revision, "charge-1")}
				return item
			},
			"unnormalized currency": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				item.Currency = "usd"
				return item
			},
			"coverage reference bound": func() StatementChargeEvidence {
				item := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1"})
				covers := make([]metering.ChargeCoverageRef, 0, MaxStatementMatchCoverageRefs+1)
				for i := 0; i <= MaxStatementMatchCoverageRefs; i++ {
					covers = append(covers, statementMatchCovered(fmt.Sprintf("evidence-observation-%d", i), 1, fmt.Sprintf("charge-%d", i)))
				}
				item.Covers = covers
				return item
			},
		}
		for name, build := range tests {
			t.Run(name, func(t *testing.T) {
				_, err := MatchStatements([]NormalizedStatement{statement}, []StatementChargeEvidence{build()})
				require.ErrorIs(t, err, ErrStatementMatchInvalid)
			})
		}
	})

	t.Run("evidence bound is enforced", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{validLine}})
		evidence := make([]StatementChargeEvidence, 0, MaxStatementMatchEvidence+1)
		for i := 0; i <= MaxStatementMatchEvidence; i++ {
			evidence = append(evidence, statementMatchEvidence(statementMatchEvidenceSpec{
				Observation:  fmt.Sprintf("evidence-observation-%d", i),
				ChargeItemID: fmt.Sprintf("charge-%d", i),
			}))
		}
		_, err := MatchStatements([]NormalizedStatement{statement}, evidence)
		require.ErrorIs(t, err, ErrStatementMatchInvalid)
	})

	t.Run("statement bound is enforced", func(t *testing.T) {
		statement := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{validLine}})
		statements := make([]NormalizedStatement, 0, MaxStatementMatchStatements+1)
		for i := 0; i <= MaxStatementMatchStatements; i++ {
			statements = append(statements, statement)
		}
		_, err := MatchStatements(statements, nil)
		require.ErrorIs(t, err, ErrStatementMatchInvalid)
	})
}

func TestStatementCoverageLinkIdentityIsStableAndDetached(t *testing.T) {
	component := statementMatchSKU("token")
	statement := statementMatchStatement(t, statementMatchStatementSpec{
		Lines: []statementMatchLineSpec{{
			ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
			Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
		}},
	})
	base := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1", ReconciliationID: "reconciliation-1"})
	other := statementMatchEvidence(statementMatchEvidenceSpec{ChargeItemID: "charge-1", ReconciliationID: "reconciliation-2"})

	first := matchStatementSet(t, []NormalizedStatement{statement}, []StatementChargeEvidence{base})
	second := matchStatementSet(t, []NormalizedStatement{statement}, []StatementChargeEvidence{base})
	require.Equal(t, first.Results[0].Links[0].Key(), second.Results[0].Links[0].Key())
	require.True(t, first.Results[0].Links[0].Equal(second.Results[0].Links[0]))

	changed := matchStatementSet(t, []NormalizedStatement{statement}, []StatementChargeEvidence{other})
	require.NotEqual(t, first.Results[0].Links[0].Key(), changed.Results[0].Links[0].Key())

	link := first.Results[0].Links[0]
	require.True(t, strings.HasPrefix(link.Key(), "statement-coverage-link:v1:"))
	clone := link.Clone()
	require.True(t, link.Equal(clone))
	clone.Charges[0].ChargeItemID = "tampered"
	require.False(t, link.Equal(clone))
	require.Equal(t, "charge-1", link.Charges[0].ChargeItemID, "clone must be detached")
}

func TestStatementMatchVocabularyIsClosed(t *testing.T) {
	statuses := []StatementMatchStatus{
		StatementMatchStatusMatched, StatementMatchStatusPartial, StatementMatchStatusIncomparable,
		StatementMatchStatusConflict, StatementMatchStatusUnmatched,
	}
	for _, status := range statuses {
		require.True(t, status.IsKnown(), "status %q must be documented", status)
	}
	require.False(t, StatementMatchStatus("bogus").IsKnown())

	reasons := []StatementMatchReason{
		StatementMatchReasonNone, StatementMatchReasonExplicitCharge, StatementMatchReasonAggregateSKUCoverage,
		StatementMatchReasonNoChargeLink, StatementMatchReasonChargeNotFound, StatementMatchReasonAccountScopedTotal,
		StatementMatchReasonEvidenceIneligible, StatementMatchReasonPartialSKUCoverage, StatementMatchReasonDanglingCoverageRef,
		StatementMatchReasonStoreMismatch, StatementMatchReasonTenantMismatch, StatementMatchReasonAccountMismatch,
		StatementMatchReasonPeriodMismatch, StatementMatchReasonCurrencyMismatch, StatementMatchReasonUnitMismatch,
		StatementMatchReasonSKUMismatch, StatementMatchReasonGranularityMismatch, StatementMatchReasonCrossRevisionAmbiguity,
		StatementMatchReasonDuplicateParentChild, StatementMatchReasonOverlappingCoverage,
	}
	for _, reason := range reasons {
		require.True(t, reason.IsKnown(), "reason %q must be documented", reason)
	}
	require.False(t, StatementMatchReason("bogus").IsKnown())

	for _, kind := range []StatementMatchKind{StatementMatchKindExplicitCharge, StatementMatchKindAggregateSKU} {
		require.True(t, kind.IsKnown(), "kind %q must be documented", kind)
	}
	require.False(t, StatementMatchKind("bogus").IsKnown())

	for _, state := range []StatementEvidenceState{StatementEvidenceEligible, StatementEvidenceIneligible} {
		require.NoError(t, state.Validate())
	}
	require.ErrorIs(t, StatementEvidenceState("bogus").Validate(), ErrStatementMatchInvalid)
}

func FuzzStatementMatchOrderIndependence(f *testing.F) {
	f.Add(uint(0), uint(0), uint(0))
	f.Add(uint(1), uint(1), uint(1))
	f.Fuzz(func(t *testing.T, statementShift, evidenceShift, reverse uint) {
		component := statementMatchSKU("token")
		line := statementMatchLineSpec{
			ID: "line-1", ChargeItemID: "charge-1", Kind: metering.ChargeKindComponent,
			Component: statementMatchComponentPointer(component), Amount: "1.25", Currency: "USD",
		}
		first := statementMatchStatement(t, statementMatchStatementSpec{Lines: []statementMatchLineSpec{line}})
		second := statementMatchStatement(t, statementMatchStatementSpec{
			Statement: "statement-2",
			Lines: []statementMatchLineSpec{{
				ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
				Component: statementMatchComponentPointer(component), Amount: "2.50", Currency: "USD",
			}},
		})
		evidence := []StatementChargeEvidence{
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1"}),
			statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2"}),
		}
		canonical, err := MatchStatements([]NormalizedStatement{first, second}, evidence)
		require.NoError(t, err)

		statements := []NormalizedStatement{first, second}
		if statementShift%2 == 1 {
			statements[0], statements[1] = statements[1], statements[0]
		}
		reordered := append([]StatementChargeEvidence(nil), evidence...)
		if (evidenceShift+reverse)%2 == 1 {
			reordered[0], reordered[1] = reordered[1], reordered[0]
		}
		set, err := MatchStatements(statements, reordered)
		require.NoError(t, err)
		require.Equal(t, canonical, set)
	})
}
