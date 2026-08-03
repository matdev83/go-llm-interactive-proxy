package openresponses_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func storeScope() lipcont.Scope {
	return lipcont.Scope{TenantID: "t", PrincipalID: "p", ConnectionID: "conn-1"}
}

func closeLocalStore(t *testing.T, store lipcont.Store) error {
	t.Helper()
	if closer, ok := store.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func wsTerminalRecord(id lipcont.ResponseID, scope lipcont.Scope, depth int) lipcont.ContinuationRecord {
	return lipcont.ContinuationRecord{
		ID:          id,
		Scope:       scope,
		ProfileID:   openresponses.DefaultProfile,
		InputItems:  []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Status: lipapi.ItemStatusCompleted, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "in"}}}},
		OutputItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Status: lipapi.ItemStatusCompleted, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "out"}}}},
		Terminal:    true,
		Status:      lipcont.RecordStatusCompleted,
		ChainDepth:  depth,
		ExpiresAt:   time.Now().Add(time.Hour),
	}
}

func TestWSLocalStore_ReserveIssuesValidProxyIDs(t *testing.T) {
	store := openresponses.NewWSLocalStore(storeScope(), lipcont.DefaultStorageLimits())
	ctx := context.Background()
	seen := make(map[string]bool)
	for i := 0; i < 5; i++ {
		id, err := store.Reserve(ctx, storeScope(), lipcont.StoragePolicy{Mode: lipcont.PersistenceConnection})
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if err := id.Validate(); err != nil {
			t.Fatalf("reserve %d produced non-proxy-safe ID %q: %v", i, id, err)
		}
		if seen[id.String()] {
			t.Fatalf("reserve %d repeated ID %q", i, id)
		}
		seen[id.String()] = true
	}
	_ = closeLocalStore(t, store)
}

func TestWSLocalStore_PutTerminalRejectsInvalidIDAndNonTerminal(t *testing.T) {
	store := openresponses.NewWSLocalStore(storeScope(), lipcont.DefaultStorageLimits())
	ctx := context.Background()

	// Weak client-selected ID is not proxy-safe and must be rejected.
	if err := store.PutTerminal(ctx, wsTerminalRecord("resp_abc", storeScope(), 1)); err == nil {
		t.Fatal("weak response ID was accepted")
	}

	// Non-terminal records are never stored.
	rec := wsTerminalRecord(lipcont.ResponseID(validProxyID("terminal")), storeScope(), 1)
	rec.Terminal = false
	if err := store.PutTerminal(ctx, rec); err == nil {
		t.Fatal("non-terminal record was accepted")
	}
	_ = closeLocalStore(t, store)
}

func TestWSLocalStore_ScopeIsolation(t *testing.T) {
	store := openresponses.NewWSLocalStore(storeScope(), lipcont.DefaultStorageLimits())
	ctx := context.Background()
	id := lipcont.ResponseID(validProxyID("scoped"))
	if err := store.PutTerminal(ctx, wsTerminalRecord(id, storeScope(), 1)); err != nil {
		t.Fatalf("put terminal: %v", err)
	}
	// A different connection scope cannot observe the record.
	other := lipcont.Scope{TenantID: "t", PrincipalID: "p", ConnectionID: "conn-other"}
	if _, err := store.Get(ctx, other, id); err == nil {
		t.Fatal("record visible to a different connection scope")
	}
	if _, err := store.Get(ctx, storeScope(), id); err != nil {
		t.Fatalf("record missing in owning scope: %v", err)
	}
	_ = closeLocalStore(t, store)
}

func TestWSLocalStore_RecordBoundEvictsOldest(t *testing.T) {
	limits := lipcont.DefaultStorageLimits()
	limits.MaxRecords = 2
	store := openresponses.NewWSLocalStore(storeScope(), limits)
	ctx := context.Background()

	id1 := lipcont.ResponseID(validProxyID("one"))
	id2 := lipcont.ResponseID(validProxyID("two"))
	id3 := lipcont.ResponseID(validProxyID("three"))
	if err := store.PutTerminal(ctx, wsTerminalRecord(id1, storeScope(), 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(ctx, wsTerminalRecord(id2, storeScope(), 2)); err != nil {
		t.Fatal(err)
	}
	// id1 evicted to make room for id3.
	if err := store.PutTerminal(ctx, wsTerminalRecord(id3, storeScope(), 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, storeScope(), id1); err == nil {
		t.Fatal("oldest record was not evicted at the record bound")
	}
	if _, err := store.Get(ctx, storeScope(), id3); err != nil {
		t.Fatalf("newest record missing: %v", err)
	}
	_ = closeLocalStore(t, store)
}

func TestWSLocalStore_ByteBoundEvictsOldest(t *testing.T) {
	limits := lipcont.DefaultStorageLimits()
	limits.MaxRecords = 100
	limits.MaxBytes = 8 << 10
	store := openresponses.NewWSLocalStore(storeScope(), limits)
	ctx := context.Background()

	var prev lipcont.ResponseID
	var first lipcont.ResponseID
	for i := 0; i < 40; i++ {
		id := lipcont.ResponseID(validProxyID(itoa(i)))
		if i == 0 {
			first = id
		}
		if err := store.PutTerminal(ctx, wsTerminalRecord(id, storeScope(), i+1)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		prev = id
	}
	if _, err := store.Get(ctx, storeScope(), first); err == nil {
		t.Fatal("oldest record survived the byte bound")
	}
	if _, err := store.Get(ctx, storeScope(), prev); err != nil {
		t.Fatalf("newest record evicted at the byte bound: %v", err)
	}
	_ = closeLocalStore(t, store)
}

func TestWSLocalStore_ChainDepthBound(t *testing.T) {
	limits := lipcont.DefaultStorageLimits()
	limits.MaxChainDepth = 3
	store := openresponses.NewWSLocalStore(storeScope(), limits)
	ctx := context.Background()

	if err := store.PutTerminal(ctx, wsTerminalRecord(lipcont.ResponseID(validProxyID("d3")), storeScope(), 3)); err != nil {
		t.Fatalf("depth-3 record rejected: %v", err)
	}
	if err := store.PutTerminal(ctx, wsTerminalRecord(lipcont.ResponseID(validProxyID("d4")), storeScope(), 4)); err == nil {
		t.Fatal("depth-4 record accepted beyond the chain bound")
	}
	_ = closeLocalStore(t, store)
}

func TestWSLocalStore_CloseClearsState(t *testing.T) {
	store := openresponses.NewWSLocalStore(storeScope(), lipcont.DefaultStorageLimits())
	ctx := context.Background()
	id := lipcont.ResponseID(validProxyID("closed"))
	if err := store.PutTerminal(ctx, wsTerminalRecord(id, storeScope(), 1)); err != nil {
		t.Fatal(err)
	}
	if err := closeLocalStore(t, store); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := closeLocalStore(t, store); err != nil {
		t.Fatalf("second close not idempotent: %v", err)
	}
	if _, err := store.Get(ctx, storeScope(), id); err == nil {
		t.Fatal("record survived close")
	}
	if err := store.PutTerminal(ctx, wsTerminalRecord(lipcont.ResponseID(validProxyID("post")), storeScope(), 1)); err == nil {
		t.Fatal("put accepted after close")
	}
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	out := make([]byte, 0, 4)
	for n > 0 {
		out = append([]byte{digits[n%10]}, out...)
		n /= 10
	}
	return string(out)
}
