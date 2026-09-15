package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const legacyReservedZeroMigrationName = "20260826000000"

var RequiredMigrationNames = []string{BaselineMigrationName, LegacyAuthorizationSchemaMigrationName, Phase4MigrationName, Phase6MigrationName, Phase7MigrationName, SessionIDMigrationName, UsageLegRecordsMigrationName, UsageCallRecordsMigrationName, ProviderCostWorkMigrationName, ProviderCostWorkRetryMigrationName, ExposureMigrationName, HoldRetirementMigrationName, UsageAppendOutboxRetirementMigrationName, AuthorizationHoldsDropMigrationName, legacyReservedZeroMigrationName, CompleteCallClaimLeaseMigrationName, UsageLegSequenceMigrationName, ProviderJournalOrderMigrationName, ProviderJournalSequenceContractMigrationName, ReservedColumnRemovalMigrationName, LegacyUsageRetirementMigrationName, ProviderMaintenanceMigrationName, ProviderMaintenanceIntegrityMigrationName, BillingV2EconomicsMigrationName, BillingV2LineBooleanRepairMigrationName, BillingV2ValuationIdentityMigrationName, CustomerUnitLedgerMigrationName, CostPassThroughHeadMigrationName, SubmissionFeeClaimMigrationName, BillingAllocationMigrationName, BillingAllocationTargetScopeMigrationName}

type Config struct {
	StoreID string
}
type DurableStore struct {
	db                  *bun.DB
	storeID             string
	settlementFaultHook func(string) error
	economicFaultHook   func(string) error
}

