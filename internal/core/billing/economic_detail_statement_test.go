package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16 Finding 4B core contract: statement-origin S evidence and statement
// detail are resolved through a small consumer-owned observation reader,
// exposed separately from the embedded leg observations, with exact statement
// line/observation lineage, explicit missing evidence and no fabricated
// attribution or cross-scope leakage.

// statementSourceStub returns retained statement observations by exact ref and
// reports absent refs through the missing sentinel. It never synthesizes an
// observation from a ref. When lines is set it also resolves the bounded
// persisted statement-line revisions for the observation's statement-line
// identity.
type statementSourceStub struct {
	byKey map[string]metering.Observation
	errs  map[string]error
	lines map[string][]economics.StatementLineView
}

func statementSourceKey(ref metering.ObservationRef) string {
	return ref.StoreID + "|" + ref.ObservationID + "|" + fmt.Sprint(ref.Revision)
}

func (s statementSourceStub) GetStatementObservation(_ context.Context, ref metering.ObservationRef) (metering.Observation, error) {
	if err, ok := s.errs[statementSourceKey(ref)]; ok {
		return metering.Observation{}, err
	}
	if observation, ok := s.byKey[statementSourceKey(ref)]; ok {
		return observation, nil
	}
	return metering.Observation{}, ErrEconomicDetailStatementObservationMissing
}

func (s statementSourceStub) GetStatementLineRevisions(_ context.Context, observation metering.Observation) ([]economics.StatementLineView, error) {
	return s.lines[observation.Subject.StatementLineID], nil
}

// statementObservationOnlySource implements only the exact observation reader,
// so the loader must fall back to an explicit observation-linkage state rather
// than claiming a persisted statement-line outcome.
type statementObservationOnlySource struct {
	observation metering.Observation
}

func (s statementObservationOnlySource) GetStatementObservation(context.Context, metering.ObservationRef) (metering.Observation, error) {
	return s.observation, nil
}

// statementTestMatchedLineView builds the exact durable matched statement line
// that authoritatively covers one observation.
func statementTestMatchedLineView(t *testing.T, observation metering.Observation, ref metering.ObservationRef) economics.StatementLineView {
	t.Helper()
	view := economics.StatementLineView{
		StatementID:        observation.Subject.StatementID,
		LineID:             observation.Subject.StatementLineID,
		Revision:           1,
		PeriodID:           observation.Subject.PeriodID,
		ProviderAccountKey: observation.Subject.ProviderAccountKey,
		Outcome:            economics.StatementLineMatched,
		Observation:        ref,
	}
	if len(observation.Charges) > 0 {
		view.ChargeItemID = observation.Charges[0].ChargeItemID
	}
	require.NoError(t, view.Validate())
	return view
}

// statementTestUnmatchedLineView builds the exact durable unmatched statement
// line for one observation's statement-line identity.
func statementTestUnmatchedLineView(observation metering.Observation, reason string) economics.StatementLineView {
	return economics.StatementLineView{
		StatementID:        observation.Subject.StatementID,
		LineID:             observation.Subject.StatementLineID,
		Revision:           1,
		PeriodID:           observation.Subject.PeriodID,
		ProviderAccountKey: observation.Subject.ProviderAccountKey,
		Outcome:            economics.StatementLineUnmatched,
		UnmatchedReason:    reason,
	}
}

func statementTestLineViews(views ...economics.StatementLineView) map[string][]economics.StatementLineView {
	byLineID := map[string][]economics.StatementLineView{}
	for _, view := range views {
		byLineID[view.LineID] = append(byLineID[view.LineID], view)
	}
	return byLineID
}

func statementTestValuation(t *testing.T, id string, subject metering.SubjectRef, refs []metering.ObservationRef) economics.Valuation {
	t.Helper()
	valuation := economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: economics.BasisStatementReported, Subject: subject,
		InputObservations:    refs,
		InputSetHash:         strings.Repeat("d", 64),
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://statement/qualifiers/v1", ContentHash: strings.Repeat("a", 64)},
		Completeness:         economics.CompletenessPartial,
		CreatedAt:            time.Unix(1_700_020_000, 0).UTC(),
	}
	require.NoError(t, valuation.Validate())
	return valuation
}

