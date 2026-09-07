package conversationview_test

import (
	"testing"
)

// TestDBParity_SQLite is the canonical parity entry point for conversationview persistence on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	TestConversationView_BunContract_SQLite(t)
}
