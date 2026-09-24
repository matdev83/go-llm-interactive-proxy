package runtimebundle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 4B end-to-end durable/composition contract: a persisted matched
// statement observation in the metering journal, referenced by a persisted
// in-scope S valuation, appears as statement evidence with exact lineage in
// the call economic detail served through the composed operator reader.

type statementE2EFixture struct {
	storeID  string
	billing  *billingstore.DurableStore
	metering *journalstore.DurableStore
	account  corebilling.Account
	callID   corebilling.BillingCallID
	aLegID   string
	bLegID   string
}

func newStatementE2EFixture(t *testing.T, storeID string) statementE2EFixture {
	t.Helper()
	ctx := context.Background()
	billingStore := newBridgeBillingStore(t, storeID)
	meteringStore := newBridgeMeteringStore(t, storeID)
	account := corebilling.Account{
		ID: "acct-statement-e2e", Currency: "USD", Mode: corebilling.AccountPrepaid,
		BalanceNano: 1_000_000, State: corebilling.AccountReady, Version: 1,
	}
	require.NoError(t, billingStore.CreateAccount(ctx, account))
	callID, err := corebilling.NewBillingCallID()
	require.NoError(t, err)
	fixture := statementE2EFixture{
		storeID: storeID, billing: billingStore, metering: meteringStore,
		account: account, callID: callID, aLegID: "a-statement-e2e", bLegID: "b-statement-e2e",
	}
	call := corebilling.CallUsageRecord{
		SchemaVersion: corebilling.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID,
		ALegID: fixture.aLegID, SessionID: "sess-" + fixture.aLegID,
		StartedAt: time.Unix(1_700_030_000, 0).UTC(), FinishedAt: time.Unix(1_700_030_001, 0).UTC(),
		Outcome:            corebilling.TurnOutcomeCompleted,
		CustomerPricingRef: corebilling.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: corebilling.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs: []string{fixture.bLegID},
	}
	require.NoError(t, billingStore.AppendCallUsage(ctx, call))
	leg := corebilling.CallLegUsageRecord{
		BLegID: fixture.bLegID, CallID: callID, ALegID: fixture.aLegID, AttemptSeq: 1,
		BackendID: "backend-statement", ProviderID: "provider-statement", ModelID: "model-statement",
		StartedAt: time.Unix(1_700_030_000, 0).UTC(), FinishedAt: time.Unix(1_700_030_001, 0).UTC(),
		Outcome: corebilling.LegOutcomeWinner, Surfaced: corebilling.SurfacedYes,
	}
	require.NoError(t, billingStore.AppendCallLegUsage(ctx, leg))
	return fixture
}

func (f statementE2EFixture) query() corebilling.EconomicDetailQuery {
	return corebilling.EconomicDetailQuery{
		StoreID: f.storeID, TenantID: "tenant-statement", AccountID: f.account.ID,
		BillingCallID: f.callID.String(), ALegID: f.aLegID,
	}
}

func (f statementE2EFixture) legSubject() metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: f.storeID, TenantID: "tenant-statement", AccountID: f.account.ID,
		ALegID: f.aLegID, BillingCallID: f.callID.String(), BLegID: f.bLegID,
	}
}

func (f statementE2EFixture) evaluation(t *testing.T, id string, refs []metering.ObservationRef) economics.Valuation {
	t.Helper()
	return f.evaluationFor(t, id, f.legSubject(), refs)
}

func (f statementE2EFixture) evaluationFor(t *testing.T, id string, subject metering.SubjectRef, refs []metering.ObservationRef) economics.Valuation {
	t.Helper()
	valuation := economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: economics.BasisStatementReported, Subject: subject,
		InputObservations:    refs,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://statement/qualifiers/v1", ContentHash: strings.Repeat("a", 64)},
		Completeness:         economics.CompletenessPartial,
		CreatedAt:            time.Unix(1_700_030_100, 0).UTC(),
	}
	require.NoError(t, valuation.Validate())
	require.NoError(t, f.billing.AppendValuation(context.Background(), valuation))
	return valuation
}

