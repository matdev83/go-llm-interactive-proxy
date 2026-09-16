package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

const (
	refinement4StockAccountID = "refinement4-stock-account"
	refinement4StockALegID    = "refinement4-stock-a-leg"
	refinement4StockBLegID    = "refinement4-stock-b-leg"
	refinement4StockCallID    = billing.BillingCallID("bc_00000000000000000000000000000071")
	refinement4StockSessionID = "refinement4-stock-session"
)

// TestRefinement4StockObservationToEconomicSettlement proves the complete
// production bridge. The observation transaction is the only input to the
// process-owned relay; work markers are read for expected identity only and
// are never manually appended by this test.
func TestRefinement4StockObservationToEconomicSettlement(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	billingStore := newCompositionBillingStore(t, "refinement4-stock-billing")
	provisionBillingHostLoopAccount(t, billingStore, refinement4StockAccountID)
	catalog, pricing, policy, _ := seedBillingHostLoopCatalog(t)

	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             billingStore,
		TerminalUsageSink: billingStore,
		Catalog:           catalog,
		Currency:          "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict: true,
		ConservativeCeiling: &billing.Money{
			Nano: 1000, Currency: "USD",
		},
		PostTurnBatchSize: 1,
	})
	require.NoError(t, err)

	journalPath := filepath.Join(t.TempDir(), "refinement4-stock-metering.sqlite")
	configPath := refinement4StockMeteringConfig(t, journalPath)
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      configPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		Production:      prod,
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)
	executor := hostActiveExecutor(t, host)

	atomicSink, ok := executor.MeteringObservationSink.(metering.AtomicObservationSink)
	require.True(t, ok, "stock composition must expose the atomic observation sink")
	journal, ok := executor.MeteringRecorder.(*journalstore.DurableStore)
	require.True(t, ok, "stock composition must expose the process-owned durable journal")
	require.NotNil(t, journal)
	terminalSink := prod.BillingTerminalUsageSink
	require.NotNil(t, terminalSink)

	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: billingStore.StoreID(), TenantID: "refinement4-stock-tenant",
		AccountID: refinement4StockAccountID, ALegID: refinement4StockALegID,
		BillingCallID: refinement4StockCallID.String(), BLegID: refinement4StockBLegID,
		AttemptID: "refinement4-stock-attempt", AttemptSeq: 1, ProviderAccountKey: "refinement4-stock-provider",
	}
	base := refinement4StockObservation(subject, "refinement4-stock-preterminal", 1, 7, metering.SemanticsDelta, nil)
	baseRef, err := base.Ref(subject.StoreID)
	require.NoError(t, err)

	// The admission record is real operational state, but it cannot settle until
	// the durable BillingCallID closure is appended below.
	exposure, err := billingStore.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: refinement4StockAccountID, CallID: refinement4StockCallID.String(),
		Max: billing.Money{Nano: 1000, Currency: "USD"}, PricingRef: pricing.Ref,
		ChargePolicyRef: policy.Ref, Now: time.Unix(1_700_100_000, 0).UTC(),
	})
	require.NoError(t, err)
	require.True(t, exposure.IsOpen())

	// Derive deterministic identities from the same production builder for
	// read-side assertions. No returned work is appended here.
	expectedWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, []metering.Observation{base})
	require.NoError(t, err)
	require.Len(t, expectedWorks, 2)
	var expectedProvider, expectedCustomer billing.EconomicRevisionWork
	for _, work := range expectedWorks {
		switch work.Queue {
		case billing.EconomicQueueProvider:
			expectedProvider = work
		case billing.EconomicQueueCustomer:
			expectedCustomer = work
		}
	}
	require.NotEmpty(t, expectedProvider.HeadKey)
	require.NotEmpty(t, expectedCustomer.HeadKey)
	expectedProviderIdentity, err := expectedProvider.Identity()
	require.NoError(t, err)
	expectedCustomerIdentity, err := expectedCustomer.Identity()
	require.NoError(t, err)

	// RED/GREEN bridge assertion: one durable observation transaction must make
	// both isolated queues observable to the stock relay and workers.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{base}))
	providerHead := waitRefinement4StockHead(t, ctx, billingStore, journal, refinement4StockAccountID, billing.EconomicQueueProvider, expectedProvider.HeadKey)
	customerHead := waitRefinement4StockHead(t, ctx, billingStore, journal, refinement4StockAccountID, billing.EconomicQueueCustomer, expectedCustomer.HeadKey)
	waitRefinement4StockOutboxDrained(t, ctx, billingStore, journal)
	require.Equal(t, uint64(1), providerHead.EvidenceRevision)
	require.Equal(t, expectedProvider.InputSetHash, providerHead.InputSetHash)
	require.Equal(t, expectedProviderIdentity.Key(), providerHead.WorkID)
	require.Equal(t, int64(7), waitRefinement4StockProviderAmount(t, ctx, billingStore, refinement4StockAccountID, expectedProvider.HeadKey))
	require.Equal(t, uint64(1), customerHead.EvidenceRevision)
	require.Equal(t, expectedCustomer.InputSetHash, customerHead.InputSetHash)
	require.Equal(t, uint32(economics.ValuationVersionV2), customerHead.ValuationVersion)

	// Provider COGS is accrued from the pure provider-reported valuation before
	// any BillingCallID closure exists. Customer balance and exposure are still
	// untouched at this point.
	require.True(t, exposureStillOpen(t, billingStore, refinement4StockCallID))
	require.Empty(t, refinement4StockCustomerTransactions(t, billingStore, refinement4StockAccountID))
	initialReport := refinement4StockAccountReport(t, billingStore, refinement4StockAccountID)
	require.Equal(t, billingHostLoopOpeningNano, initialReport.Account.BalanceNano)
	require.Len(t, refinement4StockProviderJournals(t, billingStore, refinement4StockAccountID), 1)

	leg := refinement4StockCallLeg(base)
	closure := refinement4StockCallClosure(pricing.Ref, policy.Ref, base)
	// These are the production terminal ports. They persist immutable records;
	// neither operation rates, settles, nor posts money on the receive path.
	require.NoError(t, terminalSink.AppendLeg(ctx, leg))
	require.NoError(t, terminalSink.AppendCall(ctx, closure))

	settled := waitRefinement4StockSettlement(t, ctx, billingStore, refinement4StockAccountID, refinement4StockCallID)
	require.False(t, settled.IsOpen())
	require.Equal(t, uint64(1), providerHead.EvidenceRevision)
	require.Len(t, refinement4StockProviderJournals(t, billingStore, refinement4StockAccountID), 1)
	require.Len(t, refinement4StockCustomerTransactions(t, billingStore, refinement4StockAccountID), 1)
	require.Equal(t, billingHostLoopCustomerNano, refinement4StockCustomerTransactions(t, billingStore, refinement4StockAccountID)[0].Entries[0].Amount.Nano)

	// Exact terminal replay is accepted at every durable boundary and does not
	// create a second queue marker, provider journal, closure, or settlement.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{base}))
	require.NoError(t, terminalSink.AppendLeg(ctx, leg))
	require.NoError(t, terminalSink.AppendCall(ctx, closure))
	waitRefinement4StockOutboxDrained(t, ctx, billingStore, journal)
	require.Len(t, refinement4StockProviderJournals(t, billingStore, refinement4StockAccountID), 1)
	require.Len(t, refinement4StockCustomerTransactions(t, billingStore, refinement4StockAccountID), 1)
	require.Len(t, mustListCallUsage(t, billingStore, refinement4StockAccountID), 1)

	// A late provider correction supersedes the exact first charge. It carries no
	// retail quantity, so the frozen independent-retail customer head remains at
	// revision 1 while provider COGS gets a new immutable revision and -3 delta.
	correction := refinement4StockObservation(subject, "refinement4-stock-correction", 2, 4, metering.SemanticsCorrection, []metering.ObservationRef{baseRef})
	correctionWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, []metering.Observation{base, correction})
	require.NoError(t, err)
	var expectedProviderCorrection billing.EconomicRevisionWork
	for _, work := range correctionWorks {
		if work.Queue == billing.EconomicQueueProvider {
			expectedProviderCorrection = work
		}
	}
	require.NotEmpty(t, expectedProviderCorrection.InputSetHash)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{correction}))
	waitRefinement4StockOutboxDrained(t, ctx, billingStore, journal)
	providerHead = waitRefinement4StockHeadRevision(t, ctx, billingStore, journal, refinement4StockAccountID, billing.EconomicQueueProvider, expectedProvider.HeadKey, 2)
	require.Equal(t, expectedProviderCorrection.InputSetHash, providerHead.InputSetHash)
	waitRefinement4StockProviderCurrentAmount(t, ctx, billingStore, refinement4StockAccountID, refinement4StockCallID, expectedProvider.HeadKey, 4)

	providerJournals := refinement4StockProviderJournals(t, billingStore, refinement4StockAccountID)
	require.Len(t, providerJournals, 2)
	require.Equal(t, providerJournals[0].ID, providerJournals[1].ReversalOf)
	require.Equal(t, providerJournals[0].ID, providerJournals[1].CorrectsTransactionID)
	require.Equal(t, expectedProvider.HeadKey, providerJournals[0].CorrectionGroupID)
	require.Equal(t, expectedProvider.HeadKey, providerJournals[1].CorrectionGroupID)
	require.Equal(t, int64(3), providerJournals[1].Entries[0].Amount.Nano)
	require.Equal(t, int64(3), providerJournals[1].Entries[1].Amount.Nano)

	customerHead = waitRefinement4StockHead(t, ctx, billingStore, journal, refinement4StockAccountID, billing.EconomicQueueCustomer, expectedCustomer.HeadKey)
	require.Equal(t, uint64(1), customerHead.EvidenceRevision)
	require.Equal(t, expectedCustomerIdentity.Key(), customerHead.WorkID)
	require.Len(t, refinement4StockCustomerTransactions(t, billingStore, refinement4StockAccountID), 1)
	require.False(t, exposureStillOpen(t, billingStore, refinement4StockCallID))
	require.Equal(t, billingHostLoopOpeningNano-billingHostLoopCustomerNano, refinement4StockAccountReport(t, billingStore, refinement4StockAccountID).Account.BalanceNano)

	// Read-only queue row counts prove isolation without depending on pending
	// timing: provider has exactly base+correction, customer has only base.
	var providerRows, customerRows int
	require.NoError(t, billingStore.DB().NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ? AND kind = ?`, billingStore.StoreID(), "economic_revision:provider").Scan(ctx, &providerRows))
	require.NoError(t, billingStore.DB().NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ? AND kind = ?`, billingStore.StoreID(), "economic_revision:customer").Scan(ctx, &customerRows))
	require.Equal(t, 2, providerRows)
	require.Equal(t, 1, customerRows)

	// The durable records retain the A-leg lineage, but no A-leg retirement or
	// lifecycle callback is needed by provider posting or customer settlement.
	providerCostHead, err := billingStore.GetProviderCostHead(ctx, refinement4StockAccountID, refinement4StockCallID, expectedProvider.HeadKey)
	require.NoError(t, err)
	require.Equal(t, expectedProviderCorrection.InputSetHash, providerCostHead.InputSetHash)
	require.Equal(t, refinement4StockALegID, providerCostHead.Subject.ALegID)
	require.Equal(t, refinement4StockBLegID, providerCostHead.Subject.BLegID)
}

