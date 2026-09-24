package billing

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/stretchr/testify/require"
)

// G1: bare CompletenessPartial without MissingObservations must still keep the
// aggregate partial/incomplete. Partial can represent a failed rule or
// classification without a missing observation ref (valuation contract leaves
// Partial independent; production component_rater marks partial for a failed
// sibling fixed rule without requiring missing refs). Equal known subtotals
// therefore keep exact per-item evidence but never report complete agreement.

func TestPhase172G1BarePartialEqualIsPartialThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 501, "g1-head-local-bare", "g1-obs-local-bare", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 502, "g1-head-provider-bare", "g1-obs-provider-bare", economics.BasisProviderReported)
	localLines := []economics.LineItem{f4LineKnown(t, "local-input", key, "100")}
	providerLines := []economics.LineItem{f4LineKnown(t, "provider-input", key, "100")}
	localOverride := &f4ValuationOverride{Completeness: economics.CompletenessPartial}
	providerOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}

	forward, reverse := f4RunScenarioOrdered(t, local, provider, localLines, localOverride, providerLines, providerOverride)
	for _, envelope := range []f4Envelope{forward, reverse} {
		require.Equal(t, "partial", envelope.Status, "bare partial with equal known quantities must not be matched")
		require.False(t, envelope.Complete, "bare partial must remain incomplete")
		require.Len(t, envelope.Items, 1)
		require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
		require.Equal(t, "100", *envelope.Items[0].ProviderQuantity)
	}
	f4RequireDeterministic(t, forward, reverse)
}

func TestPhase172G1BarePartialOnProviderEqualIsPartialThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 511, "g1-head-local-bare-r", "g1-obs-local-bare-r", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 512, "g1-head-provider-bare-r", "g1-obs-provider-bare-r", economics.BasisProviderReported)
	localLines := []economics.LineItem{f4LineKnown(t, "local-input", key, "100")}
	providerLines := []economics.LineItem{f4LineKnown(t, "provider-input", key, "100")}
	localOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}
	providerOverride := &f4ValuationOverride{Completeness: economics.CompletenessPartial}

	forward, reverse := f4RunScenarioOrdered(t, local, provider, localLines, localOverride, providerLines, providerOverride)
	for _, envelope := range []f4Envelope{forward, reverse} {
		require.Equal(t, "partial", envelope.Status, "bare partial on provider with equal known quantities must not be matched")
		require.False(t, envelope.Complete)
		require.Len(t, envelope.Items, 1)
		require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
		require.Equal(t, "100", *envelope.Items[0].ProviderQuantity)
	}
	f4RequireDeterministic(t, forward, reverse)
}

func TestPhase172G1BarePartialDiscrepancyIsPartialThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 521, "g1-head-local-disc", "g1-obs-local-disc", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 522, "g1-head-provider-disc", "g1-obs-provider-disc", economics.BasisProviderReported)
	localLines := []economics.LineItem{f4LineKnown(t, "local-input", key, "100")}
	providerLines := []economics.LineItem{f4LineKnown(t, "provider-input", key, "110")}
	localOverride := &f4ValuationOverride{Completeness: economics.CompletenessPartial}
	providerOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}

	forward, reverse := f4RunScenarioOrdered(t, local, provider, localLines, localOverride, providerLines, providerOverride)
	for _, envelope := range []f4Envelope{forward, reverse} {
		require.Equal(t, "partial", envelope.Status, "bare partial with a known discrepancy must stay partial, not discrepant-complete")
		require.False(t, envelope.Complete)
		require.Len(t, envelope.Items, 1)
		require.Equal(t, "discrepant", envelope.Items[0].Status, "per-item discrepancy evidence stays exact")
		require.NotNil(t, envelope.Items[0].LocalQuantity)
		require.NotNil(t, envelope.Items[0].ProviderQuantity)
		require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
		require.Equal(t, "110", *envelope.Items[0].ProviderQuantity)
	}
	f4RequireDeterministic(t, forward, reverse)
}

func TestPhase172G1CompleteControlsStayCompleteThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	t.Run("matched", func(t *testing.T) {
		t.Parallel()
		local := f4RatingWork(t, 531, "g1-head-local-ctl", "g1-obs-local-ctl", economics.BasisLocalExpected)
		provider := f4RatingWork(t, 532, "g1-head-provider-ctl", "g1-obs-provider-ctl", economics.BasisProviderReported)
		envelope := f4RunScenario(t, local, provider,
			[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete},
			[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete})
		require.Equal(t, "matched", envelope.Status)
		require.True(t, envelope.Complete)
		require.Len(t, envelope.Items, 1)
		require.Equal(t, "matched", envelope.Items[0].Status)
		require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
		require.Equal(t, "100", *envelope.Items[0].ProviderQuantity)
	})
	t.Run("discrepant", func(t *testing.T) {
		t.Parallel()
		local := f4RatingWork(t, 533, "g1-head-local-disc-ctl", "g1-obs-local-disc-ctl", economics.BasisLocalExpected)
		provider := f4RatingWork(t, 534, "g1-head-provider-disc-ctl", "g1-obs-provider-disc-ctl", economics.BasisProviderReported)
		envelope := f4RunScenario(t, local, provider,
			[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete},
			[]economics.LineItem{f4LineKnown(t, "provider-input", key, "110")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete})
		require.Equal(t, "discrepant", envelope.Status)
		require.True(t, envelope.Complete)
		require.Len(t, envelope.Items, 1)
		require.Equal(t, "discrepant", envelope.Items[0].Status)
	})
}

func TestPhase172G1SevereStatusesRemainIncompleteThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	for _, status := range []economics.Completeness{
		economics.CompletenessUnknown,
		economics.CompletenessConflict,
		economics.CompletenessUnavailable,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			local := f4RatingWork(t, 541, "g1-head-local-sev-"+string(status), "g1-obs-local-sev-"+string(status), economics.BasisLocalExpected)
			provider := f4RatingWork(t, 542, "g1-head-provider-sev-"+string(status), "g1-obs-provider-sev-"+string(status), economics.BasisProviderReported)
			envelope := f4RunScenario(t, local, provider,
				[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
				&f4ValuationOverride{Completeness: status},
				[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
				&f4ValuationOverride{Completeness: economics.CompletenessComplete})
			require.NotEqual(t, "matched", envelope.Status)
			require.False(t, envelope.Complete)
		})
	}
}
