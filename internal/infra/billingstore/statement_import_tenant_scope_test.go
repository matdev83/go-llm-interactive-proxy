package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase13 review remediation: a trusted tenant-1 batch containing any
// tenant-2 line (matched or unmatched) must fail with a typed scope error
// before ledger append and leave zero statement/line rows. Exact valid replay
// remains green.
func TestStatementImportLedgerSQLiteForeignLineTenantRejectsAtomically(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		lineIndex int
	}{
		{name: "matched", lineIndex: 0},
		{name: "unmatched", lineIndex: 1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			scope := billing.TrustedStatementScope{
				StoreID: "test", TenantID: "tenant-1", PrincipalID: "principal-1",
				ProviderAccountKeys: []string{"provider-account"},
			}
			valid := tenantScopeTestBatch(t, "test", "statement-tenant-scope")
			foreign := tenantScopeTestBatch(t, "test", "statement-tenant-scope")
			foreign.Lines[tc.lineIndex].Subject.TenantID = "tenant-2"

			service, err := billing.NewStatementImportService(store)
			require.NoError(t, err)
			_, err = service.Import(ctx, scope, foreign)
			require.ErrorIs(t, err, billing.ErrStatementImportScopeMismatch)
			require.Zero(t, statementImportRevisionCount(t, store, "statement-tenant-scope"))
			require.Zero(t, statementImportLineRows(t, store))

			result, err := service.Import(ctx, scope, valid)
			require.NoError(t, err)
			require.Equal(t, []string{"line-1"}, result.Accepted)
			require.Equal(t, []string{"line-2"}, result.Unmatched)
			require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-tenant-scope"))
			require.Equal(t, 2, statementImportLineRows(t, store))
		})
	}
}

func tenantScopeTestBatch(t *testing.T, storeID, statement string) economics.StatementBatch {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: "tenant-1",
		ProviderAccountKey: "provider-account", StatementID: statement,
		StatementLineID: "line-1", PeriodID: "period-1",
	}
	batch := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-account", StatementID: statement,
		Revision: 1, PeriodID: "period-1", Subject: subject,
	}
	observation := statementImportObservation(t, storeID, statement, "line-1", "1.25")
	batch.Observations = append(batch.Observations, observation)
	batch.Lines = append(batch.Lines, economics.StatementLine{
		ID: "line-1", Revision: 1, Subject: subject,
		Observation: metering.ObservationRef{
			StoreID: storeID, ObservationID: observation.ID, Revision: observation.Revision,
			PayloadHash: observation.Fingerprint(),
		},
		ChargeItemID: "charge-line-1", Outcome: economics.StatementLineMatched,
	})
	unmatchedSubject := subject
	unmatchedSubject.StatementLineID = "line-2"
	batch.Lines = append(batch.Lines, economics.StatementLine{
		ID: "line-2", Revision: 1, Subject: unmatchedSubject,
		Outcome: economics.StatementLineUnmatched, UnmatchedReason: "account-period aggregate",
	})
	return batch
}
