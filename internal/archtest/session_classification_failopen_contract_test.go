package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	featureclassification "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
)

// TestSessionClassificationDocs_failOpenTableUsesTheShippedOutcomeVocabulary
// pins the fail-open mapping to the vocabulary an operator can actually observe.
// The table previously published adapter-internal JevFailure kinds
// (server_unavailable, malformed_response, transport_failure) that the adapter
// maps away before any observation is emitted, so a reader matching the table
// against metrics or logs searched for values that never exist. Deleting the
// table, or inventing an outcome token in it, must now fail.
func TestSessionClassificationDocs_failOpenTableUsesTheShippedOutcomeVocabulary(t *testing.T) {
	t.Parallel()

	text := readSessionClassificationDoc(t)
	start := strings.Index(text, "## Fail-open")
	if start < 0 {
		t.Fatalf("%s has no fail-open section", sessionClassificationDocRel)
	}
	end := len(text)
	if next := strings.Index(text[start+1:], "\n## "); next >= 0 {
		end = start + 1 + next
	}
	section := text[start:end]

	// Every shipped outcome that can leave a session not-promoted must be named.
	// `positive` is deliberately excluded: it is the one outcome that is NOT a
	// fail-open case, and the promotion vocabulary belongs to 11.2's diagnostics
	// section rather than here.
	for _, outcome := range featureclassification.RemoteOutcomes() {
		if outcome == string(featureclassification.RemotePositive) {
			continue
		}
		if !strings.Contains(section, "`"+outcome+"`") {
			t.Errorf("the fail-open section does not name the shipped outcome %q; requirement 9.2/6.9 "+
				"labels are drawn from the closed RemoteOutcome vocabulary", outcome)
		}
	}

	// Adapter-internal refusal kinds must not be presented as observable outcomes.
	for _, internal := range []string{
		"server_unavailable", "malformed_response", "transport_failure",
		"response_oversized", "redirect_refused", "credential_missing",
	} {
		if strings.Contains(section, "`"+internal+"`") {
			t.Errorf("the fail-open section presents the adapter-internal refusal kind %q as an outcome; "+
				"only the closed RemoteOutcome vocabulary is observable", internal)
		}
	}

	// The mapping must actually be a table: an outcome name alone is not a mapping.
	if !strings.Contains(section, "| `422` |") {
		t.Errorf("the fail-open section publishes no failure-to-outcome mapping table; operators cannot " +
			"act on a bare vocabulary list")
	}
}

// The doc and its examples must be real, non-empty repository files. `git
// ls-files` is the honest check for "tracked": os.Stat cannot tell a committed
// file from an untracked draft, and this guide is worthless if it never lands.
func TestSessionClassificationDocs_areTrackedRepositoryFile(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(sessionClassificationDocRel)))
	if err != nil {
		t.Fatalf("stat %s: %v", sessionClassificationDocRel, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s is empty", sessionClassificationDocRel)
	}
	rels := append([]string{sessionClassificationDocRel}, sessionClassificationExamples...)
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if _, err := exec.LookPath("git"); err != nil {
			continue // git is unavailable in this sandbox; the stat check above stands
		}
		cmd := exec.Command("git", "ls-files", "--error-unmatch", "--", rel)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s is not a tracked repository file (%v): %s", rel, err, out)
		}
	}
}
