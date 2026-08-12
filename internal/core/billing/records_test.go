package billing

import (
	"errors"
	"testing"
	"time"
)

func testTurnRecord() TurnUsageRecord {
	return TurnUsageRecord{
		SchemaVersion:      CurrentRecordSchemaVersion,
		AccountID:          "acct-1",
		TurnID:             "turn-1",
		ALegID:             "a-1",
		AuthorizationID:    "auth-1",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            TurnOutcomeCompleted,
		CustomerPricingRef: VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    VersionRef{ID: "policy", Version: "v2"},
		Legs: []LegUsageRecord{
			{
				ALegID: "a-1", BLegID: "b-1", Seq: 1,
				BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a",
				Outcome: LegOutcomeFailed, Surfaced: SurfacedNo,
				StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
				Evidence: FinalBillingEvidence{
					InputTokens:  Quantity{Value: 7, Present: true},
					OutputTokens: Quantity{Value: 3, Present: true},
					Cost:         MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
					Source:       EvidenceSourceProviderReported, Authority: EvidenceAuthorityAuthoritative,
					DedupeKey: "provider-charge-1",
				},
				OperatorRateRef: VersionRef{ID: "operator-rates", Version: "v4"},
			},
			{
				ALegID: "a-1", BLegID: "b-2", Seq: 2,
				BackendID: "backend-b", ProviderID: "provider-b", ModelID: "model-b",
				Outcome: LegOutcomeWinner, Surfaced: SurfacedYes,
				StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
				Evidence: FinalBillingEvidence{
					InputTokens:  Quantity{Value: 7, Present: true},
					OutputTokens: Quantity{Value: 0, Present: true},
					Cost:         MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true},
					Source:       EvidenceSourceProviderReported, Authority: EvidenceAuthorityAuthoritative,
					DedupeKey: "provider-charge-2",
				},
				OperatorRateRef: VersionRef{ID: "operator-rates", Version: "v5"},
			},
		},
	}
}

func TestSealTurnUsageRecordAssignsDurableKeysAndFingerprints(t *testing.T) {
	t.Parallel()
	record, err := testTurnRecord().Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got, want := record.Key, "acct-1:turn-1"; got != want {
		t.Fatalf("TUR key = %q, want %q", got, want)
	}
	if len(record.Fingerprint) != 64 || record.Fingerprint == "" {
		t.Fatalf("TUR fingerprint = %q, want SHA-256 hex", record.Fingerprint)
	}
	if got, want := record.Legs[0].Key, "acct-1:turn-1:b-1"; got != want {
		t.Fatalf("LUR key = %q, want %q", got, want)
	}
	if record.Legs[0].Fingerprint == record.Legs[1].Fingerprint {
		t.Fatal("different LURs must not share a semantic fingerprint")
	}
}

func TestFingerprintIncludesSessionID(t *testing.T) {
	t.Parallel()
	withSession := testTurnRecord()
	withSession.SessionID = "proxy-sess"
	empty := testTurnRecord()
	sealedWith, err := withSession.Seal()
	if err != nil {
		t.Fatalf("seal with session: %v", err)
	}
	sealedEmpty, err := empty.Seal()
	if err != nil {
		t.Fatalf("seal empty session: %v", err)
	}
	if sealedWith.Fingerprint == sealedEmpty.Fingerprint {
		t.Fatal("session membership must change TUR fingerprint")
	}
	if sealedEmpty.SessionID != "" {
		t.Fatalf("empty session must remain empty, got %q", sealedEmpty.SessionID)
	}
	if sealedWith.SessionID != "proxy-sess" {
		t.Fatalf("SessionID = %q, want proxy-sess", sealedWith.SessionID)
	}
}

