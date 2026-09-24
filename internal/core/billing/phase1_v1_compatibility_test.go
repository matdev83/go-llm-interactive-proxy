package billing

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// phase1V1CompatibilityFixture is a checked-in serialization contract for the
// current V1 bridge. It is intentionally independent from the future V2 DTOs.
// A change to any payload field order, identity, or hash must update this
// fixture only as part of an approved schema migration.
//
//go:embed testdata/phase1_v1_compatibility.json
var phase1V1CompatibilityFixture embed.FS

type phase1SerializedRecord struct {
	Payload             json.RawMessage `json:"payload"`
	PayloadSHA256       string          `json:"payload_sha256"`
	Key                 string          `json:"key"`
	SemanticFingerprint string          `json:"semantic_fingerprint"`
}

type phase1IdentitySchema struct {
	CurrentRecordSchemaVersion int    `json:"current_record_schema_version"`
	JournalFingerprintPrefix   string `json:"journal_fingerprint_prefix"`
	ProviderCostFingerprint    string `json:"provider_cost_fingerprint_prefix"`
	CustomerSettlementPrefix   string `json:"customer_settlement_prefix"`
	ProviderCostSourcePrefix   string `json:"provider_cost_source_prefix"`
	CallKey                    string `json:"call_key"`
	LegKey                     string `json:"leg_key"`
	ProviderCostSourceKey      string `json:"provider_cost_source_key"`
}

type phase1V1CompatibilityFixtureDocument struct {
	Call         phase1SerializedRecord `json:"call"`
	Leg          phase1SerializedRecord `json:"leg"`
	ProviderCost phase1SerializedRecord `json:"provider_cost"`
	Journal      phase1SerializedRecord `json:"journal"`
	Identity     phase1IdentitySchema   `json:"identity_schema"`
}

func readPhase1V1CompatibilityFixture(t *testing.T) phase1V1CompatibilityFixtureDocument {
	t.Helper()
	payload, err := phase1V1CompatibilityFixture.ReadFile("testdata/phase1_v1_compatibility.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture phase1V1CompatibilityFixtureDocument
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatalf("decode V1 compatibility fixture: %v", err)
	}
	return fixture
}

func assertPhase1SerializedPayload(t *testing.T, name string, raw json.RawMessage, wantSHA string) {
	t.Helper()
	if len(raw) == 0 || !json.Valid(raw) {
		t.Fatalf("%s payload is empty or invalid JSON", name)
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != wantSHA {
		t.Fatalf("%s payload SHA-256 = %s, want %s", name, got, wantSHA)
	}
}

func assertPhase1PayloadRoundTrip(t *testing.T, name string, raw json.RawMessage, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s payload: %v", name, err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("%s payload changed on typed round-trip:\n got  %s\n want %s", name, encoded, raw)
	}
}