// appendCall retains another call and B-leg under the fixture's A-leg so a test
// can prove that one A-leg containing several later calls does not make
// ancestor-only statement evidence attributable to either call.
func (f statementE2EFixture) appendCall(t *testing.T, callID corebilling.BillingCallID, bLegID string) metering.SubjectRef {
	t.Helper()
	ctx := context.Background()
	call := corebilling.CallUsageRecord{
		SchemaVersion: corebilling.CurrentRecordSchemaVersion, CallID: callID, AccountID: f.account.ID,
		ALegID: f.aLegID, SessionID: "sess-" + f.aLegID,
		StartedAt: time.Unix(1_700_030_000, 0).UTC(), FinishedAt: time.Unix(1_700_030_001, 0).UTC(),
		Outcome:            corebilling.TurnOutcomeCompleted,
		CustomerPricingRef: corebilling.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: corebilling.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs: []string{bLegID},
	}
	require.NoError(t, f.billing.AppendCallUsage(ctx, call))
	leg := corebilling.CallLegUsageRecord{
		BLegID: bLegID, CallID: callID, ALegID: f.aLegID, AttemptSeq: 1,
		BackendID: "backend-statement", ProviderID: "provider-statement", ModelID: "model-statement",
		StartedAt: time.Unix(1_700_030_000, 0).UTC(), FinishedAt: time.Unix(1_700_030_001, 0).UTC(),
		Outcome: corebilling.LegOutcomeWinner, Surfaced: corebilling.SurfacedYes,
	}
	require.NoError(t, f.billing.AppendCallLegUsage(ctx, leg))
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: f.storeID, TenantID: "tenant-statement", AccountID: f.account.ID,
		ALegID: f.aLegID, BillingCallID: callID.String(), BLegID: bLegID,
	}
}

func statementE2EObservation(storeID, accountID, statementLineID, providerAccount, period string, correlation metering.CorrelationV2) metering.Observation {
	now := time.Unix(1_700_030_050, 0).UTC()
	value := metering.Decimal{Coefficient: "130", Scale: 2}
	key := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
		Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
	}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "stmt-obs-" + statementLineID, SourceEventKey: "stmt-source-" + statementLineID,
		Revision: 1, StreamID: "stmt-stream-" + statementLineID, Sequence: 1,
		Origin: metering.OriginStatement, Acquisition: metering.AcquisitionStatementImporter, Authority: metering.AuthorityVerifiedStatement,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: "tenant-statement", AccountID: accountID,
			ProviderAccountKey: providerAccount, PeriodID: period, StatementID: "stmt-e2e", StatementLineID: statementLineID,
		},
		Correlation: correlation,
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "statement.e2e.v1",
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "charge-" + statementLineID, Component: &key, Amount: &value, Currency: "USD",
			Kind: metering.ChargeKindComponent, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
		}},
	}
}

func (f statementE2EFixture) persistStatementLine(t *testing.T, observation metering.Observation, ref metering.ObservationRef) {
	t.Helper()
	subject := observation.Subject.Clone()
	batch := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-acct", StatementID: "stmt-e2e", Revision: 1, PeriodID: "2026-08",
		Subject:      subject,
		Observations: []metering.Observation{observation},
		Lines: []economics.StatementLine{{
			ID: subject.StatementLineID, Revision: 1, Subject: subject, Observation: ref,
			ChargeItemID: observation.Charges[0].ChargeItemID, Outcome: economics.StatementLineMatched,
		}},
	}
	scope := corebilling.TrustedStatementScope{
		StoreID: f.storeID, TenantID: "tenant-statement", ProviderAccountKeys: []string{"provider-acct"},
	}
	normalized, err := corebilling.NormalizeStatement(scope, batch)
	require.NoError(t, err)
	require.NoError(t, f.billing.AppendStatementRevision(context.Background(), normalized))
}

// persistUnmatchedStatementLine retains the explicit durable unmatched outcome
// for one observation's statement-line identity. The unmatched line carries no
// observation linkage, so a resolver that only reads the observation must not
// report the referenced observation as a persisted matched line.
func (f statementE2EFixture) persistUnmatchedStatementLine(t *testing.T, observation metering.Observation, revision uint64, reason string) {
	t.Helper()
	subject := observation.Subject.Clone()
	batch := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-acct", StatementID: "stmt-e2e", Revision: revision, PeriodID: "2026-08",
		Subject: subject,
		Lines: []economics.StatementLine{{
			ID: subject.StatementLineID, Revision: 1, Subject: subject,
			Outcome: economics.StatementLineUnmatched, UnmatchedReason: reason,
		}},
	}
	scope := corebilling.TrustedStatementScope{
		StoreID: f.storeID, TenantID: "tenant-statement", ProviderAccountKeys: []string{"provider-acct"},
	}
	normalized, err := corebilling.NormalizeStatement(scope, batch)
	require.NoError(t, err)
	require.NoError(t, f.billing.AppendStatementRevision(context.Background(), normalized))
}

