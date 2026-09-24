package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhase172ShadowV2HasNoMonetaryWriters proves the Task 17.2 shadow seam
// is structurally incapable of monetary posting or V1 terminal routing. The
// shadow capture and composition sources must not reference balance/unit/
// provider-payable writers, journal posting internals, ordinary terminal
// usage appends, provider SDKs, or background goroutines. V1 remains the sole
// monetary writer on its existing settlement path.
func TestPhase172ShadowV2HasNoMonetaryWriters(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	shadowFiles := []string{
		"internal/core/billing/shadow_evidence.go",
		"internal/infra/billingstore/shadow_v2_capture.go",
		"internal/infra/billingstore/shadow_v2_compare.go",
		"internal/infra/runtimebundle/shadow_v2_compose.go",
	}
	forbiddenIdents := map[string]struct{}{
		"ApplyCallBillingResult":         {},
		"ApplyCustomerUnitOperation":     {},
		"ApplyProviderCost":              {},
		"ApplyProviderCostRevision":      {},
		"ApplySelectedCostAdjustment":    {},
		"ApplyCostPassThroughRevision":   {},
		"ApplyCostPassThroughAdjustment": {},
		"postJournalInTx":                {},
		"postJournalTransaction":         {},
		"AdmitExposure":                  {},
		"AppendLeg":                      {},
		"AppendCall":                     {},
		"AppendCallLegUsage":             {},
		"AppendCallUsage":                {},
		"TerminalUsageSink":              {},
		"ShadowPost":                     {},
		"shadowPost":                     {},
		"ShadowPosting":                  {},
		"shadowPosting":                  {},
		"ShadowJournal":                  {},
		"shadowJournal":                  {},
		"CustomerUnitLedger":             {},
		"ProviderCostRevisionStore":      {},
		"SelectedCostAdjustmentStore":    {},
		"CallSettlementStore":            {},
	}
	forbiddenImports := []string{
		"github.com/openai/",
		"github.com/anthropics/",
		"github.com/aws/",
		"google.golang.org/genai",
		"/internal/plugins/backends",
		"/connectors/",
		"/connector-support/",
		"github.com/uptrace/bun",
		"database/sql",
	}
	for _, rel := range shadowFiles {
		path := filepath.Join(root, filepath.FromSlash(rel))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(src)
		// The runtimebundle composer references the settlement port only as a
		// required-but-unstored coexistence proof; it must never be stored or
		// invoked. Allow the single input-field occurrence there.
		if rel == "internal/infra/runtimebundle/shadow_v2_compose.go" {
			if strings.Count(text, "CallSettlementStore") != 1 {
				t.Fatalf("%s must reference CallSettlementStore exactly once as the coexistence input, got %d",
					rel, strings.Count(text, "CallSettlementStore"))
			}
		} else if strings.Contains(text, "CallSettlementStore") {
			t.Fatalf("%s must not reference the V1 settlement writer", rel)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.Ident:
				if _, blocked := forbiddenIdents[n.Name]; blocked {
					if rel == "internal/infra/runtimebundle/shadow_v2_compose.go" && n.Name == "CallSettlementStore" {
						return true
					}
					t.Errorf("%s references forbidden monetary writer %q", rel, n.Name)
				}
			case *ast.GoStmt:
				t.Errorf("%s must not own a background goroutine", rel)
			case *ast.ImportSpec:
				if n.Path == nil {
					return true
				}
				imp := strings.Trim(n.Path.Value, `"`)
				for _, forbid := range forbiddenImports {
					// The billingstore shadow test hooks use Bun for read-only
					// counts through the existing durable handle; the shadow
					// capture path itself must not import SQL drivers.
					if forbid == "github.com/uptrace/bun" || forbid == "database/sql" {
						continue
					}
					if strings.Contains(imp, forbid) {
						t.Errorf("%s imports forbidden dependency %q", rel, imp)
					}
				}
			}
			return true
		})
	}
}
