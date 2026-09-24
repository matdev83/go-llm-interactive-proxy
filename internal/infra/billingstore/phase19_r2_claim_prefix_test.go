package billingstore

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase19 R2 blocker 2: ClaimCompleteCalls prefix starvation.
//
// Production ClaimCompleteCalls fixes now at invocation start and defers every
// incomplete row to now+1s. Each invocation resets its pagination cursor. When
// a bounded scan exceeds 1s, the earliest 256 incomplete rows become eligible
// again and can starve a complete call behind them indefinitely.
//
// This file uses a deterministic manual claim clock (DurableStore.claimNowFunc)
// to advance past the 1s yield window without sleeps and without test-only SQL
// mutation of production next_claim_at deadlines. Call IDs are pre-sorted so
// the complete row is strictly last in scan order without timing sleeps.

// phase19R2SortedCallIDs generates n+1 IDs, sorts them, and returns the first
// n as prefix IDs plus the largest as the trailing complete ID. This makes the
// complete row strictly last in (sealed_at, call_id) order when prefix rows
// are appended in sorted order before it, independent of wall-clock
// granularity and without sleeps.
func phase19R2SortedCallIDs(t *testing.T, prefix int) ([]billing.BillingCallID, billing.BillingCallID) {
	t.Helper()
	all := make([]billing.BillingCallID, 0, prefix+1)
	for range prefix + 1 {
		id, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, id)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].String() < all[j].String() })
	return all[:prefix], all[prefix]
}

func phase19R2SeedPrefixWithTrailingComplete(t *testing.T, store *DurableStore, prefix int) (completeID billing.BillingCallID) {
	t.Helper()
	ctx := context.Background()
	prefixIDs, trailing := phase19R2SortedCallIDs(t, prefix)
	for _, id := range prefixIDs {
		if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(id, []string{"b-missing"})); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(trailing, []string{"b-ready"})); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(trailing, "b-ready")); err != nil {
		t.Fatal(err)
	}
	return trailing
}

// TestPhase19R2ClaimPrefixFairProgressDeterministic is the blocker-2
// regression: with a manual claim clock, a first bounded scan defers the
// incomplete prefix, the clock advances past the 1s yield window (simulating
// a scan duration exceeding it), and repeated ClaimCompleteCalls invocations
// must still reach the complete row behind a >page prefix. No sleeps, no
// direct SQL mutation of next_claim_at.
func TestPhase19R2ClaimPrefixFairProgressDeterministic(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const prefix = 300
	completeID := phase19R2SeedPrefixWithTrailingComplete(t, store, prefix)

	base := time.Now().UTC()
	current := base
	store.claimNowFunc = func() time.Time { return current }

	first, err := store.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("first bounded scan claimed %d calls, want 0 (incomplete prefix deferred)", len(first))
	}

	// Simulate a bounded scan duration exceeding the 1s incomplete yield
	// window: the 256 deferred prefix rows are eligible again by wall clock,
	// but production scanning must still advance fairly to the trailing
	// complete row instead of revisiting the same prefix indefinitely.
	current = base.Add(2 * time.Second)
	second, err := store.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Closure.CallID != completeID {
		t.Fatalf("second scan after yield window claimed %v, want complete call %s", second, completeID)
	}

	// The complete call is claimed exactly once; a repeat scan must not
	// return it again.
	current = current.Add(time.Second)
	third, err := store.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range third {
		if c.Closure.CallID == completeID {
			t.Fatalf("complete call %s claimed twice", completeID)
		}
	}
}

// TestPhase19R2ClaimPrefixConcurrentNoDuplicates proves concurrent worker
// safety: workers racing on a small trailing-complete arrangement elect
// exactly one winner for the complete call, never claim an incomplete prefix
// row, and keep bounded pages. The prefix is intentionally small (20): this
// control targets claim atomicity under contention, not bounded-scan paging
// (covered deterministically above); a 300-row thundering herd on the
// shared-cache memory fixture would only prove SQLite lock contention.
func TestPhase19R2ClaimPrefixConcurrentNoDuplicates(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const prefix = 20
	completeID := phase19R2SeedPrefixWithTrailingComplete(t, store, prefix)

	const workers = 4
	var wg sync.WaitGroup
	results := make([][]billing.CompleteCall, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := store.ClaimCompleteCalls(context.Background(), 1)
			results[i] = got
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	seen := map[string]int{}
	for _, got := range results {
		if len(got) > 1 {
			t.Fatalf("worker claimed %d calls with limit 1", len(got))
		}
		for _, c := range got {
			seen[c.Closure.CallID.String()]++
			if c.Closure.CallID != completeID {
				t.Fatalf("worker claimed incomplete call %s, want only %s", c.Closure.CallID, completeID)
			}
		}
	}
	if len(seen) > 1 {
		t.Fatalf("concurrent claim winners for %d distinct calls, want at most one call", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("call %s claimed %d times concurrently, want exactly once", id, n)
		}
	}
	// The complete work is eventually claimed (by exactly one worker or a
	// follow-up scan), never duplicated, and the incomplete prefix remains
	// pending (never claimed as complete).
	after, err := store.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range after {
		if c.Closure.CallID == completeID && len(seen) == 1 {
			t.Fatalf("complete call %s claimed twice across workers and follow-up", completeID)
		}
		if c.Closure.CallID != completeID {
			t.Fatalf("follow-up claimed incomplete call %s", c.Closure.CallID)
		}
	}
	if len(seen) == 0 && len(after) == 0 {
		// Workers may all have scanned only the deferred prefix (all returned
		// 0) under contention; a bounded follow-up with the prefix deferred
		// must still be able to reach the trailing complete row. Advance the
		// real yield window deterministically via the manual clock.
		base := time.Now().UTC()
		store.claimNowFunc = func() time.Time { return base.Add(2 * time.Second) }
		retry, err := store.ClaimCompleteCalls(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(retry) != 1 || retry[0].Closure.CallID != completeID {
			t.Fatalf("follow-up after contention claimed %v, want complete call %s", retry, completeID)
		}
	}
}

// TestPhase19R2ClaimPrefixRestartDurable proves restart behavior: deferrals
// are durable next_claim_at rows (not in-memory cursor), so closing and
// reopening the same file preserves fair progress to the trailing complete
// call after the yield window.
func TestPhase19R2ClaimPrefixRestartDurable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ph19r2-claim-prefix.sqlite")
	store, closeStore := openRefinement82FileBillingStore(t, path, "ph19r2-claim-prefix")
	const prefix = 300
	completeID := phase19R2SeedPrefixWithTrailingComplete(t, store, prefix)

	base := time.Now().UTC()
	current := base
	store.claimNowFunc = func() time.Time { return current }
	first, err := store.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("pre-restart scan claimed %d, want 0", len(first))
	}
	closeStore()

	reopened, closeReopened := openRefinement82FileBillingStore(t, path, "ph19r2-claim-prefix")
	defer closeReopened()
	after := base.Add(2 * time.Second)
	reopened.claimNowFunc = func() time.Time { return after }
	second, err := reopened.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Closure.CallID != completeID {
		t.Fatalf("post-restart scan claimed %v, want complete call %s", second, completeID)
	}
}
