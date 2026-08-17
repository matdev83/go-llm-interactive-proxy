package archtest

import (
	"go/ast"
	"strings"
)

// EvaluateBillingAttemptSequenceAuthority rejects positional, lexical, and
// timestamp sources for customer leg ordering and requires the rating adapter
// to copy the persisted B2BUA attempt sequence verbatim (the only authoritative
// order source).
func EvaluateBillingAttemptSequenceAuthority(root string) ([]RuleFinding, error) {
	var out []RuleFinding
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
		f, err := parseProductionFile(root, rel)
		if err != nil {
			return nil, err
		}
		out = append(out, scanSeqPositionalReconstruction(rel, src, f)...)
		out = append(out, scanSeqTimestampOrdering(rel, src)...)
		out = append(out, scanSeqLexicalOrdering(rel, src)...)
	}
	out = append(out, scanLatestAcceptedUsesPersistedSequence(root)...)
	return out, nil
}

// scanSeqPositionalReconstruction rejects rebuilding the attempt sequence from
// slice position in the customer rating adapter and any direct Seq assignment
// from an index/position expression.
func scanSeqPositionalReconstruction(rel, src string, f *ast.File) []RuleFinding {
	var out []RuleFinding
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if compositeLiteralTypeName(cl.Type) != "LegUsageRecord" {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Seq" {
				continue
			}
			sel, ok := kv.Value.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "AttemptSeq" {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleSequencePositional, rel,
					"LegUsageRecord.Seq must copy the persisted leg.AttemptSeq (b2bua.BLegRecord.Seq); positional reconstruction is forbidden"))
				return true
			}
			if _, ok := sel.X.(*ast.Ident); !ok {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleSequencePositional, rel,
					"LegUsageRecord.Seq must derive from the call-leg record, not an inline expression"))
			}
		}
		return true
	})
	// Surgical markers catch direct assignments such as leg.Seq = i + 1.
	for _, marker := range []string{
		".Seq = i", "Seq: i +", "Seq: i+", "Seq: i+1",
		"Seq: index +", "Seq: index+", "Seq: idx +", "Seq: idx+",
		"Seq: position", "Seq: pos", ".Seq = index", ".Seq = idx",
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
// latest-accepted rule to compare the persisted sequence.
func scanLatestAcceptedUsesPersistedSequence(root string) []RuleFinding {
	rel := "internal/core/billing/rating.go"
	src, err := readProductionSource(root, rel)
	if err != nil || src == "" {
		return nil
	}
	if !strings.Contains(src, "leg.Seq > best.Seq") {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleSequenceAdapterAuthoritative, rel,
			"latest-accepted selection must compare the persisted leg.Seq (not IDs/timestamps/position)")}
	}
	return nil
}
