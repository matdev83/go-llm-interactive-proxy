package billing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 second-pass Finding 3: payer completeness must be monotonic across
// the full scope. `projectPayers` sets HasUnallocated for an unknown/unallocated
// payer but the customer branch reset it to false, so a later resolved customer
// observation or retail valuation erased an earlier unresolved payer even though
// the distinct payer set retained it. Unresolved presence is now monotonic and
// the payer projection is order-independent for the same semantic facts.

var (
	payerTestOperator    = metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}
	payerTestCustomer    = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}
	payerTestUnknown     = metering.PaymentParty{Kind: metering.PaymentPartyUnknown}
	payerTestUnallocated = metering.PaymentParty{Kind: metering.PaymentPartyUnallocated}
)

// detailTestApplyValuationPayers sets every valuation payer to the supplied
// basis override, defaulting to the known operator payer so an absent payer does
// not itself force HasUnallocated and mask the order-dependent reset.
func detailTestApplyValuationPayers(t *testing.T, in EconomicDetailInput, byBasis map[economics.ValuationBasis]metering.PaymentParty) EconomicDetailInput {
	t.Helper()
	valuations := make([]economics.Valuation, 0, len(in.Valuations))
	for _, valuation := range in.Valuations {
		if payer, ok := byBasis[valuation.Basis]; ok {
			valuation.Payer = payer
		} else {
			valuation.Payer = payerTestOperator
		}
		require.NoError(t, valuation.Validate())
		valuations = append(valuations, valuation)
	}
	in.Valuations = valuations
	return in
}

// detailTestPayerChargeObservation builds one provider-side component charge
// observation carrying an explicit payer, so unresolved and BYOK payer presence
// can be exercised through the public assembly path in a controlled mark order.
func detailTestPayerChargeObservation(t *testing.T, id, streamID string, sequence uint64, subject metering.SubjectRef, payer metering.PaymentParty) metering.Observation {
	t.Helper()
	charge := metering.ReportedCharge{
		ChargeItemID: id + "-charge",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Amount:   func() *metering.Decimal { v := detailTestDecimal(t, "0.42"); return &v }(),
		Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: payer,
	}
	return detailTestObservation(t, id, metering.OriginProvider, streamID, sequence, subject, nil, []metering.ReportedCharge{charge})
}

func requireDistinctPayer(t *testing.T, payers EconomicDetailPayers, payer metering.PaymentParty) {
	t.Helper()
	require.Contains(t, payers.Distinct, payer, "distinct payer set must retain every observed payer")
}

