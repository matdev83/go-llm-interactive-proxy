package archtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Billing-final-convergence Phase-0 simplification baseline (design Section 12).
//
// This file implements the canonical physical-go-lines-v1 denominator and the
// versioned AST symbol-following fixed-point inventory. Phase 0 creates exactly
// one checked-in artifact (BillingFinalConvergenceBaselineRelPath) from the
// verified predecessor implementation SHA. The denominator is recomputable from
// the artifact inventory; scanner-rule changes require a schema/version bump.

const (
	BillingFinalConvergenceBaselineRelPath        = "internal/archtest/testdata/architecture/billing_final_convergence_baseline.json"
	BillingFinalConvergenceSchemaVersion          = 1
	BillingFinalConvergenceCountingMethod         = "physical-go-lines-v1"
	BillingFinalConvergenceSymbolFollowingVersion = 1
	BillingFinalConvergenceFeature                = "billing-architecture-final-convergence"
	BillingFinalConvergencePhase                  = "0-baseline"
)

// Ratchet statuses (Phase 0 leaves both ratchets "planned").
const (
	BillingFinalConvergenceRatchetPlanned = "planned"
	BillingFinalConvergenceRatchetActive  = "active"
)

// Activation flags (all false in Phase 0; Phase 7.2 flips them).
const (
	BillingFinalConvergenceActivationDeletionRatchet = "activate_structural_deletion"
	BillingFinalConvergenceActivationLOCRatchet      = "activate_loc_reduction"
)

// BillingFinalConvergenceVerifiedSHA is the predecessor implementation SHA
// recorded in spec.json.dependencies[0].implementation_main_sha after Phase 0
// verification (PR #354 merge commit; implementation, not the #346 spec-only
// merge 9bf9c66...).
const BillingFinalConvergenceVerifiedSHA = "cd3a603495660f49240dcb9bf1698aa3f27503a2"

// BillingFinalConvergenceInitialSeedSymbols are the migration-era monetary/usage
// concepts seeding the AST fixed-point inventory (design 12.3 step 1): the
// predecessor's money unit, money reservation fields, reserved/auth-book
// symbols, legacy usage model symbols, and direct/outbox append types.
var BillingFinalConvergenceInitialSeedSymbols = []string{
	"AmountUnitMoneyNano",
	"Spend",
	"FinalCost",
	"EstimatedCost",
	"ReservedNano",
	"JournalBookLegacyAuthorization",
	"TurnUsageRecord",
	"LegUsageRecord",
	"RetryingCallUsageAppender",
	"RetryingCallLegUsageAppender",
	"UsageAppendWorker",
	"UsageAppendOutbox",
}

// BillingFinalConvergenceRoot is a whole-file package root counted wholesale.
type BillingFinalConvergenceRoot struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	BaselineLines int    `json:"baseline_lines"`
}

// BillingFinalConvergenceFile is a whole production .go file counted wholesale
// (runtime billing_*.go and runtimebundle billing composition).
type BillingFinalConvergenceFile struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	BaselineLines int    `json:"baseline_lines"`
}

// BillingFinalConvergenceDeclaration is one followed or pinned declaration
// contributing to the denominator (design 12.3 step 5).
type BillingFinalConvergenceDeclaration struct {
	File          string   `json:"file"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"` // type|const|var|func|method
	StartLine     int      `json:"start_line"`
	EndLine       int      `json:"end_line"`
	Loc           int      `json:"loc"` // end_line - start_line + 1
	DeclaredNames []string `json:"declared_names"`
	Cause         string   `json:"cause"` // symbol-followed:v1 | executor-config
	CausedBy      string   `json:"caused_by,omitempty"`
}

// BillingFinalConvergenceDeletionTarget records a brownfield symbol/schema that
// later phases retire. Phase 0 records present=true and status="planned".
type BillingFinalConvergenceDeletionTarget struct {
	ID                 string   `json:"id"`
	Kind               string   `json:"kind"` // type|const|func|method|field|ident|schema
	Package            string   `json:"package,omitempty"`
	Name               string   `json:"name,omitempty"`
	Files              []string `json:"files,omitempty"`
	Marker             string   `json:"marker,omitempty"`
	HistoricalReaders  []string `json:"historical_readers,omitempty"`
	Present            bool     `json:"present"`
	Status             string   `json:"status"` // planned|active
	RetiringPhase      string   `json:"retiring_phase,omitempty"`
	Reason             string   `json:"reason"`
	CurrentConsumers   []string `json:"current_consumers,omitempty"`
	CurrentWriters     []string `json:"current_writers,omitempty"`
	RetentionRationale string   `json:"retention_rationale,omitempty"`
}

// BillingFinalConvergencePlannedRatchet is a non-activated end-state forbid.
type BillingFinalConvergencePlannedRatchet struct {
	ID                string   `json:"id"`
	Status            string   `json:"status"` // planned|active
	ActivationFlag    string   `json:"activation_flag,omitempty"`
	ActivationTask    string   `json:"activation_task"`
	Requirements      []string `json:"requirements"`
	DeletionTargetIDs []string `json:"deletion_target_ids,omitempty"`
	EndState          string   `json:"end_state"`
}

// BillingFinalConvergenceBaselineFile is the checked-in Phase-0 artifact.
type BillingFinalConvergenceBaselineFile struct {
	SchemaVersion          int                                     `json:"schema_version"`
	BaselineSHA            string                                  `json:"baseline_sha"`
	CountingMethod         string                                  `json:"counting_method"`
	DenominatorLOC         int                                     `json:"denominator_loc"`
	IncludedRoots          []BillingFinalConvergenceRoot           `json:"included_roots"`
	IncludedFiles          []BillingFinalConvergenceFile           `json:"included_files"`
	IncludedDeclarations   []BillingFinalConvergenceDeclaration    `json:"included_declarations"`
	ExcludedGlobs          []string                                `json:"excluded_globs"`
	SeedSymbols            []string                                `json:"seed_symbols"`
	SymbolFollowingVersion int                                     `json:"symbol_following_version"`
	DeletionTargets        []BillingFinalConvergenceDeletionTarget `json:"deletion_targets"`
	PlannedRatchets        []BillingFinalConvergencePlannedRatchet `json:"planned_ratchets"`
}

// DecodeBillingFinalConvergenceBaseline parses a baseline document.
func DecodeBillingFinalConvergenceBaseline(raw []byte) (BillingFinalConvergenceBaselineFile, error) {
	var doc BillingFinalConvergenceBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return BillingFinalConvergenceBaselineFile{}, fmt.Errorf("decode billing final-convergence baseline: %w", err)
	}
	return doc, nil
}

// LoadBillingFinalConvergenceBaseline reads the checked-in Phase-0 JSON artifact.
func LoadBillingFinalConvergenceBaseline(root string) (BillingFinalConvergenceBaselineFile, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(BillingFinalConvergenceBaselineRelPath)))
	if err != nil {
		return BillingFinalConvergenceBaselineFile{}, err
	}
	return DecodeBillingFinalConvergenceBaseline(raw)
}