func statementTestObservation(t *testing.T, id string, revision uint64, subject metering.SubjectRef, correlation metering.CorrelationV2, chargeItems ...string) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_020_000+int64(revision), 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: revision, StreamID: "stream-" + id, Sequence: revision,
		Origin: metering.OriginStatement, Acquisition: metering.AcquisitionStatementImporter, Authority: metering.AuthorityVerifiedStatement,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: correlation,
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "statement.test.v1",
	}
	for _, item := range chargeItems {
		value := detailTestDecimal(t, "1.30")
		key := metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		}
		observation.Charges = append(observation.Charges, metering.ReportedCharge{
			ChargeItemID: item, Component: &key, Amount: &value, Currency: "USD",
			Kind: metering.ChargeKindComponent, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
		})
	}
	require.NoError(t, observation.Validate())
	return observation
}

func statementTestRef(t *testing.T, storeID string, observation metering.Observation) metering.ObservationRef {
	t.Helper()
	ref, err := observation.Ref(storeID)
	require.NoError(t, err)
	return ref
}

func statementTestSubject(storeID, aLegID, billingCallID, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "tenant-statement", AccountID: "acct-detail",
		ALegID: aLegID, BillingCallID: billingCallID, BLegID: bLegID,
	}
}

func statementTestStatementSubject(storeID, statementLineID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: "tenant-statement", AccountID: "acct-detail",
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08", StatementID: "stmt-1", StatementLineID: statementLineID,
	}
}

func TestLoadEconomicDetailStatementEvidenceLinkedObservation(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-1")
	statementSubject := statementTestStatementSubject(storeID, "line-1")
	observation := statementTestObservation(t, "stmt-obs-1", 1, statementSubject, metering.CorrelationV2{
		StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-statement-1",
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	}, "charge-line-1")
	ref := statementTestRef(t, storeID, observation)
	valuation := statementTestValuation(t, "val-statement-1", subject, []metering.ObservationRef{ref})

	query := EconomicDetailQuery{StoreID: storeID, TenantID: "tenant-statement", AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	source := statementSourceStub{
		byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
		lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref)),
	}
	evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
	require.NoError(t, err)
	require.True(t, evidence.ReaderAvailable)
	require.Empty(t, evidence.ReaderReason)
	require.Len(t, evidence.Lines, 1)
	line := evidence.Lines[0]
	require.Equal(t, "stmt-1", line.StatementID)
	require.Equal(t, "line-1", line.StatementLineID)
	require.Equal(t, uint64(1), line.Revision)
	require.Equal(t, "provider-acct", line.ProviderAccountKey)
	require.Equal(t, "2026-08", line.PeriodID)
	require.Equal(t, economics.StatementLineMatched, line.Outcome)
	require.Equal(t, EconomicDetailStatementOutcomePersisted, line.OutcomeSource)
	require.Equal(t, []string{"charge-line-1"}, line.ChargeItemIDs)
	require.Equal(t, ref, line.ObservationRef)
	require.Equal(t, observation.ID, line.Observation.ID)
	require.Equal(t, "tenant-statement", line.Subject.TenantID)
	require.Equal(t, []string{"val-statement-1"}, line.ValuationIDs)
	require.Empty(t, evidence.MissingRefs)
	require.Empty(t, evidence.UnattributedRefs)
	require.Empty(t, evidence.UnresolvedRefs)
}

func TestLoadEconomicDetailStatementEvidenceMissingRefIsExplicit(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-missing")
	ref := metering.ObservationRef{StoreID: storeID, ObservationID: "stmt-obs-absent", Revision: 1, PayloadHash: strings.Repeat("b", 64)}
	valuation := statementTestValuation(t, "val-statement-missing", subject, []metering.ObservationRef{ref})

	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, statementSourceStub{})
	require.NoError(t, err)
	require.True(t, evidence.ReaderAvailable)
	require.Empty(t, evidence.Lines, "a missing observation must never be fabricated into a statement line")
	require.Equal(t, []metering.ObservationRef{ref}, evidence.MissingRefs)
	require.Empty(t, evidence.UnattributedRefs)
}

