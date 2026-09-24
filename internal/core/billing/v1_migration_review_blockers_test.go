package billing

import (
	"errors"
	"testing"
)

// Review blocker 1 RED: writer claims must not repair invalid durable identity.
func TestPhase171ReviewWriterClaimRejectsRepairedIdentity(t *testing.T) {
	t.Parallel()
	base := phase171BaselineLeg(t)

	t.Run("wrong fingerprint", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.Fingerprint = "deadbeef"
		if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{mutated}, HistoricalV1WriterVersion); err == nil {
			t.Fatal("wrong fingerprint must fail closed")
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.Key = base.Key + "-tampered"
		if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{mutated}, HistoricalV1WriterVersion); err == nil {
			t.Fatal("wrong key must fail closed")
		}
	})

	t.Run("missing key", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.Key = ""
		if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{mutated}, HistoricalV1WriterVersion); err == nil {
			t.Fatal("missing key must fail closed")
		}
	})

	t.Run("missing fingerprint", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.Fingerprint = ""
		if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{mutated}, HistoricalV1WriterVersion); err == nil {
			t.Fatal("missing fingerprint must fail closed")
		}
	})

	t.Run("mixed calls", func(t *testing.T) {
		t.Parallel()
		otherCall := phase171BaselineCall(t)
		// Derive a second valid call ID distinct from the baseline.
		second := otherCall
		second.CallID = BillingCallID("bc_ffffffffffffffffffffffffffffffff")
		secondKey, err := CallUsageKey(second.CallID)
		if err != nil {
			t.Fatalf("second call key: %v", err)
		}
		second.Key = secondKey
		sealedSecond, err := second.Seal()
		if err != nil {
			t.Fatalf("seal second call: %v", err)
		}
		_ = sealedSecond
		otherLeg := base
		otherLeg.CallID = BillingCallID("bc_ffffffffffffffffffffffffffffffff")
		otherKey, err := CallLegUsageKey(otherLeg.CallID, otherLeg.BLegID)
		if err != nil {
			t.Fatalf("second leg key: %v", err)
		}
		otherLeg.Key = otherKey
		sealedOther, err := otherLeg.Seal()
		if err != nil {
			t.Fatalf("seal second leg: %v", err)
		}
		if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{base, sealedOther}, HistoricalV1WriterVersion); err == nil {
			t.Fatal("mixed-call legs must fail closed")
		}
	})
}

// Review blocker 2 RED: version resolver table.
func TestPhase171ReviewVersionResolverTable(t *testing.T) {
	t.Parallel()
	base := phase171BaselineLeg(t)

	tests := []struct {
		name    string
		mutate  func(CallLegUsageRecord) CallLegUsageRecord
		want    string
		wantErr bool
	}{
		{
			name:    "valid V1",
			mutate:  func(l CallLegUsageRecord) CallLegUsageRecord { return l },
			want:    HistoricalV1WriterVersion,
			wantErr: false,
		},
		{
			name: "valid current V2",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = EvidenceFormatVersionV2
				l.EvidenceProjection = EvidenceProjectionV1
				return l
			},
			want:    V2WriterVersion,
			wantErr: false,
		},
		{
			name: "unsupported evidence version 3",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = 3
				l.EvidenceProjection = EvidenceProjectionV1
				return l
			},
			wantErr: true,
		},
		{
			name: "unsupported evidence version 999",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = 999
				l.EvidenceProjection = EvidenceProjectionV1
				return l
			},
			wantErr: true,
		},
		{
			name: "V2 missing projection",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = EvidenceFormatVersionV2
				l.EvidenceProjection = ""
				return l
			},
			wantErr: true,
		},
		{
			name: "V2 contradictory projection",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = EvidenceFormatVersionV2
				l.EvidenceProjection = "bogus"
				return l
			},
			wantErr: true,
		},
		{
			name: "V1 contradictory projection",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = 0
				l.EvidenceProjection = EvidenceProjectionV1
				return l
			},
			wantErr: true,
		},
		{
			name: "auxiliary provenance version 1 without dispositions",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = 0
				l.EvidenceProjection = ""
				l.EconomicEvidenceVersion = 1
				return l
			},
			wantErr: true,
		},
		{
			name: "auxiliary provenance version 2 unsupported",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = 0
				l.EvidenceProjection = ""
				l.EconomicEvidenceVersion = 2
				return l
			},
			wantErr: true,
		},
		{
			name: "V2 envelope with auxiliary version 1 but no dispositions",
			mutate: func(l CallLegUsageRecord) CallLegUsageRecord {
				l.Key = ""
				l.Fingerprint = ""
				l.EvidenceVersion = EvidenceFormatVersionV2
				l.EvidenceProjection = EvidenceProjectionV1
				l.EconomicEvidenceVersion = 1
				return l
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			leg := tc.mutate(base)
			got, err := WriterVersionForLeg(leg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("WriterVersionForLeg must fail closed for %s", tc.name)
				}
				if !errors.Is(err, ErrHistoricalV1Ambiguous) && !errors.Is(err, ErrInvalidRecord) {
					t.Fatalf("error must be version failure, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriterVersionForLeg: %v", err)
			}
			if got != tc.want {
				t.Fatalf("version = %q, want %q", got, tc.want)
			}
		})
	}
}
