package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// phase171V1MigrationFixture is the named baseline V1 fixture for Task 17.1.
// It pins Migration Strategy steps 1-3 baseline bytes/hashes for the
// historical compatibility reader: the same sealed V1 call/leg identity as
// the Phase 1 baseline, now with explicit writer version and legacy
// semantics for the new storage/query path.
//
//go:embed testdata/phase17_1_v1_migration_baseline.json
var phase171V1MigrationFixture embed.FS

type phase171V1FixtureDocument struct {
	FixtureVersion  string `json:"fixture_version"`
	WriterVersion   string `json:"writer_version"`
	LegacySemantics string `json:"legacy_semantics"`
	Call            struct {
		Payload             json.RawMessage `json:"payload"`
		PayloadSHA256       string          `json:"payload_sha256"`
		Key                 string          `json:"key"`
		SemanticFingerprint string          `json:"semantic_fingerprint"`
	} `json:"call"`
	Leg struct {
		Payload             json.RawMessage `json:"payload"`
		PayloadSHA256       string          `json:"payload_sha256"`
		Key                 string          `json:"key"`
		SemanticFingerprint string          `json:"semantic_fingerprint"`
	} `json:"leg"`
	PostingIdentity map[string]string `json:"posting_identity"`
}

func readPhase171V1Fixture(t *testing.T) phase171V1FixtureDocument {
	t.Helper()
	raw, err := phase171V1MigrationFixture.ReadFile("testdata/phase17_1_v1_migration_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture phase171V1FixtureDocument
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode phase17.1 fixture: %v", err)
	}
	if fixture.FixtureVersion != "phase17-1-v1-migration-baseline" {
		t.Fatalf("fixture version = %q", fixture.FixtureVersion)
	}
	if fixture.WriterVersion != billing.HistoricalV1WriterVersion {
		t.Fatalf("fixture writer = %q, want v1", fixture.WriterVersion)
	}
	if fixture.LegacySemantics != billing.LegacyScalarSemantics {
		t.Fatalf("fixture legacy semantics = %q, want %q", fixture.LegacySemantics, billing.LegacyScalarSemantics)
	}
	return fixture
}

func assertPhase171PayloadSHA(t *testing.T, name string, raw json.RawMessage, want string) {
	t.Helper()
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("%s payload SHA = %s, want %s", name, got, want)
	}
}

func phase171SeedBaselineV1(t *testing.T, store *DurableStore) (billing.CallUsageRecord, billing.CallLegUsageRecord) {
	t.Helper()
	ctx := context.Background()

	fixture := readPhase171V1Fixture(t)
	assertPhase171PayloadSHA(t, "call", fixture.Call.Payload, fixture.Call.PayloadSHA256)
	assertPhase171PayloadSHA(t, "leg", fixture.Leg.Payload, fixture.Leg.PayloadSHA256)

	var call billing.CallUsageRecord
	if err := json.Unmarshal(fixture.Call.Payload, &call); err != nil {
		t.Fatalf("decode call payload: %v", err)
	}
	var leg billing.CallLegUsageRecord
	if err := json.Unmarshal(fixture.Leg.Payload, &leg); err != nil {
		t.Fatalf("decode leg payload: %v", err)
	}
	sealedCall, err := call.Seal()
	if err != nil {
		t.Fatalf("seal call: %v", err)
	}
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal leg: %v", err)
	}
	if sealedCall.Key != fixture.Call.Key || sealedCall.Fingerprint != fixture.Call.SemanticFingerprint {
		t.Fatalf("call identity = %q/%q, want %q/%q", sealedCall.Key, sealedCall.Fingerprint, fixture.Call.Key, fixture.Call.SemanticFingerprint)
	}
	if sealedLeg.Key != fixture.Leg.Key || sealedLeg.Fingerprint != fixture.Leg.SemanticFingerprint {
		t.Fatalf("leg identity = %q/%q, want %q/%q", sealedLeg.Key, sealedLeg.Fingerprint, fixture.Leg.Key, fixture.Leg.SemanticFingerprint)
	}
	if _, err := billing.WriterVersionForLeg(sealedLeg); err != nil {
		t.Fatalf("baseline leg must be V1-owned: %v", err)
	}
	if err := store.AppendCallUsage(ctx, sealedCall); err != nil {
		t.Fatalf("AppendCallUsage: %v", err)
	}
	if err := store.AppendCallLegUsage(ctx, sealedLeg); err != nil {
		t.Fatalf("AppendCallLegUsage: %v", err)
	}
	return sealedCall, sealedLeg
}