func TestLoadEconomicDetailStatementEvidenceForeignScopeFailsClosed(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	query := EconomicDetailQuery{StoreID: storeID, TenantID: "tenant-statement", AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}

	t.Run("foreign account", func(t *testing.T) {
		t.Parallel()
		subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-foreign-account")
		statementSubject := statementTestStatementSubject(storeID, "line-foreign-account")
		statementSubject.AccountID = "acct-foreign"
		observation := statementTestObservation(t, "stmt-obs-fa", 1, statementSubject, metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-statement-foreign-account",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-fa")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-statement-fa", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): observation}}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
		require.Empty(t, evidence.Lines)
	})

	t.Run("foreign tenant", func(t *testing.T) {
		t.Parallel()
		subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-foreign-tenant")
		statementSubject := statementTestStatementSubject(storeID, "line-foreign-tenant")
		statementSubject.TenantID = ""
		observation := statementTestObservation(t, "stmt-obs-ft", 1, statementSubject, metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-foreign", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-statement-foreign-tenant",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-ft")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-statement-ft", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): observation}}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
		require.Empty(t, evidence.Lines)
	})

	t.Run("foreign call", func(t *testing.T) {
		t.Parallel()
		subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-foreign-call")
		statementSubject := statementTestStatementSubject(storeID, "line-foreign-call")
		foreignCall := "bc_" + strings.Repeat("b", 32)
		observation := statementTestObservation(t, "stmt-obs-fc", 1, statementSubject, metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: foreignCall, BLegID: "b-statement-foreign-call",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-fc")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-statement-fc", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): observation}}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
		require.Empty(t, evidence.Lines)
	})

	t.Run("non statement evidence is not statement detail", func(t *testing.T) {
		t.Parallel()
		subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-nonstatement")
		providerObservation := detailTestObservation(t, "obs-provider-stray", metering.OriginProvider, "stream-stray", 1, subject,
			[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, "7")}, nil)
		ref := statementTestRef(t, storeID, providerObservation)
		valuation := statementTestValuation(t, "val-statement-stray", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): providerObservation}}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.NoError(t, err)
		require.Empty(t, evidence.Lines)
		require.Equal(t, []metering.ObservationRef{ref}, evidence.MissingRefs)
	})
}

func TestLoadEconomicDetailStatementEvidenceUnattributedAggregateOmitted(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-aggregate")
	statementSubject := statementTestStatementSubject(storeID, "line-aggregate")
	// A statement/account-period aggregate observation carries no request
	// lineage: it must not be attributed to the call.
	observation := statementTestObservation(t, "stmt-obs-aggregate", 1, statementSubject, metering.CorrelationV2{
		StoreID: storeID, TenantID: "tenant-statement", ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	}, "charge-aggregate")
	ref := statementTestRef(t, storeID, observation)
	valuation := statementTestValuation(t, "val-statement-aggregate", subject, []metering.ObservationRef{ref})

	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): observation}}
	evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
	require.NoError(t, err)
	require.Empty(t, evidence.Lines, "an unlinked aggregate line must never be attributed to a call/B-leg")
	require.Equal(t, []metering.ObservationRef{ref}, evidence.UnattributedRefs)
	require.Empty(t, evidence.MissingRefs)
}