func refinement4StockMeteringConfig(t *testing.T, journalPath string) string {
	t.Helper()
	base := writeBillingHostLoopConfig(t)
	contents, err := os.ReadFile(base)
	require.NoError(t, err)
	contentsText := strings.Replace(string(contents), "continuity:\n  in_memory: true\n  store: memory\n", "continuity:\n  in_memory: true\n  store: memory\nmetering:\n  enabled: true\n  journal:\n    store: sqlite\n    sqlite_path: \""+filepath.ToSlash(journalPath)+"\"\n", 1)
	configPath := filepath.Join(t.TempDir(), "refinement4-stock-host.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(contentsText), 0o600))
	return configPath
}

func refinement4StockObservation(subject metering.SubjectRef, id string, revision uint64, providerNano int64, semantics string, supersedes []metering.ObservationRef) metering.Observation {
	now := time.Unix(1_700_101_000+int64(revision), 0).UTC()
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	input := metering.Decimal{Coefficient: "1000000", Scale: 0}
	output := metering.Decimal{Coefficient: "1000000", Scale: 0}
	amount := metering.DecimalFromNanoUnits(providerNano)
	correlation := metering.CorrelationV2{
		StoreID: subject.StoreID, TenantID: subject.TenantID, ALegID: subject.ALegID,
		BillingCallID: subject.BillingCallID, BLegID: subject.BLegID, AttemptID: subject.AttemptID,
		AttemptSeq: subject.AttemptSeq, ProviderAccountKey: subject.ProviderAccountKey,
	}
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: "refinement4-stock-source-" + id, Revision: revision,
		StreamID: "refinement4-stock-stream", Sequence: revision, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: correlation, Semantics: semantics,
		ObservedAt: now, ReceivedAt: now, MappingRef: "refinement4-stock:v1", Supersedes: supersedes,
		Charges: []metering.ReportedCharge{{ChargeItemID: "provider-cost", Amount: &amount, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator}}},
	}
	if semantics == metering.SemanticsDelta {
		observation.Measures = []metering.Measure{
			{Key: inputKey, Value: &input, Quality: metering.QualityObserved},
			{Key: outputKey, Value: &output, Quality: metering.QualityObserved},
		}
	}
	return observation
}

