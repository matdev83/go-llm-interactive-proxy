package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteUsageAppendOutboxRecoversFailedLegAppend(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	leg := testOutboxLeg(t, call.CallID)
	appendErr := errors.New("temporary leg database I/O")
	appender, err := billing.NewRetryingCallLegUsageAppender(
		billing.CallLegUsageAppenderFunc(func(context.Context, billing.CallLegUsageRecord) error { return appendErr }),
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := appender.AppendCallLegUsage(ctx, leg); !errors.Is(err, appendErr) {
		t.Fatalf("initial leg append error = %v, want %v", err, appendErr)
	}
	worker, err := billing.NewUsageAppendWorker(store, store, store, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
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
}