func TestLoadEconomicDetailStatementEvidenceALegScope(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-aleg")
	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID}

	t.Run("linked by A-leg", func(t *testing.T) {
		t.Parallel()
		observation := statementTestObservation(t, "stmt-obs-aleg-linked", 1, statementTestStatementSubject(storeID, "line-aleg-linked"), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BLegID: "b-statement-aleg",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-aleg-linked")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-statement-aleg", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): observation}}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.NoError(t, err)
		require.Len(t, evidence.Lines, 1)
		require.Equal(t, "line-aleg-linked", evidence.Lines[0].StatementLineID)
	})

	t.Run("foreign A-leg fails closed", func(t *testing.T) {
		t.Parallel()
		observation := statementTestObservation(t, "stmt-obs-aleg-foreign", 1, statementTestStatementSubject(storeID, "line-aleg-foreign"), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: "a-foreign",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-aleg-foreign")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-statement-aleg-foreign", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(ref): observation}}
		_, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})
}

// Finding 5A: an A-leg/session can contain many later calls, so an
// observation that carries only A-leg ancestry cannot be attributed to any one
// of them. Individual call queries must keep such evidence unattributed.
func TestLoadEconomicDetailStatementEvidenceCallScopeAncestorOnlyUnattributed(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	callTwo := "bc_" + strings.Repeat("b", 32)
	statementSubject := statementTestStatementSubject(storeID, "line-ancestor")
	observation := statementTestObservation(t, "stmt-obs-ancestor", 1, statementSubject, metering.CorrelationV2{
		StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID,
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	}, "charge-ancestor")
	ref := statementTestRef(t, storeID, observation)

	valuationOne := statementTestValuation(t, "val-ancestor-1", statementTestSubject(storeID, aLegID, billingCallID, "b-ancestor-1"), []metering.ObservationRef{ref})
	valuationTwo := statementTestValuation(t, "val-ancestor-2", statementTestSubject(storeID, aLegID, callTwo, "b-ancestor-2"), []metering.ObservationRef{ref})
	// Both persisted lines exist and are matched, so a wrong A-leg-only
	// attribution would surface a matched line for the call.
	source := statementSourceStub{
		byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
		lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref)),
	}

	for _, scope := range []struct {
		name      string
		call      string
		valuation economics.Valuation
	}{
		{name: "first call", call: billingCallID, valuation: valuationOne},
		{name: "second call", call: callTwo, valuation: valuationTwo},
	} {
		t.Run(scope.name, func(t *testing.T) {
			t.Parallel()
			query := EconomicDetailQuery{StoreID: storeID, TenantID: "tenant-statement", AccountID: accountID, ALegID: aLegID, BillingCallID: scope.call}
			evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{scope.valuation}, source)
			require.NoError(t, err)
			require.Empty(t, evidence.Lines, "A-leg ancestry alone cannot attribute a statement line to one call")
			require.Equal(t, []metering.ObservationRef{ref}, evidence.UnattributedRefs)
			require.Empty(t, evidence.MissingRefs)
		})
	}
}

// Finding 5A: authoritative requested-call or in-scope B-leg ownership still
// attributes the statement evidence correctly.
func TestLoadEconomicDetailStatementEvidenceCallScopeAuthoritativeLinkage(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	query := EconomicDetailQuery{StoreID: storeID, TenantID: "tenant-statement", AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-authoritative")

	t.Run("requested billing call id", func(t *testing.T) {
		t.Parallel()
		observation := statementTestObservation(t, "stmt-obs-call", 1, statementTestStatementSubject(storeID, "line-call"), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID,
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-call")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-authoritative", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
			lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref)),
		}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.NoError(t, err)
		require.Len(t, evidence.Lines, 1)
		require.Equal(t, economics.StatementLineMatched, evidence.Lines[0].Outcome)
		require.Equal(t, EconomicDetailStatementOutcomePersisted, evidence.Lines[0].OutcomeSource)
		require.Empty(t, evidence.UnattributedRefs)
	})

	t.Run("in-scope b-leg ownership", func(t *testing.T) {
		t.Parallel()
		observation := statementTestObservation(t, "stmt-obs-bleg", 1, statementTestStatementSubject(storeID, "line-bleg"), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BLegID: "b-authoritative",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-bleg")
		ref := statementTestRef(t, storeID, observation)
		valuation := statementTestValuation(t, "val-authoritative", subject, []metering.ObservationRef{ref})
		source := statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
			lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref)),
		}
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, source)
		require.NoError(t, err)
		require.Len(t, evidence.Lines, 1)
		require.Equal(t, EconomicDetailStatementOutcomePersisted, evidence.Lines[0].OutcomeSource)
		require.Empty(t, evidence.UnattributedRefs)
	})
}

