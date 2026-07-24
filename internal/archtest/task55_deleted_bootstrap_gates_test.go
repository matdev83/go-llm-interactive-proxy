package archtest

import (
	"testing"
)

func TestTask55_DeletedBootstrapProductionForbidden(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask55DeletedBootstrap, scanTask55DeletedBootstrapSource)
	if len(got) > 0 {
		t.Fatalf("Task 5.5: deleted bootstrap/attachment symbols must stay gone (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}
