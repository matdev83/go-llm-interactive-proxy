package ledgerstore

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore/contract"
)

// TestDBParity_SQLite is the canonical parity entry point for the control-plane
// event ledger persistence on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	contract.RunSuite(t, sqliteFactory{})
}
