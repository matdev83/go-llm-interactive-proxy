package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func refinement43ValidProviderCostRevisionInput(t *testing.T) ProviderCostRevisionInput {
	t.Helper()
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	input, err := BuildProviderCostRevisionInput(work, economics.Valuation{ID: "refinement43-authority-valuation"})
	require.NoError(t, err)
	return input
}

func TestRefinement43ProviderCostRevisionRequiresTrustedProviderEvidence(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*ProviderCostRevisionInput)
		wantField string
	}{
		{
			name: "local origin",
			mutate: func(input *ProviderCostRevisionInput) {
				observation := &input.Evidence.Observations[0]
				observation.Origin = metering.OriginLocal
				observation.Acquisition = metering.AcquisitionLocalTransport
			},
			wantField: "origin",
		},
		{
			name: "estimated authority",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Authority = metering.AuthorityEstimatedClaim
			},
			wantField: "authority",
		},
		{
			name: "unavailable authority on payable cost",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Authority = metering.AuthorityUnavailableClaim
			},
			wantField: "authority",
		},
		{
			name: "advisory perspective",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Perspective = metering.PerspectiveNone
				input.Evidence.Observations[0].Perspective = metering.PerspectiveNone
			},
			wantField: "perspective",
		},
		{
			name: "customer plane",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Perspective = metering.PerspectiveCustomer
				input.Evidence.Observations[0].Perspective = metering.PerspectiveCustomer
			},
			wantField: "perspective",
		},
		{
			name: "frontend boundary",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Boundary = metering.BoundaryFrontendIngress
			},
			wantField: "boundary",
		},
		{
			name: "local valuation basis",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Basis = economics.BasisLocalExpected
			},
			wantField: "basis",
		},
		{
			name: "customer payer",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
				input.Evidence.Observations[0].Charges[0].Payer = input.Evidence.Payer
			},
			wantField: "payer",
		},
		{
			name: "unknown payer",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Payer = metering.PaymentParty{Kind: metering.PaymentPartyUnknown}
			},
			wantField: "payer",
		},
		{
			name: "invented payer",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Charges[0].Payer.Kind = metering.PaymentPartyKind("invented-payer")
			},
			wantField: "payer",
		},
		{
			name: "invented authority",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence.Observations[0].Authority = "invented-authority"
			},
			wantField: "authority",
		},
		{
			name: "boolean without provenance",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Evidence = economics.PostUsageRatingInput{}
				input.Authoritative = true
			},
			wantField: "evidence",
		},
		{
			name: "payable without authority assertion",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Authoritative = false
			},
			wantField: "authoritative",
		},
		{
			name: "selected cost not supported by evidence",
			mutate: func(input *ProviderCostRevisionInput) {
				input.Cost.KnownSubtotalByCurrency["USD"] = Money{Nano: 11_000_000_000, Currency: "USD"}
				input.Cost.KnownSubtotal = Money{Nano: 11_000_000_000, Currency: "USD"}
			},
			wantField: "cost",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := refinement43ValidProviderCostRevisionInput(t)
			tc.mutate(&input)

			_, err := input.Normalize()
			var authorityErr *ProviderCostRevisionAuthorityError
			require.ErrorAs(t, err, &authorityErr)
			require.Equal(t, tc.wantField, authorityErr.Field)
			require.ErrorIs(t, err, ErrProviderCostRevisionAuthority)
		})
	}
}

func TestRefinement43ProviderCostRevisionAcceptsProviderReportedOperatorEvidence(t *testing.T) {
	input := refinement43ValidProviderCostRevisionInput(t)

	normalized, err := input.Normalize()
	require.NoError(t, err)
	require.True(t, normalized.Authoritative)
	require.True(t, normalized.Cost.Payable)
	require.Equal(t, economics.BasisProviderReported, normalized.Evidence.Basis)
	require.Equal(t, metering.PerspectiveOperator, normalized.Evidence.Perspective)
	require.Equal(t, metering.PaymentPartyOperator, normalized.Evidence.Payer.Kind)
	require.Equal(t, metering.AuthorityObservedClaim, normalized.Evidence.Observations[0].Authority)
}

func TestRefinement43ProviderCostRevisionAcceptsProviderChargeEvidence(t *testing.T) {
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	chargeSubject := work.Subject
	chargeSubject.Kind = metering.SubjectProviderCharge
	chargeSubject.ProviderAccountKey = "provider-account"
	chargeSubject.ProviderChargeID = "provider-charge-43"
	work.Subject = chargeSubject
	work.Input.Subject = chargeSubject
	observation := &work.Input.Observations[0]
	observation.Subject = chargeSubject
	observation.Correlation.ProviderAccountKey = chargeSubject.ProviderAccountKey
	observation.Correlation.ProviderChargeID = chargeSubject.ProviderChargeID

	input, err := BuildProviderCostRevisionInput(work, economics.Valuation{ID: "refinement43-provider-charge-valuation"})
	require.NoError(t, err)
	require.Equal(t, metering.SubjectProviderCharge, input.Subject.Kind)
	require.True(t, input.Cost.Payable)
	require.True(t, input.Authoritative)
}

