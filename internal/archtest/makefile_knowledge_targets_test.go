package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// The durable-knowledge targets below are part of the Makefile contract the
// docs-check / knowledge-check composition must keep publishing.
func TestPhase91_DocsCheckAndKnowledgeCheckTargets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mk := readFile(t, filepath.Join(root, "Makefile"))
	if !strings.Contains(mk, "docs-check:") {
		t.Fatal("Makefile missing docs-check")
	}
	if !strings.Contains(mk, "knowledge-check:") {
		t.Fatal("Makefile missing knowledge-check")
	}
	if !strings.Contains(mk, "knowledge-check") || !strings.Contains(firstPHONYLine(mk), "knowledge-check") {
		t.Fatal(".PHONY must include knowledge-check")
	}
}