func refinement4StockCallLeg(observation metering.Observation) billing.CallLegUsageRecord {
	started := observation.ObservedAt
	finished := started.Add(time.Second)
	return billing.CallLegUsageRecord{
		CallID: observationCallID(observation), ALegID: observation.Subject.ALegID, BLegID: observation.Subject.BLegID,
		AttemptSeq: int(observation.Subject.AttemptSeq), BackendID: billingHostLoopBackendID, ProviderID: "refinement4-stock-provider", ModelID: billingHostLoopModelID,
		StartedAt: started, FinishedAt: finished, Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: billingHostLoopInputTokens, Present: true}, OutputTokens: billing.Quantity{Value: billingHostLoopOutputTokens, Present: true},
			TotalTokens: billing.Quantity{Value: billingHostLoopInputTokens + billingHostLoopOutputTokens, Present: true},
			Source:      billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative, DedupeKey: "refinement4-stock-terminal-usage",
		},
		Observations: []metering.Observation{observation},
	}
}

func refinement4StockCallClosure(pricingRef, policyRef billing.VersionRef, observation metering.Observation) billing.CallUsageRecord {
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: observationCallID(observation), AccountID: observation.Subject.AccountID,
		ALegID: observation.Subject.ALegID, SessionID: refinement4StockSessionID,
		StartedAt: observation.ObservedAt, FinishedAt: observation.ObservedAt.Add(2 * time.Second), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricingRef, ChargePolicyRef: policyRef, ExpectedBLegIDs: []string{observation.Subject.BLegID},
	}
}

