package authoritystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	_ "modernc.org/sqlite"
)

func BenchmarkDurableReserveReplayHistorySize(b *testing.B) {
	for _, historySize := range []int{0, 1_000, 10_000} {
		b.Run(fmt.Sprintf("history=%d", historySize), func(b *testing.B) {
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "authority.db")
			sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
			if err != nil {
				b.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
			if err != nil {
				b.Fatal(err)
			}
			row := controlplane.AccountingLimitStatusRow{RuleID: "bench", RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitRequests), Limit: 1_000_000, Remaining: 1_000_000, Authority: controlplane.AccountingAuthoritySourceAuthoritative}
			store, err := NewDurable(ctx, bunDB, Config{StoreID: "bench", Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}})
			if err != nil {
				b.Fatal(err)
			}
			cmd := reconcileReserveCommandInternal("bench", 1)
			if _, err := store.Reserve(ctx, cmd); err != nil {
				b.Fatal(err)
			}
			tx, err := bunDB.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			for i := range historySize {
				record := decisionRecord{Seq: int64(i + 10), SourceKey: fmt.Sprintf("history-%d", i), Row: controlplane.AccountingDecisionRow{RuleID: "history"}}
				raw, err := json.Marshal(record.Row)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO usage_authority_decisions(store_id, decision_seq, source_key, row_json) VALUES(?,?,?,?)`, "bench", record.Seq, record.SourceKey, string(raw)); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if result, err := store.Reserve(ctx, cmd); err != nil || result.Applied {
					b.Fatalf("reserve replay = %#v, err=%v", result, err)
				}
			}
			_ = store.Close()
		})
	}
}

func BenchmarkDurableActiveLimitHistorySize(b *testing.B) {
	for _, historySize := range []int{0, 1_000, 10_000} {
		b.Run(fmt.Sprintf("history=%d", historySize), func(b *testing.B) {
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "authority.db")
			sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
			if err != nil {
				b.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
			if err != nil {
				b.Fatal(err)
			}
			row := controlplane.AccountingLimitStatusRow{RuleID: "active", RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitInputTokens), Limit: 1_000_000, Remaining: 1_000_000}
			store, err := NewDurable(ctx, bunDB, Config{StoreID: "active-bench", Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}})
			if err != nil {
				b.Fatal(err)
			}
			tx, err := bunDB.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			raw, err := json.Marshal(row)
			if err != nil {
				b.Fatal(err)
			}
			for i := range historySize {
				if _, err := tx.ExecContext(ctx, `INSERT INTO usage_authority_limit_rows(store_id, row_key, row_json) VALUES(?,?,?)`, "active-bench", fmt.Sprintf("history-%d", i), string(raw)); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			query := app.ActiveLimitQuery{RuleID: "active", At: time.Now()}
			b.ReportAllocs()
			for b.Loop() {
				if _, configured, err := store.ActiveLimit(ctx, query); err != nil || !configured {
					b.Fatalf("ActiveLimit configured=%v err=%v", configured, err)
				}
			}
			_ = store.Close()
		})
	}
}
