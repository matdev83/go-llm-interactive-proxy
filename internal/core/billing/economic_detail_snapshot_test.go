package billing

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 7 RED contract (composed reader): statement evidence is resolved
// above the durable reader, so the statement wrapper must fold it into the same
// authenticated snapshot boundary the durable reader binds. A persisted
// statement-line correction between pages must invalidate the continuation
// rather than change supposedly frozen full-scope facts.

// snapshotEconomicDetailStub is a durable-reader fake that also implements the
// core snapshot continuation encoder, mirroring the durable adapter seam.
type snapshotEconomicDetailStub struct {
	detail EconomicDetail
}

func (s snapshotEconomicDetailStub) QueryEconomicDetail(context.Context, EconomicDetailQuery) (EconomicDetail, error) {
	return s.detail, nil
}

func (s snapshotEconomicDetailStub) EncodeEconomicDetailSnapshotContinuation(_ EconomicDetailQuery, _ EconomicObservationPosition, _, outerFingerprint string) string {
	return "op2." + outerFingerprint
}

func TestStatementEvidenceSnapshotInvalidatesOnLateStatementCorrection(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := statementTestSubject(storeID, aLegID, billingCallID, "b-snap-statement")
	statementSubject := statementTestStatementSubject(storeID, "line-snap")
	observation := statementTestObservation(t, "stmt-obs-snap", 1, statementSubject, metering.CorrelationV2{
		StoreID: storeID, TenantID: "tenant-statement", ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-snap-statement",
		ProviderAccountKey: "provider-acct", PeriodID: "2026-08",
	}, "charge-snap")
	ref := statementTestRef(t, storeID, observation)
	valuation := statementTestValuation(t, "val-snap-statement", subject, []metering.ObservationRef{ref})

	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	normalized := normalizedDetailQuery(t, query)
	position := EconomicObservationPosition{StreamID: "stream-snap", ObservationID: "obs-snap", Revision: 1}

	matchedSource := statementSourceStub{
		byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
		lines: statementTestLineViews(statementTestMatchedLineView(t, observation, ref)),
	}
	base := snapshotEconomicDetailStub{detail: EconomicDetail{
		Scope: normalized, Valuations: []economics.Valuation{valuation},
		SnapshotFingerprint: "base-snapshot-fingerprint",
		NextPosition:        &position,
	}}
	reader := NewStatementEvidenceEconomicDetailReader(base, matchedSource)
	first, err := reader.QueryEconomicDetail(context.Background(), query)
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)
	require.Len(t, first.StatementEvidence.Lines, 1)
	require.Equal(t, economics.StatementLineMatched, first.StatementEvidence.Lines[0].Outcome)

	composed := strings.TrimPrefix(first.NextCursor, "op2.")
	require.NotEmpty(t, composed)

	t.Run("unchanged statement evidence continues", func(t *testing.T) {
		t.Parallel()
		unchangedBase := snapshotEconomicDetailStub{detail: EconomicDetail{
			Scope: normalized, Valuations: []economics.Valuation{valuation},
			SnapshotFingerprint:         "base-snapshot-fingerprint",
			PreviousSnapshotFingerprint: composed,
			NextPosition:                &position,
		}}
		unchangedReader := NewStatementEvidenceEconomicDetailReader(unchangedBase, matchedSource)
		next, err := unchangedReader.QueryEconomicDetail(context.Background(), query)
		require.NoError(t, err)
		require.NotEmpty(t, next.NextCursor)
	})

	t.Run("late statement correction invalidates", func(t *testing.T) {
		t.Parallel()
		correctedSource := statementSourceStub{
			byKey: map[string]metering.Observation{statementSourceKey(ref): observation},
			lines: statementTestLineViews(statementTestUnmatchedLineView(observation, "provider_credit_missing")),
		}
		correctedBase := snapshotEconomicDetailStub{detail: EconomicDetail{
			Scope: normalized, Valuations: []economics.Valuation{valuation},
			SnapshotFingerprint:         "base-snapshot-fingerprint",
			PreviousSnapshotFingerprint: composed,
			NextPosition:                &position,
		}}
		correctedReader := NewStatementEvidenceEconomicDetailReader(correctedBase, correctedSource)
		_, err := correctedReader.QueryEconomicDetail(context.Background(), query)
		require.ErrorIs(t, err, economics.ErrOperatorCursorStale,
			"a persisted statement-line correction must invalidate the composed snapshot")
	})
}
