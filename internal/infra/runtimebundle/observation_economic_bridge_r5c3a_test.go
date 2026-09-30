package runtimebundle

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// R5-C3a preterminal incremental revision-completeness seam.
//
// The R5-C2b runtime proof shows the crash leaves exactly the accepted prefix in
// the file-backed journal plus one pending outbox entry per accepted
// observation, and nothing durable records that the prefix is known-truncated.
// This test takes that durable state through a real close/reopen and then runs
// the *stock* observation economic relay with the stock CustomerInput and the
// real provider and customer economic revision workers (the same worker type
// the process owner starts).
//
// Parent requirement 10.2 makes each active B-leg durable, reducible
// observation a valid revision input, so the persisted prefix is a real,
// rateable input set even though the call never reached a terminal append.
// Requirement 10.5 degraded completeness is conditional on an actual
// evidence-persistence failure; a healthy preterminal prefix with no
// persistence failure must not be forced into a false partial status merely
// because a terminal append is absent. Complete-call/customer settlement stays
// fenced here because no terminal B-leg and no sealed call exist, so the
// incremental provider valuation and the provisional customer-policy
// (incremental B-leg) valuation are complete for exactly the persisted input
// set they were built over, while no final customer monetary posting occurs.
func TestR5C3aPreterminalIncrementalRevisionValuationsCompleteForPersistedInputSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		storeID     = "r5c3a-bridge-store"
		prefixCount = 8
	)

	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)

	dir := t.TempDir()
	journalPath := filepath.Join(dir, "metering.sqlite")
	billingPath := filepath.Join(dir, "billing.sqlite")

	journal, journalSQL := r5c2bOpenJournal(t, journalPath, storeID)
	billingStore, billingSQL := r5c2bOpenBilling(t, billingPath, storeID)

	// Accepted prefix: durable observations and, in one transaction, their
	// economic outbox entries. This is exactly what the runtime checkpoint flush
	// writes before the crash.
	prefix := r5c2bBridgeObservations(storeID, callID, prefixCount)
	require.Len(t, prefix, prefixCount)
	sink := journalstore.NewObservationSinkWithOutbox(journal)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, prefix))

	// Crash before any terminal append: discard the process-owned runtime and
	// close both durable files.
	require.NoError(t, journal.Close())
	require.NoError(t, journalSQL.Close())
	require.NoError(t, billingStore.Close())
	require.NoError(t, billingSQL.Close())

	// New process composition: reopen the same files.
	journal2, journalSQL2 := r5c2bOpenJournal(t, journalPath, storeID)
	defer func() { _ = journal2.Close(); _ = journalSQL2.Close() }()
	billing2, billingSQL2 := r5c2bOpenBilling(t, billingPath, storeID)
	defer func() { _ = billing2.Close(); _ = billingSQL2.Close() }()

	// The stock observation-to-work classifier, including the customer policy
	// plane the R5-C2b test omitted.
	tariff := r5c3aCustomerTariff(t)
	policy := r5c3aChargePolicy()
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{
		CustomerInput: func(_ context.Context, subject metering.SubjectRef, observations []metering.Observation) (economics.PostUsageRatingInput, error) {
			return billing.BuildCustomerPolicyObservationInput(subject, observations, policy.Clone(), tariff.Clone())
		},
	})
	require.NoError(t, err)
	relay := newObservationEconomicRelay(journal2, billing2, builder)
	require.NoError(t, relay.ProcessOnce(ctx))

	pending, err := journal2.ListPendingObservationOutbox(ctx, 500)
	require.NoError(t, err)
	require.Empty(t, pending, "relay must acknowledge the prefix outbox")

	providerWork, err := billing2.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Len(t, providerWork, 1, "one provider revision work item for the durable prefix")
	customerWork, err := billing2.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 10)
	require.NoError(t, err)
	require.Len(t, customerWork, 1, "stock CustomerInput must enqueue the customer quantity plane for the provider-native prefix")
	require.Len(t, providerWork[0].Input.Observations, prefixCount)
	require.Len(t, customerWork[0].Input.Observations, prefixCount)

	// The real provider and customer economic revision workers.
	// NewEconomicRevisionWorker is the pure test constructor: it wires no
	// provider-cost/posting port and postProviderCost is a no-op without one, so
	// this composition performs valuation persistence only and never posts final
	// money. The fenced-effect assertions below therefore describe this pure
	// composition; they are not a claim about a fully wired production host.
	providerWorker, err := billing.NewEconomicRevisionWorker(billing2, billing2, r5c3aRater{tariff: tariff}, billing.EconomicQueueProvider, 16)
	require.NoError(t, err)
	require.NoError(t, providerWorker.ProcessOnce(ctx))
	customerWorker, err := billing.NewEconomicRevisionWorker(billing2, billing2, r5c3aRater{tariff: tariff}, billing.EconomicQueueCustomer, 16)
	require.NoError(t, err)
	require.NoError(t, customerWorker.ProcessOnce(ctx))

	providerValuation := r5c3aValuation(t, billing2, billing.EconomicQueueProvider, providerWork[0].HeadKey)
	customerValuation := r5c3aValuation(t, billing2, billing.EconomicQueueCustomer, customerWork[0].HeadKey)

	// Complete-call customer settlement stays fenced: the crash produced no
	// terminal B-leg and no sealed call, so there is no call/leg usage for a
	// final call-level posting to claim. This is a terminal-ownership boundary,
	// not an incremental revision-completeness failure.
	callUsage, err := billing2.ListCallUsage(ctx, "")
	require.NoError(t, err)
	legs, err := billing2.ListCallLegUsage(ctx, callID)
	require.NoError(t, err)

	// Gather every effect before asserting so one run carries the full
	// revision-path classification and blast-radius evidence.
	t.Logf("R5-C3a provider preterminal valuation: completeness=%q lines=%d totals=%+v missing=%d sealedCalls=%d terminalLegs=%d",
		providerValuation.Completeness, len(providerValuation.Lines), providerValuation.Totals, len(providerValuation.MissingObservations), len(callUsage), len(legs))
	t.Logf("R5-C3a customer preterminal valuation: completeness=%q lines=%d totals=%+v missing=%d",
		customerValuation.Completeness, len(customerValuation.Lines), customerValuation.Totals, len(customerValuation.MissingObservations))
	t.Logf("R5-C3a durable effects: journalTransactions=%d providerCostHeads=%d providerPostingFences=%d economicWorkRows=%d",
		r5c3aCount(t, billing2, "journal_transactions"),
		r5c3aCount(t, billing2, "billing_provider_cost_heads"),
		r5c3aCount(t, billing2, "billing_provider_cost_posting_fences"),
		r5c3aCount(t, billing2, "billing_economic_work"))

	// Observed effects of this pure composition, not a production guarantee:
	// the crash produced no terminal B-leg and no sealed call, and the pure
	// revision workers carry no posting port, so no call/leg usage, journal
	// entry or provider pin/head exists here. A production host that wires the
	// posting dependencies is deliberately out of scope for these observations.
	require.Empty(t, callUsage, "no sealed call exists after the crash")
	require.Empty(t, legs, "no terminal leg exists after the crash")
	require.Zero(t, r5c3aCount(t, billing2, "journal_transactions"), "this pure revision composition wrote no journal entry")
	require.Zero(t, r5c3aCount(t, billing2, "billing_provider_cost_heads"), "this pure revision composition wrote no provider pin/head")

	// Truthful characterization: the durable prefix is a complete revision
	// input set (requirement 10.2). No evidence-persistence failure occurred, so
	// requirement 10.5 degraded completeness does not apply; both the real
	// provider valuation and the provisional customer-policy (incremental
	// B-leg) valuation are complete for exactly the persisted observations they
	// were built over. Absent terminal ownership does not, by itself, make an
	// incremental revision valuation partial.
	require.Len(t, providerValuation.InputObservations, prefixCount, "provider valuation covers the full persisted prefix")
	require.Len(t, customerValuation.InputObservations, prefixCount, "customer valuation covers the full persisted prefix")
	require.Empty(t, providerValuation.MissingObservations, "no provider observation is missing from the persisted prefix")
	require.Empty(t, customerValuation.MissingObservations, "no customer observation is missing from the persisted prefix")
	require.Equal(t, economics.CompletenessComplete, providerValuation.Completeness,
		"real provider revision valuation is complete for its persisted input set (lines=%d totals=%+v)", len(providerValuation.Lines), providerValuation.Totals)
	require.Equal(t, economics.CompletenessComplete, customerValuation.Completeness,
		"provisional customer-policy revision valuation is complete for its persisted input set (lines=%d totals=%+v)", len(customerValuation.Lines), customerValuation.Totals)
}

