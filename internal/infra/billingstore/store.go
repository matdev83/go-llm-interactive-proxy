package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

var RequiredMigrationNames = []string{BaselineMigrationName, LegacyAuthorizationSchemaMigrationName, Phase4MigrationName, Phase6MigrationName, Phase7MigrationName, SessionIDMigrationName, UsageLegRecordsMigrationName, UsageCallRecordsMigrationName, ProviderCostWorkMigrationName, ProviderCostWorkRetryMigrationName, ExposureMigrationName, HoldRetirementMigrationName, UsageAppendOutboxMigrationName, AuthorizationHoldsDropMigrationName, ReservedNanoZeroMigrationName, CompleteCallClaimLeaseMigrationName}

type Config struct {
	StoreID string
}
type DurableStore struct {
	db                  *bun.DB
	storeID             string
	settlementFaultHook func(string) error
}

func Migrate(ctx context.Context, database *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("billingstore: nil context")
	}
	if database == nil {
		return fmt.Errorf("billingstore: nil database")
	}
	return runSchemaMigrate(ctx, database)
}

func VerifySchema(ctx context.Context, database *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("billingstore: nil context")
	}
	if database == nil {
		return fmt.Errorf("billingstore: nil database")
	}
	for _, table := range []string{
		"billing_accounts", "billing_account_openings", "billing_reconciliation_events", "billing_account_policy_events",
		"turn_usage_records", "leg_usage_records", "usage_leg_records", "usage_call_records", "provider_cost_work", "usage_append_outbox", "usage_record_processing", "call_exposures",
		"journal_transactions", "journal_entries", "billing_operation_snapshots",
	} {
		var probe int
		if err := database.NewRaw("SELECT 1 FROM "+table+" WHERE 1 = 0").Scan(ctx, &probe); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: schema verification %s: %w", table, err)
		}
	}
	if database.Dialect().Name() == dialect.SQLite {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'bun_billing_migrations'`).Scan(ctx, &count); err != nil || count != 1 {
			if err != nil {
				return fmt.Errorf("billingstore: migration history verification: %w", err)
			}
			return fmt.Errorf("billingstore: migration history missing")
		}
		for _, migrationName := range RequiredMigrationNames {
			if err := database.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, migrationName).Scan(ctx, &count); err != nil || count != 1 {
				if err != nil {
					return fmt.Errorf("billingstore: migration %s verification: %w", migrationName, err)
				}
				return fmt.Errorf("billingstore: migration %s is not recorded", migrationName)
			}
		}
		for _, index := range []string{"idx_billing_processing_status", "idx_billing_journal_account_sequence", "idx_billing_journal_source", journalReversalUniqueIndex, sessionAccountIndex, usageLegCallBLegIndex, usageCallCallIDIndex, usageCallAccountSessionIndex, usageCallClaimStatusIndex, usageCallClaimPendingIndex, providerCostWorkStatusIndex, providerCostWorkPendingIndex, exposureAccountStatusIndex} {
			var name string
			if err := database.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &name); err != nil || name != index {
				if err != nil {
					return fmt.Errorf("billingstore: missing SQLite index %s: %w", index, err)
				}
				return fmt.Errorf("billingstore: missing SQLite index %s", index)
			}
		}
		tableFragments := map[string][]string{
			"billing_accounts":              {"CHECK", "credit_limit_nano", "opening_balance_nano", "reserved_nano", "reconcile_required"},
			"billing_account_openings":      {"FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"billing_reconciliation_events": {"FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"billing_account_policy_events": {"FOREIGN KEY(account_id) REFERENCES billing_accounts", "UNIQUE(account_id, source_key)"},
			"turn_usage_records":            {"CHECK", "UNIQUE(account_id, turn_id)", "FOREIGN KEY(account_id) REFERENCES billing_accounts", "session_id"},
			"leg_usage_records":             {"CHECK", "UNIQUE(tur_key, b_leg_id)", "UNIQUE(tur_key, sequence)", "FOREIGN KEY(tur_key) REFERENCES turn_usage_records"},
			"usage_leg_records":             {"usage_leg_key", "call_id", "b_leg_id", "payload_json", "fingerprint"},
			"provider_cost_work":            {"usage_leg_key", "call_id", "status", "attempt_count", "next_attempt_at", "last_error", "updated_at"},
			"usage_call_records":            {"usage_call_key", "call_id", "account_id", "a_leg_id", "session_id", "expected_b_leg_ids", "payload_json", "fingerprint", "claim_status", "claim_attempt_count", "next_claim_at", "last_claim_error"},
			"call_exposures":                {"exposure_key", "account_id", "call_id", "max_exposure_nano", "pricing_ref", "charge_policy_ref", "fingerprint", "status", "FOREIGN KEY(account_id) REFERENCES billing_accounts", "UNIQUE(account_id, call_id)"},
			"usage_record_processing":       {"CHECK", "FOREIGN KEY(tur_key) REFERENCES turn_usage_records"},
			"journal_transactions":          {"CHECK", "UNIQUE(account_id, book, source_key)", "UNIQUE(account_id, account_sequence)", "FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"journal_entries":               {"CHECK", "side IN ('debit','credit')", "amount_nano > 0", "FOREIGN KEY(transaction_id) REFERENCES journal_transactions"},
			"billing_operation_snapshots":   {"FOREIGN KEY(account_id) REFERENCES billing_accounts", "UNIQUE(account_id, operation_kind, source_key)", "integrity_fingerprint"},
		}
		for table, fragments := range tableFragments {
			var ddl string
			if err := database.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &ddl); err != nil {
				return fmt.Errorf("billingstore: SQLite table definition %s: %w", table, err)
			}
			lowerDDL := strings.ToLower(ddl)
			for _, fragment := range fragments {
				if !strings.Contains(lowerDDL, strings.ToLower(fragment)) {
					return fmt.Errorf("billingstore: SQLite table %s missing protection %q", table, fragment)
				}
			}
		}
		for _, trigger := range []string{"billing_exposure_immutable_update", "billing_exposure_immutable_delete", "billing_operation_snapshots_immutable_update", "billing_operation_snapshots_immutable_delete", "billing_account_openings_immutable_update", "billing_account_openings_immutable_delete", "billing_reconciliation_events_immutable_update", "billing_reconciliation_events_immutable_delete", "billing_policy_events_immutable_update", "billing_policy_events_immutable_delete", "billing_tur_immutable_update", "billing_tur_immutable_delete", "billing_lur_immutable_update", "billing_lur_immutable_delete", "billing_usage_leg_immutable_update", "billing_usage_leg_immutable_delete", "billing_usage_call_immutable_update", "billing_usage_call_immutable_delete", "billing_journal_tx_immutable_update", "billing_journal_tx_immutable_delete", "billing_journal_entry_immutable_update", "billing_journal_entry_immutable_delete"} {
			var name string
			if err := database.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(ctx, &name); err != nil || name != trigger {
				return fmt.Errorf("billingstore: missing SQLite immutability trigger %s", trigger)
			}
		}
		return nil
	}
	checks := []struct {
		description string
		query       string
		args        []any
		fragments   []string
	}{
		{"account opening table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_account_openings' LIMIT 1`, nil, []string{"billing_account_openings"}},
		{"account opening column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_account_openings' AND column_name = 'opening_balance_nano' LIMIT 1`, nil, []string{"opening_balance_nano"}},
		{"operation snapshot integrity column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_operation_snapshots' AND column_name = 'integrity_fingerprint' LIMIT 1`, nil, []string{"integrity_fingerprint"}},
		{"account opening balance column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_accounts' AND column_name = 'opening_balance_nano' LIMIT 1`, nil, []string{"opening_balance_nano"}},
		{"migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{BaselineMigrationName}, []string{BaselineMigrationName}},
		{"authorization schema migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{LegacyAuthorizationSchemaMigrationName}, []string{LegacyAuthorizationSchemaMigrationName}},
		{"hold retirement migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{HoldRetirementMigrationName}, []string{HoldRetirementMigrationName}},
		{"authorization holds drop migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{AuthorizationHoldsDropMigrationName}, []string{AuthorizationHoldsDropMigrationName}},
		{"session id migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{SessionIDMigrationName}, []string{SessionIDMigrationName}},
		{"usage leg records migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{UsageLegRecordsMigrationName}, []string{UsageLegRecordsMigrationName}},
		{"usage call records migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{UsageCallRecordsMigrationName}, []string{UsageCallRecordsMigrationName}},
		{"provider cost work migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ProviderCostWorkMigrationName}, []string{ProviderCostWorkMigrationName}},
		{"journal account sequence index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{"idx_billing_journal_account_sequence"}, []string{"account_id", "account_sequence"}},
		{"journal source index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{"idx_billing_journal_source"}, []string{"account_id", "book", "source_key"}},
		{"journal reversal unique index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{journalReversalUniqueIndex}, []string{"account_id", "book", "reversal_of"}},
		{"processing status index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{"idx_billing_processing_status"}, []string{"status", "updated_at"}},
		{"TUR session column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'turn_usage_records' AND column_name = 'session_id' LIMIT 1`, nil, []string{"session_id"}},
		{"TUR session index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{sessionAccountIndex}, []string{"account_id", "session_id", "tur_key"}},
		{"usage leg table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'usage_leg_records' LIMIT 1`, nil, []string{"usage_leg_records"}},
		{"usage leg CallID/BLegID unique index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageLegCallBLegIndex}, []string{"UNIQUE", "call_id", "b_leg_id"}},
		{"usage call table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'usage_call_records' LIMIT 1`, nil, []string{"usage_call_records"}},
		{"usage call CallID unique index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageCallCallIDIndex}, []string{"UNIQUE", "call_id"}},
		{"usage call account/session index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageCallAccountSessionIndex}, []string{"account_id", "session_id", "call_id"}},
		{"usage call claim status index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageCallClaimStatusIndex}, []string{"claim_status", "sealed_at"}},
		{"usage call claim pending index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageCallClaimPendingIndex}, []string{"claim_status", "next_claim_at", "sealed_at", "call_id"}},
		{"complete call claim lease migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{CompleteCallClaimLeaseMigrationName}, []string{CompleteCallClaimLeaseMigrationName}},
		{"provider cost work table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'provider_cost_work' LIMIT 1`, nil, []string{"provider_cost_work"}},
		{"provider cost attempt column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'provider_cost_work' AND column_name = 'attempt_count' LIMIT 1`, nil, []string{"attempt_count"}},
		{"provider cost retry column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'provider_cost_work' AND column_name = 'next_attempt_at' LIMIT 1`, nil, []string{"next_attempt_at"}},
		{"provider cost work retry migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ProviderCostWorkRetryMigrationName}, []string{ProviderCostWorkRetryMigrationName}},
		{"provider cost work pending index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerCostWorkPendingIndex}, []string{"status", "next_attempt_at", "updated_at", "usage_leg_key"}},
		{"provider cost work status index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerCostWorkStatusIndex}, []string{"status", "updated_at", "usage_leg_key"}},
		{"exposure table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'call_exposures' LIMIT 1`, nil, []string{"call_exposures"}},
		{"exposure account status index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{exposureAccountStatusIndex}, []string{"account_id", "status", "created_at"}},
		{"TUR uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'turn_usage_records' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%account_id%turn_id%' LIMIT 1`, nil, []string{"UNIQUE", "account_id", "turn_id"}},
		{"LUR B-leg uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'leg_usage_records' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%tur_key%b_leg_id%' LIMIT 1`, nil, []string{"UNIQUE", "tur_key", "b_leg_id"}},
		{"LUR sequence uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'leg_usage_records' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%tur_key%sequence%' LIMIT 1`, nil, []string{"UNIQUE", "tur_key", "sequence"}},
		{"journal source uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%account_id%book%source_key%' LIMIT 1`, nil, []string{"UNIQUE", "account_id", "book", "source_key"}},
		{"journal sequence uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%account_id%account_sequence%' LIMIT 1`, nil, []string{"UNIQUE", "account_id", "account_sequence"}},
		{"policy account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_account_policy_events' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"TUR account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'turn_usage_records' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"LUR TUR foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'leg_usage_records' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "turn_usage_records"}},
		{"processing TUR foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'usage_record_processing' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "turn_usage_records"}},
		{"journal transaction account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"journal entry transaction foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_entries' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "journal_transactions"}},
		{"journal entry amount check", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_entries' AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%amount_nano%' LIMIT 1`, nil, []string{"CHECK", "amount_nano"}},
		{"journal entry side check", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_entries' AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%side%' LIMIT 1`, nil, []string{"CHECK", "side"}},
		{"operation snapshot immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_operation_snapshots' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_operation_snapshots_immutable"}, []string{"billing_operation_snapshots_immutable"}},
		{"reconciliation immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_reconciliation_events' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_reconciliation_events_immutable"}, []string{"billing_reconciliation_events_immutable"}},
		{"opening immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_account_openings' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_account_openings_immutable"}, []string{"billing_account_openings_immutable"}},
		{"policy immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_account_policy_events' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_policy_events_immutable"}, []string{"billing_policy_events_immutable"}},
		{"TUR immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'turn_usage_records' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_tur_immutable"}, []string{"billing_tur_immutable"}},
		{"LUR immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'leg_usage_records' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_lur_immutable"}, []string{"billing_lur_immutable"}},
		{"usage leg immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'usage_leg_records' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_usage_leg_immutable"}, []string{"billing_usage_leg_immutable"}},
		{"usage call immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'usage_call_records' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_usage_call_immutable"}, []string{"billing_usage_call_immutable"}},
		{"exposure immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'call_exposures' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_exposure_immutable"}, []string{"billing_exposure_immutable"}},
		{"journal transaction immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'journal_transactions' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_journal_tx_immutable"}, []string{"billing_journal_tx_immutable"}},
		{"journal entry immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'journal_entries' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_journal_entry_immutable"}, []string{"billing_journal_entry_immutable"}},
	}
	for _, check := range checks {
		if err := dbinfra.VerifyPostgresQueryRowContains(ctx, database, check.description, check.query, check.args, check.fragments...); err != nil {
			return fmt.Errorf("billingstore: PostgreSQL schema verification failed: %w", err)
		}
	}
	return nil
}

func NewDurableStore(ctx context.Context, database *bun.DB, cfg Config) (*DurableStore, error) {
	if err := Migrate(ctx, database); err != nil {
		return nil, err
	}
	return openStore(ctx, database, cfg)
}

func openStore(ctx context.Context, database *bun.DB, cfg Config) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("billingstore: nil context")
	}
	if database == nil {
		return nil, fmt.Errorf("billingstore: nil database")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("billingstore: store id is required")
	}
	return &DurableStore{db: database, storeID: strings.TrimSpace(cfg.StoreID)}, nil
}

func (s *DurableStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *DurableStore) CheckReadiness(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("billingstore: unavailable: %w", err)
	}
	return nil
}

func (s *DurableStore) StoreID() string {
	if s == nil {
		return ""
	}
	return s.storeID
}
