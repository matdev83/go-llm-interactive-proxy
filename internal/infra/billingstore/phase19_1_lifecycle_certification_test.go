package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/normalize"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

// Phase 19.1 lifecycle certification.
//
// These tests close the one genuine gap found in the task 19.1 inventory:
// every acceptance vector had unit/contract coverage, but no integrated test
// ran real-family evidence through the production collector, normalizer,
// durable stores, reference rater, reconciler, settlement and query seams in
// one lifecycle. Each test below uses only production seams:
//
//   - collector: coremetering.ProviderEvidenceBuffer drain with trusted B-leg
//     binding (provider data cannot choose its subject);
//   - family evidence: openaiusage.ProviderEvidenceDraft over real wire-shaped
//     lipapi.Event payloads (OpenAI/OpenResponses family, text plus native
//     multimodal units) and versioned normalize.Mapping partitions with
//     Anthropic-style separate counters and OpenAI-style inclusive totals;
//   - storage: journalstore.AppendObservationsWithOutbox plus billingstore
//     AppendCallLegUsage/AppendCallUsage/AppendValuation/AppendReconciliation;
//   - rating: billing.RateWithTariff (E and Q), billing.RateProviderReported
//     (P), billing.RateCall (R plus settlement result);
//   - discrepancy: billing.CompareComponentQuantities and
//     billing.DecomposeMonetaryDiscrepancies;
//   - settlement: billing.AttributeOperatorCOGS,
//     billing.SelectRetailBLegEvidence, AdmitExposure, ApplyProviderCost,
//     ApplyCallBillingResult;
//   - query: CallExplanation and QueryEconomicDetail.
//
// No direct SQL and no hand-built settlement bypass the production seams.

const (
	ph191StoreID = "ph191"
	ph191ALeg    = "a-191"
)

func ph191BillingStore(t *testing.T) (*DurableStore, string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ph191-billing.sqlite")
	store, closeStore := openRefinement82FileBillingStore(t, path, ph191StoreID)
	return store, path, closeStore
}

func ph191JournalStore(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ph191-journal.sqlite")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: ph191StoreID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ph191Decimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &value
}

func ph191TokenKey(direction metering.FlowDirection, component string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
}

// ph191SeparateMapping is the Anthropic-family shape: the provider reports an
// already-separated uncached input plus cache read/write counters.
func ph191SeparateMapping() normalize.Mapping {
	tokenKey := func(direction metering.FlowDirection, component string) metering.ComponentKey {
		return ph191TokenKey(direction, component)
	}
	return normalize.Mapping{
		ID: "ph191.anthropic", Family: "anthropic", Version: "v1", InputMode: normalize.InputSeparate,
		InputUncached: normalize.FieldSpec{Name: "input", EvidencePath: "$.usage.input_tokens", Key: tokenKey(metering.DirectionInput, metering.ComponentInputToken)},
		CacheRead:     []normalize.FieldSpec{{Name: "read", EvidencePath: "$.usage.cache_read_input_tokens", Key: tokenKey(metering.DirectionInput, metering.ComponentCacheReadInputToken)}},
		CacheWrite:    []normalize.FieldSpec{{Name: "write", EvidencePath: "$.usage.cache_write_input_tokens", Key: tokenKey(metering.DirectionInput, metering.ComponentCacheWriteInputToken)}},
		Output:        normalize.FieldSpec{Name: "output", EvidencePath: "$.usage.output_tokens", Key: tokenKey(metering.DirectionOutput, metering.ComponentOutputToken)},
		Reasoning:     normalize.FieldSpec{Name: "reasoning", EvidencePath: "$.usage.reasoning_output_tokens", Key: tokenKey(metering.DirectionOutput, metering.ComponentReasoningOutputToken)},
		ReasoningMode: normalize.ReasoningIncluded,
	}
}

// ph191DrainProviderCapture runs one wire-shaped OpenAI-family usage event
// through the production family adapter and the production collector. The
// trusted identity is the only attribution authority. Drained provider
// charges carry the test deployment's explicit payer classification
// (Req 6.6): this fixture uses operator-owned credentials, so payer-less
// provider charges are classified operator-payable here; a BYOK deployment
// would classify them customer-paid instead (covered by the existing
// refinement83 BYOK certification referenced in the evidence matrix).
func ph191DrainProviderCapture(t *testing.T, callID billing.BillingCallID, bLegID string, attemptSeq uint64, ev lipapi.Event) []metering.Observation {
	t.Helper()
	draft := openaiusage.ProviderEvidenceDraft(ev, "openai.chat.v2", "ph191:"+bLegID)
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.Add(draft)
	buffer.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: ph191StoreID, BillingCallID: callID.String(), ALegID: ph191ALeg,
		BLegID: bLegID, AttemptID: "att-" + bLegID, AttemptSeq: attemptSeq,
		ObservedAt: time.Unix(1_700_191_000, 0).UTC(), ReceivedAt: time.Unix(1_700_191_001, 0).UTC(),
	})
	drained := buffer.DrainEconomicObservations()
	if len(drained) == 0 {
		t.Fatalf("production collector drained no observation for B-leg %q", bLegID)
	}
	for i := range drained {
		for j := range drained[i].Charges {
			if drained[i].Charges[j].Payer.Kind == "" {
				drained[i].Charges[j].Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator}
			}
		}
		if err := drained[i].Validate(); err != nil {
			t.Fatalf("classified provider observation invalid: %v", err)
		}
	}
	return drained
}

