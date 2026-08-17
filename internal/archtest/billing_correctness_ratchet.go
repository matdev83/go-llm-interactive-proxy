package archtest

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
)

// Phase 5.1 correctness ratchets for the corrected post-usage billing baseline.
//
// Four semantic guards protect the converged architecture against regressions:
//  1. attempt sequence authority — customer leg selection may never derive
//     B2BUA attempt order from slice position, opaque B-leg ids, timestamps, or
//     provider order; the rating adapter must copy the persisted AttemptSeq.
//  2. customer/operator snapshot independence — customer rating must never
//     resolve, carry, or depend on operator-rate data.
//  3. request-scoped billing state — executor-global lifetime-growing
//     billing-call registries/maps are forbidden; bookkeeping lives on
//     request/BillingCallID-scoped objects.
//  4. monetary hold and stream-money protection — the existing hold-deletion
//     and no-stream-money ratchets must remain active and green.
//
// Guards target symbols and semantics (AST), not formatting.

const (
	BillingCorrectnessRuleSequencePositional                = "billing_attempt_sequence_positional"
	BillingCorrectnessRuleSequenceTimestamp                 = "billing_attempt_sequence_timestamp"
	BillingCorrectnessRuleSequenceLexical                   = "billing_attempt_sequence_lexical"
	BillingCorrectnessRuleSequenceAdapterAuthoritative      = "billing_attempt_sequence_authoritative_adapter"
	BillingCorrectnessRuleCustomerOperatorCoupling          = "billing_customer_operator_coupling"
	BillingCorrectnessRuleCustomerInputCarriesOperatorRates = "billing_customer_input_carries_operator_rates"
	BillingCorrectnessRuleExecutorGlobalBillingRegistry     = "billing_executor_global_registry"
	BillingCorrectnessRuleExecutorMapField                  = "billing_executor_map_field"
	BillingCorrectnessRuleCallScopedStateOwnerMissing       = "billing_call_scoped_owner_missing"
	BillingCorrectnessRuleHoldAndStreamMoneyLock            = "billing_hold_stream_money_lock"
)

// billingCorrectnessOperatorRateIdents are the customer/operator coupling
// symbols that must never appear inside customer rating resolution, customer
// rating inputs, or the customer post-usage worker.
var billingCorrectnessOperatorRateIdents = []string{
	"OperatorRate",
	"OperatorRates",
	"OperatorRateSet",
	"OperatorRateRef",
	"operatorRates",
}

// billingCorrectnessLifetimeRegistryIdents are the executor-global billing-call
// registries the corrected baseline removed. Their reappearance anywhere in
// runtime production would reintroduce lifetime-growing bookkeeping.
var billingCorrectnessLifetimeRegistryIdents = []string{
	"billingTurnCollector",
	"allocatedByCall",
	"frozenByCall",
	"legTimesByCall",
	"finalizeByKey",
}

func billingCorrectnessRuleFinding(rule, path, detail string) RuleFinding {
	return RuleFinding{Rule: rule, Path: path, Detail: detail}
}

// readProductionSource reads a repo-relative production file. Missing files
// return ("", nil) so partial trees (unit-test fixtures) skip cleanly.
func readProductionSource(root, rel string) (string, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(src), nil
}

// parseProductionFile parses a repo-relative production file into an AST.
func parseProductionFile(root, rel string) (*ast.File, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	src, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_, f, err := ParseGoSource(abs, src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	return f, nil
}

// findTypeSpec returns the TypeSpec with the given name, or nil.
func findTypeSpec(f *ast.File, name string) *ast.TypeSpec {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok.String() != "type" {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if ok && ts.Name != nil && ts.Name.Name == name {
				return ts
			}
		}
	}
	return nil
}

// findFuncDecl returns the first function (with any receiver) named funcName.
func findFuncDecl(f *ast.File, funcName string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name != nil && fd.Name.Name == funcName {
			return fd
		}
	}
	return nil
}

// collectIdentNames returns every identifier name referenced inside node.
func collectIdentNames(node ast.Node) map[string]struct{} {
	names := make(map[string]struct{})
	if node == nil {
		return names
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			names[id.Name] = struct{}{}
		}
		return true
	})
	return names
}

func compositeLiteralTypeName(t ast.Expr) string {
	switch v := t.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}

// scanFileForbiddenIdents rejects forbidden identifiers anywhere in a file.
func scanFileForbiddenIdents(root, rel string, forbidden []string, rule, detail string) []RuleFinding {
	f, err := parseProductionFile(root, rel)
	if err != nil || f == nil {
		return nil
	}
	names := collectIdentNames(f)
	return forbiddenIdentFindings(rel, names, forbidden, rule, detail)
}

// scanFuncBodyForbiddenIdents rejects forbidden identifiers inside one named
// function body.
func scanFuncBodyForbiddenIdents(root, rel, funcName string, forbidden []string, rule, detail string) []RuleFinding {
	f, err := parseProductionFile(root, rel)
	if err != nil || f == nil {
		return nil
	}
	fd := findFuncDecl(f, funcName)
	if fd == nil {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleSequenceAdapterAuthoritative, rel, "expected function "+funcName+" is missing")}
	}
	names := collectIdentNames(fd.Body)
	return forbiddenIdentFindings(rel, names, forbidden, rule, detail)
}

func forbiddenIdentFindings(rel string, names map[string]struct{}, forbidden []string, rule, detail string) []RuleFinding {
	var out []RuleFinding
	for _, name := range forbidden {
		if _, ok := names[name]; ok {
			out = append(out, billingCorrectnessRuleFinding(rule, rel, detail+": "+name))
		}
	}
	return out
}

// scanStructFieldNamesForbidden rejects struct fields whose names contain a
// forbidden substring.
func scanStructFieldNamesForbidden(root, rel, typeName, forbidden string, rule, detail string) []RuleFinding {
	f, err := parseProductionFile(root, rel)
	if err != nil || f == nil {
		return nil
	}
	ts := findTypeSpec(f, typeName)
	if ts == nil {
		return nil
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil
	}
	var out []RuleFinding
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if strings.Contains(name.Name, forbidden) {
				out = append(out, billingCorrectnessRuleFinding(
					rule, rel, detail+": "+typeName+"."+name.Name))
			}
		}
	}
	return out
}

func containsString(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

func isBillingCallIDKeyExpr(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return sel.Sel.Name == "BillingCallID"
}
