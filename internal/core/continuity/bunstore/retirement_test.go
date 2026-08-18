package bunstore

import (
	"context"
	"sync"
	"testing"
)

func TestRetirementObserverBindingIsConcurrentSafe(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := store.CreateALeg(ctx, "shared"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.SetALegRetirementObserver(func(string) {})
		}()
		go func() {
			defer wg.Done()
			_, _ = store.CreateALeg(ctx, "shared")
		}()
	}
	wg.Wait()
}