func r5c3aCount(t *testing.T, store *billingstore.DurableStore, table string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, store.DB().NewRaw("SELECT COUNT(*) FROM "+table).Scan(context.Background(), &count))
	return count
}

// r5c3aRater is a test-only dispatcher over the pure stock rater helpers:
// provider-reported P bypasses any tariff, customer policy R prices the frozen
// B-leg quantity envelope from the supplied immutable tariff snapshot. It has
// no posting, journal or lifecycle dependency and never mutates money state.
type r5c3aRater struct {
	tariff economics.TariffSnapshot
}

func (r r5c3aRater) Rate(ctx context.Context, in economics.PostUsageRatingInput) (economics.Valuation, error) {
	switch in.Basis {
	case economics.BasisProviderReported:
		return billing.RateProviderReported(ctx, in)
	case economics.BasisCustomerPolicy:
		return billing.RateCustomerPolicyObservation(ctx, in, r.tariff)
	default:
		return economics.Valuation{}, fmt.Errorf("r5c3a: unsupported basis %q", in.Basis)
	}
}

func r5c3aValuation(t *testing.T, store *billingstore.DurableStore, queue billing.EconomicQueue, headKey string) economics.Valuation {
	t.Helper()
	head, err := store.GetEconomicValuationHead(context.Background(), queue, headKey)
	require.NoError(t, err)
	valuation, err := store.GetValuation(context.Background(), head.ValuationID, head.ValuationVersion)
	require.NoError(t, err)
	return valuation
}

func r5c3aCustomerTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	price, err := metering.ParseDecimal("0.5")
	require.NoError(t, err)
	key := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentImage,
		Unit: metering.UnitImage, SchemaID: "r5c2b.provider.schema",
	}
	return economics.NewTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r5c3a-rater", Version: "v1"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{{ID: "r5c3a-image-input", Component: &key, Currency: "USD", UnitPrice: &price}},
	)
}

func r5c3aChargePolicy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "r5c3a-policy", Version: "v1"},
		PricingRef:          billing.VersionRef{ID: "r5c3a-pricing", Version: "v1"},
		Scope:               billing.ChargeAllPotentialLegs,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
}
