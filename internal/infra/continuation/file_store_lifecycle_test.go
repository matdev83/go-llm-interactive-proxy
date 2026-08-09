package continuation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	infracont "github.com/matdev83/go-llm-interactive-proxy/internal/infra/continuation"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func testScope(principal, session string) lipcont.Scope {
	return lipcont.Scope{PrincipalID: principal, SessionID: session}
}

func TestFileStoreCloseContractAndCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "continuation_store.json")
	store, err := infracont.NewFileStore(path, lipcont.StorageLimits{})
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	ctx := context.Background()
	scope := testScope("p", "s")
	id, err := store.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Idempotent Close
	if err := store.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	// Reject operations after close
	if _, err := store.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour}); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("reserve after close: want ErrStoreClosed, got %v", err)
	}
	rec := lipcont.ContinuationRecord{ID: id, Scope: scope, Terminal: true}
	if err := store.PutTerminal(ctx, rec); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("put terminal after close: want ErrStoreClosed, got %v", err)
	}
	if _, err := store.Get(ctx, scope, id); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("get after close: want ErrStoreClosed, got %v", err)
	}
	if err := store.Delete(ctx, scope, id); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("delete after close: want ErrStoreClosed, got %v", err)
	}

	// Verify no leftover temporary files in directory
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".continuation-") {
			t.Fatalf("stale temp file found: %s", entry.Name())
		}
	}
}
