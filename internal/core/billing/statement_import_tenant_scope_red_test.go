package billing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RED: trusted tenant-1 batch containing a tenant-2 line (matched or
// unmatched outcome) must fail ErrStatementImportScopeMismatch before ledger
// append. This is the Phase13 review blocker (Requirements 13.1, 16.3).
func TestStatementImportForeignLineTenantIsScopeMismatchRed(t *testing.T) {
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
			ledger := newMemoryStatementLedger()
			service := newStatementImportService(t, ledger)
			scope := trustedStatementImportScope()
			batch := statementImportFixture(t, statementImportFixtureOptions{})
			batch.Lines[tc.lineIndex].Subject.TenantID = "tenant-2"
			_, err := service.Import(context.Background(), scope, batch)
			require.ErrorIs(t, err, ErrStatementImportScopeMismatch)
			assert.Empty(t, ledger.statements)
			assert.Empty(t, ledger.lines)
			assert.Zero(t, ledger.appends)
		})
	}
}