var (
	_ billing.AccountStore           = (*DurableStore)(nil)
	_ billing.AccountProvisioner     = (*DurableStore)(nil)
	_ billing.CreditScreenStore      = (*DurableStore)(nil)
	_ billing.CallUsageStore         = (*DurableStore)(nil)
	_ billing.CallUsageReader        = (*DurableStore)(nil)
	_ billing.CallLegUsageReader     = (*DurableStore)(nil)
	_ billing.ExposureStore          = (*DurableStore)(nil)
	_ billing.ExposureAdmissionStore = (*DurableStore)(nil)
	_ billing.JournalStore           = (*DurableStore)(nil)
	_ billing.ProviderCostStore      = (*DurableStore)(nil)
	_ billing.ProviderCostWorkStore  = (*DurableStore)(nil)
	_ billing.ReportsStore           = (*DurableStore)(nil)
	_ billing.ReportingStore         = (*DurableStore)(nil)
	_ billing.CallSettlementStore    = (*DurableStore)(nil)
	_ billing.CompleteCallClaimer    = (*DurableStore)(nil)
	_ billing.AuthoritativeBilling   = (*DurableStore)(nil)
	_ billing.AllocationStore        = (*DurableStore)(nil)
)

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
	if retiredHolds, err := authorizationHoldsTableExists(ctx, database); err != nil {
		return fmt.Errorf("billingstore: retired authorization holds verification: %w", err)
	} else if retiredHolds {
		return fmt.Errorf("billingstore: retired authorization_holds table is still present")
	}
	if retiredOutbox, err := usageAppendOutboxTableExists(ctx, database); err != nil {
		return fmt.Errorf("billingstore: retired usage append outbox verification: %w", err)
	} else if retiredOutbox {
		return fmt.Errorf("billingstore: retired usage_append_outbox table is still present")
	}
	if retiredLegacy, err := legacyUsageTableNames(ctx, database); err != nil {
		return fmt.Errorf("billingstore: retired legacy usage verification: %w", err)
	} else if len(retiredLegacy) != 0 {
		return fmt.Errorf("billingstore: retired legacy tables are still present: %s", strings.Join(retiredLegacy, ", "))
	}
	for _, table := range []string{
		"billing_accounts", "billing_account_openings", "billing_reconciliation_events", "billing_account_policy_events",
		"usage_leg_records", "usage_call_records", "provider_cost_work", "call_exposures",
		"journal_transactions", "journal_entries", "billing_operation_snapshots", "provider_maintenance_usage",
		"billing_valuations", "billing_valuation_lines", "billing_reconciliations", "billing_economic_work",
		"billing_unit_balances", "billing_unit_operations", "billing_unit_reservations",
		"billing_cost_pass_through_heads", "billing_submission_fee_claims",
		"billing_allocations", "billing_allocation_targets",
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
		var accountSequenceNotNull int
		if err := database.NewRaw(`SELECT "notnull" FROM pragma_table_info('journal_transactions') WHERE name = 'account_sequence'`).Scan(ctx, &accountSequenceNotNull); err != nil {
			return fmt.Errorf("billingstore: SQLite journal account sequence verification: %w", err)
		}
		if accountSequenceNotNull != 0 {
			return fmt.Errorf("billingstore: SQLite journal account_sequence must be nullable")
		}
		for _, index := range []string{"idx_billing_journal_account_sequence", "idx_billing_journal_source", journalReversalUniqueIndex, providerJournalOrderIndex, providerJournalBookOrderIndex, usageLegCallBLegIndex, usageLegCallAttemptSeqIndex, usageCallCallIDIndex, usageCallAccountSessionIndex, usageCallClaimStatusIndex, usageCallClaimPendingIndex, providerCostWorkStatusIndex, providerCostWorkPendingIndex, exposureAccountStatusIndex, providerMaintenanceFingerprintIndex, billingValuationInputIndex, billingValuationSubjectIndex, billingValuationLineItemIndex, billingReconciliationSubjectIdx, billingReconciliationInputIndex, billingEconomicWorkPendingIndex, costPassThroughHeadCallIndex, submissionFeeClaimScopeIndex, billingAllocationSourceIndex, billingAllocationTargetIndex} {
			var name string
			if err := database.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &name); err != nil || name != index {
				if err != nil {
					return fmt.Errorf("billingstore: missing SQLite index %s: %w", index, err)
				}
				return fmt.Errorf("billingstore: missing SQLite index %s", index)
			}
		}
		tableFragments := map[string][]string{
			"billing_accounts":                {"CHECK", "credit_limit_nano", "opening_balance_nano", "reconcile_required"},
			"billing_account_openings":        {"FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"billing_reconciliation_events":   {"FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"billing_account_policy_events":   {"FOREIGN KEY(account_id) REFERENCES billing_accounts", "UNIQUE(account_id, source_key)"},
			"usage_leg_records":               {"usage_leg_key", "call_id", "b_leg_id", "attempt_seq", "payload_json", "fingerprint"},
			"provider_cost_work":              {"usage_leg_key", "call_id", "status", "attempt_count", "next_attempt_at", "last_error", "updated_at"},
			"usage_call_records":              {"usage_call_key", "call_id", "account_id", "a_leg_id", "session_id", "expected_b_leg_ids", "payload_json", "fingerprint", "claim_status", "claim_attempt_count", "next_claim_at", "last_claim_error"},
			"call_exposures":                  {"exposure_key", "account_id", "call_id", "max_exposure_nano", "pricing_ref", "charge_policy_ref", "fingerprint", "status", "FOREIGN KEY(account_id) REFERENCES billing_accounts", "UNIQUE(account_id, call_id)"},
			"journal_transactions":            {"CHECK", "operation_kind = 'provider_call_cogs'", "account_sequence IS NULL OR account_sequence > 0", "operation_kind <> 'provider_call_cogs'", "UNIQUE(account_id, book, source_key)", "UNIQUE(account_id, account_sequence)", "FOREIGN KEY(account_id) REFERENCES billing_accounts", "recorded_at"},
			"journal_entries":                 {"CHECK", "side IN ('debit','credit')", "amount_nano > 0", "FOREIGN KEY(transaction_id) REFERENCES journal_transactions"},
			"billing_operation_snapshots":     {"FOREIGN KEY(account_id) REFERENCES billing_accounts", "UNIQUE(account_id, operation_kind, source_key)", "integrity_fingerprint"},
			"provider_maintenance_usage":      {"operation_id", "PRIMARY KEY", "a_leg_id", "target_id", "backend_id", "model_id", "recorded_at", "evidence_json", "fingerprint"},
			"billing_valuations":              {"valuation_id", "valuation_version", "input_set_hash", "canonical_json", "fingerprint", "UNIQUE(store_id, valuation_id, valuation_version)"},
			"billing_valuation_lines":         {"valuation_id", "valuation_version", "line_id", "amount_coefficient", "amount_scale", "amount_present", "rounded_nano", "rounded_currency", "rounded_present", "FOREIGN KEY(store_id, valuation_id, valuation_version) REFERENCES billing_valuations"},
			"billing_reconciliations":         {"reconciliation_id", "reconciliation_version", "subject_json", "input_set_hash", "result_json", "canonical_json", "fingerprint", "UNIQUE(store_id, reconciliation_id, reconciliation_version)"},
			"billing_economic_work":           {"work_id", "work_version", "payload_json", "fingerprint", "status", "UNIQUE(store_id, work_id, work_version)"},
			"billing_unit_balances":           {"identity_key", "canonical_key", "account_id", "pool_id", "period_id", "component_key", "status", "granted_coefficient", "available_coefficient", "reserved_coefficient", "consumed_coefficient", "version", "fence", "UNIQUE(store_id, identity_key)"},
			"billing_unit_operations":         {"operation_id", "identity_key", "canonical_key", "kind", "source", "quantity_coefficient", "expected_version", "fence", "fingerprint", "operation_json", "result_json", "UNIQUE(store_id, operation_id)"},
			"billing_unit_reservations":       {"reservation_id", "identity_key", "canonical_key", "quantity_coefficient", "status", "source_operation_id", "UNIQUE(store_id, reservation_id)"},
			"billing_cost_pass_through_heads": {"head_key", "account_id", "call_id", "settlement_operation_key", "original_transaction_id", "a_leg_id", "policy_id", "policy_version", "missing_cost", "safe_bound_nano", "currency", "allow_late_adjustment", "status", "posted_amount_nano", "provider_lur_key", "provider_valuation_id", "provider_revision", "provider_input_hash", "settlement_fingerprint", "head_version", "fence", "UNIQUE(account_id, call_id)", "FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"billing_submission_fee_claims":   {"claim_key", "store_id", "account_id", "submission_id", "source_call_id", "tariff_id", "tariff_version", "tariff_content_hash", "policy_id", "policy_version", "policy_content_hash", "context_fingerprint", "amount_nano", "currency", "UNIQUE(store_id, account_id, submission_id)", "FOREIGN KEY(account_id) REFERENCES billing_accounts"},
			"billing_allocations":             {"allocation_id", "allocation_version", "revision", "source_subject_kind", "source_subject_json", "source_basis", "source_amount_coefficient", "source_quantity_coefficient", "policy_method", "policy_version", "policy_hash", "rounding_residual_policy", "source_observation_refs_json", "canonical_json", "fingerprint", "UNIQUE(store_id, allocation_id, allocation_version)"},
			"billing_allocation_targets":      {"allocation_id", "allocation_version", "target_id", "target_json", "target_tenant_id", "target_pool_id", "target_window_id", "target_reset_at_unix", "target_start_at_unix", "target_end_at_unix", "unallocated", "informational", "weight_numerator", "weight_denominator", "share_numerator", "share_denominator", "FOREIGN KEY(store_id, allocation_id, allocation_version) REFERENCES billing_allocations"},
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
		var retiredReconciliationColumns int
		if err := database.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliation_events') WHERE name LIKE 'reserved%nano'`).Scan(ctx, &retiredReconciliationColumns); err != nil {
			return fmt.Errorf("billingstore: SQLite reconciliation event retired-column verification: %w", err)
		}
		if retiredReconciliationColumns != 0 {
			return fmt.Errorf("billingstore: SQLite reconciliation events contain retired columns")
		}
		for _, trigger := range []string{"billing_exposure_immutable_update", "billing_exposure_immutable_delete", "billing_operation_snapshots_immutable_update", "billing_operation_snapshots_immutable_delete", "billing_account_openings_immutable_update", "billing_account_openings_immutable_delete", "billing_reconciliation_events_immutable_update", "billing_reconciliation_events_immutable_delete", "billing_policy_events_immutable_update", "billing_policy_events_immutable_delete", "billing_usage_leg_immutable_update", "billing_usage_leg_immutable_delete", "billing_usage_call_immutable_update", "billing_usage_call_immutable_delete", "billing_journal_tx_immutable_update", "billing_journal_tx_immutable_delete", "billing_journal_entry_immutable_update", "billing_journal_entry_immutable_delete", "billing_provider_maintenance_immutable_update", "billing_provider_maintenance_immutable_delete", "billing_v2_valuations_immutable_update", "billing_v2_valuations_immutable_delete", "billing_v2_reconciliations_immutable_update", "billing_v2_reconciliations_immutable_delete", "billing_v2_economic_work_immutable_update", "billing_v2_economic_work_immutable_delete", "billing_unit_operations_immutable_update", "billing_unit_operations_immutable_delete", "billing_submission_fee_claims_immutable_update", "billing_submission_fee_claims_immutable_delete", "billing_allocations_immutable_update", "billing_allocations_immutable_delete", "billing_allocation_targets_immutable_update", "billing_allocation_targets_immutable_delete"} {
			var name string
			if err := database.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(ctx, &name); err != nil || name != trigger {
				return fmt.Errorf("billingstore: missing SQLite immutability trigger %s", trigger)
			}
		}
		return nil
	}
	var retiredReconciliationColumns int
	if err := database.NewRaw(`SELECT COUNT(1) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_reconciliation_events' AND column_name LIKE 'reserved%nano'`).Scan(ctx, &retiredReconciliationColumns); err != nil {
		return fmt.Errorf("billingstore: PostgreSQL reconciliation event retired-column verification: %w", err)
	}
	if retiredReconciliationColumns != 0 {
		return fmt.Errorf("billingstore: PostgreSQL reconciliation events contain retired columns")
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
		{"reserved column removal migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ReservedColumnRemovalMigrationName}, []string{ReservedColumnRemovalMigrationName}},
		{"legacy usage retirement migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{LegacyUsageRetirementMigrationName}, []string{LegacyUsageRetirementMigrationName}},
		{"usage append outbox retirement migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{UsageAppendOutboxRetirementMigrationName}, []string{UsageAppendOutboxRetirementMigrationName}},
		{"session id migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{SessionIDMigrationName}, []string{SessionIDMigrationName}},
		{"usage leg records migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{UsageLegRecordsMigrationName}, []string{UsageLegRecordsMigrationName}},
		{"usage call records migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{UsageCallRecordsMigrationName}, []string{UsageCallRecordsMigrationName}},
		{"provider cost work migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ProviderCostWorkMigrationName}, []string{ProviderCostWorkMigrationName}},
		{"provider maintenance migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ProviderMaintenanceMigrationName}, []string{ProviderMaintenanceMigrationName}},
		{"journal account sequence index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{"idx_billing_journal_account_sequence"}, []string{"account_id", "account_sequence"}},
		{"journal provider order index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerJournalOrderIndex}, []string{"account_id", "recorded_at", "transaction_id"}},
		{"journal book provider order index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerJournalBookOrderIndex}, []string{"book", "currency", "recorded_at", "transaction_id"}},
		{"journal account sequence nullable", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'journal_transactions' AND column_name = 'account_sequence' AND is_nullable = 'YES' LIMIT 1`, nil, []string{"account_sequence"}},
		{"journal source index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{"idx_billing_journal_source"}, []string{"account_id", "book", "source_key"}},
		{"journal reversal unique index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{journalReversalUniqueIndex}, []string{"account_id", "book", "reversal_of"}},
		{"usage leg table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'usage_leg_records' LIMIT 1`, nil, []string{"usage_leg_records"}},
		{"usage leg CallID/BLegID unique index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageLegCallBLegIndex}, []string{"UNIQUE", "call_id", "b_leg_id"}},
		{"usage leg attempt seq column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'usage_leg_records' AND column_name = 'attempt_seq' LIMIT 1`, nil, []string{"attempt_seq"}},
		{"usage leg CallID/attempt_seq unique index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{usageLegCallAttemptSeqIndex}, []string{"UNIQUE", "call_id", "attempt_seq"}},
		{"usage leg sequence migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{UsageLegSequenceMigrationName}, []string{UsageLegSequenceMigrationName}},
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
		{"provider maintenance migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ProviderMaintenanceMigrationName}, []string{ProviderMaintenanceMigrationName}},
		{"provider maintenance integrity migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{ProviderMaintenanceIntegrityMigrationName}, []string{ProviderMaintenanceIntegrityMigrationName}},
		{"provider maintenance table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'provider_maintenance_usage' LIMIT 1`, nil, []string{"provider_maintenance_usage"}},
		{"provider maintenance operation column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'provider_maintenance_usage' AND column_name = 'operation_id' LIMIT 1`, nil, []string{"operation_id"}},
		{"provider maintenance evidence column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'provider_maintenance_usage' AND column_name = 'evidence_json' LIMIT 1`, nil, []string{"evidence_json"}},
		{"provider maintenance fingerprint index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerMaintenanceFingerprintIndex}, []string{"UNIQUE", "fingerprint"}},
		{"provider maintenance operation primary key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'provider_maintenance_usage' AND c.contype = 'p' LIMIT 1`, nil, []string{"PRIMARY KEY", "operation_id"}},
		{"provider maintenance immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'provider_maintenance_usage' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_provider_maintenance_immutable"}, []string{"billing_provider_maintenance_immutable"}},
		{"provider cost work pending index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerCostWorkPendingIndex}, []string{"status", "next_attempt_at", "updated_at", "usage_leg_key"}},
		{"provider cost work status index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{providerCostWorkStatusIndex}, []string{"status", "updated_at", "usage_leg_key"}},
		{"exposure table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'call_exposures' LIMIT 1`, nil, []string{"call_exposures"}},
		{"exposure account status index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{exposureAccountStatusIndex}, []string{"account_id", "status", "created_at"}},
		{"journal source uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%account_id%book%source_key%' LIMIT 1`, nil, []string{"UNIQUE", "account_id", "book", "source_key"}},
		{"journal sequence uniqueness", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.contype = 'u' AND pg_get_constraintdef(c.oid) LIKE '%account_id%account_sequence%' LIMIT 1`, nil, []string{"UNIQUE", "account_id", "account_sequence"}},
		{"journal sequence contract", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.conname = ? LIMIT 1`, []any{providerJournalSequenceCheck}, []string{"provider_call_cogs", "account_sequence", "IS NULL", "account_sequence"}},
		{"policy account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_account_policy_events' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"journal transaction account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"journal entry transaction foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_entries' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "journal_transactions"}},
		{"journal entry amount check", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_entries' AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%amount_nano%' LIMIT 1`, nil, []string{"CHECK", "amount_nano"}},
		{"journal entry side check", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_entries' AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%side%' LIMIT 1`, nil, []string{"CHECK", "side"}},
		{"operation snapshot immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_operation_snapshots' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_operation_snapshots_immutable"}, []string{"billing_operation_snapshots_immutable"}},
		{"reconciliation immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_reconciliation_events' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_reconciliation_events_immutable"}, []string{"billing_reconciliation_events_immutable"}},
		{"opening immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_account_openings' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_account_openings_immutable"}, []string{"billing_account_openings_immutable"}},
		{"policy immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_account_policy_events' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_policy_events_immutable"}, []string{"billing_policy_events_immutable"}},
		{"usage leg immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'usage_leg_records' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_usage_leg_immutable"}, []string{"billing_usage_leg_immutable"}},
		{"usage call immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'usage_call_records' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_usage_call_immutable"}, []string{"billing_usage_call_immutable"}},
		{"exposure immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'call_exposures' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_exposure_immutable"}, []string{"billing_exposure_immutable"}},
		{"journal transaction immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'journal_transactions' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_journal_tx_immutable"}, []string{"billing_journal_tx_immutable"}},
		{"journal entry immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'journal_entries' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_journal_entry_immutable"}, []string{"billing_journal_entry_immutable"}},
		{"V2 economics migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{BillingV2EconomicsMigrationName}, []string{BillingV2EconomicsMigrationName}},
		{"billing valuations table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_valuations' LIMIT 1`, nil, []string{"billing_valuations"}},
		{"billing valuation lines table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_valuation_lines' LIMIT 1`, nil, []string{"billing_valuation_lines"}},
		{"billing reconciliations table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_reconciliations' LIMIT 1`, nil, []string{"billing_reconciliations"}},
		{"billing economic work table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_economic_work' LIMIT 1`, nil, []string{"billing_economic_work"}},
		{"billing unit balances table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_unit_balances' LIMIT 1`, nil, []string{"billing_unit_balances"}},
		{"billing unit operations table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_unit_operations' LIMIT 1`, nil, []string{"billing_unit_operations"}},
		{"billing unit reservations table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_unit_reservations' LIMIT 1`, nil, []string{"billing_unit_reservations"}},
		{"billing unit balance identity index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{customerUnitBalanceIdentityIndex}, []string{"store_id", "identity_key"}},
		{"billing unit operation identity index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{customerUnitOperationIdentityIndex}, []string{"store_id", "operation_id"}},
		{"billing unit reservation identity index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{customerUnitReservationIdentityIndex}, []string{"store_id", "reservation_id"}},
		{"billing unit operation immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_unit_operations' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_unit_operations_immutable"}, []string{"billing_unit_operations_immutable"}},
		{"cost pass-through head migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{CostPassThroughHeadMigrationName}, []string{CostPassThroughHeadMigrationName}},
		{"cost pass-through heads table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_cost_pass_through_heads' LIMIT 1`, nil, []string{"billing_cost_pass_through_heads"}},
		{"cost pass-through head call index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{costPassThroughHeadCallIndex}, []string{"account_id", "call_id"}},
		{"cost pass-through head account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_cost_pass_through_heads' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"submission fee claim migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{SubmissionFeeClaimMigrationName}, []string{SubmissionFeeClaimMigrationName}},
		{"submission fee claims table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_submission_fee_claims' LIMIT 1`, nil, []string{"billing_submission_fee_claims"}},
		{"submission fee claim scope index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{submissionFeeClaimScopeIndex}, []string{"store_id", "account_id", "submission_id"}},
		{"submission fee claim account foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_submission_fee_claims' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_accounts"}},
		{"submission fee claim immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_submission_fee_claims' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_submission_fee_claims_immutable"}, []string{"billing_submission_fee_claims_immutable"}},
		{"allocation migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{BillingAllocationMigrationName}, []string{BillingAllocationMigrationName}},
		{"billing allocations table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_allocations' LIMIT 1`, nil, []string{"billing_allocations"}},
		{"billing allocation targets table", `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' LIMIT 1`, nil, []string{"billing_allocation_targets"}},
		{"billing allocation source index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingAllocationSourceIndex}, []string{"store_id", "source_subject_kind", "source_subject_id", "created_at_unix", "allocation_id", "allocation_version", "id"}},
		{"billing allocation target index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingAllocationTargetIndex}, []string{"store_id", "target_kind", "target_subject_id", "target_json", "allocation_id", "allocation_version", "target_id"}},
		{"allocation target scope migration history", `SELECT name FROM bun_billing_migrations WHERE name = ? LIMIT 1`, []any{BillingAllocationTargetScopeMigrationName}, []string{BillingAllocationTargetScopeMigrationName}},
		{"billing allocation target tenant column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' AND column_name = 'target_tenant_id' LIMIT 1`, nil, []string{"target_tenant_id"}},
		{"billing allocation target pool column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' AND column_name = 'target_pool_id' LIMIT 1`, nil, []string{"target_pool_id"}},
		{"billing allocation target window column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' AND column_name = 'target_window_id' LIMIT 1`, nil, []string{"target_window_id"}},
		{"billing allocation target reset column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' AND column_name = 'target_reset_at_unix' LIMIT 1`, nil, []string{"target_reset_at_unix"}},
		{"billing allocation target start column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' AND column_name = 'target_start_at_unix' LIMIT 1`, nil, []string{"target_start_at_unix"}},
		{"billing allocation target end column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_allocation_targets' AND column_name = 'target_end_at_unix' LIMIT 1`, nil, []string{"target_end_at_unix"}},
		{"billing allocation target foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_allocation_targets' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_allocations", "store_id", "allocation_id", "allocation_version"}},
		{"billing allocation immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_allocations' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_allocations_immutable"}, []string{"billing_allocations_immutable"}},
		{"billing allocation targets immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_allocation_targets' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_allocation_targets_immutable"}, []string{"billing_allocation_targets_immutable"}},
		{"billing valuation canonical JSON column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_valuations' AND column_name = 'canonical_json' LIMIT 1`, nil, []string{"canonical_json"}},
		{"billing reconciliation canonical JSON column", `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_reconciliations' AND column_name = 'canonical_json' LIMIT 1`, nil, []string{"canonical_json"}},
		{"billing valuation input identity index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingValuationInputIndex}, []string{"store_id", "input_set_hash", "rater_id", "rater_version", "policy_id", "policy_version", "tariff_id", "tariff_version", "basis"}},
		{"billing valuation subject index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingValuationSubjectIndex}, []string{"store_id", "subject_kind", "subject_id", "created_at_unix", "valuation_id", "valuation_version", "id"}},
		{"billing valuation line item index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingValuationLineItemIndex}, []string{"store_id", "item_id", "valuation_id", "valuation_version", "line_id"}},
		{"billing reconciliation subject index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingReconciliationSubjectIdx}, []string{"store_id", "subject_kind", "subject_id", "created_at_unix", "reconciliation_id", "reconciliation_version", "id"}},
		{"billing reconciliation basis/input index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingReconciliationInputIndex}, []string{"store_id", "basis", "input_set_hash", "created_at_unix", "reconciliation_id", "reconciliation_version", "id"}},
		{"billing economic work pending index", `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ? LIMIT 1`, []any{billingEconomicWorkPendingIndex}, []string{"store_id", "status", "created_at_unix", "work_id", "work_version", "id"}},
		{"billing valuation lines foreign key", `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_valuation_lines' AND c.contype = 'f' LIMIT 1`, nil, []string{"FOREIGN KEY", "billing_valuations", "store_id", "valuation_id", "valuation_version"}},
		{"billing valuation immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_valuations' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_v2_valuations_immutable"}, []string{"billing_v2_valuations_immutable"}},
		{"billing reconciliation immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_reconciliations' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_v2_reconciliations_immutable"}, []string{"billing_v2_reconciliations_immutable"}},
		{"billing economic work immutable trigger", `SELECT tr.tgname FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = 'billing_economic_work' AND tr.tgname = ? AND NOT tr.tgisinternal LIMIT 1`, []any{"billing_v2_economic_work_immutable"}, []string{"billing_v2_economic_work_immutable"}},
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

// DB exposes the owned Bun handle to infrastructure composition code only.
// Public host/SDK contracts intentionally do not depend on Bun.
func (s *DurableStore) DB() *bun.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Database is an explicit infra spelling used by composition checks.
func (s *DurableStore) Database() *bun.DB { return s.DB() }