// Finding 5B: a line's outcome is only ever the exact persisted statement-line
// outcome; when no authoritative line or a conflicting/absent ledger resolves
// the identity the evidence stays explicitly observation-linked and is never
// presented as matched.
func TestLoadEconomicDetailStatementEvidencePersistedOutcome(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	query := EconomicDetailQuery{StoreID: storeID, TenantID: "tenant-statement", AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-outcome")
	observation := statementTestObservation(t, "stmt-obs-outcome", 1, statementTestStatementSubject(storeID, "line-outcome"), metering.CorrelationV2{
		StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-outcome",
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	}, "charge-outcome")
	ref := statementTestRef(t, storeID, observation)

	load := func(t *testing.T, source EconomicDetailStatementSource) EconomicDetailStatementEvidence {
		t.Helper()
		scoped := statementTestValuation(t, "val-outcome", subject, []metering.ObservationRef{ref})
		evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{scoped}, source)
		require.NoError(t, err)
		require.Len(t, evidence.Lines, 1)
		return evidence
	}

	t.Run("persisted matched", func(t *testing.T) {
		t.Parallel()
		evidence := load(t, statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
			lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref)),
		})
		require.Equal(t, EconomicDetailStatementOutcomePersisted, evidence.Lines[0].OutcomeSource)
		require.Equal(t, economics.StatementLineMatched, evidence.Lines[0].Outcome)
	})

	t.Run("persisted unmatched", func(t *testing.T) {
		t.Parallel()
		evidence := load(t, statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
			lines: statementTestLineViews(statementTestUnmatchedLineView(observation, "aggregate")),
		})
		require.Equal(t, EconomicDetailStatementOutcomePersisted, evidence.Lines[0].OutcomeSource)
		require.Equal(t, economics.StatementLineUnmatched, evidence.Lines[0].Outcome)
		require.Equal(t, "aggregate", evidence.Lines[0].UnmatchedReason)
	})

	t.Run("missing persisted line stays observation-linked", func(t *testing.T) {
		t.Parallel()
		evidence := load(t, statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
		})
		require.Equal(t, EconomicDetailStatementOutcomeObservation, evidence.Lines[0].OutcomeSource)
		require.Empty(t, evidence.Lines[0].Outcome, "an unverified persisted outcome must never be reported as matched")
	})

	t.Run("no line source stays observation-linked", func(t *testing.T) {
		t.Parallel()
		evidence := load(t, statementObservationOnlySource{observation: observation})
		require.Equal(t, EconomicDetailStatementOutcomeObservation, evidence.Lines[0].OutcomeSource)
		require.Empty(t, evidence.Lines[0].Outcome)
	})

	t.Run("conflicting persisted revisions stay observation-linked", func(t *testing.T) {
		t.Parallel()
		other := statementTestObservation(t, "stmt-obs-outcome-other", 1, statementTestStatementSubject(storeID, "line-outcome"), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-outcome",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-outcome")
		otherRef := statementTestRef(t, storeID, other)
		conflicting := statementTestMatchedLineView(t, observation, ref)
		conflicting.Observation = otherRef
		evidence := load(t, statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
			lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref), conflicting),
		})
		require.Equal(t, EconomicDetailStatementOutcomeObservation, evidence.Lines[0].OutcomeSource)
		require.Empty(t, evidence.Lines[0].Outcome)
	})
}