func TestRefinement43ProviderCostBuilderRejectsUntrustedWorkEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*EconomicRevisionWork)
		field  string
	}{
		{
			name: "local origin",
			mutate: func(work *EconomicRevisionWork) {
				observation := &work.Input.Observations[0]
				observation.Origin = metering.OriginLocal
				observation.Acquisition = metering.AcquisitionLocalTransport
			},
			field: "origin",
		},
		{
			name: "estimated authority",
			mutate: func(work *EconomicRevisionWork) {
				work.Input.Observations[0].Authority = metering.AuthorityEstimatedClaim
			},
			field: "authority",
		},
		{
			name: "customer plane",
			mutate: func(work *EconomicRevisionWork) {
				work.Input.Perspective = metering.PerspectiveCustomer
			},
			field: "perspective",
		},
		{
			name: "local basis",
			mutate: func(work *EconomicRevisionWork) {
				work.Input.Basis = economics.BasisProviderQuantityLocal
			},
			field: "basis",
		},
		{
			name: "customer payer assertion",
			mutate: func(work *EconomicRevisionWork) {
				work.Input.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
			},
			field: "payer",
		},
		{
			name: "unknown payer assertion",
			mutate: func(work *EconomicRevisionWork) {
				work.Input.Payer = metering.PaymentParty{Kind: metering.PaymentPartyUnknown}
			},
			field: "payer",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
			tc.mutate(&work)

			_, err := BuildProviderCostRevisionInput(work, economics.Valuation{ID: "refinement43-builder-authority"})
			var authorityErr *ProviderCostRevisionAuthorityError
			require.ErrorAs(t, err, &authorityErr)
			require.Equal(t, tc.field, authorityErr.Field)
		})
	}
}

func TestRefinement43ProviderCostRevisionPreservesPartialCoverageWithoutAuthority(t *testing.T) {
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	ref, err := work.Input.Observations[0].Ref(work.Subject.StoreID)
	require.NoError(t, err)
	work.Input.Observations = nil
	work.Input.ObservationRefs = []metering.ObservationRef{ref}

	input, err := BuildProviderCostRevisionInput(work, economics.Valuation{ID: "refinement43-partial-authority"})
	require.NoError(t, err)
	require.Equal(t, CostCompletenessPartial, input.Cost.Completeness)
	require.False(t, input.Cost.Payable)
	require.False(t, input.Authoritative)
	require.Empty(t, input.Evidence.Observations)
	require.Len(t, input.Evidence.ObservationRefs, 1)

	normalized, err := input.Normalize()
	require.NoError(t, err)
	require.False(t, normalized.Cost.Payable)
}

func TestRefinement43ProviderCostBuilderPreservesUnavailableProviderCoverage(t *testing.T) {
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	work.Input.Observations[0].Authority = metering.AuthorityUnavailableClaim

	input, err := BuildProviderCostRevisionInput(work, economics.Valuation{ID: "refinement43-unavailable-provider"})
	require.NoError(t, err)
	require.Equal(t, CostCompletenessPartial, input.Cost.Completeness)
	require.False(t, input.Cost.Payable)
	require.False(t, input.Authoritative)
	require.Len(t, input.Evidence.Observations, 1)
	require.Equal(t, metering.AuthorityUnavailableClaim, input.Evidence.Observations[0].Authority)
}

func TestRefinement43ProviderCostBuilderPreservesUnknownPayerCoverage(t *testing.T) {
	work := refinement43Work(t, 1, "10", metering.PaymentPartyUnknown)

	input, err := BuildProviderCostRevisionInput(work, economics.Valuation{ID: "refinement43-unknown-payer"})
	require.NoError(t, err)
	require.Equal(t, CostCompletenessPartial, input.Cost.Completeness)
	require.False(t, input.Cost.Payable)
	require.False(t, input.Authoritative)
	require.Equal(t, metering.PaymentPartyUnknown, input.Evidence.Observations[0].Charges[0].Payer.Kind)
}

func TestRefinement43ProviderCostRevisionAuthorityErrorIsTyped(t *testing.T) {
	input := refinement43ValidProviderCostRevisionInput(t)
	input.Evidence.Observations[0].Authority = metering.AuthorityEstimatedClaim

	_, err := input.Normalize()
	var authorityErr *ProviderCostRevisionAuthorityError
	require.True(t, errors.As(err, &authorityErr))
	require.Equal(t, "authority", authorityErr.Field)
}
