package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BillingFinalConvergencePhase1BridgeForbid is deliberately independent from
// the later structural-deletion ratchet. Phase 1 may remove the authoritative
// rating bridge now, while Phase 7 retains ownership of the broader deletion
// and LOC gates.
const BillingFinalConvergencePhase1BridgeForbid = true

var billingFinalConvergencePhase1ForbiddenIdentifiers = []string{
	"TurnUsageRecord",
	"LegUsageRecord",
	"RatingInput",
	"calculateCustomerCharge",
}

// EvaluateBillingFinalConvergencePhase1BridgeForbid rejects production source
// that reconstructs the deleted TUR/LUR rating path. Tests and historical
// migration sources are intentionally outside this guard.
func EvaluateBillingFinalConvergencePhase1BridgeForbid(root string) ([]RuleFinding, error) {
	if !BillingFinalConvergencePhase1BridgeForbid {
		return []RuleFinding{{Rule: "billing_final_convergence_phase1_bridge_forbid", Detail: "Phase 1 bridge-forbid guard is not active"}}, nil
	}
	var findings []RuleFinding
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != filepath.Join(root, "internal") && (strings.HasSuffix(filepath.ToSlash(path), "/archtest") || strings.Contains(filepath.ToSlash(path), "/testdata/")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || isBillingFinalConvergenceMigrationName(filepath.Base(rel)) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBillingFinalConvergenceGenerated(src) {
			return nil
		}
		_, file, err := ParseGoSource(rel, src)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		names := collectIdentNames(file)
		for _, forbidden := range billingFinalConvergencePhase1ForbiddenIdentifiers {
			if _, ok := names[forbidden]; ok {
				findings = append(findings, RuleFinding{
					Rule:   "billing_final_convergence_phase1_bridge_forbid",
					Path:   rel,
					Detail: "deleted legacy customer rating identifier appears in production source: " + forbidden,
				})
			}
		}
		return nil
	})
	return findings, err
}
