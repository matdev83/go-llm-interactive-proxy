package billingstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func mustCluster4Time(t *testing.T, unix int64) time.Time {
	t.Helper()
	return time.Unix(unix, 0).UTC()
}

func mustCluster4Identity(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicRevisionIdentity {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	return identity
}

func ratingInputForCluster4(t *testing.T, observation metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
	return economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
}

func cluster4ReconWorkFor(t *testing.T, rating billing.EconomicRevisionWork) billing.EconomicRevisionWork {
	t.Helper()
	normalized, err := rating.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	observation := phase4EconomicsObservation("test", "c4-recon-evidence", 46)
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: "c4-recon-head", Subject: observation.Subject,
		EvidenceRevision: 46, Input: ratingInputForCluster4(t, observation),
		Dependencies: []billing.EconomicJobDependency{dependency},
		CreatedAt:    mustCluster4Time(t, 1_700_192_500),
	}
	normalizedRecon, err := work.Normalize()
	require.NoError(t, err)
	return normalizedRecon
}

// cluster4RealReconciliation derives a reconciliation envelope from the exact
// persisted dependency valuations of one reconciliation work. Every identity
// and hash is resolved against durable state and fails closed on mismatch; no
// caller-authored placeholder status is accepted.
func cluster4RealReconciliation(t *testing.T, ctx context.Context, store *DurableStore, reconWork billing.EconomicRevisionWork) billing.EconomicReconciliation {
	t.Helper()
	normalized, err := reconWork.Normalize()
	require.NoError(t, err)
	require.NotEmpty(t, normalized.Dependencies, "reconciliation work must declare rating dependencies")
	identity, err := normalized.Identity()
	require.NoError(t, err)

	var linked economics.Valuation
	for _, dependency := range normalized.Dependencies {
		valuation, err := store.LoadEconomicRevisionDependencyOutput(ctx, dependency)
		require.NoError(t, err, "dependency output must resolve from persisted valuations")
		outputIdentity, err := dependency.OutputIdentity()
		require.NoError(t, err)
		require.Equal(t, outputIdentity.ValuationKey(), valuation.ID)
		require.Equal(t, economics.ValuationVersionV2, valuation.Version)
		observationHash, err := economics.CanonicalInputSetHash(valuation.Basis, valuation.InputObservations)
		require.NoError(t, err)
		require.Equal(t, dependency.InputSetHash, observationHash)
		fullHash, err := economics.CanonicalValuationInputSetHash(valuation.Basis, valuation.InputObservations, valuation.AllocationCoverageRefs)
		require.NoError(t, err)
		expectedFull := dependency.InputSetHash
		if dependency.DerivationHash != "" {
			expectedFull = dependency.DerivationHash
		}
		require.Equal(t, expectedFull, valuation.InputSetHash)
		require.Equal(t, expectedFull, fullHash)
		linked = valuation
	}
	var totalsNano int64
	for _, total := range linked.Totals {
		if total.RoundedAmount.Present {
			totalsNano += total.RoundedAmount.NanoUnits
		}
	}
	payload, err := json.Marshal(map[string]any{
		"valuation_id":        linked.ID,
		"input_set_hash":      normalized.InputSetHash,
		"provider_input_hash": linked.InputSetHash,
		"totals_nano":         totalsNano,
		"currency":            "USD",
		"dependency_kind":     string(normalized.Dependencies[0].Kind),
	})
	require.NoError(t, err)
	return billing.EconomicReconciliation{
		ID:                identity.ReconciliationKey(),
		Version:           identity.EvidenceRevision,
		Subject:           normalized.Subject,
		Scope:             normalized.Input.Scope,
		Basis:             normalized.Input.Basis,
		InputSetHash:      normalized.InputSetHash,
		ProviderInputHash: linked.InputSetHash,
		ResultJSON:        payload,
		CreatedAt:         normalized.CreatedAt,
	}
}
