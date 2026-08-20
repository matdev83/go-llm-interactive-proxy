package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteAdmitExposureIsAtomicAndDoesNotMutateMoney(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "exposure-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	input := billing.AdmitExposureInput{AccountID: account.ID, CallID: "bc_call_1", Max: billing.Money{Nano: 60, Currency: "USD"}, PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"}}
	got, err := store.AdmitExposure(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsOpen() || got.Max.Nano != 60 || got.Basis.OpenExposureNano != 0 || got.Basis.SafetyMarginAfterNano != 40 {
		t.Fatalf("exposure = %+v", got)
	}
	unchanged, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.BalanceNano != account.BalanceNano {
		t.Fatalf("exposure mutated money/account state: before=%+v after=%+v", account, unchanged)
	}
	if _, err := store.AdmitExposure(ctx, input); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	conflict := input
	conflict.Max.Nano = 61
	if _, err := store.AdmitExposure(ctx, conflict); !errors.Is(err, billing.ErrExposureConflict) {
		t.Fatalf("conflicting replay = %v, want ErrExposureConflict", err)
	}
}

func TestSQLiteAdmitExposureSerializesConcurrentCalls(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "exposure-concurrent", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	const count = 2
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			_, err := store.AdmitExposure(context.Background(), billing.AdmitExposureInput{
				AccountID: "exposure-concurrent", CallID: "bc_concurrent_" + string(rune('1'+i)), Max: billing.Money{Nano: 60, Currency: "USD"},
				PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	var admitted int
	for err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, billing.ErrExposureInsufficient):
		default:
			t.Fatalf("concurrent admission = %v", err)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted = %d, want exactly 1", admitted)
	}
}
