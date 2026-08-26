package dbparity

import (
	"fmt"
	"slices"
	"strings"
)

// BackendClass classifies a persistence capability's backend and topology posture.
type BackendClass string

const (
	Common              BackendClass = "common"
	SQLiteSpecific      BackendClass = "sqlite"
	PostgresDirect      BackendClass = "postgres-direct"
	PostgresDistributed BackendClass = "postgres-distributed"
	PostgresPooler      BackendClass = "postgres-pooler"
)

// Capability describes an individual persistence capability and its evidence posture.
type Capability struct {
	ID        string       `json:"id"`
	Class     BackendClass `json:"class"`
	Evidence  string       `json:"evidence"`
	Rationale string       `json:"rationale,omitempty"` // Required for non-common capabilities
}

// Component records the authoritative source, test, migration, and contract metadata
// for a production persistence component supporting SQLite and PostgreSQL.
type Component struct {
	ID             string       `json:"id"`
	SourceRoots    []string     `json:"source_roots"`
	TestPackages   []string     `json:"test_packages"`
	StoreContracts []string     `json:"store_contracts"`
	MigrationRoots []string     `json:"migration_roots"`
	Capabilities   []Capability `json:"capabilities"`
}

// Inventory captures all dual-dialect persistence components and shared infrastructure.
type Inventory struct {
	Components  []Component `json:"components"`
	SharedInfra []string    `json:"shared_infra"`
}

