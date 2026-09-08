//go:build integration

package conversationview_test

import (
	"testing"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for conversationview persistence on PostgreSQL.
func TestDBParity_PostgresDirect(t *testing.T) {
	TestConversationView_PostgresContract(t)
}
