//go:build integration

package billingstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestPostgresProviderAndExposureConcurrentAdmission(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 16)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "phase3-concurrent"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	account := billing.Account{ID: fmt.Sprintf("phase3-concurrent-%d", time.Now().UnixNano()), Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	errs := make(chan error, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			callID, err := billing.NewBillingCallID()
			if err != nil {
				errs <- err
				return
			}
			leg := testIndependentCallLegFor(callID, fmt.Sprintf("b-provider-%d", i))
			sealed, err := leg.Seal()
			if err != nil {
				errs <- err
				return
			}
			_, err = store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
				AccountID: account.ID, CallID: callID, Leg: leg,
				Result: billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 1, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
			})
			if err != nil {
				errs <- fmt.Errorf("provider %d: %w", i, err)
			}
		}()
		go func() {
			defer wg.Done()
			callID, err := billing.NewBillingCallID()
			if err != nil {
				errs <- err
				return
			}
			_, err = store.AdmitExposure(ctx, billing.AdmitExposureInput{
				AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 1, Currency: "USD"},
				PricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
			})
			if err != nil {
				errs <- fmt.Errorf("exposure %d: %w", i, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
