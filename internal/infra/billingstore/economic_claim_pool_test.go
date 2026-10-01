package billingstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestEconomicClaimUsesTransactionConnection(t *testing.T) {
	for _, size := range []int{1, 2} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			storeID := fmt.Sprintf("claim-pool-%d", size)
			store := openRefinement52BillingStore(t, storeID)
			store.db.SetMaxOpenConns(size)
			assertEconomicClaimUsesTransactionConnection(t, store)
		})
	}
}

func assertEconomicClaimUsesTransactionConnection(t *testing.T, store *DurableStore) {
	t.Helper()
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	initial := refinement52ProviderObservation(store.storeID, callID, 1, "claim-initial", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	work := refinement82ProviderWork(t, context.Background(), builder, initial)
	if err := store.AppendEconomicRevisionWork(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, acquired, err := store.ClaimEconomicRevisionWork(ctx, work, "claim-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("claim must succeed without taking a second pool connection: acquired=%t err=%v", acquired, err)
	}
}