// DefaultInventory returns the frozen inventory of all 8 production persistence families
// and shared database infrastructure, audited against current repository state.
func DefaultInventory() Inventory {
	return Inventory{
		Components: []Component{
			{
				ID: "billing",
				SourceRoots: []string{
					"internal/infra/billingstore",
				},
				TestPackages: []string{
					"internal/infra/billingstore",
				},
				StoreContracts: []string{
					"internal/core/billing.AccountStore",
					"internal/core/billing.ExposureStore",
					"internal/core/billing.JournalStore",
					"internal/core/billing.CallUsageStore",
					"internal/core/billing.ProviderCostStore",
					"internal/core/billing.ProviderCostWorkStore",
					"internal/core/billing.ReportsStore",
				},
				MigrationRoots: []string{
					"internal/infra/billingstore",
				},
				Capabilities: []Capability{
					{
						ID:       "balanced-posting-replay-settlement",
						Class:    Common,
						Evidence: "internal/infra/billingstore/contract_test.go:runBillingStoreContract",
					},
					{
						ID:       "dual-engine-schema-verification",
						Class:    Common,
						Evidence: "internal/infra/billingstore/store.go:VerifySchema",
					},
					{
						ID:        "postgres-direct-isolated-schema",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/billingstore/postgres_isolated_integration_test.go",
						Rationale: "Direct PostgreSQL verifies isolated schema creation, sequence integrity, and provider cost independence.",
					},
					{
						ID:        "sqlite-local-transaction-safety",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/billingstore/store_test.go",
						Rationale: "SQLite executes against local in-memory or file-backed single-writer connections.",
					},
				},
			},
			{
				ID: "concurrency-authority",
				SourceRoots: []string{
					"internal/infra/concurrencyauthority/leasestore",
				},
				TestPackages: []string{
					"internal/infra/concurrencyauthority/leasestore",
				},
				StoreContracts: []string{
					"internal/core/concurrencyauthority/app.LeaseStore",
				},
				MigrationRoots: []string{
					"internal/infra/concurrencyauthority/leasestore",
				},
				Capabilities: []Capability{
					{
						ID:       "acquire-renew-release-query",
						Class:    Common,
						Evidence: "internal/infra/concurrencyauthority/leasestore/contract_test.go",
					},
					{
						ID:       "atomic-lease-set-operations",
						Class:    Common,
						Evidence: "internal/infra/concurrencyauthority/leasestore/contract_test.go",
					},
					{
						ID:        "postgres-distributed-strictness",
						Class:     PostgresDistributed,
						Evidence:  "internal/infra/concurrencyauthority/leasestore/postgres_integration_test.go",
						Rationale: "PostgreSQL provides multi-instance distributed strict row locks and schema catalog checks.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/concurrencyauthority/leasestore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction poolers require non-session-pinned lease management under dual-endpoint topology.",
					},
					{
						ID:        "sqlite-single-node-serialization",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/concurrencyauthority/leasestore/sqlite_test.go",
						Rationale: "SQLite leases operate in single-node mode using serialized writer transactions.",
					},
				},
			},
			{
				ID: "continuity",
				SourceRoots: []string{
					"internal/core/continuity/bunstore",
				},
				TestPackages: []string{
					"internal/core/continuity/bunstore",
				},
				StoreContracts: []string{
					"internal/core/b2bua.Store",
					"internal/core/b2bua.InterleavedStateStore",
					"internal/core/b2bua.ALegRetirementObserver",
					"internal/core/routeoverride.Store",
					"internal/core/conversationview.Store",
				},
				MigrationRoots: []string{
					"internal/core/continuity/bunstore",
				},
				Capabilities: []Capability{
					{
						ID:       "a-leg-lifecycle-lineage-persistence",
						Class:    Common,
						Evidence: "internal/core/continuity/bunstore/store_test.go",
					},
					{
						ID:       "route-override-persistence",
						Class:    Common,
						Evidence: "internal/core/continuity/bunstore/routeoverride_persist_test.go",
					},
					{
						ID:       "conversation-view-snapshot-mutations",
						Class:    Common,
						Evidence: "internal/core/continuity/bunstore/conversationview_test.go",
					},
					{
						ID:        "postgres-direct-b-leg-returning-allocation",
						Class:     PostgresDirect,
						Evidence:  "internal/core/continuity/bunstore/postgres_integration_test.go",
						Rationale: "PostgreSQL uses atomic UPDATE ... RETURNING for B-leg sequence allocation.",
					},
					{
						ID:        "sqlite-serialized-b-leg-allocation",
						Class:     SQLiteSpecific,
						Evidence:  "internal/core/continuity/bunstore/legacy_sqlite_compat_test.go",
						Rationale: "SQLite relies on serialized write transactions for B-leg allocation.",
					},
				},
			},
			{
				ID: "control-plane-ledger",
				SourceRoots: []string{
					"internal/infra/controlplane/ledgerstore",
				},
				TestPackages: []string{
					"internal/infra/controlplane/ledgerstore",
					"internal/infra/controlplane/ledgerstore/contract",
				},
				StoreContracts: []string{
					"internal/core/controlplane.Store",
				},
				MigrationRoots: []string{
					"internal/infra/controlplane/ledgerstore",
				},
				Capabilities: []Capability{
					{
						ID:       "append-query-projections-retention",
						Class:    Common,
						Evidence: "internal/infra/controlplane/ledgerstore/contract/contract.go",
					},
					{
						ID:        "postgres-direct-catalog-schema",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/controlplane/ledgerstore/postgres_integration_test.go",
						Rationale: "PostgreSQL validates information_schema catalog constraints and type bindings.",
					},
					{
						ID:        "sqlite-local-type-adaptation",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/controlplane/ledgerstore/store_test.go",
						Rationale: "SQLite adapts boolean and integer placeholders via driver-specific query translation.",
					},
				},
			},
			{
				ID: "metering-journal",
				SourceRoots: []string{
					"internal/infra/metering/journalstore",
				},
				TestPackages: []string{
					"internal/infra/metering/journalstore",
				},
				StoreContracts: []string{
					"pkg/lipsdk/metering.Recorder",
					"pkg/lipsdk/metering.Querier",
				},
				MigrationRoots: []string{
					"internal/infra/metering/journalstore",
				},
				Capabilities: []Capability{
					{
						ID:       "fact-append-query-restart-reconstruction",
						Class:    Common,
						Evidence: "internal/infra/metering/journalstore/phase3_contract_red_test.go",
					},
					{
						ID:       "schema-v2-bounded-queries",
						Class:    Common,
						Evidence: "internal/infra/metering/journalstore/phase35_bounded_query_test.go",
					},
					{
						ID:        "sqlite-busy-retry-backoff",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/metering/journalstore/sqlite_retry_contract_test.go",
						Rationale: "SQLite requires locked/busy error classification and backoff under concurrency.",
					},
					{
						ID:        "postgres-direct-schema-verification",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/metering/journalstore/postgres_integration_test.go",
						Rationale: "PostgreSQL verifies table structures, bounded index definitions, and migration history.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/metering/journalstore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction pooler support for metering fact intake.",
					},
				},
			},
			{
				ID: "secure-sessions",
				SourceRoots: []string{
					"internal/core/securesession/adapters/bunstore",
				},
				TestPackages: []string{
					"internal/core/securesession/adapters/bunstore",
					"internal/core/securesession/storecontract",
				},
				StoreContracts: []string{
					"internal/core/securesession/app.Store",
					"internal/core/securesession/app.SessionUsageRollup",
				},
				MigrationRoots: []string{
					"internal/core/securesession/adapters/bunstore",
				},
				Capabilities: []Capability{
					{
						ID:       "session-lifecycle-transcript-audit-quarantine",
						Class:    Common,
						Evidence: "internal/core/securesession/storecontract/contract.go",
					},
					{
						ID:        "postgres-direct-text-json-bytea-handling",
						Class:     PostgresDirect,
						Evidence:  "internal/core/securesession/storecontract/postgres_bun_contract_test.go",
						Rationale: "PostgreSQL requires JSON text binding to prevent BYTEA hex-escaping on readback.",
					},
					{
						ID:        "sqlite-pragma-table-info-migration",
						Class:     SQLiteSpecific,
						Evidence:  "internal/core/securesession/adapters/bunstore/legacy_sqlite_compat_test.go",
						Rationale: "SQLite uses pragma_table_info column probes for safe incremental schema migration.",
					},
				},
			},
			{
				ID: "terminal-work",
				SourceRoots: []string{
					"internal/infra/terminalwork/workstore",
				},
				TestPackages: []string{
					"internal/infra/terminalwork/workstore",
				},
				StoreContracts: []string{
					"internal/core/terminalwork/app.WorkStore",
					"internal/core/terminalwork/app.RecoveryStore",
				},
				MigrationRoots: []string{
					"internal/infra/terminalwork/workstore",
				},
				Capabilities: []Capability{
					{
						ID:       "claim-renew-complete-retry-quarantine",
						Class:    Common,
						Evidence: "internal/infra/terminalwork/workstore/phase43_contract_test.go",
					},
					{
						ID:       "runtime-generation-and-instance-pinning",
						Class:    Common,
						Evidence: "internal/infra/terminalwork/workstore/runtime_generation_migration_test.go",
					},
					{
						ID:        "postgres-direct-integration",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/terminalwork/workstore/postgres_integration_test.go",
						Rationale: "PostgreSQL verifies schema migration history, unique constraint violations, and processor execution.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/terminalwork/workstore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction pooler support for work claim/heartbeat loop.",
					},
					{
						ID:        "sqlite-local-execution",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/terminalwork/workstore/append_outcome_test.go",
						Rationale: "SQLite executes durable work claims and queries against local storage.",
					},
				},
			},
			{
				ID: "usage-authority",
				SourceRoots: []string{
					"internal/infra/usageauthority/authoritystore",
				},
				TestPackages: []string{
					"internal/infra/usageauthority/authoritystore",
					"internal/infra/usageauthority/authoritystore/contract",
				},
				StoreContracts: []string{
					"internal/core/usageauthority/app.Store",
				},
				MigrationRoots: []string{
					"internal/infra/usageauthority/authoritystore",
				},
				Capabilities: []Capability{
					{
						ID:       "reserve-settle-release-apply-usage",
						Class:    Common,
						Evidence: "internal/infra/usageauthority/authoritystore/contract/contract.go",
					},
					{
						ID:       "lost-update-prevention-replay-convergence",
						Class:    Common,
						Evidence: "internal/infra/usageauthority/authoritystore/contract/contract.go",
					},
					{
						ID:        "postgres-distributed-strictness",
						Class:     PostgresDistributed,
						Evidence:  "internal/infra/usageauthority/authoritystore/postgres_integration_test.go",
						Rationale: "PostgreSQL uses SELECT ... FOR UPDATE row-locking across distributed proxy instances.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/usageauthority/authoritystore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction pooler support under dual-endpoint topology.",
					},
					{
						ID:        "sqlite-begin-immediate-serialization",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/usageauthority/authoritystore/store_test.go",
						Rationale: "SQLite uses BEGIN IMMEDIATE txlock for single-node serialized writer transactions.",
					},
				},
			},
		},
		SharedInfra: []string{
			"internal/infra/db",
			"internal/infra/dbmigrate",
		},
	}
}

