package authoritystore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestDurableDecisionHistoryBackfillsLegacyRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "authority.db")
	db := openSeedRaceDB(t, path)
	store, err := NewDurable(ctx, db, Config{StoreID: "legacy-decisions", Backing: domain.BackingCapabilityAtomic})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db = openSeedRaceDB(t, path)
	row := queryDecisionRow("legacy-rule", "legacy-trace", scope.Known(""))
	raw, _ := json.Marshal(row)
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_authority_decisions(store_id, decision_seq, source_key, row_json) VALUES(?,?,?,?)`, "legacy-decisions", 10, "legacy-source", string(raw)); err != nil {
		t.Fatal(err)
	}
	store, err = NewDurable(ctx, db, Config{StoreID: "legacy-decisions", Backing: domain.BackingCapabilityAtomic})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	page, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{TraceID: "legacy-trace", Scope: controlplane.ScopeFilters{ProjectID: scope.Known("")}},
		RuleID: "legacy-rule", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].RuleID != "legacy-rule" {
		t.Fatalf("legacy query page = %#v", page)
	}
}

func TestDurableDecisionHistoryFiltersBeforeDecodeAndPagesBySequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSeedRaceMemDB(t)
	store, err := NewDurable(ctx, db, Config{StoreID: "bounded-decisions", Backing: domain.BackingCapabilityAtomic})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Batched multi-row INSERTs: same 2000 decisions and filter rows as
	// per-row seeding, chunked to avoid pure-Go SQLite per-statement cost.
	const flushSize = 200
	pending := make([]decisionRecord, 0, flushSize)
	var targetRecs []decisionRecord
	flush := func() {
		if len(pending) == 0 {
			return
		}
		var rowsQuery strings.Builder
		rowsArgs := make([]any, 0, len(pending)*4)
		rowsQuery.WriteString(`INSERT INTO usage_authority_decisions(store_id, decision_seq, source_key, row_json) VALUES `)
		var filtersQuery strings.Builder
		filtersArgs := make([]any, 0, len(pending)*60)
		filtersQuery.WriteString(`INSERT INTO usage_authority_decision_filters(store_id, decision_seq, field_name, field_value) VALUES `)
		for i, rec := range pending {
			if i > 0 {
				rowsQuery.WriteByte(',')
			}
			rowsQuery.WriteString(`(?,?,?,?)`)
			raw, _ := json.Marshal(rec.Row)
			rowsArgs = append(rowsArgs, "bounded-decisions", rec.Seq, rec.SourceKey, string(raw))
			for j, filter := range decisionFiltersForRow(rec.Row) {
				if i > 0 || j > 0 {
					filtersQuery.WriteByte(',')
				}
				filtersQuery.WriteString(`(?,?,?,?)`)
				filtersArgs = append(filtersArgs, "bounded-decisions", rec.Seq, filter.name, filter.value)
			}
		}
		filtersQuery.WriteString(` ON CONFLICT DO NOTHING`)
		if _, err := tx.ExecContext(ctx, rowsQuery.String(), rowsArgs...); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, filtersQuery.String(), filtersArgs...); err != nil {
			t.Fatal(err)
		}
		pending = pending[:0]
	}
	for i := 1; i <= 2000; i++ {
		ruleID := "unrelated"
		traceID := "other"
		if i == 1000 || i == 2000 {
			ruleID = "target"
			traceID = "target-trace"
		}
		rec := decisionRecord{Seq: int64(i + 10), SourceKey: fmt.Sprintf("source-%d", i), Row: queryDecisionRow(ruleID, traceID, scope.Known("project"))}
		if ruleID == "target" {
			targetRecs = append(targetRecs, rec)
		}
		pending = append(pending, rec)
		if len(pending) == flushSize {
			flush()
		}
	}
	flush()
	// Route the target records through the production filter-replacement
	// helper so this test keeps covering it; final state is unchanged.
	for _, rec := range targetRecs {
		if err := store.replaceDecisionFiltersTx(ctx, tx, "bounded-decisions", rec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_authority_decisions SET row_json = ? WHERE store_id = ? AND decision_seq = ?`, `{not-json`, "bounded-decisions", 11); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	query := controlplane.AccountingDecisionQuery{Common: controlplane.CommonFilters{TraceID: "target-trace"}, RuleID: "target", Limit: 1}
	first, err := store.DecisionHistory(ctx, query)
	if err != nil {
		t.Fatalf("first page decoded unrelated history: %v", err)
	}
	if len(first.Items) != 1 || first.Next.IsZero() {
		t.Fatalf("first page = %#v", first)
	}
	query.Cursor = first.Next
	second, err := store.DecisionHistory(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || !second.Next.IsZero() {
		t.Fatalf("second page = %#v", second)
	}
	query.RuleID = "different-query"
	if _, err := store.DecisionHistory(ctx, query); err == nil {
		t.Fatal("cross-query cursor reuse must fail")
	}
}

func TestDurableDecisionProjectionFailureRollsBackMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSeedRaceMemDB(t)
	row := controlplane.AccountingLimitStatusRow{RuleID: "atomic-projection", RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitRequests), Limit: 10, Remaining: 10, Authority: controlplane.AccountingAuthoritySourceAuthoritative}
	store, err := NewDurable(ctx, db, Config{StoreID: "atomic-projection", Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_decision_filter BEFORE INSERT ON usage_authority_decision_filters BEGIN SELECT RAISE(ABORT, 'filter write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	cmd := reconcileReserveCommandInternal("atomic-projection", 3)
	if _, err := store.Reserve(ctx, cmd); err == nil {
		t.Fatal("Reserve must fail when its decision projection cannot commit")
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_decision_filter`); err != nil {
		t.Fatal(err)
	}
	status, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{RuleID: "atomic-projection", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Items) != 1 || status.Items[0].Reserved != 0 || status.Items[0].Remaining != 10 {
		t.Fatalf("rolled-back status = %#v", status)
	}
	decisions, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Items) != 0 {
		t.Fatalf("rolled-back decisions = %#v", decisions.Items)
	}
}

func queryDecisionRow(ruleID, traceID string, projectID scope.Value) controlplane.AccountingDecisionRow {
	return controlplane.AccountingDecisionRow{
		Correlation: controlplane.Correlation{TraceID: traceID, BackendID: "backend", Model: "model"},
		Scope:       controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal"), ProjectID: projectID},
		RuleID:      ruleID, Outcome: controlplane.AccountingOutcomeAllow, Authority: controlplane.AccountingAuthoritySourceAuthoritative,
		Unit: string(domain.AmountUnitRequests), EvidenceState: controlplane.EvidenceRecorded, RedactionState: controlplane.RedactionSummarized,
	}
}

func TestCapacityReasonUsesStableRuleCode(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		string(domain.RuleKindQuota): "quota_exceeded",
		string(domain.RuleKindRate):  "rate_limited",
	}
	for kind, want := range cases {
		if got := capacityReason(kind); got != want {
			t.Errorf("capacityReason(%q) = %q, want %q", kind, got, want)
		}
	}
}
