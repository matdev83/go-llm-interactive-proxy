package continuity_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestManager_ResolveSession_createsNewWhenEmpty(t *testing.T) {
	t.Parallel()

	store := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	m := continuity.NewManager(store)

	sess, err := m.ResolveSession(context.Background(), lipapi.SessionRef{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.ALegID == "" {
		t.Fatal("expected ALegID")
	}
	if !sess.IsNew {
		t.Fatal("expected IsNew")
	}
}

func TestManager_ResolveSession_resolvesByContinuityKey(t *testing.T) {
	t.Parallel()

	store := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	m := continuity.NewManager(store)

	sess1, err := m.ResolveSession(context.Background(), lipapi.SessionRef{
		ContinuityKey: "key-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sess1.IsNew {
		t.Fatal("expected new session")
	}

	sess2, err := m.ResolveSession(context.Background(), lipapi.SessionRef{
		ContinuityKey: "key-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess2.IsNew {
		t.Fatal("expected existing session")
	}
	if sess2.ALegID != sess1.ALegID {
		t.Fatalf("expected same ALegID: %q != %q", sess2.ALegID, sess1.ALegID)
	}
}

func TestResolveALegRecord_returnsStoreRow(t *testing.T) {
	t.Parallel()

	store := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	rec, err := continuity.ResolveALegRecord(context.Background(), store, lipapi.SessionRef{ContinuityKey: "k"})
	if err != nil {
		t.Fatalf("ResolveALegRecord: %v", err)
	}
	if rec.ALegID == "" {
		t.Fatal("expected ALegID on record")
	}
	if rec.ContinuityKey != "k" {
		t.Fatalf("ContinuityKey: got %q", rec.ContinuityKey)
	}
}

func TestManager_ResolveSession_resolvesByALegID(t *testing.T) {
	t.Parallel()

	store := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	m := continuity.NewManager(store)

	sess1, err := m.ResolveSession(context.Background(), lipapi.SessionRef{
		ContinuityKey: "ck",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess2, err := m.ResolveSession(context.Background(), lipapi.SessionRef{
		ALegID: sess1.ALegID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess2.ALegID != sess1.ALegID {
		t.Fatal("expected same ALegID")
	}
	if sess2.IsNew {
		t.Fatal("expected existing session")
	}
}

func TestManager_Store_returnsUnderlyingStore(t *testing.T) {
	t.Parallel()

	store := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	m := continuity.NewManager(store)
	if m.Store() != store {
		t.Fatal("expected same store")
	}
}