func TestPhase1V1SerializedCallLegAndPostingFixtures(t *testing.T) {
	t.Parallel()
	fixture := readPhase1V1CompatibilityFixture(t)

	var call CallUsageRecord
	if err := json.Unmarshal(fixture.Call.Payload, &call); err != nil {
		t.Fatalf("decode call payload: %v", err)
	}
	sealedCall, err := call.Seal()
	if err != nil {
		t.Fatalf("seal call payload: %v", err)
	}
	assertPhase1SerializedPayload(t, "call", fixture.Call.Payload, fixture.Call.PayloadSHA256)
	assertPhase1PayloadRoundTrip(t, "call", fixture.Call.Payload, sealedCall)
	if sealedCall.Key != fixture.Call.Key || sealedCall.Fingerprint != fixture.Call.SemanticFingerprint {
		t.Fatalf("call identity = key %q/fingerprint %q, want %q/%q", sealedCall.Key, sealedCall.Fingerprint, fixture.Call.Key, fixture.Call.SemanticFingerprint)
	}

	var leg CallLegUsageRecord
	if err := json.Unmarshal(fixture.Leg.Payload, &leg); err != nil {
		t.Fatalf("decode leg payload: %v", err)
	}
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal leg payload: %v", err)
	}
	assertPhase1SerializedPayload(t, "leg", fixture.Leg.Payload, fixture.Leg.PayloadSHA256)
	assertPhase1PayloadRoundTrip(t, "leg", fixture.Leg.Payload, sealedLeg)
	if sealedLeg.Key != fixture.Leg.Key || sealedLeg.Fingerprint != fixture.Leg.SemanticFingerprint {
		t.Fatalf("leg identity = key %q/fingerprint %q, want %q/%q", sealedLeg.Key, sealedLeg.Fingerprint, fixture.Leg.Key, fixture.Leg.SemanticFingerprint)
	}

	var providerCost OperatorCostResult
	if err := json.Unmarshal(fixture.ProviderCost.Payload, &providerCost); err != nil {
		t.Fatalf("decode provider cost payload: %v", err)
	}
	providerCostFingerprint, err := providerCost.SemanticFingerprint()
	if err != nil {
		t.Fatalf("provider cost fingerprint: %v", err)
	}
	assertPhase1SerializedPayload(t, "provider cost", fixture.ProviderCost.Payload, fixture.ProviderCost.PayloadSHA256)
	assertPhase1PayloadRoundTrip(t, "provider cost", fixture.ProviderCost.Payload, providerCost)
	if providerCostFingerprint != fixture.ProviderCost.SemanticFingerprint {
		t.Fatalf("provider cost fingerprint = %q, want %q", providerCostFingerprint, fixture.ProviderCost.SemanticFingerprint)
	}

	var journal JournalTransaction
	if err := json.Unmarshal(fixture.Journal.Payload, &journal); err != nil {
		t.Fatalf("decode journal payload: %v", err)
	}
	sealedJournal, err := journal.Seal()
	if err != nil {
		t.Fatalf("seal journal payload: %v", err)
	}
	assertPhase1SerializedPayload(t, "journal", fixture.Journal.Payload, fixture.Journal.PayloadSHA256)
	assertPhase1PayloadRoundTrip(t, "journal", fixture.Journal.Payload, sealedJournal)
	journalFingerprint, err := journal.CanonicalFingerprint()
	if err != nil {
		t.Fatalf("journal fingerprint: %v", err)
	}
	if journalFingerprint != fixture.Journal.SemanticFingerprint || sealedJournal.SemanticFingerprint != fixture.Journal.SemanticFingerprint {
		t.Fatalf("journal fingerprint = %q/%q, want %q", journalFingerprint, sealedJournal.SemanticFingerprint, fixture.Journal.SemanticFingerprint)
	}

	if got, err := CallUsageKey(sealedCall.CallID); err != nil || got != fixture.Identity.CallKey {
		t.Fatalf("call key = %q/%v, want %q", got, err, fixture.Identity.CallKey)
	}
	if got, err := CallLegUsageKey(sealedLeg.CallID, sealedLeg.BLegID); err != nil || got != fixture.Identity.LegKey {
		t.Fatalf("leg key = %q/%v, want %q", got, err, fixture.Identity.LegKey)
	}
	if got, err := ProviderCostSourceKey(providerCost.LURKey); err != nil || got != fixture.Identity.ProviderCostSourceKey {
		t.Fatalf("provider cost source key = %q/%v, want %q", got, err, fixture.Identity.ProviderCostSourceKey)
	}
	customerSource, err := CustomerSettlementSourceKey(sealedCall.AccountID, sealedCall.CallID)
	if err != nil {
		t.Fatalf("customer source key: %v", err)
	}
	if !strings.HasPrefix(customerSource, fixture.Identity.CustomerSettlementPrefix) {
		t.Fatalf("customer source key %q lacks %q prefix", customerSource, fixture.Identity.CustomerSettlementPrefix)
	}
	if CurrentRecordSchemaVersion != fixture.Identity.CurrentRecordSchemaVersion || JournalFingerprintPrefix != fixture.Identity.JournalFingerprintPrefix {
		t.Fatalf("schema identity changed: record=%d journal=%q; fixture record=%d journal=%q", CurrentRecordSchemaVersion, JournalFingerprintPrefix, fixture.Identity.CurrentRecordSchemaVersion, fixture.Identity.JournalFingerprintPrefix)
	}
	if !strings.HasPrefix(providerCostFingerprint, fixture.Identity.ProviderCostFingerprint) {
		t.Fatalf("provider cost fingerprint %q lacks %q prefix", providerCostFingerprint, fixture.Identity.ProviderCostFingerprint)
	}
}

func TestPhase1V1SerializedFixturesRejectIdentityMutation(t *testing.T) {
	t.Parallel()
	fixture := readPhase1V1CompatibilityFixture(t)
	var call CallUsageRecord
	if err := json.Unmarshal(fixture.Call.Payload, &call); err != nil {
		t.Fatal(err)
	}
	var leg CallLegUsageRecord
	if err := json.Unmarshal(fixture.Leg.Payload, &leg); err != nil {
		t.Fatal(err)
	}
	mutatedCall := call
	mutatedCall.AccountID = "acct-mutated"
	mutatedCall, err := mutatedCall.Seal()
	if err != nil {
		t.Fatal(err)
	}
	var existingCall CallUsageRecord
	if err := json.Unmarshal(fixture.Call.Payload, &existingCall); err != nil {
		t.Fatal(err)
	}
	if err := CheckCallUsageReplay(existingCall, mutatedCall); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("mutated call replay = %v, want ErrReplayConflict", err)
	}
	mutatedLeg := leg
	mutatedLeg.AttemptSeq++
	mutatedLeg, err = mutatedLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	var existingLeg CallLegUsageRecord
	if err := json.Unmarshal(fixture.Leg.Payload, &existingLeg); err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(existingLeg, mutatedLeg); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("mutated leg replay = %v, want ErrReplayConflict", err)
	}
}

func TestPhase1V1FixtureHasNoRawEconomicContent(t *testing.T) {
	t.Parallel()
	fixture := readPhase1V1CompatibilityFixture(t)
	for name, raw := range map[string]json.RawMessage{
		"call": fixture.Call.Payload, "leg": fixture.Leg.Payload, "provider_cost": fixture.ProviderCost.Payload, "journal": fixture.Journal.Payload,
	} {
		text := strings.ToLower(string(raw))
		for _, forbidden := range []string{"prompt", "completion", "header", "authorization", "secret", "credential", "raw_usage_json"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s fixture contains forbidden raw economic content marker %q", name, forbidden)
			}
		}
	}
}
