package domain

import (
	"testing"
	"time"
)

func BenchmarkReservationKeyString(b *testing.B) {
	key := ReservationKey{
		LogicalRequestID: "req-1",
		ALegID:           "a-1",
		BLegID:           "b-1",
		AttemptID:        "attempt-1",
		RuleID:           "quota-strict",
		Sequence:         1,
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = key.String()
	}
}

func BenchmarkSettlementKeyString(b *testing.B) {
	key := SettlementKey{
		ReservationKey: ReservationKey{
			LogicalRequestID: "req-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           "quota-strict",
			Sequence:         1,
		},
		Sequence: 1,
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = key.String()
	}
}

func BenchmarkWindowKeyString(b *testing.B) {
	start := time.Date(2026, 7, 18, 12, 0, 0, 123456789, time.UTC)
	key := WindowKey{
		RuleID:       "quota-strict",
		DimensionKey: DimensionKey("principal=p1|tenant=t1"),
		Start:        start,
		End:          start.Add(time.Hour),
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = key.String()
	}
}