func observationCallID(observation metering.Observation) billing.BillingCallID {
	return billing.BillingCallID(observation.Subject.BillingCallID)
}

func waitRefinement4StockHead(t *testing.T, parent context.Context, store *billingstore.DurableStore, journal *journalstore.DurableStore, accountID string, queue billing.EconomicQueue, headKey string) billing.EconomicValuationHead {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		head, err := store.GetEconomicValuationHead(ctx, queue, headKey)
		if err == nil {
			return head
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s economic head %q: %s", queue, headKey, refinement4StockDiagnostics(t, store, journal, accountID, err))
		case <-ticker.C:
		}
	}
}

func waitRefinement4StockHeadRevision(t *testing.T, parent context.Context, store *billingstore.DurableStore, journal *journalstore.DurableStore, accountID string, queue billing.EconomicQueue, headKey string, revision uint64) billing.EconomicValuationHead {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		head, err := store.GetEconomicValuationHead(ctx, queue, headKey)
		if err == nil && head.EvidenceRevision >= revision {
			return head
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s economic head %q revision %d: %s", queue, headKey, revision, refinement4StockDiagnostics(t, store, journal, accountID, err))
		case <-ticker.C:
		}
	}
}

func waitRefinement4StockOutboxDrained(t *testing.T, parent context.Context, store *billingstore.DurableStore, journal *journalstore.DurableStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		pending, err := journal.ListPendingObservationOutbox(ctx, 64)
		if err == nil && len(pending) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for observation outbox relay: pending=%+v err=%v", pending, err)
		case <-ticker.C:
		}
	}
}

func waitRefinement4StockSettlement(t *testing.T, parent context.Context, store *billingstore.DurableStore, accountID string, callID billing.BillingCallID) billing.CallExposure {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, exposureErr := store.GetCallExposure(ctx, callID)
		records, recordsErr := store.ListCallUsage(ctx, accountID)
		transactions := refinement4StockCustomerTransactions(t, store, accountID)
		if exposureErr == nil && recordsErr == nil && len(records) == 1 && !exposure.IsOpen() && len(transactions) == 1 {
			return exposure
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for BillingCallID settlement: exposure=%+v exposureErr=%v records=%+v recordsErr=%v customerTransactions=%+v", exposure, exposureErr, records, recordsErr, transactions)
		case <-ticker.C:
		}
	}
}

