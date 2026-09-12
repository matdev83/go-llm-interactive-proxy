package billingstore

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// phase1PostingSchemaFixture is the checked-in V1 posting and dialect schema
// boundary. It protects legacy identity and migration assumptions while V2
// tables are designed; it is not a declaration that these are V2 contracts.
//
//go:embed testdata/phase1_v1_posting_schema_compatibility.json
var phase1PostingSchemaFixture embed.FS

type phase1PostingSchemaDocument struct {
	FixtureVersion  string                       `json:"fixture_version"`
	MigrationIDs    map[string]string            `json:"migration_ids"`
	PostingIdentity map[string]string            `json:"posting_identity"`
	SchemaColumns   map[string][]string          `json:"schema_columns"`
	DialectTypes    map[string]map[string]string `json:"dialect_types"`
}

func readPhase1PostingSchemaFixture(t *testing.T) phase1PostingSchemaDocument {
	t.Helper()
	payload, err := phase1PostingSchemaFixture.ReadFile("testdata/phase1_v1_posting_schema_compatibility.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture phase1PostingSchemaDocument
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatalf("decode posting/schema fixture: %v", err)
	}
	return fixture
}

func phase1BillingStoreSource(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), name))
	if err != nil {
		t.Fatalf("read billingstore source %s: %v", name, err)
	}
	return string(source)
}

func requirePhase1DDLTerms(t *testing.T, name, ddl string, terms []string) {
	t.Helper()
	lower := strings.ToLower(ddl)
	for _, term := range terms {
		if !strings.Contains(lower, strings.ToLower(term)) {
			t.Fatalf("%s schema lost frozen term %q", name, term)
		}
	}
}