func TestOperatorReportsStatementEvidenceEndToEnd(t *testing.T) {
	t.Parallel()
	fixture := newStatementE2EFixture(t, "statement-e2e-linked")
	ctx := context.Background()
	observation := statementE2EObservation(fixture.storeID, fixture.account.ID, "line-e2e", "provider-acct", "2026-08", metering.CorrelationV2{
		StoreID: fixture.storeID, TenantID: "tenant-statement", ALegID: fixture.aLegID,
		BillingCallID: fixture.callID.String(), BLegID: fixture.bLegID,
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	})
	require.NoError(t, observation.Validate())
	require.NoError(t, fixture.metering.AppendObservation(ctx, observation))
	// The statement ledger's immutable observation reference hashes the full
	// canonical envelope (Observation.Fingerprint), so the S valuation and the
	// persisted matched line share that exact ref.
	ref := metering.ObservationRef{
		StoreID: fixture.storeID, ObservationID: observation.ID,
		Revision: observation.Revision, PayloadHash: observation.Fingerprint(),
	}
	valuation := fixture.evaluation(t, "val-statement-e2e", []metering.ObservationRef{ref})

	// The same immutable observation ref is a persisted matched statement line
	// in the statement ledger, so the exposed lineage is not an invented row.
	fixture.persistStatementLine(t, observation, ref)
	ledger, err := fixture.billing.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: fixture.storeID, TenantID: "tenant-statement"},
		ProviderAccountKey: "provider-acct", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, ledger.Lines, 1)
	require.Equal(t, "stmt-e2e", ledger.Lines[0].StatementID)
	require.Equal(t, "line-e2e", ledger.Lines[0].LineID)
	require.Equal(t, ref, ledger.Lines[0].Observation)

	reports := operatorReportsForMount(fixture.billing, fixture.metering)
	require.NotNil(t, reports.EconomicDetail)
	detail, err := reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
	require.NoError(t, err)

	// The embedded leg carries no observations; the statement evidence must
	// come from the composed journal read, not from a fabricated row.
	require.Empty(t, detail.Observations)
	require.True(t, detail.StatementEvidence.ReaderAvailable)
	require.Len(t, detail.StatementEvidence.Lines, 1)
	line := detail.StatementEvidence.Lines[0]
	require.Equal(t, "stmt-e2e", line.StatementID)
	require.Equal(t, "line-e2e", line.StatementLineID)
	require.Equal(t, uint64(1), line.Revision)
	require.Equal(t, "provider-acct", line.ProviderAccountKey)
	require.Equal(t, "2026-08", line.PeriodID)
	require.Equal(t, economics.StatementLineMatched, line.Outcome)
	require.Equal(t, corebilling.EconomicDetailStatementOutcomePersisted, line.OutcomeSource)
	require.Equal(t, []string{"charge-line-e2e"}, line.ChargeItemIDs)
	require.Equal(t, ref, line.ObservationRef)
	require.Equal(t, observation.ID, line.Observation.ID)
	require.Equal(t, observation.Fingerprint(), line.Observation.Fingerprint(), "the exact verified observation must round-trip")
	require.Equal(t, fixture.account.ID, line.Subject.AccountID)
	require.Equal(t, "tenant-statement", line.Subject.TenantID)
	require.Equal(t, []string{valuation.ID}, line.ValuationIDs)
	require.Empty(t, detail.StatementEvidence.MissingRefs)
	require.Empty(t, detail.StatementEvidence.UnattributedRefs)
	require.Empty(t, detail.StatementEvidence.UnresolvedRefs)

	// Determinism: the same durable read rebuilds the same evidence.
	again, err := reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
	require.NoError(t, err)
	require.Equal(t, detail.StatementEvidence, again.StatementEvidence)
}

