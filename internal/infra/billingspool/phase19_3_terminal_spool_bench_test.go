package billingspool

// Phase 19.3 terminal write certification (Requirements 18.5, 18.6).
//
// The durable terminal handoff is the billing spool: the runtime terminal
// sink appends one sealed call and one sealed B-leg per execution, and the
// spool flusher delivers them to the authoritative store. These benchmarks
// measure the bounded cost of that terminal write path, including the
// per-append pending-capacity aggregate scan that issue #394 tracks as a
// candidate hotspot. They are certification measurements, not thresholds.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func phase193SpoolLeg(tb testing.TB, i int) billing.CallLegUsageRecord {
	tb.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		tb.Fatal(err)
	}
	return billing.CallLegUsageRecord{
		CallID:     callID,
		ALegID:     "a-phase193",
		BLegID:     fmt.Sprintf("b-phase193-%d", i),
		AttemptSeq: 1,
		BackendID:  "backend-a",
		ProviderID: "provider-a",
		ModelID:    "model-a",
		StartedAt:  time.Unix(193, 0).UTC(),
		FinishedAt: time.Unix(193, 500000000).UTC(),
		Outcome:    billing.LegOutcomeWinner,
		Surfaced:   billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 7, Present: true},
			OutputTokens: billing.Quantity{Value: 3, Present: true},
			Cost:         billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
			DedupeKey:    fmt.Sprintf("provider-charge-%d", i),
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v4"},
	}
}

func phase193SpoolCall(tb testing.TB, i int) billing.CallUsageRecord {
	tb.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		tb.Fatal(err)
	}
	now := time.Unix(193, 0).UTC()
	record, err := (billing.CallUsageRecord{
		SchemaVersion:   billing.CurrentRecordSchemaVersion,
		CallID:          callID,
		AccountID:       "account-phase193",
		ALegID:          "a-phase193",
		StartedAt:       now,
		FinishedAt:      now.Add(500 * time.Millisecond),
		Outcome:         billing.TurnOutcomeCompleted,
		ExpectedBLegIDs: []string{fmt.Sprintf("b-phase193-%d", i)},
	}).Seal()
	if err != nil {
		tb.Fatal(err)
	}
	return record
}

func phase193OpenSpool(b *testing.B, sink billing.TerminalUsageSink) *Spool {
	b.Helper()
	spool, err := Open(context.Background(), Config{Path: filepath.Join(b.TempDir(), "phase193-spool.db")}, sink)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = spool.Close() })
	return spool
}

// BenchmarkPhase193TerminalSpoolAppendLeg measures the durable terminal B-leg
// append (seal, pending-capacity scan, insert, commit) with a real SQLite
// spool file.
func BenchmarkPhase193TerminalSpoolAppendLeg(b *testing.B) {
	spool := phase193OpenSpool(b, &recordingSink{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := spool.AppendLeg(context.Background(), phase193SpoolLeg(b, i)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(spool.PendingCount()), "pending-records")
}

// BenchmarkPhase193TerminalSpoolAppendCall measures the durable terminal call
// closure append with a real SQLite spool file.
func BenchmarkPhase193TerminalSpoolAppendCall(b *testing.B) {
	spool := phase193OpenSpool(b, &recordingSink{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := spool.AppendCall(context.Background(), phase193SpoolCall(b, i)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(spool.PendingCount()), "pending-records")
}

// BenchmarkPhase193TerminalSpoolAppendAndDeliver measures the complete
// terminal handoff: one durable append plus one flusher delivery to the
// authoritative sink.
func BenchmarkPhase193TerminalSpoolAppendAndDeliver(b *testing.B) {
	spool := phase193OpenSpool(b, &recordingSink{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := spool.AppendLeg(context.Background(), phase193SpoolLeg(b, i)); err != nil {
			b.Fatal(err)
		}
		if err := spool.ProcessOnce(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(spool.PendingCount()), "pending-records")
}