// TestAssembleEconomicDetailUnresolvedPayerMonotonic covers unknown and
// unallocated payers meeting resolved customer/BYOK payers in both mark orders:
// unresolved-before-resolved (previously erased) and resolved-before-unresolved.
func TestAssembleEconomicDetailUnresolvedPayerMonotonic(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-payer-monotonic")

	t.Run("unknown valuation then resolved retail customer valuation", func(t *testing.T) {
		t.Parallel()
		in := detailTestApplyValuationPayers(t, detailTestCompleteInput(t), map[economics.ValuationBasis]metering.PaymentParty{
			economics.BasisProviderReported: payerTestUnknown,
			economics.BasisCustomerPolicy:   payerTestCustomer,
		})
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Payers.HasUnallocated, "a resolved retail payer must not erase an earlier unresolved payer")
		require.True(t, got.Payers.HasOperatorPayable)
		require.False(t, got.Payers.HasCustomerBYOK, "a retail customer-policy payer is not BYOK")
		requireDistinctPayer(t, got.Payers, payerTestUnknown)
		requireDistinctPayer(t, got.Payers, payerTestCustomer)
	})

	t.Run("unknown valuation then resolved customer charge observation", func(t *testing.T) {
		t.Parallel()
		in := detailTestApplyValuationPayers(t, detailTestCompleteInput(t), map[economics.ValuationBasis]metering.PaymentParty{
			economics.BasisProviderReported: payerTestUnknown,
		})
		in.Observations = append(in.Observations,
			detailTestPayerChargeObservation(t, "obs-payer-customer", "stream-payer-zz", 1, subject, payerTestCustomer))
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Payers.HasUnallocated, "a later BYOK customer charge must not erase an earlier unresolved payer")
		require.True(t, got.Payers.HasCustomerBYOK)
		requireDistinctPayer(t, got.Payers, payerTestUnknown)
		requireDistinctPayer(t, got.Payers, payerTestCustomer)
	})

	t.Run("resolved retail customer valuation then unknown observation", func(t *testing.T) {
		t.Parallel()
		in := detailTestApplyValuationPayers(t, detailTestCompleteInput(t), map[economics.ValuationBasis]metering.PaymentParty{
			economics.BasisCustomerPolicy: payerTestCustomer,
		})
		in.Observations = append(in.Observations,
			detailTestPayerChargeObservation(t, "obs-payer-unknown", "stream-payer-zz", 1, subject, payerTestUnknown))
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Payers.HasUnallocated)
		require.False(t, got.Payers.HasCustomerBYOK, "a retail customer-policy payer is not BYOK")
		requireDistinctPayer(t, got.Payers, payerTestUnknown)
		requireDistinctPayer(t, got.Payers, payerTestCustomer)
	})

	t.Run("unknown retail valuation then resolved customer charge observation", func(t *testing.T) {
		t.Parallel()
		in := detailTestApplyValuationPayers(t, detailTestCompleteInput(t), map[economics.ValuationBasis]metering.PaymentParty{
			economics.BasisCustomerPolicy: payerTestUnknown,
		})
		in.Observations = append(in.Observations,
			detailTestPayerChargeObservation(t, "obs-payer-customer", "stream-payer-zz", 1, subject, payerTestCustomer))
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Payers.HasUnallocated, "a later BYOK customer charge must not erase an unresolved retail payer")
		require.True(t, got.Payers.HasCustomerBYOK)
		requireDistinctPayer(t, got.Payers, payerTestUnknown)
		requireDistinctPayer(t, got.Payers, payerTestCustomer)
	})

	t.Run("unallocated valuation then resolved retail customer valuation", func(t *testing.T) {
		t.Parallel()
		in := detailTestApplyValuationPayers(t, detailTestCompleteInput(t), map[economics.ValuationBasis]metering.PaymentParty{
			economics.BasisProviderReported: payerTestUnallocated,
			economics.BasisCustomerPolicy:   payerTestCustomer,
		})
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Payers.HasUnallocated, "an unallocated payer must stay visible after a later resolved retail payer")
		require.False(t, got.Payers.HasCustomerBYOK, "a retail customer-policy payer is not BYOK")
		requireDistinctPayer(t, got.Payers, payerTestUnallocated)
		requireDistinctPayer(t, got.Payers, payerTestCustomer)
	})
}

// TestAssembleEconomicDetailPayerProjectionOrderIndependent builds the same two
// observation charges (one unresolved, one resolved BYOK customer) and only
// changes their canonical stream order. The full payer projection must be
// identical, proving HasUnallocated no longer depends on which payer is marked
// last.
func TestAssembleEconomicDetailPayerProjectionOrderIndependent(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-payer-order")

	build := func(t *testing.T, unknownStream, customerStream string) EconomicDetail {
		t.Helper()
		in := detailTestApplyValuationPayers(t, detailTestCompleteInput(t), nil)
		in.Observations = append(in.Observations,
			detailTestPayerChargeObservation(t, "obs-payer-unknown", unknownStream, 1, subject, payerTestUnknown),
			detailTestPayerChargeObservation(t, "obs-payer-customer", customerStream, 1, subject, payerTestCustomer),
		)
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		return got
	}

	unresolvedFirst := build(t, "stream-payer-a", "stream-payer-b")
	resolvedFirst := build(t, "stream-payer-z", "stream-payer-a")

	require.True(t, unresolvedFirst.Payers.HasUnallocated)
	require.True(t, resolvedFirst.Payers.HasUnallocated)
	require.True(t, unresolvedFirst.Payers.HasCustomerBYOK)
	require.True(t, resolvedFirst.Payers.HasCustomerBYOK)
	require.Equal(t, unresolvedFirst.Payers, resolvedFirst.Payers,
		"payer projection must be order-independent for the same payer facts")
}
