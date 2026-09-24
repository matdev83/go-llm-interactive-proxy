package qa

import (
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhase8ProducerCensusHasExplicitDisposition keeps the closed Phase 1
// inventory honest. A producer row may be certified, losslessly bridged,
// explicitly negotiated unsupported, or explicitly retired/removed with a
// replacement parent task (Task 18 Migration Strategy step 8), but it may
// not silently remain pending.
func TestPhase8ProducerCensusHasExplicitDisposition(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, ".kiro", "specs", "archive", "usage-economics-b-leg-multimodal-refinement", "evidence", "phase1-producer-consumer-census.tsv")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open Phase 1 census: %v", err)
	}
	defer func() { _ = file.Close() }()

	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.FieldsPerRecord = 7
	header, err := reader.Read()
	if err != nil {
		t.Fatalf("read Phase 1 census header: %v", err)
	}
	if strings.Join(header, "\t") != "category\tpath\tsymbol_anchor\tsymbols_or_boundary\tprotocol_family\towner\tdisposition" {
		t.Fatalf("unexpected Phase 1 census header: %v", header)
	}

	const relevant = "provider-producer"
	count := 0
	for row := 2; ; row++ {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read Phase 1 census row %d: %v", row, readErr)
		}
		category := strings.TrimSpace(record[0])
		if category != relevant && category != "prompt-cache" && category != "compaction" && category != "sideband-finalizer" {
			continue
		}
		count++
		disposition := strings.ToLower(strings.TrimSpace(record[6]))
		if strings.Contains(disposition, "pending") || strings.HasPrefix(disposition, "red;") || disposition == "red" {
			t.Errorf("census row %d (%s) retains unresolved disposition %q", row, record[1], record[6])
		}
		certified := strings.Contains(disposition, "v2-certified")
		bridged := strings.Contains(disposition, "lossless-v1-bridge")
		unsupported := strings.Contains(disposition, "unsupported advanced evidence")
		// Task 18.1/18.2 retires superseded V1 live producers; the census
		// marks them removed with an explicit replacement and parent task.
		// The archtest census guard verifies the anchor is absent and the
		// replacement is present, so accept removed here as terminal.
		removed := strings.Contains(disposition, "removed")
		if !certified && !bridged && !unsupported && !removed {
			t.Errorf("census row %d (%s) lacks V2/bridge/unsupported/removed disposition: %q", row, record[1], record[6])
		}
		if unsupported {
			parts := strings.SplitN(disposition, ";", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
				t.Errorf("census row %d (%s) unsupported disposition lacks bounded reason: %q", row, record[1], record[6])
			}
		}
	}
	if count == 0 {
		t.Fatal("Phase 1 census has no Phase 8 producer/auxiliary rows")
	}
}
