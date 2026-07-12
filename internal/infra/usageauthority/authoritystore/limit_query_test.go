package authoritystore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestDurableLimitStatusBackfillsLegacyRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "authority.db")
	db := openSeedRaceDB(t, path)
	store, err := NewDurable(ctx, db, Config{StoreID: "legacy-limits", Backing: domain.BackingCapabilityAtomic})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db = openSeedRaceDB(t, path)
	row := queryLimitRow("legacy-rule", "legacy-trace", time.Unix(1_700_000_000, 0).UTC())
	key := limitRowKey(row)
	raw, _ := json.Marshal(row)
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_authority_limit_rows(store_id, row_key, row_json) VALUES(?,?,?)`, "legacy-limits", key, string(raw)); err != nil {
		t.Fatal(err)
	}
	store, err = NewDurable(ctx, db, Config{StoreID: "legacy-limits", Backing: domain.BackingCapabilityAtomic})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	page, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{TraceID: "legacy-trace"},
		RuleID: "legacy-rule", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].RuleID != "legacy-rule" {
		t.Fatalf("legacy query page = %#v", page)
	}
}

func TestDurableLimitStatusFiltersBeforeDecodeAndPagesBySortKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSeedRaceDB(t, filepath.Join(t.TempDir(), "authority.db"))
	store, err := NewDurable(ctx, db, Config{StoreID: "bounded-limits", Backing: domain.BackingCapabilityAtomic})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	var firstCorruptKey string
	for i := 1; i <= 2000; i++ {
		ruleID := "unrelated"
		traceID := "other"
		if i == 1000 || i == 2000 {
			ruleID = "target"
			traceID = "target-trace"
		}
		row := queryLimitRow(ruleID, traceID, base.Add(time.Duration(i)*time.Second))
		row.Correlation.RequestID = fmt.Sprintf("req-%04d", i)
		key := limitRowKey(row)
		if i == 1 {
			firstCorruptKey = key
		}
		raw, _ := json.Marshal(row)
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_authority_limit_rows(store_id, row_key, row_json) VALUES(?,?,?)`, "bounded-limits", key, string(raw)); err != nil {
			t.Fatal(err)
		}
		if err := store.replaceLimitFiltersTx(ctx, tx, "bounded-limits", key, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_authority_limit_rows SET row_json = ? WHERE store_id = ? AND row_key = ?`, `{not-json`, "bounded-limits", firstCorruptKey); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	query := controlplane.AccountingLimitStatusQuery{Common: controlplane.CommonFilters{TraceID: "target-trace"}, RuleID: "target", Limit: 1}
	first, err := store.LimitStatus(ctx, query)
	if err != nil {
		t.Fatalf("first page decoded unrelated history: %v", err)
	}
	if len(first.Items) != 1 || first.Next.IsZero() {
		t.Fatalf("first page = %#v", first)
	}
	if first.Items[0].Correlation.RequestID != "req-1000" {
		t.Fatalf("first item = %#v", first.Items[0])
	}
	query.Cursor = first.Next
	second, err := store.LimitStatus(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || !second.Next.IsZero() {
		t.Fatalf("second page = %#v", second)
	}
	if second.Items[0].Correlation.RequestID != "req-2000" {
		t.Fatalf("second item = %#v", second.Items[0])
	}
	query.RuleID = "different-query"
	if _, err := store.LimitStatus(ctx, query); err == nil {
		t.Fatal("cross-query cursor reuse must fail")
	}
}

func TestDurableLimitStatusReportsUnsupportedFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSeedRaceDB(t, filepath.Join(t.TempDir(), "authority.db"))
	row := controlplane.AccountingLimitStatusRow{
		RuleID: "unsupported", RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitRequests),
		Limit: 10, Remaining: 10, Authority: controlplane.AccountingAuthoritySourceAuthoritative,
	}
	store, err := NewDurable(ctx, db, Config{StoreID: "unsupported-limits", Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	page, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		RuleID: "unsupported", Limit: 10,
		SettlementState: controlplane.AccountingSettlementSettled,
		Common: controlplane.CommonFilters{
			Outcome:    string(controlplane.AccountingOutcomeAllow),
			ReasonCode: "quota_exceeded",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %#v", page.Items)
	}
	got := map[string]string{}
	for _, filter := range page.Unsupported {
		got[filter.Field] = filter.Reason
	}
	for _, field := range []string{"settlement_state", "outcome", "reason_code"} {
		if got[field] == "" {
			t.Fatalf("unsupported filters = %#v, missing %q", page.Unsupported, field)
		}
	}
}

func queryLimitRow(ruleID, traceID string, windowStart time.Time) controlplane.AccountingLimitStatusRow {
	return controlplane.AccountingLimitStatusRow{
		Correlation: controlplane.Correlation{TraceID: traceID, BackendID: "backend", Model: "model"},
		Scope:       controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal"), ProjectID: scope.Known("project")},
		RuleID:      ruleID, RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitRequests),
		Limit: 10, Remaining: 10, Authority: controlplane.AccountingAuthoritySourceAuthoritative,
		WindowStart: windowStart, WindowEnd: windowStart.Add(time.Hour), WindowResetAt: windowStart.Add(time.Hour),
		EvidenceState: controlplane.EvidenceRecorded, RedactionState: controlplane.RedactionSummarized,
	}
}
