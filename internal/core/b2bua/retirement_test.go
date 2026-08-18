package b2bua

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreRetirementObserverRunsAfterEvictionUnlock(t *testing.T) {
	tick := time.Unix(1_700_000_000, 0).UTC()
	store, err := NewMemoryStore(MemoryStoreOptions{MaxLegs: 1, Now: func() time.Time { return tick }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := store.CreateALeg(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	tick = tick.Add(time.Second)
	called := make(chan struct{})
	store.SetALegRetirementObserver(func(aLegID string) {
		if aLegID != first.ALegID {
			t.Errorf("retired A-leg = %q, want %q", aLegID, first.ALegID)
		}
		// Re-entering the store would deadlock if the callback still ran under
		// the mutation lock.
		store.SetALegRetirementObserver(nil)
		close(called)
	})
	if _, err := store.CreateALeg(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("retirement callback did not complete outside the store lock")
	}
}
