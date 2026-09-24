package billing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

var _ economics.StatementImporter = (*BoundStatementImporter)(nil)

func TestBoundStatementImporterMapsPublicBatchIntoBoundScope(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	bound, err := NewBoundStatementImporter(service, trustedStatementImportScope())
	require.NoError(t, err)
	require.NotNil(t, bound)

	var importer economics.StatementImporter = bound
	batch := statementImportFixture(t, statementImportFixtureOptions{})
	result, err := importer.Import(context.Background(), batch)
	require.NoError(t, err)
	require.NoError(t, result.Validate())
	require.Equal(t, []string{"line-1"}, result.Accepted)
	require.Equal(t, []string{"line-2"}, result.Unmatched)

	replayed, err := importer.Import(context.Background(), batch)
	require.NoError(t, err)
	require.Equal(t, []string{"line-1", "line-2"}, replayed.Replayed)
	require.Equal(t, 1, ledger.appends)
}

func TestBoundStatementImporterKeepsScopesIsolated(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)

	tenantOne, err := NewBoundStatementImporter(service, trustedStatementImportScope())
	require.NoError(t, err)

	tenantTwoScope := trustedStatementImportScope()
	tenantTwoScope.TenantID = "tenant-2"
	tenantTwo, err := NewBoundStatementImporter(service, tenantTwoScope)
	require.NoError(t, err)

	tenantTwoBatch := statementImportFixture(t, statementImportFixtureOptions{Tenant: "tenant-2", Statement: "statement-2"})
	result, err := tenantTwo.Import(context.Background(), tenantTwoBatch)
	require.NoError(t, err)
	require.Equal(t, []string{"line-1"}, result.Accepted)

	// The same public batch shape under the wrong bound scope fails closed and
	// writes nothing.
	result, err = tenantOne.Import(context.Background(), tenantTwoBatch)
	require.ErrorIs(t, err, ErrStatementImportScopeMismatch)
	require.Empty(t, result.Accepted)
	require.Equal(t, 1, ledger.appends, "a scope-rejected import must not write")
}

func TestBoundStatementImporterConstructionFailsClosed(t *testing.T) {
	t.Parallel()

	service := newStatementImportService(t, newMemoryStatementLedger())
	if _, err := NewBoundStatementImporter(nil, trustedStatementImportScope()); err == nil {
		t.Fatal("nil service must be rejected")
	}
	if _, err := NewBoundStatementImporter(service, TrustedStatementScope{StoreID: "store-1"}); err == nil {
		t.Fatal("scope without tenant or account authority must be rejected")
	}
	bound, err := NewBoundStatementImporter(service, trustedStatementImportScope())
	require.NoError(t, err)

	// The bound scope is a detached copy: widening the caller's slice after
	// construction must not change the authorized import scope.
	scope := trustedStatementImportScope()
	detached, err := NewBoundStatementImporter(service, scope)
	require.NoError(t, err)
	scope.ProviderAccountKeys[0] = "other-account"
	require.False(t, detached.scope.AuthorizesProviderAccount("other-account"))
	require.True(t, bound.scope.AuthorizesProviderAccount("provider-account"))
}
