//go:build integration

package ledgerstore

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for the
// control-plane event ledger persistence on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	contract.RunSuite(t, pgFactory{dsn: dsn})
}
