package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

func TestMatrixEvidence_docsPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	doc := filepath.Join(root, "docs", "conformance-matrix-evidence.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	text := string(b)
	for _, needle := range []string{
		"AllCells()",
		"conformance_text_test.go",
		"conformance_tools_test.go",
		"conformance_multimodal_test.go",
		"matrix.go",
		"TextViable",
		"ToolsViable",
		"MultimodalViable",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/conformance-matrix-evidence.md missing %q", needle)
		}
	}
}

func TestMatrixEvidence_sourceFilesIterateAllCells(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "testkit", "conformance")

	assertFile := func(name, mustContain string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		s := string(b)
		if !strings.Contains(s, "AllCells()") {
			t.Fatalf("%s: expected iteration over AllCells()", name)
		}
		if !strings.Contains(s, mustContain) {
			t.Fatalf("%s: expected %q filter for matrix subset", name, mustContain)
		}
	}

	for _, f := range MatrixEvidenceTextFiles {
		assertFile(f, "TextViable")
	}
	for _, f := range MatrixEvidenceToolsFiles {
		assertFile(f, "ToolsViable")
	}
	for _, f := range MatrixEvidenceMultimodalFiles {
		assertFile(f, "MultimodalViable")
	}
}

func TestMatrixEvidence_acpSubsetMatchesMatrixMeta(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if cell.Backend != "acp" {
			continue
		}
		if cell.Meta.ToolsViable {
			t.Fatalf("ACP tools must be non-viable per matrix footnote, cell=%+v", cell)
		}
		if cell.Meta.MultimodalViable {
			t.Fatalf("ACP multimodal must be non-viable per matrix footnote, cell=%+v", cell)
		}
		if cell.Meta.SubsetJustification == "" {
			t.Fatal("ACP matrix cells must carry SubsetJustification")
		}
	}
}