func waitRefinement4StockProviderAmount(t *testing.T, parent context.Context, store *billingstore.DurableStore, accountID, headKey string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		report := refinement4StockAccountReport(t, store, accountID)
		for _, transaction := range report.Transactions {
			if transaction.OperationKind == "provider_call_cogs" && transaction.CorrectionGroupID == headKey {
				return transaction.Entries[0].Amount.Nano
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for provider amount head %q: report=%+v", headKey, report)
		case <-ticker.C:
		}
	}
}

func waitRefinement4StockProviderCurrentAmount(t *testing.T, parent context.Context, store *billingstore.DurableStore, accountID string, callID billing.BillingCallID, headKey string, want int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var head billing.ProviderCostHead
	var err error
	for {
		head, err = store.GetProviderCostHead(ctx, accountID, callID, headKey)
		if err == nil && head.CurrentAmount.Nano == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for provider current amount head %q=%d: head=%+v err=%v", headKey, want, head, err)
		case <-ticker.C:
		}
	}
}

func exposureStillOpen(t *testing.T, store *billingstore.DurableStore, callID billing.BillingCallID) bool {
	t.Helper()
	exposure, err := store.GetCallExposure(context.Background(), callID)
	require.NoError(t, err)
	return exposure.IsOpen()
}

func refinement4StockAccountReport(t *testing.T, store *billingstore.DurableStore, accountID string) billing.AccountReport {
	t.Helper()
	report, err := store.AccountReport(context.Background(), accountID, billing.PageRequest{Limit: 100})
	require.NoError(t, err)
	return report
}

func refinement4StockProviderJournals(t *testing.T, store *billingstore.DurableStore, accountID string) []billing.JournalTransaction {
	t.Helper()
	rows, err := store.JournalTransactions(context.Background(), accountID)
	require.NoError(t, err)
	filtered := make([]billing.JournalTransaction, 0, len(rows))
	for _, row := range rows {
		if row.OperationKind == "provider_call_cogs" {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func refinement4StockCustomerTransactions(t *testing.T, store *billingstore.DurableStore, accountID string) []billing.JournalTransaction {
	t.Helper()
	rows, err := store.JournalTransactions(context.Background(), accountID)
	require.NoError(t, err)
	filtered := make([]billing.JournalTransaction, 0, len(rows))
	for _, row := range rows {
		if row.OperationKind == "customer_call_settlement" {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func mustListCallUsage(t *testing.T, store *billingstore.DurableStore, accountID string) []billing.CallUsageRecord {
	t.Helper()
	rows, err := store.ListCallUsage(context.Background(), accountID)
	require.NoError(t, err)
	return rows
}

func refinement4StockDiagnostics(t *testing.T, store *billingstore.DurableStore, journal *journalstore.DurableStore, accountID string, cause error) string {
	t.Helper()
	pendingProvider, providerErr := store.ListPendingEconomicRevisionWork(context.Background(), billing.EconomicQueueProvider, 10)
	pendingCustomer, customerErr := store.ListPendingEconomicRevisionWork(context.Background(), billing.EconomicQueueCustomer, 10)
	pendingOutbox, outboxErr := journal.ListPendingObservationOutbox(context.Background(), 64)
	report := refinement4StockAccountReport(t, store, accountID)
	var states []struct {
		Queue     string `bun:"queue"`
		HeadKey   string `bun:"head_key"`
		Status    string `bun:"status"`
		Attempts  int64  `bun:"attempt_count"`
		LastError string `bun:"last_error"`
	}
	stateErr := store.DB().NewRaw(`SELECT queue, head_key, status, attempt_count, last_error FROM billing_economic_revision_work_state WHERE store_id = ? ORDER BY id`, store.StoreID()).Scan(context.Background(), &states)
	return fmt.Sprintf("cause=%v pendingProvider=%+v providerErr=%v pendingCustomer=%+v customerErr=%v pendingOutbox=%+v outboxErr=%v states=%+v stateErr=%v transactions=%+v", cause, pendingProvider, providerErr, pendingCustomer, customerErr, pendingOutbox, outboxErr, states, stateErr, report.Transactions)
}
