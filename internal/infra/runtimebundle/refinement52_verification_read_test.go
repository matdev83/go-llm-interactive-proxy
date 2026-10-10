package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

const (
	billingHostLoopBackendID    = "backend"
	billingHostLoopModelID      = "model"
	billingHostLoopHoldNano     = int64(1000)
	billingHostLoopOpeningNano  = int64(10000)
	billingHostLoopInputTokens  = 1_000_000
	billingHostLoopOutputTokens = 1_000_000
	billingHostLoopCustomerNano = int64(310) // 100 input + 200 output + 10 fixed
	billingHostLoopOperatorNano = int64(125) // 50 input + 75 output
)

// refinement52VerificationBudget bounds the verification and restart-replay
// tail after the refinement5.2 sentinel has already proven durable
// convergence. It is deliberately rooted at context.Background rather than at
// the convergence sentinel: under race-heavy package load a healthy-but-slow
// relay drain can consume the whole sentinel before the verification reads
// run, and a read reusing that exhausted parent then fails with
// context.DeadlineExceeded even though the durable state is complete (the
// 60.43s Linux race failure at ListCallLegUsage after convergence). This only
// bounds the post-convergence verification and restart-replay lifecycle; the
// convergence deadline itself is unchanged, and missing convergence is still
// surfaced by the sentinel-scoped head/amount waits before any read here.
const refinement52VerificationBudget = 60 * time.Second

// refinement52VerificationContext returns a fresh bounded context for the
// post-convergence verification reads of the refinement5.2 integration tests.
func refinement52VerificationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), refinement52VerificationBudget)
}

// TestRefinement52VerificationReadSurvivesExhaustedConvergenceParent is the
// deterministic regression for the Linux race failure at
// "ListCallLegUsage after convergence: context deadline exceeded". It
// reproduces the exact condition: the convergence sentinel parent is already
// exhausted when the post-convergence verification read runs, while the
// durable leg is present. The raw parent-scoped read fails with
// context.DeadlineExceeded; the fresh bounded verification context reads the
// same durable row, proving the failure was the expired parent and not missing
// convergence.
func TestRefinement52VerificationReadSurvivesExhaustedConvergenceParent(t *testing.T) {
	t.Parallel()
	store := openRefinement52ConcurrentBillingStore(t, filepath.Join(t.TempDir(), "billing.sqlite"), "refinement52-verification-read")
	callID := billing.BillingCallID("bc_0000000000000000000000000000007a")
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), TenantID: "refinement52-verification-tenant",
		AccountID: "refinement52-verification-account", ALegID: "refinement52-verification-a-leg",
		BillingCallID: callID.String(), BLegID: "refinement52-verification-b-leg",
		AttemptID: "refinement52-verification-attempt", AttemptSeq: 1, ProviderAccountKey: "refinement52-verification-provider",
	}
	observation := refinement4StockObservation(subject, "refinement52-verification-observation", 1, 7, metering.SemanticsDelta, nil)
	sealed, err := refinement4StockCallLeg(observation).Seal()
	if err != nil {
		t.Fatalf("seal durable verification leg: %v", err)
	}
	if err := store.AppendLeg(context.Background(), sealed); err != nil {
		t.Fatalf("append durable verification leg: %v", err)
	}

	exhausted, exhaustedCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer exhaustedCancel()
	if _, err := store.ListCallLegUsage(exhausted, callID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted convergence parent must fail the raw verification read, got %v", err)
	}

	verifyCtx, verifyCancel := refinement52VerificationContext(t)
	defer verifyCancel()
	legs, err := store.ListCallLegUsage(verifyCtx, callID)
	if err != nil {
		t.Fatalf("fresh bounded verification context must read the durable leg: %v", err)
	}
	if len(legs) != 1 || legs[0].BLegID != sealed.BLegID || legs[0].Fingerprint != sealed.Fingerprint {
		t.Fatalf("verification read = %+v, want the durable leg %+v", legs, sealed)
	}
}

func openRefinement52ConcurrentBillingStore(t *testing.T, path, storeID string) *billingstore.DurableStore {
	t.Helper()
	runtimebundle.PrepareBillingSchemaForTest(t, path)
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = sqlDB.Close()
	})
	return store
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

func observationCallID(observation metering.Observation) billing.BillingCallID {
	return billing.BillingCallID(observation.Subject.BillingCallID)
}