func TestLoadEconomicDetailStatementEvidenceDeterministicAndDeduplicated(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subjectOne := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-det-1")
	subjectTwo := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-det-2")
	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	byKey := map[string]metering.Observation{}

	build := func(lineID, callLeg, valuationID string) economics.Valuation {
		observation := statementTestObservation(t, "stmt-obs-"+lineID, 1, statementTestStatementSubject(storeID, lineID), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: callLeg,
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-"+lineID)
		ref := statementTestRef(t, storeID, observation)
		byKey[statementSourceKey(ref)] = observation
		return statementTestValuation(t, valuationID, subjectOne, []metering.ObservationRef{ref})
	}

	// Deliberately non-deterministic input order; line keys must sort.
	valuationC := build("line-c", "b-statement-det-1", "val-det-c")
	valuationA := build("line-a", "b-statement-det-1", "val-det-a")
	valuationB := build("line-b", "b-statement-det-1", "val-det-b")
	// A second in-scope valuation references line-b again: the line is one fact
	// linked to both S valuations, not duplicated.
	duplicate := statementTestValuation(t, "val-det-b2", subjectTwo, valuationB.InputObservations)

	evidence, err := LoadEconomicDetailStatementEvidence(context.Background(), query,
		[]economics.Valuation{valuationC, duplicate, valuationA, valuationB}, statementSourceStub{byKey: byKey})
	require.NoError(t, err)
	require.Len(t, evidence.Lines, 3)
	require.Equal(t, "line-a", evidence.Lines[0].StatementLineID)
	require.Equal(t, "line-b", evidence.Lines[1].StatementLineID)
	require.Equal(t, "line-c", evidence.Lines[2].StatementLineID)
	require.Equal(t, []string{"val-det-b", "val-det-b2"}, evidence.Lines[1].ValuationIDs)
}

func TestLoadEconomicDetailStatementEvidenceBoundFailsClosed(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-bound")
	byKey := map[string]metering.Observation{}
	refs := make([]metering.ObservationRef, 0, MaxEconomicDetailStatementLines+1)
	for i := 0; i <= MaxEconomicDetailStatementLines; i++ {
		lineID := fmt.Sprintf("line-bound-%03d", i)
		observation := statementTestObservation(t, fmt.Sprintf("stmt-obs-bound-%03d", i), 1, statementTestStatementSubject(storeID, lineID), metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-statement-bound",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-"+lineID)
		ref := statementTestRef(t, storeID, observation)
		byKey[statementSourceKey(ref)] = observation
		refs = append(refs, ref)
	}
	valuation := statementTestValuation(t, "val-statement-bound", subject, refs)
	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	_, err := LoadEconomicDetailStatementEvidence(context.Background(), query, []economics.Valuation{valuation}, statementSourceStub{byKey: byKey})
	require.ErrorIs(t, err, ErrEconomicDetailBoundExceeded)
}

func TestNewStatementEvidenceEconomicDetailReader(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-statement-wrap")
	ref := metering.ObservationRef{StoreID: storeID, ObservationID: "stmt-obs-wrap", Revision: 1, PayloadHash: strings.Repeat("c", 64)}
	valuation := statementTestValuation(t, "val-statement-wrap", subject, []metering.ObservationRef{ref})
	base := economicDetailReaderStub{detail: EconomicDetail{
		Scope:      normalizedDetailQuery(t, EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}),
		Valuations: []economics.Valuation{valuation},
	}}
	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}

	t.Run("nil reader returns nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, NewStatementEvidenceEconomicDetailReader(nil, statementSourceStub{}))
	})

	t.Run("unavailable reader enumerates unresolved refs", func(t *testing.T) {
		t.Parallel()
		reader := NewStatementEvidenceEconomicDetailReader(base, nil)
		detail, err := reader.QueryEconomicDetail(context.Background(), query)
		require.NoError(t, err)
		require.False(t, detail.StatementEvidence.ReaderAvailable)
		require.NotEmpty(t, detail.StatementEvidence.ReaderReason)
		require.Empty(t, detail.StatementEvidence.Lines)
		require.Equal(t, []metering.ObservationRef{ref}, detail.StatementEvidence.UnresolvedRefs)
	})

	t.Run("available reader attaches linked line", func(t *testing.T) {
		t.Parallel()
		statementSubject := statementTestStatementSubject(storeID, "line-wrap")
		observation := statementTestObservation(t, "stmt-obs-wrap", 1, statementSubject, metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-statement-wrap",
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		}, "charge-wrap")
		observationRef := statementTestRef(t, storeID, observation)
		valuation.InputObservations = []metering.ObservationRef{observationRef}
		baseWithRef := economicDetailReaderStub{detail: EconomicDetail{
			Scope:      normalizedDetailQuery(t, query),
			Valuations: []economics.Valuation{valuation},
		}}
		reader := NewStatementEvidenceEconomicDetailReader(baseWithRef, statementSourceStub{byKey: map[string]metering.Observation{statementSourceKey(observationRef): observation}})
		detail, err := reader.QueryEconomicDetail(context.Background(), query)
		require.NoError(t, err)
		require.True(t, detail.StatementEvidence.ReaderAvailable)
		require.Len(t, detail.StatementEvidence.Lines, 1)
		require.Equal(t, "line-wrap", detail.StatementEvidence.Lines[0].StatementLineID)
	})

	t.Run("base error propagates", func(t *testing.T) {
		t.Parallel()
		reader := NewStatementEvidenceEconomicDetailReader(economicDetailReaderStub{err: errors.New("base failure")}, statementSourceStub{})
		_, err := reader.QueryEconomicDetail(context.Background(), query)
		require.Error(t, err)
	})
}