func TestFingerprintExcludesStoredKeysAndFingerprintFields(t *testing.T) {
	t.Parallel()
	base, err := testTurnRecord().Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	mutated := base
	mutated.Key = "database-key"
	mutated.Fingerprint = "database-fingerprint"
	for i := range mutated.Legs {
		mutated.Legs[i].Key = "database-lur-key"
		mutated.Legs[i].Fingerprint = "database-lur-fingerprint"
	}
	got, err := mutated.SemanticFingerprint()
	if err != nil {
		t.Fatalf("SemanticFingerprint: %v", err)
	}
	if got != base.Fingerprint {
		t.Fatalf("fingerprint changed when only stored identity fields changed: got %q want %q", got, base.Fingerprint)
	}
}

func TestExplicitZeroAndAbsentEvidenceRemainDistinct(t *testing.T) {
	t.Parallel()
	zero := testTurnRecord()
	zero.Legs[0].Evidence.Cost = MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true}
	absent := testTurnRecord()
	absent.Legs[0].Evidence.Cost = MoneyEvidence{Currency: "USD", Present: false}
	zeroSealed, err := zero.Seal()
	if err != nil {
		t.Fatalf("seal zero: %v", err)
	}
	absentSealed, err := absent.Seal()
	if err != nil {
		t.Fatalf("seal absent: %v", err)
	}
	if zeroSealed.Fingerprint == absentSealed.Fingerprint {
		t.Fatal("authoritative zero cost and absent cost must fingerprint differently")
	}
	if !zeroSealed.Legs[0].Evidence.Cost.Present || absentSealed.Legs[0].Evidence.Cost.Present {
		t.Fatal("cost presence was not preserved")
	}
}

func TestReplayDecision(t *testing.T) {
	t.Parallel()
	existing, err := testTurnRecord().Seal()
	if err != nil {
		t.Fatalf("Seal existing: %v", err)
	}
	if err := CheckReplay(existing, existing); err != nil {
		t.Fatalf("same key/fingerprint replay: %v", err)
	}
	conflict := testTurnRecord()
	conflict.Legs[1].ModelID = "different-model"
	conflict, err = conflict.Seal()
	if err != nil {
		t.Fatalf("Seal conflict: %v", err)
	}
	if err := CheckReplay(existing, conflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrReplayConflict", err)
	}
}

func TestSealRejectsUnorderedOrMisattributedLegs(t *testing.T) {
	t.Parallel()
	record := testTurnRecord()
	record.Legs[1].Seq = record.Legs[0].Seq
	if _, err := record.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("duplicate sequence error = %v, want ErrInvalidRecord", err)
	}
	record = testTurnRecord()
	record.Legs[0].ALegID = "other-a-leg"
	if _, err := record.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("wrong A-leg error = %v, want ErrInvalidRecord", err)
	}
}

func TestTURKeyRejectsColonAmbiguity(t *testing.T) {
	t.Parallel()
	// Without rejection, account "a" + turn "b:c" and account "a:b" + turn "c"
	// both yield "a:b:c" and collide on the global tur_key primary key.
	if _, err := TURKey("a", "b:c"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("colon in turn = %v, want ErrInvalidRecord", err)
	}
	if _, err := TURKey("a:b", "c"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("colon in account = %v, want ErrInvalidRecord", err)
	}
	key, err := TURKey("a", "b")
	if err != nil || key != "a:b" {
		t.Fatalf("TURKey(a,b) = %q, %v", key, err)
	}
	if _, err := LURKey("a:b", "c:d"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("colon in B-leg = %v, want ErrInvalidRecord", err)
	}
}

func TestSealFailsClosedOnColonBearingBLegID(t *testing.T) {
	t.Parallel()
	// Seal must not ignore LURKey errors and persist an empty lur_key.
	record := testTurnRecord()
	record.Legs[0].BLegID = "seq:1"
	if _, err := record.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Seal with colon BLegID = %v, want ErrInvalidRecord", err)
	}
}

func TestSealFailsClosedOnColonBearingAccountOrTurn(t *testing.T) {
	t.Parallel()
	record := testTurnRecord()
	record.AccountID = "acct:1"
	if _, err := record.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Seal with colon account = %v, want ErrInvalidRecord", err)
	}
	record = testTurnRecord()
	record.TurnID = "turn:1"
	if _, err := record.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Seal with colon turn = %v, want ErrInvalidRecord", err)
	}
}