func TestOperatorReportsStatementEvidenceMissingRefIsExplicit(t *testing.T) {
	t.Parallel()
	fixture := newStatementE2EFixture(t, "statement-e2e-missing")
	ctx := context.Background()
	absent := metering.ObservationRef{StoreID: fixture.storeID, ObservationID: "stmt-obs-absent", Revision: 1, PayloadHash: strings.Repeat("b", 64)}
	fixture.evaluation(t, "val-statement-missing", []metering.ObservationRef{absent})

	reports := operatorReportsForMount(fixture.billing, fixture.metering)
	detail, err := reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
	require.NoError(t, err)
	require.True(t, detail.StatementEvidence.ReaderAvailable)
	require.Empty(t, detail.StatementEvidence.Lines, "an absent observation must never be fabricated")
	require.Equal(t, []metering.ObservationRef{absent}, detail.StatementEvidence.MissingRefs)
	require.Empty(t, detail.StatementEvidence.UnresolvedRefs)
}

func TestOperatorReportsStatementEvidenceForeignAccountFailsClosed(t *testing.T) {
	t.Parallel()
	fixture := newStatementE2EFixture(t, "statement-e2e-foreign")
	ctx := context.Background()
	observation := statementE2EObservation(fixture.storeID, "other-account", "line-foreign", "provider-acct", "2026-08", metering.CorrelationV2{
		StoreID: fixture.storeID, TenantID: "tenant-statement", ALegID: fixture.aLegID,
		BillingCallID: fixture.callID.String(), BLegID: fixture.bLegID,
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	})
	require.NoError(t, fixture.metering.AppendObservation(ctx, observation))
	ref, err := observation.Ref(fixture.storeID)
	require.NoError(t, err)
	fixture.evaluation(t, "val-statement-foreign", []metering.ObservationRef{ref})

	reports := operatorReportsForMount(fixture.billing, fixture.metering)
	_, err = reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
	require.ErrorIs(t, err, corebilling.ErrEconomicDetailScopeMismatch)
}

func TestOperatorReportsStatementEvidenceUnavailableReaderIsExplicit(t *testing.T) {
	t.Parallel()
	fixture := newStatementE2EFixture(t, "statement-e2e-unavailable")
	ctx := context.Background()
	ref := metering.ObservationRef{StoreID: fixture.storeID, ObservationID: "stmt-obs-unavailable", Revision: 1, PayloadHash: strings.Repeat("c", 64)}
	fixture.evaluation(t, "val-statement-unavailable", []metering.ObservationRef{ref})

	// No metering querier is composed: the reader is explicitly unavailable and
	// the reference is enumerated rather than implied.
	reports := operatorReportsForMount(fixture.billing, nil)
	detail, err := reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
	require.NoError(t, err)
	require.False(t, detail.StatementEvidence.ReaderAvailable)
	require.Equal(t, corebilling.EconomicDetailStatementReaderUnavailableReason, detail.StatementEvidence.ReaderReason)
	require.Equal(t, []metering.ObservationRef{ref}, detail.StatementEvidence.UnresolvedRefs)
	require.Empty(t, detail.StatementEvidence.MissingRefs)
}

// Finding 5A end-to-end: one A-leg can contain several later calls, so an
// observation carrying only A-leg ancestry must never be presented as a
// matched line of either call even when a matching ledger line exists.
func TestOperatorReportsStatementEvidenceAncestorOnlyUnattributed(t *testing.T) {
	t.Parallel()
	fixture := newStatementE2EFixture(t, "statement-e2e-ancestor")
	ctx := context.Background()
	secondCall, err := corebilling.NewBillingCallID()
	require.NoError(t, err)
	secondSubject := fixture.appendCall(t, secondCall, "b-statement-e2e-second")

	observation := statementE2EObservation(fixture.storeID, fixture.account.ID, "line-ancestor", "provider-acct", "2026-08", metering.CorrelationV2{
		StoreID: fixture.storeID, TenantID: "tenant-statement", ALegID: fixture.aLegID,
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	})
	require.NoError(t, observation.Validate())
	require.NoError(t, fixture.metering.AppendObservation(ctx, observation))
	ref := metering.ObservationRef{
		StoreID: fixture.storeID, ObservationID: observation.ID,
		Revision: observation.Revision, PayloadHash: observation.Fingerprint(),
	}
	fixture.evaluation(t, "val-ancestor-first", []metering.ObservationRef{ref})
	fixture.evaluationFor(t, "val-ancestor-second", secondSubject, []metering.ObservationRef{ref})
	// A durable matched line exists, so a wrong A-leg-only attribution would
	// surface a matched line for the call rather than unattributed evidence.
	fixture.persistStatementLine(t, observation, ref)

	reports := operatorReportsForMount(fixture.billing, fixture.metering)
	for _, scope := range []struct {
		name  string
		query corebilling.EconomicDetailQuery
	}{
		{name: "first call", query: fixture.query()},
		{name: "second call", query: corebilling.EconomicDetailQuery{
			StoreID: fixture.storeID, TenantID: "tenant-statement", AccountID: fixture.account.ID,
			BillingCallID: secondCall.String(), ALegID: fixture.aLegID,
		}},
	} {
		t.Run(scope.name, func(t *testing.T) {
			detail, err := reports.EconomicDetail.QueryEconomicDetail(ctx, scope.query)
			require.NoError(t, err)
			require.Empty(t, detail.StatementEvidence.Lines, "ancestor-only evidence must not be attributed to one call")
			require.Equal(t, []metering.ObservationRef{ref}, detail.StatementEvidence.UnattributedRefs)
		})
	}
}