// Validate verifies that the inventory satisfies all structural invariants:
// - no empty component IDs
// - no duplicate component IDs
// - deterministic alphabetical sorting of components by ID
// - non-empty source roots, test packages, migration roots, store contracts, capabilities
// - non-common capabilities have non-empty rationale and non-empty evidence
// - common capabilities have non-empty evidence
// - shared infrastructure roots are disjoint from component source roots
func (inv Inventory) Validate() error {
	if len(inv.Components) == 0 {
		return fmt.Errorf("inventory contains no components")
	}

	seenIDs := make(map[string]struct{}, len(inv.Components))
	sharedMap := make(map[string]struct{}, len(inv.SharedInfra))
	for _, infra := range inv.SharedInfra {
		if strings.TrimSpace(infra) == "" {
			return fmt.Errorf("shared infrastructure entry must not be blank")
		}
		sharedMap[infra] = struct{}{}
	}

	var prevID string
	for i, comp := range inv.Components {
		if strings.TrimSpace(comp.ID) == "" {
			return fmt.Errorf("component at index %d has blank ID", i)
		}
		if _, ok := seenIDs[comp.ID]; ok {
			return fmt.Errorf("duplicate component ID %q", comp.ID)
		}
		seenIDs[comp.ID] = struct{}{}

		if prevID != "" && comp.ID < prevID {
			return fmt.Errorf("components are not sorted deterministically: %q appears after %q", comp.ID, prevID)
		}
		prevID = comp.ID

		if len(comp.SourceRoots) == 0 {
			return fmt.Errorf("component %q has no source roots", comp.ID)
		}
		for _, src := range comp.SourceRoots {
			if strings.TrimSpace(src) == "" {
				return fmt.Errorf("component %q has blank source root", comp.ID)
			}
			if _, isShared := sharedMap[src]; isShared {
				return fmt.Errorf("component %q source root %q conflicts with shared infrastructure", comp.ID, src)
			}
		}

		if len(comp.TestPackages) == 0 {
			return fmt.Errorf("component %q has no test packages", comp.ID)
		}
		for _, tp := range comp.TestPackages {
			if strings.TrimSpace(tp) == "" {
				return fmt.Errorf("component %q has blank test package", comp.ID)
			}
		}

		if len(comp.StoreContracts) == 0 {
			return fmt.Errorf("component %q has no store contracts", comp.ID)
		}
		for _, sc := range comp.StoreContracts {
			if strings.TrimSpace(sc) == "" {
				return fmt.Errorf("component %q has blank store contract", comp.ID)
			}
		}

		if len(comp.MigrationRoots) == 0 {
			return fmt.Errorf("component %q has no migration roots", comp.ID)
		}
		for _, mr := range comp.MigrationRoots {
			if strings.TrimSpace(mr) == "" {
				return fmt.Errorf("component %q has blank migration root", comp.ID)
			}
		}

		if len(comp.Capabilities) == 0 {
			return fmt.Errorf("component %q has no capabilities", comp.ID)
		}
		capIDs := make(map[string]struct{}, len(comp.Capabilities))
		hasCommon := false
		for _, cap := range comp.Capabilities {
			if strings.TrimSpace(cap.ID) == "" {
				return fmt.Errorf("component %q has capability with blank ID", comp.ID)
			}
			if _, ok := capIDs[cap.ID]; ok {
				return fmt.Errorf("component %q has duplicate capability ID %q", comp.ID, cap.ID)
			}
			capIDs[cap.ID] = struct{}{}

			if strings.TrimSpace(cap.Evidence) == "" {
				return fmt.Errorf("component %q capability %q has blank evidence", comp.ID, cap.ID)
			}
			if cap.Class == Common {
				hasCommon = true
			} else {
				if strings.TrimSpace(cap.Rationale) == "" {
					return fmt.Errorf("component %q capability %q (%s) has blank rationale", comp.ID, cap.ID, cap.Class)
				}
			}
		}
		if !hasCommon {
			return fmt.Errorf("component %q has no Common capabilities", comp.ID)
		}
	}

	return nil
}

// ComponentByID returns the component with the given ID, or false if not found.
func (inv Inventory) ComponentByID(id string) (Component, bool) {
	idx := slices.IndexFunc(inv.Components, func(c Component) bool {
		return c.ID == id
	})
	if idx < 0 {
		return Component{}, false
	}
	return inv.Components[idx], true
}

// ComponentIDs returns the ordered list of component IDs in the inventory.
func (inv Inventory) ComponentIDs() []string {
	ids := make([]string, len(inv.Components))
	for i, c := range inv.Components {
		ids[i] = c.ID
	}
	return ids
}
