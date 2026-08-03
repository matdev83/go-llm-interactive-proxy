package continuation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corecont "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestMemoryStoreCloseContract(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	ctx := context.Background()
	scope := testScope("p", "s")

	id, err := store.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("reserve before close: %v", err)
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

	rec := terminalRecord(id, scope, "", nil, nil)
	if err := store.PutTerminal(ctx, rec); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("put terminal after close: want ErrStoreClosed, got %v", err)
	}

	if _, err := store.Get(ctx, scope, id); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("get after close: want ErrStoreClosed, got %v", err)
	}

	if err := store.Delete(ctx, scope, id); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("delete after close: want ErrStoreClosed, got %v", err)
	}

	// Lookup maps closed store error to ErrPreviousResponseNotFound
	if _, err := lipcont.Lookup(ctx, store, scope, id); !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("lookup after close: want ErrPreviousResponseNotFound, got %v", err)
	}
}
