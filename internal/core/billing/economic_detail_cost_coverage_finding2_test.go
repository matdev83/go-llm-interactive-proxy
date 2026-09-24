package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 fifth-pass Finding 2 core contract: a selected frozen valuation is
// resolved by its exact immutable identity (valuation id plus canonical
// input-set hash), and every selected line source reference is verified against
// the original persisted observation payload before it may seed or transitively
// prove coverage. A stale, mutated, wrong or absent frozen identity fails
// closed instead of certifying a complete margin.

// costCoverageRefreezeLineSources rebuilds every valuation line source
// reference that names the given observation from the observation's current
// in-memory payload. Positive fixtures must construct and freeze refs only
// after the final graph is built; this helper enforces that ordering explicitly
// instead of relying on a ref captured before a fixture mutation.
func costCoverageRefreezeLineSources(t *testing.T, in *EconomicDetailInput, observationID string) {
	t.Helper()
	var current metering.Observation
	found := false
	for i := range in.Observations {
		if in.Observations[i].ID == observationID {
			current = in.Observations[i]
			found = true
			break
		}
	}
	require.True(t, found, "observation %s must exist to re-freeze its line refs", observationID)
	ref, err := current.Ref(current.Subject.StoreID)
	require.NoError(t, err)
	for i := range in.Valuations {
		for j := range in.Valuations[i].Lines {
			refs := in.Valuations[i].Lines[j].SourceObservationRefs
			for k := range refs {
				if refs[k].ObservationID != observationID {
					continue
				}
				refs[k] = ref
			}
		}
		require.NoError(t, in.Valuations[i].Validate())
	}
}

// costCoverageMutatedObservationInput is the exact review scenario: the selected
// valuation line froze the provider money observation's reference before the
// fixture added a new inclusive sibling edge to that observation. The frozen
// payload identity no longer describes the retained observation, so coverage
// must stay unresolved.
func costCoverageMutatedObservationInput(t *testing.T) EconomicDetailInput {
	t.Helper()
	storeID, _, aLegID, billingCallID := detailTestScope()
	selected := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-detail-1"}

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := costCoverageChargeObservation(t, "obs-cost-finding2-inclusive", "stream-cost-finding2-inclusive", 1, selected, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		require.NotEmpty(t, in.Observations[i].Charges)
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, extra)}
	}
	return in
}

// TestAssembleEconomicDetailCostCoverageRejectsMutatedObservationPayload proves
// a selected line source reference that no longer matches the original
// persisted observation payload cannot seed or transitively prove coverage.
func TestAssembleEconomicDetailCostCoverageRejectsMutatedObservationPayload(t *testing.T) {
	t.Parallel()

	in := costCoverageMutatedObservationInput(t)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete, "a mutated observation payload cannot be proven included by a stale frozen ref")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.Nil(t, got.Margin.Amount)
}

// TestAssembleEconomicDetailCostCoverageRefrozenObservationPayloadComplete is the
// positive control: once the fixture re-freezes the selected line source
// reference against the final graph, the exact immutable identity proves
// coverage again.
func TestAssembleEconomicDetailCostCoverageRefrozenObservationPayloadComplete(t *testing.T) {
	t.Parallel()

	in := costCoverageMutatedObservationInput(t)
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}

// TestAssembleEconomicDetailCostCoverageRejectsWrongPayloadHash proves a
// syntactically valid but wrong payload hash on a selected line source
// reference proves nothing.
func TestAssembleEconomicDetailCostCoverageRejectsWrongPayloadHash(t *testing.T) {
	t.Parallel()

	in := detailTestCompleteInput(t)
	in.Selection = nil
	tampered := false
	for i := range in.Valuations {
		if in.Valuations[i].ID != "valuation-p" {
			continue
		}
		for j := range in.Valuations[i].Lines {
			for k := range in.Valuations[i].Lines[j].SourceObservationRefs {
				in.Valuations[i].Lines[j].SourceObservationRefs[k].PayloadHash = strings.Repeat("b", 64)
				tampered = true
			}
		}
	}
	require.True(t, tampered, "the selected provider-reported line must carry a source observation reference")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete, "a wrong payload hash must fail closed")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailCostCoverageRejectsMissingInputSetHash proves a
// frozen valuation whose input-set identity is absent cannot prove a nonempty
// selected reference identity: equality is required, not optional.
func TestAssembleEconomicDetailCostCoverageRejectsMissingInputSetHash(t *testing.T) {
	t.Parallel()

	in := detailTestCompleteInput(t)
	in.Selection = nil
	found := false
	for i := range in.Valuations {
		if in.Valuations[i].ID != "valuation-p" {
			continue
		}
		in.Valuations[i].InputSetHash = ""
		found = true
	}
	require.True(t, found)
	require.NotEmpty(t, in.Heads[0].Selected.Ref.InputSetHash, "the frozen selected ref still requires its input-set identity")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete, "an absent frozen input-set hash must fail closed")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailCostCoverageResolvesExactSelectedRevision proves a
// selected head resolves its exact frozen valuation revision even when the
// latest-per-stream display set carries a newer same-stream revision. The
// frozen revision lives in SelectedValuations, mirroring the durable reader's
// bounded exact-identity load, while Valuations keeps latest-per-stream display.
func TestAssembleEconomicDetailCostCoverageResolvesExactSelectedRevision(t *testing.T) {
	t.Parallel()
	storeID, _, _, _ := detailTestScope()

	in := detailTestCompleteInput(t)
	in.Selection = nil
	var frozen economics.Valuation
	found := false
	for i := range in.Valuations {
		if in.Valuations[i].ID != "valuation-p" {
			continue
		}
		frozen = in.Valuations[i]
		// Replace the display slot with a newer revision of the same logical
		// stream (same subject/perspective/basis) under a different immutable id.
		in.Valuations[i] = detailTestValuation(t, "valuation-p-newer", economics.BasisProviderReported, in.Valuations[i].Subject, storeID, detailTestCurrencyTotal(t, "USD", "9.99"))
		found = true
	}
	require.True(t, found)
	require.Equal(t, "valuation-p", in.Heads[0].Selected.Ref.ValuationID)
	require.Nil(t, costCoverageLookupValuation(in.Valuations, "valuation-p"), "the frozen revision must be absent from the latest-per-stream display set")

	in.SelectedValuations = []economics.Valuation{frozen}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete, "the exact frozen selected revision must be resolved, not the latest display revision")
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}

func costCoverageLookupValuation(valuations []economics.Valuation, id string) *economics.Valuation {
	for i := range valuations {
		if valuations[i].ID == id {
			return &valuations[i]
		}
	}
	return nil
}
