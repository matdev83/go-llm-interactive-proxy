package billingstore

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

type rollbackCollisionHook struct {
	failed atomic.Int64
	errors chan error
	cancel context.CancelFunc
}

func (*rollbackCollisionHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *rollbackCollisionHook) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if event.Err == nil || !strings.HasPrefix(strings.TrimSpace(event.Query), "INSERT INTO journal_transactions") {
		return
	}
	h.failed.Add(1)
	select {
	case h.errors <- event.Err:
	default:
	}
	// The collision is permanent. Cancel only after observing the real failed
	// insert, so the public API rolls back without repeating the same failure
	// through its entire production contention backoff.
	h.cancel()
}

func assertJournalRollbackFailure(ctx context.Context, t *testing.T, store *DurableStore, input billing.JournalTransaction) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hook := &rollbackCollisionHook{errors: make(chan error, 1), cancel: cancel}
	store.db.AddQueryHook(hook)
	_, err := store.postJournalTransaction(ctx, input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("journal error=%v, want cancellation after the actual primary-key failure", err)
	}
	if got := hook.failed.Load(); got != 1 {
		t.Fatalf("failed journal inserts=%d, want one real rollback attempt", got)
	}
	select {
	case collision := <-hook.errors:
		if !isUniqueViolation(collision) {
			t.Fatalf("journal insert failed with %v, want a primary-key collision", collision)
		}
	default:
		t.Fatal("rollback fixture did not execute the failing journal insert")
	}
}
