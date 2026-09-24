package billingstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func refinement43ProviderCostFenceCount(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID) int {
	t.Helper()
	var count int
	err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_provider_cost_posting_fences WHERE store_id = ? AND account_id = ? AND call_id = ?`, store.storeID, accountID, callID.String()).Scan(context.Background(), &count)
	require.NoError(t, err)
	return count
}

func TestRefinement43ProviderCostRevisionRejectsUntrustedAuthorityBeforeMutation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*billing.ProviderCostRevisionInput)
	}{
		{
			name: "local measurement origin",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				observation := &input.Evidence.Observations[0]
				observation.Origin = metering.OriginLocal
				observation.Acquisition = metering.AcquisitionLocalTransport
			},
		},
		{
			name: "estimated authority",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Authority = metering.AuthorityEstimatedClaim
			},
		},
		{
			name: "customer perspective",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Perspective = metering.PerspectiveCustomer
				input.Evidence.Observations[0].Perspective = metering.PerspectiveCustomer
			},
		},
		{
			name: "frontend boundary",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Boundary = metering.BoundaryFrontendIngress
			},
		},
		{
			name: "local basis",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Basis = economics.BasisLocalExpected
			},
		},
		{
			name: "customer payer",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				payer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
				input.Evidence.Payer = payer
				input.Evidence.Observations[0].Charges[0].Payer = payer
			},
		},
		{
			name: "unknown payer",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Payer = metering.PaymentParty{Kind: metering.PaymentPartyUnknown}
			},
		},
		{
			name: "invented payer",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Charges[0].Payer.Kind = metering.PaymentPartyKind("invented-payer")
			},
		},
		{
			name: "invented authority",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Authority = "invented-authority"
			},
		},
		{
			name: "boolean without provenance",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Evidence = economics.PostUsageRatingInput{}
			},
		},
		{
			name: "payable without authority assertion",
			mutate: func(input *billing.ProviderCostRevisionInput) {
				input.Authoritative = false
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			account := billing.Account{ID: fmt.Sprintf("refinement43-authority-%d", i), Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
			require.NoError(t, store.CreateAccount(ctx, account))
			input := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "authority-head", 1, 10, true)
			tc.mutate(&input)

			_, err := store.ApplyProviderCostRevision(ctx, input)
			var authorityErr *billing.ProviderCostRevisionAuthorityError
			require.ErrorAs(t, err, &authorityErr)
			require.ErrorIs(t, err, billing.ErrProviderCostRevisionAuthority)
			require.Empty(t, refinement43ProviderJournals(t, store, account.ID))
			require.Equal(t, 0, refinement43ProviderCostFenceCount(t, store, account.ID, input.CallID))
			_, err = store.GetProviderCostHead(ctx, account.ID, input.CallID, input.HeadKey)
			require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)
		})
	}
}
