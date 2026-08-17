package archtest

import "testing"

func TestBillingFinalConvergenceActivatedDeletionTargetsAbsentFromCurrentTree(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	findings, err := EvaluateBillingFinalConvergenceCurrentDeletionRatchet(root, doc)
	if err != nil {
		t.Fatalf("current deletion ratchet: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("activated deletion targets remain in current source:\n%s", formatRatchetFindings(findings))
	}
}
