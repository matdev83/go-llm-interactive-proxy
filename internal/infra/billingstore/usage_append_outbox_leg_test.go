package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteUsageAppendOutboxDrainReplaysLeg(t *testing.T) {
	store := newLegacyOutboxTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	leg := testOutboxLeg(t, call.CallID)
	if err := store.EnqueueCallLegUsageAppend(ctx, leg, "legacy terminal fallback"); err != nil {
		t.Fatal(err)
	}
	if err := store.DrainUsageAppendOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCallLegUsage(ctx, sealed.Key)
	if err != nil {
		t.Fatalf("replayed call-leg usage: %v", err)
	}
	if got.CallID != call.CallID || got.BLegID != leg.BLegID {
		t.Fatalf("replayed leg = %+v, want %+v", got, leg)
	}
	if billing.UsageAppendLeg == "" {
		t.Fatal("legacy leg kind must remain defined for migration decoding")
	}
}