func ph191WinnerEvent() lipapi.Event {
	return lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   110,
		OutputTokens:  200,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		CostNanoUnits: 1_320_000_000,
		Currency:      "USD",
		CostPresent:   true,
		RawUsageJSON:  `{"input_tokens":110,"output_tokens":200,"input_image_tokens":2,"output_audio_seconds":12,"cost":1.32}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source: lipapi.UsageSourceProviderReported, ProviderAccountKey: "ph191-provider-acct",
			ProviderRequestID: "req-ph191-winner", ProviderChargeID: "chg-ph191-winner", DedupeKey: "ph191-winner",
		},
	}
}

func ph191LocalWinnerObservation(t *testing.T, callID billing.BillingCallID, subject metering.SubjectRef, correlation metering.CorrelationV2, id string) metering.Observation {
	t.Helper()
	mapping := ph191SeparateMapping()
	result, err := normalize.Normalize(normalize.Input{
		Mapping: mapping,
		Fields: []normalize.Field{
			{Name: "input", Lexeme: "100", Present: true},
			{Name: "output", Lexeme: "200", Present: true},
		},
	})
	if err != nil {
		t.Fatalf("Normalize local winner: %v", err)
	}
	if result.Status != normalize.StatusComplete {
		t.Fatalf("local winner status = %q, want complete (diagnostics=%v)", result.Status, result.Diagnostics)
	}
	now := time.Unix(1_700_191_010, 0).UTC()
	// The subject is the same B-leg attempt the provider reported on (same
	// store/A-leg/call/B-leg lineage) so quantity reconciliation can join the
	// two channels; only the origin, acquisition and source identity stay
	// independent. The callID parameter pins the lineage the test owns.
	if subject.BillingCallID != callID.String() {
		t.Fatalf("local subject call = %q, want %q", subject.BillingCallID, callID.String())
	}
	base := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: "ph191-local-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTokenizer, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: correlation,
		// A complete-request local measurement is a cumulative present-field
		// snapshot, matching the provider snapshot channel it reconciles
		// against; streaming deltas would use SemanticsDelta instead.
		Semantics: metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now,
	}
	observation, err := result.Attach(base)
	if err != nil {
		t.Fatalf("Attach local winner: %v", err)
	}
	// Independent local output-audio transport measurement in the same media
	// frame as the provider report (10s locally observed versus 12s
	// provider-reported), so the lifecycle carries a genuinely comparable
	// non-token pair alongside the tokenizer-guarded text pair.
	observation.Measures = append(observation.Measures, metering.Measure{
		Key:       metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"},
		Value:     ph191Decimal(t, "10"),
		Quality:   metering.QualityObserved,
		MethodRef: "ph191.local.transport.v1",
	})
	if err := observation.Validate(); err != nil {
		t.Fatalf("local winner with audio invalid: %v", err)
	}
	return observation
}

func ph191Tariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	return ph191BuildTariff(t, "ph191-tariff", false)
}

// ph191CustomerTariff adds the once-per-submission commercial fee on top of
// the usage rules. E/Q valuations rate usage only; R settlement applies the
// fee once per trusted submission through RateCall.
func ph191CustomerTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	return ph191BuildTariff(t, "ph191-customer-tariff", true)
}

func ph191BuildTariff(t *testing.T, id string, withSubmissionFee bool) economics.TariffSnapshot {
	t.Helper()
	rule := func(id string, key metering.ComponentKey, price string) economics.RatingRule {
		return economics.RatingRule{ID: id, Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: ph191Decimal(t, price)}
	}
	imageIn := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}
	audioOut := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"}
	rules := []economics.RatingRule{
		rule("input-token", ph191TokenKey(metering.DirectionInput, metering.ComponentInputToken), "0.01"),
		rule("output-token", ph191TokenKey(metering.DirectionOutput, metering.ComponentOutputToken), "0.03"),
		rule("image-input", imageIn, "0.05"),
		rule("audio-output", audioOut, "0.02"),
	}
	if withSubmissionFee {
		rules = append(rules, economics.RatingRule{ID: "submission-fee", Kind: economics.RatingRuleFixed, Currency: "USD", FixedAmount: ph191Decimal(t, "1"), FixedScope: economics.FixedFeeScopeSubmission})
	}
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: id, Version: "v1"}, RaterID: "reference"},
		"USD",
		rules,
	)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func ph191WinnerPolicy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "ph191-retail-policy", Version: "v1"},
		PricingRef:          billing.VersionRef{ID: "ph191-customer-tariff", Version: "v1"},
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
		Retail:              &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
	}
}

func ph191Leg(t *testing.T, callID billing.BillingCallID, bLegID string, seq int, outcome billing.LegOutcome, surfaced billing.SurfacedState, observations []metering.Observation, v1 billing.FinalBillingEvidence) billing.CallLegUsageRecord {
	t.Helper()
	return billing.CallLegUsageRecord{
		CallID: callID, ALegID: ph191ALeg, BLegID: bLegID, AttemptSeq: seq,
		BackendID: "ph191-backend", ProviderID: "ph191-provider", ModelID: "ph191-model",
		StartedAt: time.Unix(1_700_191_100, 0).UTC(), FinishedAt: time.Unix(1_700_191_101, 0).UTC(),
		Outcome: outcome, Surfaced: surfaced, Observations: observations, Evidence: v1,
		EvidenceVersion: billing.EvidenceFormatVersionV2, EvidenceProjection: billing.EvidenceProjectionV1,
	}
}

// ph191V1Claim builds the terminal provider-reported compatibility envelope
// the production terminal handoff would attach for one provider charge: the
// same counts and aggregate money the V2 observations carry, with
// provider-reported authoritative provenance. Legs without provider money
// (shell, missing, credit-only) carry the zero envelope.
func ph191V1Claim(bLegID string, input, output int64, costNano int64) billing.FinalBillingEvidence {
	return billing.FinalBillingEvidence{
		InputTokens:  billing.Quantity{Value: input, Present: true},
		OutputTokens: billing.Quantity{Value: output, Present: true},
		Cost:         billing.MoneyEvidence{NanoUnits: costNano, Currency: "USD", Present: true},
		Source:       billing.EvidenceSourceProviderReported,
		Authority:    billing.EvidenceAuthorityAuthoritative,
		DedupeKey:    "ph191-" + bLegID,
	}
}

func ph191Call(callID billing.BillingCallID, accountID string, policy billing.ChargePolicy, submissionID string, legs ...string) billing.CallUsageRecord {
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: ph191ALeg, SessionID: "sess-191",
		StartedAt: time.Unix(1_700_191_100, 0).UTC(), FinishedAt: time.Unix(1_700_191_102, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted, SubmissionID: submissionID,
		CustomerPricingRef: policy.PricingRef, ChargePolicyRef: policy.Ref,
		ExpectedBLegIDs: append([]string(nil), legs...),
	}
}

func ph191RatingInput(t *testing.T, basis economics.ValuationBasis, tariff economics.TariffSnapshot, observations []metering.Observation) economics.RatingInput {
	t.Helper()
	return economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: basis,
		Subject: observations[0].Subject, Scope: "call:ph191", Observations: observations,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "ph191-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://ph191/rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               tariff.Ref,
		TariffContent:        &tariff.Content,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://ph191/qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(1_700_191_200, 0).UTC(),
	}
}

func ph191Total(valuation economics.Valuation, currency string) string {
	for _, total := range valuation.Totals {
		if total.Currency == currency && total.Amount != nil {
			return total.Amount.CanonicalString()
		}
	}
	return ""
}

func ph191FindItem(items []billing.ComponentQuantityComparisonItem, direction metering.FlowDirection, component string) *billing.ComponentQuantityComparisonItem {
	for i := range items {
		if items[i].Key.Direction == direction && items[i].Key.Component == component {
			return &items[i]
		}
	}
	return nil
}

// TestPhase191LifecycleFullEconomicsCertification runs the complete
// independent-economics lifecycle through production seams: OpenAI-family
// provider capture plus independent local measurement, durable journal and
// billing storage, E/Q/P/R rating, quantity and monetary discrepancy, all-leg
// COGS with winner-only retail selection, settlement, and query linkage.
func TestPhase191LifecycleFullEconomicsCertification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	billingStore, _, closeBilling := ph191BillingStore(t)
	defer closeBilling()
	journal := ph191JournalStore(t)

	account := billing.Account{ID: "ph191-account", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100_000_000_000, State: billing.AccountReady, Version: 1}
	if err := billingStore.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := ph191WinnerPolicy()
	tariff := ph191Tariff(t)

	// Provider capture through the production family adapter and collector.
	// The winner carries text usage, native multimodal units and a genuine
	// aggregate provider charge; the provider cannot choose its subject.
	winnerProvider := ph191DrainProviderCapture(t, callID, "b-winner", 2, ph191WinnerEvent())
	winnerObs := winnerProvider[0]
	if winnerObs.Origin != metering.OriginProvider || winnerObs.Acquisition != metering.AcquisitionProviderResponse {
		t.Fatalf("winner provenance = %q/%q, want provider/provider_response", winnerObs.Origin, winnerObs.Acquisition)
	}
	if winnerObs.Subject.BLegID != "b-winner" || winnerObs.Subject.StoreID != ph191StoreID || winnerObs.Subject.ALegID != ph191ALeg {
		t.Fatalf("winner subject = %+v, want trusted b-winner binding", winnerObs.Subject)
	}
	if len(winnerObs.Charges) != 1 || winnerObs.Charges[0].Amount == nil || winnerObs.Charges[0].Component != nil {
		t.Fatalf("winner charges = %+v, want one aggregate-only provider charge", winnerObs.Charges)
	}
	if got := winnerObs.Charges[0].Amount.CanonicalString(); got != "132/2" {
		t.Fatalf("winner aggregate charge = %q, want 132/2", got)
	}

	// Independent local measurement through the versioned normalizer. Local
	// input (100) deliberately differs from provider input (110) so the
	// lifecycle preserves a real disagreement instead of echoing the claim.
	localWinner := ph191LocalWinnerObservation(t, callID, winnerObs.Subject, winnerObs.Correlation, "ph191-local-winner")
	if localWinner.Origin != metering.OriginLocal || localWinner.Acquisition != metering.AcquisitionLocalTokenizer {
		t.Fatalf("local provenance = %q/%q, want local/local_tokenizer", localWinner.Origin, localWinner.Acquisition)
	}

	// Durable journal round-trip through the production store, not SQL.
	for _, observation := range append(append([]metering.Observation(nil), winnerProvider...), localWinner) {
		if err := journal.AppendObservationsWithOutbox(ctx, []metering.Observation{observation}); err != nil {
			t.Fatalf("journal append %q: %v", observation.ID, err)
		}
		readBack, err := journal.GetObservation(ctx, observation.ID, observation.Revision)
		if err != nil {
			t.Fatalf("journal read-back %q: %v", observation.ID, err)
		}
		wantRef, err := observation.Ref(ph191StoreID)
		if err != nil {
			t.Fatal(err)
		}
		gotRef, err := readBack.Ref(ph191StoreID)
		if err != nil {
			t.Fatal(err)
		}
		if gotRef != wantRef {
			t.Fatalf("journal round-trip ref = %+v, want %+v", gotRef, wantRef)
		}
	}

	// Auxiliary, retry and loser legs carry their own provider charges; the
	// never-started shell carries nothing and must stay a known zero.
	retryProvider := ph191DrainProviderCapture(t, callID, "b-retry", 1, lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 40, OutputTokens: 10,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		CostNanoUnits: 500_000_000, Currency: "USD", CostPresent: true,
		RawUsageJSON: `{"input_tokens":40,"output_tokens":10,"cost":0.5}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:             lipapi.UsageSourceProviderReported,
			ProviderAccountKey: "ph191-provider-acct", ProviderRequestID: "req-ph191-retry", ProviderChargeID: "chg-ph191-retry", DedupeKey: "ph191-retry",
		},
	})
	loserProvider := ph191DrainProviderCapture(t, callID, "b-loser", 3, lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 20, OutputTokens: 5,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		CostNanoUnits: 250_000_000, Currency: "USD", CostPresent: true,
		RawUsageJSON: `{"input_tokens":20,"output_tokens":5,"cost":0.25}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:             lipapi.UsageSourceProviderReported,
			ProviderAccountKey: "ph191-provider-acct", ProviderRequestID: "req-ph191-loser", ProviderChargeID: "chg-ph191-loser", DedupeKey: "ph191-loser",
		},
	})
	auxProvider := ph191DrainProviderCapture(t, callID, "b-aux", 4, lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 0,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		CostNanoUnits: 100_000_000, Currency: "USD", CostPresent: true,
		RawUsageJSON: `{"input_tokens":10,"output_tokens":0,"cost":0.1}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:             lipapi.UsageSourceProviderReported,
			ProviderAccountKey: "ph191-provider-acct", ProviderRequestID: "req-ph191-aux", ProviderChargeID: "chg-ph191-aux", DedupeKey: "ph191-aux",
		},
	})
	legs := []billing.CallLegUsageRecord{
		ph191Leg(t, callID, "b-retry", 1, billing.LegOutcomeFailed, billing.SurfacedNo, retryProvider, ph191V1Claim("b-retry", 40, 10, 500_000_000)),
		ph191Leg(t, callID, "b-winner", 2, billing.LegOutcomeWinner, billing.SurfacedYes, append(append([]metering.Observation(nil), winnerProvider...), localWinner), ph191V1Claim("b-winner", 110, 200, 1_320_000_000)),
		ph191Leg(t, callID, "b-loser", 3, billing.LegOutcomeLoser, billing.SurfacedNo, loserProvider, ph191V1Claim("b-loser", 20, 5, 250_000_000)),
		ph191Leg(t, callID, "b-aux", 4, billing.LegOutcomeCanceled, billing.SurfacedNo, auxProvider, ph191V1Claim("b-aux", 10, 0, 100_000_000)),
		ph191Leg(t, callID, "b-shell", 5, billing.LegOutcomeNeverStarted, billing.SurfacedNo, nil, billing.FinalBillingEvidence{}),
	}
	call := ph191Call(callID, account.ID, policy, "submission-191", "b-retry", "b-winner", "b-loser", "b-aux", "b-shell")

	// E/Q/P perspectives through the production reference rater.
	eValuation, err := billing.RateWithTariff(ctx, ph191RatingInput(t, economics.BasisLocalExpected, tariff, []metering.Observation{localWinner}), tariff)
	if err != nil {
		t.Fatalf("RateWithTariff E: %v", err)
	}
	qValuation, err := billing.RateWithTariff(ctx, ph191RatingInput(t, economics.BasisProviderQuantityLocal, tariff, winnerProvider), tariff)
	if err != nil {
		t.Fatalf("RateWithTariff Q: %v", err)
	}
	pValuation, err := billing.RateProviderReported(ctx, ph191RatingInput(t, economics.BasisProviderReported, tariff, winnerProvider))
	if err != nil {
		t.Fatalf("RateProviderReported P: %v", err)
	}
	// E covers local text plus local audio: 100*0.01 + 200*0.03 + 10*0.02.
	// Q covers provider text plus native multimodal:
	// 110*0.01 + 200*0.03 + 2*0.05 + 12*0.02.
	if got := ph191Total(eValuation, "USD"); got != "72/1" {
		t.Fatalf("E total = %q, want 72/1", got)
	}
	if got := ph191Total(qValuation, "USD"); got != "744/2" {
		t.Fatalf("Q total = %q, want 744/2", got)
	}
	if eValuation.Basis == qValuation.Basis || qValuation.Basis == pValuation.Basis {
		t.Fatalf("E/Q/P bases collapsed: %q %q %q", eValuation.Basis, qValuation.Basis, pValuation.Basis)
	}

	// Quantity discrepancy keeps both honest outcomes on real family
	// evidence. Text tokens carry no declared shared tokenizer, so the
	// production guard reports them incomparable instead of inventing a
	// match across different counting pipelines. The native audio pair
	// needs no tokenizer declaration: provider 12s versus local 10s is an
	// exact +2s discrepancy, and provider-only image evidence stays
	// missing_local rather than zero.
	quantityComparison, err := billing.CompareComponentQuantities(
		billing.ReconciliationEvidenceSet{Subject: localWinner.Subject, Observations: []metering.Observation{localWinner}},
		billing.ReconciliationEvidenceSet{Subject: winnerObs.Subject, Observations: winnerProvider},
	)
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}
	inputItem := ph191FindItem(quantityComparison.Items, metering.DirectionInput, metering.ComponentInputToken)
	if inputItem == nil || inputItem.Status != billing.ReconciliationStatusIncomparable ||
		inputItem.Reason != billing.ReconciliationReasonTokenizerRequired {
		t.Fatalf("input item = %+v, want incomparable/tokenizer_required", inputItem)
	}
	if inputItem.SignedDelta != nil {
		t.Fatalf("guarded input item carries a delta: %+v", inputItem.SignedDelta)
	}
	outputItem := ph191FindItem(quantityComparison.Items, metering.DirectionOutput, metering.ComponentOutputToken)
	if outputItem == nil || outputItem.Status != billing.ReconciliationStatusIncomparable ||
		outputItem.Reason != billing.ReconciliationReasonTokenizerRequired {
		t.Fatalf("output item = %+v, want incomparable/tokenizer_required", outputItem)
	}
	audioItem := ph191FindItem(quantityComparison.Items, metering.DirectionOutput, metering.ComponentAudio)
	if audioItem == nil || audioItem.SignedDelta == nil || audioItem.SignedDelta.CanonicalString() != "2/0" {
		t.Fatalf("audio delta item = %+v, want signed 2/0", audioItem)
	}
	if audioItem.Status != billing.ReconciliationStatusDiscrepant {
		t.Fatalf("audio status = %q, want discrepant", audioItem.Status)
	}
	imageItem := ph191FindItem(quantityComparison.Items, metering.DirectionInput, metering.ComponentImageToken)
	if imageItem == nil || imageItem.Status != billing.ReconciliationStatusMissingLocal {
		t.Fatalf("image item = %+v, want missing_local", imageItem)
	}

	// Monetary decomposition keeps every perspective: metering effect Q-E,
	// residual P-Q and end-to-end P-E with all sources retained.
	monetaryComparison, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{
		Valuations:         []economics.Valuation{eValuation, qValuation, pValuation},
		QuantityComparison: &quantityComparison,
	})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
	}
	if len(monetaryComparison.Rows) == 0 {
		t.Fatalf("monetary comparison has no rows: %+v", monetaryComparison)
	}
	row := monetaryComparison.Rows[0]
	if row.MeteringCostEffect.Amount == nil || row.MeteringCostEffect.Amount.Decimal == nil ||
		row.MeteringCostEffect.Amount.Decimal.CanonicalString() != "24/2" {
		t.Fatalf("metering cost effect = %+v, want 24/2", row.MeteringCostEffect)
	}
	if row.ReportedPriceResidual.Amount == nil || row.ReportedPriceResidual.Amount.Decimal == nil ||
		row.ReportedPriceResidual.Amount.Decimal.CanonicalString() != "-612/2" {
		t.Fatalf("reported price residual = %+v, want -612/2", row.ReportedPriceResidual)
	}
	if row.EndToEndCostDelta.Amount == nil || row.EndToEndCostDelta.Amount.Decimal == nil ||
		row.EndToEndCostDelta.Amount.Decimal.CanonicalString() != "-588/2" {
		t.Fatalf("end-to-end delta = %+v, want -588/2", row.EndToEndCostDelta)
	}
	if len(monetaryComparison.Valuations) != 3 {
		t.Fatalf("comparison valuations = %d, want E/Q/P retained", len(monetaryComparison.Valuations))
	}

	// Durable valuation and reconciliation envelopes through the production
	// store; every perspective survives the round-trip independently.
	for _, valuation := range []economics.Valuation{eValuation, qValuation, pValuation} {
		if err := billingStore.AppendValuation(ctx, valuation); err != nil {
			t.Fatalf("AppendValuation %q: %v", valuation.ID, err)
		}
	}
	reconciliationPayload, err := json.Marshal(monetaryComparison)
	if err != nil {
		t.Fatal(err)
	}
	if err := billingStore.AppendReconciliation(ctx, ReconciliationRecord{
		ID: "ph191-reconciliation", Version: 1, Subject: winnerObs.Subject, Scope: "call:ph191",
		Basis: economics.BasisProviderQuantityLocal, InputSetHash: strings.Repeat("9", 64),
		PolicyID: "ph191-policy", PolicyVersion: "v1", ResultJSON: reconciliationPayload, CreatedAt: time.Unix(1_700_191_300, 0).UTC(),
	}); err != nil {
		t.Fatalf("AppendReconciliation: %v", err)
	}

	// All-leg COGS rolls up every payable B-leg (1.32 + 0.50 + 0.25 + 0.10);
	// the never-started shell is excluded as a known zero, never as unknown.
	cogs, err := billing.AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if cogs.KnownSubtotal != (billing.Money{Nano: 2_170_000_000, Currency: "USD"}) || !cogs.Payable || cogs.Completeness != billing.CostCompletenessKnown {
		t.Fatalf("operator COGS = %+v, want known payable 2.17 USD", cogs)
	}
	if len(cogs.IncludedLegKeys) != 4 {
		t.Fatalf("COGS included = %v, want four payable B-legs", cogs.IncludedLegKeys)
	}
	if len(cogs.ExcludedLegKeys) != 1 || len(cogs.UnknownLegKeys) != 0 {
		t.Fatalf("COGS excluded=%v unknown=%v, want shell excluded and nothing unknown", cogs.ExcludedLegKeys, cogs.UnknownLegKeys)
	}

	// B-leg-rooted retail selection bills only the surfaced winner while COGS
	// stays all-leg; the two planes never collapse into each other.
	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedBLegs) != 1 || selection.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("retail selection = %+v, want winner only", selection.SelectedBLegs)
	}

	// R settlement through the production rating and posting seams. The
	// submission fee applies once for the whole call, not once per B-leg.
	rated, err := billing.RateCall(billing.CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: billing.Money{Nano: 100_000_000_000, Currency: "USD"},
		CustomerPricing: billing.PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:  policy, CustomerTariff: ph191CustomerTariff(t),
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	// R bills one frozen contractual inference basis, not competing channels:
	// provider quantity 7.44 (110*0.01+200*0.03+2*0.05+12*0.02) wins over the
	// independent local 7.20 for the same winner B-leg/component/work, plus
	// exactly one 1.00 submission fee for the whole call: 8.44. Both
	// observations stay in E/Q/P, reconciliation and query evidence; R lines
	// reference only the selected provider basis deterministically. Five
	// B-legs never multiply the call-scoped commercial fee.
	if rated.CustomerCharge != (billing.Money{Nano: 8_440_000_000, Currency: "USD"}) {
		t.Fatalf("R customer charge = %+v, want 8.44 USD (frozen provider basis + one fee)", rated.CustomerCharge)
	}
	// Frozen basis evidence: every R inference line references only the
	// provider observation; the losing local channel contributes no inference
	// line but remains in the durable legs, E/Q/P valuations, reconciliation
	// and QueryEconomicDetail below.
	providerID := winnerProvider[0].ID
	for _, line := range rated.CustomerValuation.Lines {
		if line.FixedFee != nil {
			continue
		}
		if line.Component == nil {
			t.Fatalf("R inference line without component: %+v", line)
		}
		if len(line.SourceObservationRefs) != 1 || line.SourceObservationRefs[0].ObservationID != providerID {
			t.Fatalf("R inference line source = %+v, want exactly provider %q (frozen basis)", line.SourceObservationRefs, providerID)
		}
		for _, ref := range line.SourceObservationRefs {
			if ref.ObservationID == "ph191-local-winner" {
				t.Fatalf("losing local channel leaked into R inference: %+v", line)
			}
		}
	}
	if len(legs[1].Observations) != 2 {
		t.Fatalf("winner leg observations = %d, want both channels preserved for E/Q/P", len(legs[1].Observations))
	}
	for _, leg := range legs {
		if err := billingStore.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatalf("append leg %q: %v", leg.BLegID, err)
		}
	}
	for _, key := range cogs.IncludedLegKeys {
		var leg billing.CallLegUsageRecord
		for _, candidate := range legs {
			sealed, err := candidate.Seal()
			if err != nil {
				t.Fatal(err)
			}
			if sealed.Key == key {
				leg = candidate
			}
		}
		amount, _, err := ph191OperatorCharge(t, leg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := billingStore.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
			AccountID: account.ID, CallID: callID, Leg: leg,
			Result: billing.OperatorCostResult{LURKey: key, Amount: amount, AmountPresent: true, Reconciled: true, Authoritative: true},
		}); err != nil {
			t.Fatalf("provider cost %q: %v", key, err)
		}
	}
	exposure, err := billingStore.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 100_000_000_000, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef, RouteTariffs: rated.RouteTariffs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := billingStore.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	settled, err := billingStore.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: rated})
	if err != nil {
		t.Fatalf("customer settlement: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("first settlement replayed: %+v", settled)
	}
	replay, err := billingStore.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: rated})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed {
		t.Fatalf("settlement replay = %+v, want idempotent replay", replay)
	}

	// Durable query and audit linkage: explanation carries every leg, the
	// customer settlement, both payable provider postings lineage and the
	// settled result; the economic detail view reconstructs source-separated
	// evidence for the call.
	explained, err := billingStore.CallExplanation(ctx, callID.String())
	if err != nil {
		t.Fatalf("CallExplanation: %v", err)
	}
	if len(explained.Legs) != 5 {
		t.Fatalf("explanation legs = %d, want all five durable legs", len(explained.Legs))
	}
	if len(explained.CustomerOperations) != 1 {
		t.Fatalf("customer operations = %+v, want exactly the winner-only settlement", explained.CustomerOperations)
	}
	if len(explained.ProviderCostOperations) != len(cogs.IncludedLegKeys) {
		t.Fatalf("provider operations = %d, want one per payable B-leg", len(explained.ProviderCostOperations))
	}
	if !explained.Result.Processed {
		t.Fatalf("explanation result not processed: %+v", explained.Result)
	}
	detail, err := billingStore.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: ph191StoreID, AccountID: account.ID, BillingCallID: callID.String(), Limit: 100,
	})
	if err != nil {
		t.Fatalf("QueryEconomicDetail: %v", err)
	}
	if len(detail.Observations) == 0 {
		t.Fatalf("economic detail has no observations: %+v", detail)
	}
	seenLocal, seenProvider := false, false
	for _, observation := range detail.Observations {
		switch observation.Origin {
		case metering.OriginLocal:
			seenLocal = true
		case metering.OriginProvider:
			seenProvider = true
		}
	}
	if !seenLocal || !seenProvider {
		t.Fatalf("economic detail lost source separation: local=%v provider=%v", seenLocal, seenProvider)
	}
}