// Finding 5B end-to-end: the composed reader must expose the exact durable
// statement-line outcome and never stamp matched without it.
func TestOperatorReportsStatementEvidencePersistedOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("persisted unmatched is not matched", func(t *testing.T) {
		t.Parallel()
		fixture := newStatementE2EFixture(t, "statement-e2e-unmatched")
		observation := statementE2EObservation(fixture.storeID, fixture.account.ID, "line-unmatched", "provider-acct", "2026-08", metering.CorrelationV2{
			StoreID: fixture.storeID, TenantID: "tenant-statement", ALegID: fixture.aLegID,
			BillingCallID: fixture.callID.String(), BLegID: fixture.bLegID,
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		})
		require.NoError(t, observation.Validate())
		require.NoError(t, fixture.metering.AppendObservation(ctx, observation))
		ref := metering.ObservationRef{
			StoreID: fixture.storeID, ObservationID: observation.ID,
			Revision: observation.Revision, PayloadHash: observation.Fingerprint(),
		}
		fixture.evaluation(t, "val-statement-unmatched", []metering.ObservationRef{ref})
		fixture.persistUnmatchedStatementLine(t, observation, 1, "aggregate")

		reports := operatorReportsForMount(fixture.billing, fixture.metering)
		detail, err := reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
		require.NoError(t, err)
		require.Len(t, detail.StatementEvidence.Lines, 1)
		line := detail.StatementEvidence.Lines[0]
		require.Equal(t, corebilling.EconomicDetailStatementOutcomePersisted, line.OutcomeSource)
		require.Equal(t, economics.StatementLineUnmatched, line.Outcome)
		require.NotEqual(t, economics.StatementLineMatched, line.Outcome)
		require.Equal(t, "aggregate", line.UnmatchedReason)
	})

	t.Run("missing persisted line stays observation-linked", func(t *testing.T) {
		t.Parallel()
		fixture := newStatementE2EFixture(t, "statement-e2e-missing-outcome")
		observation := statementE2EObservation(fixture.storeID, fixture.account.ID, "line-missing-outcome", "provider-acct", "2026-08", metering.CorrelationV2{
			StoreID: fixture.storeID, TenantID: "tenant-statement", ALegID: fixture.aLegID,
			BillingCallID: fixture.callID.String(), BLegID: fixture.bLegID,
			ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
		})
		require.NoError(t, observation.Validate())
		require.NoError(t, fixture.metering.AppendObservation(ctx, observation))
		ref := metering.ObservationRef{
			StoreID: fixture.storeID, ObservationID: observation.ID,
			Revision: observation.Revision, PayloadHash: observation.Fingerprint(),
		}
		fixture.evaluation(t, "val-statement-missing-outcome", []metering.ObservationRef{ref})
		// No durable statement line is persisted.

		reports := operatorReportsForMount(fixture.billing, fixture.metering)
		detail, err := reports.EconomicDetail.QueryEconomicDetail(ctx, fixture.query())
		require.NoError(t, err)
		require.Len(t, detail.StatementEvidence.Lines, 1)
		line := detail.StatementEvidence.Lines[0]
		require.Equal(t, corebilling.EconomicDetailStatementOutcomeObservation, line.OutcomeSource)
		require.Empty(t, line.Outcome, "an unresolved persisted outcome must never be presented as matched")
	})
}