func phase171JournalCount(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM journal_transactions WHERE account_id = ?`, accountID).Scan(context.Background(), &count); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("journal count: %v", err)
	}
	return count
}

func TestPhase171HistoricalV1ReaderRoundTripsBaselineThroughStore(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sealedCall, sealedLeg := phase171SeedBaselineV1(t, store)

	bundle, err := store.ReadHistoricalV1CallBundle(ctx, sealedCall.CallID)
	if err != nil {
		t.Fatalf("ReadHistoricalV1CallBundle: %v", err)
	}
	if bundle.Call.Key != sealedCall.Key || bundle.Call.Fingerprint != sealedCall.Fingerprint {
		t.Fatalf("call bundle identity = %q/%q, want %q/%q", bundle.Call.Key, bundle.Call.Fingerprint, sealedCall.Key, sealedCall.Fingerprint)
	}
	if len(bundle.Legs) != 1 {
		t.Fatalf("bundle legs = %d, want 1", len(bundle.Legs))
	}
	got := bundle.Legs[0]
	if got.Key != sealedLeg.Key || got.Fingerprint != sealedLeg.Fingerprint {
		t.Fatalf("leg bundle identity = %q/%q, want %q/%q", got.Key, got.Fingerprint, sealedLeg.Key, sealedLeg.Fingerprint)
	}
	if got.BreakdownAvailable || got.SourceSeparationAvailable {
		t.Fatalf("V1 bundle must not invent breakdown/source separation: %+v", got)
	}
	if got.LegacySemantics != billing.LegacyScalarSemantics {
		t.Fatalf("legacy semantics = %q, want %q", got.LegacySemantics, billing.LegacyScalarSemantics)
	}
	if bundle.WriterVersion != billing.HistoricalV1WriterVersion {
		t.Fatalf("bundle writer = %q, want v1", bundle.WriterVersion)
	}

	single, err := store.ReadHistoricalV1Leg(ctx, sealedCall.CallID, sealedLeg.BLegID)
	if err != nil {
		t.Fatalf("ReadHistoricalV1Leg: %v", err)
	}
	if single.Key != sealedLeg.Key || single.Fingerprint != sealedLeg.Fingerprint || single.PayloadSHA256 != got.PayloadSHA256 {
		t.Fatalf("single leg view mismatch: %+v vs %+v", single, got)
	}
}

func TestPhase171HistoricalV1ReaderLeavesBalancesUnchanged(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sealedCall, _ := phase171SeedBaselineV1(t, store)

	beforeCount := phase171JournalCount(t, store, sealedCall.AccountID)
	beforeBundle, err := store.ReadHistoricalV1CallBundle(ctx, sealedCall.CallID)
	if err != nil {
		t.Fatalf("pre-read bundle: %v", err)
	}
	afterCount := phase171JournalCount(t, store, sealedCall.AccountID)
	if afterCount != beforeCount {
		t.Fatalf("compatibility read mutated journal effects: before=%d after=%d", beforeCount, afterCount)
	}
	afterBundle, err := store.ReadHistoricalV1CallBundle(ctx, sealedCall.CallID)
	if err != nil {
		t.Fatalf("post-read bundle: %v", err)
	}
	if beforeBundle.Call.Fingerprint != afterBundle.Call.Fingerprint || beforeBundle.Legs[0].Fingerprint != afterBundle.Legs[0].Fingerprint {
		t.Fatal("repeated compatibility reads must preserve replay identity")
	}
}

func TestPhase171HistoricalV1WriterFenceV2CannotClaim(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sealedCall, _ := phase171SeedBaselineV1(t, store)

	if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.HistoricalV1WriterVersion); err != nil {
		t.Fatalf("V1 claim on V1-owned call: %v", err)
	}
	if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.V2WriterVersion); !errors.Is(err, billing.ErrHistoricalV1WriterConflict) {
		t.Fatalf("V2 claim on V1-owned call = %v, want ErrHistoricalV1WriterConflict", err)
	}
}

func TestPhase171HistoricalV1ReaderRejectsMalformed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sealedCall, sealedLeg := phase171SeedBaselineV1(t, store)

	if _, err := store.ReadHistoricalV1Leg(ctx, sealedCall.CallID, "  "); err == nil {
		t.Fatal("blank B-leg identity must fail closed")
	}
	if _, err := store.ReadHistoricalV1Leg(ctx, sealedCall.CallID, sealedLeg.BLegID+"-missing"); err == nil {
		t.Fatal("unknown B-leg must fail closed")
	}
	if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, "bogus"); err == nil {
		t.Fatal("bogus claimant must fail closed")
	}
	var missing billing.BillingCallID
	if err := missing.Validate(); err == nil {
		t.Fatal("test precondition: zero call ID must be invalid")
	}
}