type economicDetailReaderStub struct {
	detail EconomicDetail
	err    error
}

func (s economicDetailReaderStub) QueryEconomicDetail(context.Context, EconomicDetailQuery) (EconomicDetail, error) {
	return s.detail, s.err
}

func normalizedDetailQuery(t *testing.T, query EconomicDetailQuery) EconomicDetailQuery {
	t.Helper()
	normalized, err := query.Normalize()
	require.NoError(t, err)
	return normalized
}

func TestAssembleEconomicDetailStatementEvidenceScope(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	storeID := "store-detail"
	statementSubject := metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: storeID,
		ProviderAccountKey: "provider-acct", StatementID: "stmt-1", StatementLineID: "line-1", PeriodID: "2026-08",
	}
	observation := statementTestObservation(t, "stmt-obs-assembly", 1, statementSubject, metering.CorrelationV2{
		StoreID: storeID, ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	}, "charge-assembly")
	ref := statementTestRef(t, storeID, observation)
	in.StatementEvidence = &EconomicDetailStatementEvidence{
		ReaderAvailable: true,
		Lines: []EconomicDetailStatementLine{{
			StatementID: "stmt-1", StatementLineID: "line-1", Revision: 1,
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
			Outcome: economics.StatementLineMatched, OutcomeSource: EconomicDetailStatementOutcomePersisted, Subject: statementSubject,
			ObservationRef: ref, Observation: observation, ChargeItemIDs: []string{"charge-assembly"},
			ValuationIDs: []string{"valuation-s"},
		}},
	}
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.StatementEvidence.Lines, 1)
	require.Equal(t, ref, got.StatementEvidence.Lines[0].ObservationRef)

	t.Run("foreign store line fails closed", func(t *testing.T) {
		t.Parallel()
		foreign := in
		foreignInput := *in.StatementEvidence
		foreignInput.Lines = append([]EconomicDetailStatementLine(nil), in.StatementEvidence.Lines...)
		foreignInput.Lines[0].Subject.StoreID = "other-store"
		foreign.StatementEvidence = &foreignInput
		_, err := AssembleEconomicDetail(foreign)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("missing observation ref store mismatch fails closed", func(t *testing.T) {
		t.Parallel()
		foreign := in
		foreignInput := *in.StatementEvidence
		foreignInput.Lines = append([]EconomicDetailStatementLine(nil), in.StatementEvidence.Lines...)
		foreignInput.Lines[0].ObservationRef.StoreID = "other-store"
		foreign.StatementEvidence = &foreignInput
		_, err := AssembleEconomicDetail(foreign)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})
}