func TestPhase1V1PostingIdentityAndDialectSchemaCompatibility(t *testing.T) {
	t.Parallel()
	fixture := readPhase1PostingSchemaFixture(t)
	if fixture.FixtureVersion != "phase1-v1-posting-schema-compatibility" {
		t.Fatalf("fixture version = %q", fixture.FixtureVersion)
	}
	for _, table := range []string{"journal_transactions", "usage_leg_records", "usage_call_records"} {
		if len(fixture.SchemaColumns[table]) == 0 {
			t.Fatalf("schema column fixture for %s is empty", table)
		}
	}
	for _, kind := range []string{"journal_account_sequence", "journal_amount_nano", "usage_leg_attempt_sequence"} {
		for _, dialect := range []string{"sqlite", "postgres"} {
			if strings.TrimSpace(fixture.DialectTypes[kind][dialect]) == "" {
				t.Fatalf("dialect type fixture for %s/%s is empty", kind, dialect)
			}
		}
	}

	wantMigrations := map[string]string{
		"billing_baseline":                   BaselineMigrationName,
		"usage_leg_records":                  UsageLegRecordsMigrationName,
		"usage_call_records":                 UsageCallRecordsMigrationName,
		"usage_leg_attempt_sequence":         UsageLegSequenceMigrationName,
		"provider_journal_order":             ProviderJournalOrderMigrationName,
		"provider_journal_sequence_contract": ProviderJournalSequenceContractMigrationName,
	}
	for key, want := range wantMigrations {
		if got := fixture.MigrationIDs[key]; got != want {
			t.Fatalf("migration %s = %q, want %q", key, got, want)
		}
	}

	wantIdentity := map[string]string{
		"journal_book":                        string(billing.JournalBookFinancial),
		"provider_cogs_operation_kind":        "provider_call_cogs",
		"provider_cost_source_prefix":         "provider-cost:v1:",
		"journal_fingerprint_prefix":          billing.JournalFingerprintPrefix,
		"provider_debit_account":              "inference_provider_cogs",
		"provider_credit_account":             "provider_payable_clearing",
		"journal_reversal_unique_index":       journalReversalUniqueIndex,
		"provider_journal_order_index":        providerJournalOrderIndex,
		"provider_journal_book_order_index":   providerJournalBookOrderIndex,
		"usage_leg_call_bleg_unique_index":    usageLegCallBLegIndex,
		"usage_leg_call_attempt_unique_index": usageLegCallAttemptSeqIndex,
		"usage_call_id_unique_index":          usageCallCallIDIndex,
		"usage_call_account_session_index":    usageCallAccountSessionIndex,
		"usage_call_claim_status_index":       usageCallClaimStatusIndex,
		"usage_call_claim_pending_index":      usageCallClaimPendingIndex,
		"provider_journal_sequence_check":     providerJournalSequenceCheck,
	}
	for key, want := range wantIdentity {
		if got := fixture.PostingIdentity[key]; got != want {
			t.Fatalf("posting identity %s = %q, want %q", key, got, want)
		}
	}
	providerSource, err := billing.ProviderCostSourceKey("bc_0123456789abcdef0123456789abcdef:b-primary")
	if err != nil || !strings.HasPrefix(providerSource, fixture.PostingIdentity["provider_cost_source_prefix"]) {
		t.Fatalf("provider source key = %q/%v, want prefix %q", providerSource, err, fixture.PostingIdentity["provider_cost_source_prefix"])
	}

	providerCostSource := phase1BillingStoreSource(t, "provider_cost_store.go")
	requirePhase1DDLTerms(t, "provider cost writer", providerCostSource, []string{
		`OperationKind: "provider_call_cogs"`,
		`LedgerAccount: "inference_provider_cogs"`,
		`LedgerAccount: "provider_payable_clearing"`,
	})

	journalSource := phase1BillingStoreSource(t, "20260830000000_billing_provider_journal_order.go") + "\n" + phase1BillingStoreSource(t, "20260816000000_billing_phase7.go")
	requirePhase1DDLTerms(t, "provider journal migration", journalSource, []string{
		fixture.PostingIdentity["journal_reversal_unique_index"],
		fixture.PostingIdentity["provider_journal_order_index"],
		fixture.PostingIdentity["provider_journal_book_order_index"],
		fixture.PostingIdentity["provider_journal_sequence_check"],
		fixture.PostingIdentity["provider_cogs_operation_kind"],
	})

	sqliteJournalStatements := append([]string{}, sqliteDDL()...)
	for _, column := range sqlitePhase4Columns() {
		sqliteJournalStatements = append(sqliteJournalStatements, column.definition)
	}
	sqliteJournalStatements = append(sqliteJournalStatements, sqlitePhase4Tables()...)
	sqliteJournal := strings.Join(sqliteJournalStatements, "\n")
	postgresJournalStatements := append([]string{}, postgresDDL()...)
	postgresJournalStatements = append(postgresJournalStatements, postgresPhase4Statements()...)
	postgresJournal := strings.Join(postgresJournalStatements, "\n")
	for _, dialectDDL := range []struct {
		name string
		ddl  string
	}{
		{name: "sqlite journal", ddl: sqliteJournal},
		{name: "postgres journal", ddl: postgresJournal},
	} {
		requirePhase1DDLTerms(t, dialectDDL.name, dialectDDL.ddl, fixture.SchemaColumns["journal_transactions"])
	}
	requirePhase1DDLTerms(t, "sqlite journal types", sqliteJournal, []string{
		"account_sequence INTEGER", "amount_nano INTEGER",
	})
	requirePhase1DDLTerms(t, "postgres journal types", postgresJournal, []string{
		"account_sequence BIGINT", "amount_nano BIGINT",
	})

	sqliteLeg := strings.Join(sqliteUsageLegRecordsDDL(), "\n")
	postgresLeg := strings.Join(postgresUsageLegRecordsDDL(), "\n")
	for _, dialectDDL := range []struct {
		name string
		ddl  string
	}{
		{name: "sqlite usage leg", ddl: sqliteLeg},
		{name: "postgres usage leg", ddl: postgresLeg},
	} {
		requirePhase1DDLTerms(t, dialectDDL.name, dialectDDL.ddl, fixture.SchemaColumns["usage_leg_records"])
	}
	sqliteCall := strings.Join(sqliteUsageCallRecordsDDL(), "\n")
	postgresCall := strings.Join(postgresUsageCallRecordsDDL(), "\n")
	for _, dialectDDL := range []struct {
		name string
		ddl  string
	}{
		{name: "sqlite usage call", ddl: sqliteCall},
		{name: "postgres usage call", ddl: postgresCall},
	} {
		requirePhase1DDLTerms(t, dialectDDL.name, dialectDDL.ddl, fixture.SchemaColumns["usage_call_records"])
	}

	sequenceSource := phase1BillingStoreSource(t, "20260828000000_billing_usage_leg_sequence.go")
	for dialect, typeName := range fixture.DialectTypes["usage_leg_attempt_sequence"] {
		needle := typeName
		if dialect == "postgres" {
			needle = "ADD COLUMN IF NOT EXISTS attempt_seq " + typeName
		} else {
			needle = "ADD COLUMN attempt_seq " + typeName
		}
		requirePhase1DDLTerms(t, dialect+" attempt sequence", sequenceSource, []string{needle})
	}
	for dialect, typeName := range fixture.DialectTypes["journal_account_sequence"] {
		requirePhase1DDLTerms(t, dialect+" journal account sequence", map[string]string{"sqlite": sqliteJournal, "postgres": postgresJournal}[dialect], []string{"account_sequence " + typeName})
	}
	for dialect, typeName := range fixture.DialectTypes["journal_amount_nano"] {
		requirePhase1DDLTerms(t, dialect+" journal amount", map[string]string{"sqlite": sqliteJournal, "postgres": postgresJournal}[dialect], []string{"amount_nano " + typeName})
	}
}