// TestPhase191LifecycleResumeMissingAggregateCreditsSubmission certifies the
// resumable A-leg container, attempted-missing versus never-started honesty,
// aggregate-only provider money, trusted submission fees, credit units and
// synthetic non-token extensibility through the same production seams.
func TestPhase191LifecycleResumeMissingAggregateCreditsSubmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	billingStore, _, closeBilling := ph191BillingStore(t)
	defer closeBilling()

	account := billing.Account{ID: "ph191-resume-account", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100_000_000_000, State: billing.AccountReady, Version: 1}
	if err := billingStore.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	policy := ph191WinnerPolicy()

	// First call on the A-leg: winner plus a never-started shell. DONE seals
	// this BillingCallID; it never seals the A-leg.
	call1, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	winner1 := ph191DrainProviderCapture(t, call1, "b-c1-winner", 1, ph191WinnerEvent())
	legs1 := []billing.CallLegUsageRecord{
		ph191Leg(t, call1, "b-c1-winner", 1, billing.LegOutcomeWinner, billing.SurfacedYes, winner1, ph191V1Claim("b-c1-winner", 110, 200, 1_320_000_000)),
		ph191Leg(t, call1, "b-c1-shell", 2, billing.LegOutcomeNeverStarted, billing.SurfacedNo, nil, billing.FinalBillingEvidence{}),
	}
	callRecord1 := ph191Call(call1, account.ID, policy, "submission-191-a", "b-c1-winner", "b-c1-shell")
	rated1, err := billing.RateCall(billing.CallRatingInput{
		Call: callRecord1, Legs: legs1, MaxCustomerCharge: billing.Money{Nano: 100_000_000_000, Currency: "USD"},
		CustomerPricing: billing.PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:  policy, CustomerTariff: ph191CustomerTariff(t),
	})
	if err != nil {
		t.Fatalf("RateCall call1: %v", err)
	}
	// Provider winner usage 7.44 plus one 1.00 submission fee.
	if rated1.CustomerCharge != (billing.Money{Nano: 8_440_000_000, Currency: "USD"}) {
		t.Fatalf("call1 R charge = %+v, want 8.44 USD", rated1.CustomerCharge)
	}
	for _, leg := range legs1 {
		if err := billingStore.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatal(err)
		}
	}
	fingerprintBefore, err := legs1[0].Seal()
	if err != nil {
		t.Fatal(err)
	}
	beforeLeg, err := billingStore.GetCallLegUsage(ctx, fingerprintBefore.Key)
	if err != nil {
		t.Fatalf("GetCallLegUsage call1 winner before resume: %v", err)
	}
	exposure1, err := billingStore.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: call1.String(), Max: billing.Money{Nano: 100_000_000_000, Currency: "USD"},
		PricingRef: callRecord1.CustomerPricingRef, ChargePolicyRef: callRecord1.ChargePolicyRef, RouteTariffs: rated1.RouteTariffs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := billingStore.AppendCallUsage(ctx, callRecord1); err != nil {
		t.Fatal(err)
	}
	settled1, err := billingStore.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: callRecord1, Exposure: exposure1, Result: rated1})
	if err != nil {
		t.Fatalf("settle call1: %v", err)
	}
	if settled1.Replayed {
		t.Fatalf("first call1 settlement replayed")
	}

	// Same A-leg resumes after DONE: a fresh BillingCallID with new B-legs.
	// The resumed call carries an attempted leg with local evidence but no
	// provider charge (missing, never zero), an aggregate-only provider leg,
	// and a credit/time leg proving non-money units stay distinct.
	call2, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if call2 == call1 {
		t.Fatal("resumed call reused the sealed BillingCallID")
	}
	localOnly := ph191LocalWinnerObservation(t, call2,
		metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: ph191StoreID, ALegID: ph191ALeg,
			BillingCallID: call2.String(), BLegID: "b-c2-missing", AttemptID: "att-b-c2-missing", AttemptSeq: 1,
		},
		metering.CorrelationV2{
			StoreID: ph191StoreID, ALegID: ph191ALeg,
			BillingCallID: call2.String(), BLegID: "b-c2-missing", AttemptID: "att-b-c2-missing", AttemptSeq: 1,
		},
		"ph191-local-missing")
	aggregateOnly := ph191DrainProviderCapture(t, call2, "b-c2-aggregate", 2, lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 30, OutputTokens: 0,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		CostNanoUnits: 12_000_000_000, Currency: "USD", CostPresent: true,
		RawUsageJSON: `{"input_tokens":30,"output_tokens":0,"cost":12}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:             lipapi.UsageSourceProviderReported,
			ProviderAccountKey: "ph191-provider-acct", ProviderRequestID: "req-ph191-agg", ProviderChargeID: "chg-ph191-agg", DedupeKey: "ph191-agg",
		},
	})
	now := time.Unix(1_700_191_400, 0).UTC()
	creditObs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "ph191-credits", SourceEventKey: "ph191-credits-event",
		Revision: 1, StreamID: "ph191-credit-stream", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: ph191StoreID,
			ALegID: ph191ALeg, BillingCallID: call2.String(), BLegID: "b-c2-credits", AttemptSeq: 3,
		},
		Correlation: metering.CorrelationV2{StoreID: ph191StoreID, ALegID: ph191ALeg, BillingCallID: call2.String(), BLegID: "b-c2-credits", AttemptSeq: 3},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "ph191.credits.v1",
		Measures: []metering.Measure{
			{Key: metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider.credit", Unit: "credit", SchemaID: "ph191.credits.v1"}, Value: ph191Decimal(t, "0.125"), Quality: metering.QualityObserved, MethodRef: "ph191.credits.v1"},
			{Key: metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"}, Value: ph191Decimal(t, "1.250"), Quality: metering.QualityObserved, MethodRef: "ph191.credits.v1"},
			{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: "example.synthetic.widget", Unit: "widget", SchemaID: "example.synthetic.v1"}, Value: ph191Decimal(t, "7"), Quality: metering.QualityObserved, MethodRef: "example.synthetic.v1"},
		},
	}
	if err := creditObs.Validate(); err != nil {
		t.Fatalf("credit observation invalid: %v", err)
	}
	legs2 := []billing.CallLegUsageRecord{
		ph191Leg(t, call2, "b-c2-missing", 1, billing.LegOutcomeFailed, billing.SurfacedNo, []metering.Observation{localOnly}, billing.FinalBillingEvidence{}),
		ph191Leg(t, call2, "b-c2-aggregate", 2, billing.LegOutcomeWinner, billing.SurfacedYes, aggregateOnly, ph191V1Claim("b-c2-aggregate", 30, 0, 12_000_000_000)),
		ph191Leg(t, call2, "b-c2-credits", 3, billing.LegOutcomeFailed, billing.SurfacedNo, []metering.Observation{creditObs}, billing.FinalBillingEvidence{}),
	}
	callRecord2 := ph191Call(call2, account.ID, policy, "submission-191-b", "b-c2-missing", "b-c2-aggregate", "b-c2-credits")

	// Aggregate-only money is preserved without an invented component split.
	aggObs := aggregateOnly[0]
	if len(aggObs.Charges) != 1 || aggObs.Charges[0].Component != nil {
		t.Fatalf("aggregate charges = %+v, want one component-less aggregate", aggObs.Charges)
	}
	if got := aggObs.Charges[0].Amount.CanonicalString(); got != "12/0" {
		t.Fatalf("aggregate amount = %q, want 12/0", got)
	}

	// Attempted-with-missing-evidence stays unknown and partial; the
	// never-started shell from call1 remains excluded, never unknown.
	cogs2, err := billing.AttributeOperatorCOGS(legs2, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if cogs2.Completeness != billing.CostCompletenessPartial || cogs2.Payable {
		t.Fatalf("resumed COGS = %+v, want partial non-payable with missing evidence", cogs2)
	}
	foundUnknown := false
	for _, key := range cogs2.UnknownLegKeys {
		if strings.HasSuffix(key, ":b-c2-missing") {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatalf("unknown legs = %v, want the attempted-missing B-leg", cogs2.UnknownLegKeys)
	}
	if len(cogs2.ExcludedLegKeys) != 0 {
		t.Fatalf("excluded legs = %v, want none (no never-started leg in call2)", cogs2.ExcludedLegKeys)
	}

	// B-leg-rooted retail selection still bills only the surfaced winner; the
	// missing and credit legs never become customer inference usage.
	selection2, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: callRecord2, Legs: legs2, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection2.SelectedBLegs) != 1 || selection2.SelectedBLegs[0].BLegID != "b-c2-aggregate" {
		t.Fatalf("resumed retail selection = %+v, want aggregate winner only", selection2.SelectedBLegs)
	}

	// A second submission on the resumed call earns its own fee without
	// reopening the sealed first call: settle call2 and prove call1 is
	// byte-identical afterwards.
	rated2, err := billing.RateCall(billing.CallRatingInput{
		Call: callRecord2, Legs: legs2, MaxCustomerCharge: billing.Money{Nano: 100_000_000_000, Currency: "USD"},
		CustomerPricing: billing.PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:  policy, CustomerTariff: ph191CustomerTariff(t),
	})
	if err != nil {
		t.Fatalf("RateCall call2: %v", err)
	}
	// Aggregate winner usage 30*0.01 plus a new 1.00 fee for the resumed
	// submission; the missing and credit legs never enter retail usage.
	if rated2.CustomerCharge != (billing.Money{Nano: 1_300_000_000, Currency: "USD"}) {
		t.Fatalf("call2 R charge = %+v, want 1.30 USD", rated2.CustomerCharge)
	}
	for _, leg := range legs2 {
		if err := billingStore.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatal(err)
		}
	}
	exposure2, err := billingStore.AdmitExposure(ctx, billing.AdmitExposureInput{
		// The ceiling reflects the post-call1 balance: admission stays
		// conservative after real settlement instead of reusing the old max.
		AccountID: account.ID, CallID: call2.String(), Max: billing.Money{Nano: 50_000_000_000, Currency: "USD"},
		PricingRef: callRecord2.CustomerPricingRef, ChargePolicyRef: callRecord2.ChargePolicyRef, RouteTariffs: rated2.RouteTariffs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := billingStore.AppendCallUsage(ctx, callRecord2); err != nil {
		t.Fatal(err)
	}
	if _, err := billingStore.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: callRecord2, Exposure: exposure2, Result: rated2}); err != nil {
		t.Fatalf("settle call2: %v", err)
	}
	sealedAfter, err := billingStore.GetCallLegUsage(ctx, fingerprintBefore.Key)
	if err != nil {
		t.Fatalf("GetCallLegUsage call1 winner: %v", err)
	}
	if sealedAfter.Fingerprint != beforeLeg.Fingerprint {
		t.Fatalf("resume rewrote sealed call1 leg: before=%q after=%q", beforeLeg.Fingerprint, sealedAfter.Fingerprint)
	}
	explained1, err := billingStore.CallExplanation(ctx, call1.String())
	if err != nil {
		t.Fatalf("CallExplanation call1: %v", err)
	}
	if len(explained1.CustomerOperations) != 1 || len(explained1.Legs) != 2 {
		t.Fatalf("call1 explanation changed after resume: %+v", explained1)
	}
	explained2, err := billingStore.CallExplanation(ctx, call2.String())
	if err != nil {
		t.Fatalf("CallExplanation call2: %v", err)
	}
	if len(explained2.Legs) != 3 {
		t.Fatalf("call2 explanation legs = %d, want three", len(explained2.Legs))
	}
	if rated1.CustomerCharge == rated2.CustomerCharge {
		t.Fatalf("resumed submission charge %+v equals first call charge; submission fees must be per-submission", rated2.CustomerCharge)
	}

	// Credit units round-trip exactly and never become money. The provider
	// credit measure has no money rule, so rating the full observation fails
	// closed with a typed rate-missing error instead of coercing credits
	// into currency; rating the monetizable subset completes exactly.
	// The synthetic non-token component persists without any storage change
	// and rates only through its own schema rule.
	creditTariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "ph191-credit-tariff", Version: "v1"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{
			{
				ID: "audio-out", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.02"),
			},
			{
				ID: "synthetic-widget", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionInput, Component: "example.synthetic.widget", Unit: "widget", SchemaID: "example.synthetic.v1"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.10"),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := billing.RateWithTariff(ctx, ph191RatingInput(t, economics.BasisProviderQuantityLocal, creditTariff, []metering.Observation{creditObs}), creditTariff); !isRateMissingError(err) {
		t.Fatalf("full credit rating err = %v, want typed rate-missing failure", err)
	}
	monetizable := creditObs.Clone()
	monetizable.ID = "ph191-credits-monetizable"
	monetizable.SourceEventKey = "ph191-credits-monetizable-event"
	kept := monetizable.Measures[:0]
	for _, measure := range monetizable.Measures {
		if measure.Key.Component == "provider.credit" {
			continue
		}
		kept = append(kept, measure)
	}
	monetizable.Measures = kept
	creditValuation, err := billing.RateWithTariff(ctx, ph191RatingInput(t, economics.BasisProviderQuantityLocal, creditTariff, []metering.Observation{monetizable}), creditTariff)
	if err != nil {
		t.Fatalf("RateWithTariff credits: %v", err)
	}
	// 1.250s * 0.02 + 7 widgets * 0.10 = 0.025 + 0.70 = 0.725. The 0.125
	// credit measure has no money rule and must not be coerced into currency.
	if got := ph191Total(creditValuation, "USD"); got != "725/3" {
		t.Fatalf("credit valuation total = %q, want 725/3", got)
	}
	for _, line := range creditValuation.Lines {
		if line.Component != nil && line.Component.Component == "provider.credit" {
			t.Fatalf("credit measure was monetized: %+v", line)
		}
	}
	if err := billingStore.AppendValuation(ctx, creditValuation); err != nil {
		t.Fatalf("AppendValuation credits: %v", err)
	}
}

// TestPhase191LifecycleMultimodalTransforms certifies image/audio/video
// economics in both directions with provider-bound versus customer-delivered
// separation through the production OpenAI-family adapter and reference
// rater: no text-token coercion and no input/output collision.
func TestPhase191LifecycleMultimodalTransforms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	// Provider-bound representation after resize/transcode: what inference
	// actually consumed. Customer-delivered form is a separate observation.
	providerDrained := ph191DrainProviderCapture(t, callID, "b-media", 1, lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 50, OutputTokens: 60,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		RawUsageJSON: `{"input_tokens":50,"output_tokens":60,"input_image_tokens":4,"output_image_tokens":7,` +
			`"input_audio_seconds":3.5,"output_audio_seconds":12,"input_video_seconds":2.0,"output_video_frames":48,` +
			`"input_document_pages":2,"output_bytes":4096}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:             lipapi.UsageSourceProviderReported,
			ProviderAccountKey: "ph191-provider-acct", ProviderRequestID: "req-ph191-media", ProviderChargeID: "chg-ph191-media", DedupeKey: "ph191-media",
		},
	})
	providerObs := providerDrained[0]
	now := time.Unix(1_700_191_500, 0).UTC()
	deliveredObs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "ph191-delivered", SourceEventKey: "ph191-delivered-event",
		Revision: 1, StreamID: "ph191-delivered-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: ph191StoreID,
			ALegID: ph191ALeg, BillingCallID: callID.String(), BLegID: "b-media", AttemptSeq: 1,
		},
		Correlation: metering.CorrelationV2{StoreID: ph191StoreID, ALegID: ph191ALeg, BillingCallID: callID.String(), BLegID: "b-media", AttemptSeq: 1},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "ph191.delivery.v1",
		Measures: []metering.Measure{
			{Key: metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"}, Value: ph191Decimal(t, "10"), Quality: metering.QualityObserved, MethodRef: "ph191.delivery.v1"},
		},
	}
	if err := deliveredObs.Validate(); err != nil {
		t.Fatalf("delivered observation invalid: %v", err)
	}

	// Every media direction/unit key is disjoint; nothing collapses into text
	// tokens and input never collides with output on the same unit.
	seenKeys := make(map[string]int)
	for _, measure := range providerObs.Measures {
		seenKeys[measure.Key.CanonicalKey()]++
		if measure.Key.Component == metering.ComponentInputToken || measure.Key.Component == metering.ComponentOutputToken {
			continue
		}
		if measure.Key.Unit == metering.UnitToken && measure.Key.Component != metering.ComponentImageToken &&
			measure.Key.Component != metering.ComponentAudioToken && measure.Key.Component != metering.ComponentVideoToken &&
			measure.Key.Component != metering.ComponentDocumentToken && measure.Key.Component != metering.ComponentTextToken {
			t.Fatalf("non-token media coerced into generic tokens: %+v", measure.Key)
		}
	}
	for key, count := range seenKeys {
		if count != 1 {
			t.Fatalf("provider media key %q appears %d times, want disjoint keys", key, count)
		}
	}
	imageIn := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}.CanonicalKey()
	imageOut := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}.CanonicalKey()
	if imageIn == imageOut {
		t.Fatal("image input/output canonical keys collide")
	}

	// The supplier record preserves provider-side 12s audio; the delivered
	// 10s form is separate evidence, never a replacement.
	providerAudio := ph191Decimal(t, "12")
	deliveredAudio := ph191Decimal(t, "10")
	foundProviderAudio := false
	for _, measure := range providerObs.Measures {
		if measure.Key.Direction == metering.DirectionOutput && measure.Key.Component == metering.ComponentAudio && measure.Key.Unit == metering.UnitSecond {
			foundProviderAudio = true
			if measure.Value.CanonicalString() != providerAudio.CanonicalString() {
				t.Fatalf("provider audio = %q, want 12", measure.Value.CanonicalString())
			}
		}
	}
	if !foundProviderAudio {
		t.Fatalf("provider output audio missing: %+v", providerObs.Measures)
	}
	if got := deliveredObs.Measures[0].Value.CanonicalString(); got != deliveredAudio.CanonicalString() {
		t.Fatalf("delivered audio = %q, want 10", got)
	}

	// Direction-specific rating: each tariff line rates only its compatible
	// component; unlike modalities and directions never share one line.
	mediaTariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "ph191-media-tariff", Version: "v1"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{
			{
				ID: "image-in", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.05"),
			},
			{
				ID: "image-out", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.20"),
			},
			{
				ID: "audio-in", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.01"),
			},
			{
				ID: "audio-out", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.02"),
			},
			{
				ID: "video-in", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentVideo, Unit: metering.UnitSecond, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.03"),
			},
			{
				ID: "video-out", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentVideo, Unit: metering.UnitFrame, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.001"),
			},
			{
				ID: "doc-in", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentDocument, Unit: metering.UnitPage, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.04"),
			},
			{
				ID: "bytes-out", Kind: economics.RatingRuleLinear,
				Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentFile, Unit: metering.UnitByte, SchemaID: "openai.usage.v2"},
				Currency:  "USD", UnitPrice: ph191Decimal(t, "0.00001"),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mediaOnly := make([]metering.Measure, 0, len(providerObs.Measures))
	for _, measure := range providerObs.Measures {
		if measure.Key.Component == metering.ComponentInputToken || measure.Key.Component == metering.ComponentOutputToken {
			continue
		}
		mediaOnly = append(mediaOnly, measure)
	}
	mediaObs := providerObs.Clone()
	mediaObs.ID = "ph191-media-only"
	mediaObs.SourceEventKey = "ph191-media-only-event"
	mediaObs.Measures = mediaOnly
	mediaValuation, err := billing.RateWithTariff(ctx, ph191RatingInput(t, economics.BasisProviderQuantityLocal, mediaTariff, []metering.Observation{mediaObs}), mediaTariff)
	if err != nil {
		t.Fatalf("RateWithTariff media: %v", err)
	}
	// 4*0.05 + 7*0.20 + 3.5*0.01 + 12*0.02 + 2*0.03 + 48*0.001 + 2*0.04 +
	// 4096*0.00001 = 0.20+1.40+0.035+0.24+0.06+0.048+0.08+0.04096 = 2.10396.
	if got := ph191Total(mediaValuation, "USD"); got != "210396/5" {
		t.Fatalf("media valuation total = %q, want 210396/5", got)
	}
	ruleIDs := make(map[string]int)
	for _, line := range mediaValuation.Lines {
		ruleIDs[line.RuleID]++
	}
	for _, want := range []string{"image-in", "image-out", "audio-in", "audio-out", "video-in", "video-out", "doc-in", "bytes-out"} {
		if ruleIDs[want] != 1 {
			t.Fatalf("media rating lines = %v, want exactly one %q line", ruleIDs, want)
		}
	}
}

