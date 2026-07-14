package authoritystore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	_ "modernc.org/sqlite"
)

// BenchmarkDurableIndependentPrincipals measures concurrent unrelated-rule reserves (16.6).
func BenchmarkDurableIndependentPrincipals(b *testing.B) {
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "indep.db")
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		b.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		b.Fatal(err)
	}
	const principals = 32
	rows := make([]controlplane.AccountingLimitStatusRow, 0, principals)
	for i := 0; i < principals; i++ {
		rows = append(rows, controlplane.AccountingLimitStatusRow{
			RuleID:         fmt.Sprintf("rule-%d", i),
			RuleType:       string(domain.RuleKindQuota),
			Unit:           string(domain.AmountUnitRequests),
			Limit:          1_000_000,
			Remaining:      1_000_000,
			Authority:      controlplane.AccountingAuthoritySourceAuthoritative,
			EvidenceState:  controlplane.EvidenceRecorded,
			RedactionState: controlplane.RedactionSummarized,
		})
	}
	store, err := NewDurable(ctx, bunDB, Config{StoreID: "bench-indep", Backing: domain.BackingCapabilityAtomic, LimitRows: rows})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			rule := fmt.Sprintf("rule-%d", i%principals)
			cmd := reconcileReserveCommandInternal(rule, 1)
			cmd.ReservationKey.LogicalRequestID = fmt.Sprintf("req-%d-%d", i, b.N)
			cmd.ReservationKey.AttemptID = fmt.Sprintf("att-%d-%d", i, b.N)
			cmd.SourceKey = cmd.ReservationKey.String()
			if _, err := store.Reserve(ctx, cmd); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

// BenchmarkDurableHotAccountContention measures many contenders on one rule (16.6).
func BenchmarkDurableHotAccountContention(b *testing.B) {
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "hot.db")
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		b.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		b.Fatal(err)
	}
	row := controlplane.AccountingLimitStatusRow{
		RuleID: "hot", RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitRequests),
		Limit: 1_000_000, Remaining: 1_000_000, Authority: controlplane.AccountingAuthoritySourceAuthoritative,
		EvidenceState: controlplane.EvidenceRecorded, RedactionState: controlplane.RedactionSummarized,
	}
	store, err := NewDurable(ctx, bunDB, Config{StoreID: "bench-hot", Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			cmd := reconcileReserveCommandInternal("hot", 1)
			cmd.ReservationKey.LogicalRequestID = fmt.Sprintf("hot-req-%d-%d", i, b.N)
			cmd.ReservationKey.AttemptID = fmt.Sprintf("hot-att-%d-%d", i, b.N)
			cmd.SourceKey = cmd.ReservationKey.String()
			if _, err := store.Reserve(ctx, cmd); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}
