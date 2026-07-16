package leasestore_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
)

// BenchmarkMemoryFiveSlotHundredContenders storms 100 concurrent acquires against 5 slots (16.6).
func BenchmarkMemoryFiveSlotHundredContenders(b *testing.B) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ttl := time.Minute
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: fmt.Sprintf("bench-five-%d", i)})
		var acquired atomic.Int64
		var exceeded atomic.Int64
		done := make(chan struct{}, 100)
		for c := range 100 {
			go func() {
				res, err := store.Acquire(ctx, acquireCmd(fmt.Sprintf("lease-%d", c), fmt.Sprintf("req-%d", c), now, ttl))
				if err != nil {
					b.Errorf("acquire: %v", err)
				} else if res.CapacityExceeded {
					exceeded.Add(1)
				} else {
					acquired.Add(1)
				}
				done <- struct{}{}
			}()
		}
		for range 100 {
			<-done
		}
		if got := acquired.Load(); got != testLimit {
			b.Fatalf("acquired=%d want %d (exceeded=%d)", got, testLimit, exceeded.Load())
		}
	}
}
