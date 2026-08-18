package archtest

import (
	"go/ast"
	"go/token"
	"strings"
)

// EvaluateBillingAttemptSequenceAuthority rejects positional, lexical, and
// timestamp sources for customer leg ordering and requires the rating adapter
// to copy the persisted B2BUA attempt sequence verbatim (the only authoritative
// order source).
func EvaluateBillingAttemptSequenceAuthority(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	readable := false
	for _, rel := range []string{
		"internal/core/billing/call_rating.go",
		"internal/core/billing/rating.go",
	} {
		src, err := readProductionSource(root, rel)
		if err != nil {
			return nil, err
		}
		if src == "" {
			continue
		}
		readable = true
		f, err := parseProductionFile(root, rel)
		if err != nil {
			return nil, err
		}
		out = append(out, scanSeqPositionalReconstruction(rel, src, f)...)
		out = append(out, scanSeqTimestampOrdering(rel, src)...)
		out = append(out, scanSeqLexicalOrdering(rel, src)...)
	}
	if !readable {
		out = append(out, billingCorrectnessRuleFinding(
			BillingCorrectnessRuleSequenceAdapterAuthoritative,
			"internal/core/billing",
			"attempt-sequence authority targets are missing"))
		return out, nil
	}
	out = append(out, scanLatestAcceptedUsesPersistedSequence(root)...)
	return out, nil
}

// scanSeqPositionalReconstruction rejects rebuilding the attempt sequence from
// slice position in the customer rating adapter and any direct attempt-sequence assignment
// from an index/position expression.
func scanSeqPositionalReconstruction(rel, src string, f *ast.File) []RuleFinding {
	var out []RuleFinding
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typeName := compositeLiteralTypeName(cl.Type)
		if typeName != "LegUsageRecord" && typeName != "CallLegUsageRecord" {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || (key.Name != "Seq" && key.Name != "AttemptSeq") {
				continue
			}
			sel, ok := kv.Value.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || (sel.Sel.Name != "AttemptSeq" && sel.Sel.Name != "Seq") {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleSequencePositional, rel,
					"CallLegUsageRecord.AttemptSeq must copy the persisted attempt sequence; positional reconstruction is forbidden"))
				return true
			}
			if _, ok := sel.X.(*ast.Ident); !ok {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleSequencePositional, rel,
					"CallLegUsageRecord.AttemptSeq must derive from the call-leg record, not an inline expression"))
			}
		}
		return true
	})
	// Surgical markers catch direct assignments such as leg.Seq = i + 1.
	for _, marker := range []string{
		".Seq = i", ".AttemptSeq = i", "Seq: i +", "Seq: i+", "AttemptSeq: i +", "AttemptSeq: i+", "Seq: i+1", "AttemptSeq: i+1",
		"Seq: index +", "Seq: index+", "AttemptSeq: index +", "AttemptSeq: index+", "Seq: idx +", "Seq: idx+", "AttemptSeq: idx +", "AttemptSeq: idx+",
		"Seq: position", "Seq: pos", "AttemptSeq: position", "AttemptSeq: pos", ".Seq = index", ".Seq = idx", ".AttemptSeq = index", ".AttemptSeq = idx",
	} {
		if strings.Contains(src, marker) {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleSequencePositional, rel,
				"forbidden positional sequence reconstruction marker "+marker))
		}
	}
	return out
}

// scanSeqTimestampOrdering rejects timestamps as a customer leg-ordering source.
func scanSeqTimestampOrdering(rel, src string) []RuleFinding {
	var out []RuleFinding
	for _, marker := range []string{
		"StartedAt.After(", "StartedAt.Before(",
		"FinishedAt.After(", "FinishedAt.Before(",
	} {
		if strings.Contains(src, marker) {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleSequenceTimestamp, rel,
				"customer leg selection must not derive order from timestamps ("+marker+")"))
		}
	}
	return out
}

// scanSeqLexicalOrdering rejects sorting/comparing B-leg identities as a
// customer leg-ordering source. The canonical ExpectedBLegIDs set sort in
// call_usage.go is completeness-only and deliberately outside these files.
func scanSeqLexicalOrdering(rel, src string) []RuleFinding {
	var out []RuleFinding
	for _, marker := range []string{
		"sort.Slice(", "slices.SortFunc(", "slices.Sort(", "slices.SortStableFunc(",
		"strings.Compare(",
	} {
		if strings.Contains(src, marker) {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleSequenceLexical, rel,
				"customer leg selection must not derive order from sorting or comparing B-leg identities ("+marker+")"))
		}
	}
	return out
}

// scanLatestAcceptedUsesPersistedSequence requires the interrupted-call
// latest-accepted rule to compare persisted sequence selectors.
func scanLatestAcceptedUsesPersistedSequence(root string) []RuleFinding {
	rel := "internal/core/billing/rating.go"
	f, err := parseProductionFile(root, rel)
	if err != nil {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleSequenceAdapterAuthoritative, rel,
			"latest-accepted sequence target failed to parse: "+err.Error())}
	}
	if f == nil {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleSequenceAdapterAuthoritative, rel,
			"latest-accepted sequence target is missing")}
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		if !ok || !isSequenceComparison(binary.Op) {
			return true
		}
		if isSeqSelector(binary.X) && isSeqSelector(binary.Y) {
			found = true
		}
		return true
	})
	if !found {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleSequenceAdapterAuthoritative, rel,
			"latest-accepted selection must compare persisted sequence selectors (not IDs/timestamps/position)")}
	}
	return nil
}

func isSeqSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && (sel.Sel.Name == "Seq" || sel.Sel.Name == "AttemptSeq")
}

func isSequenceComparison(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}