// isRateMissingError reports the typed fail-closed outcome for an unratable
// component: no silent zero, no currency coercion.
func isRateMissingError(err error) bool {
	return errors.Is(err, billing.ErrRateMissing)
}

// ph191OperatorCharge extracts the exact operator-payable amount and immutable
// observation ref from one leg, failing closed unless the leg carries exactly
// one operator-payer charge.
func ph191OperatorCharge(t *testing.T, leg billing.CallLegUsageRecord) (billing.Money, metering.ObservationRef, error) {
	t.Helper()
	count := 0
	var amount billing.Money
	var ref metering.ObservationRef
	for _, observation := range leg.Observations {
		for _, charge := range observation.Charges {
			if charge.Payer.Kind != metering.PaymentPartyOperator || charge.Amount == nil {
				continue
			}
			count++
			nano, err := charge.Amount.ToNanoUnits()
			if err != nil {
				return billing.Money{}, metering.ObservationRef{}, err
			}
			amount = billing.Money{Nano: nano, Currency: charge.Currency}
			ref, err = observation.Ref(observation.Subject.StoreID)
			if err != nil {
				return billing.Money{}, metering.ObservationRef{}, err
			}
		}
	}
	if count != 1 {
		t.Fatalf("leg %q has %d operator charges, want exactly one", leg.BLegID, count)
	}
	return amount, ref, nil
}
