package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
)

const enterpriseModuleRelPath = "testdata/enterprise_module"

// TestEnterpriseModulePublicOnlyCompileGate proves a sibling module can build
// and run against public packages only (requirements 12.6, 12.9, 17.7).
func TestEnterpriseModulePublicOnlyCompileGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, enterpriseModuleRelPath)

	assertNoInternalImportsInDir(t, dir)

	cfg := bpkit.WriteDogfoodLocalStubConfig(t)
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "LIP_ENTERPRISE_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enterprise module go test: %v\n%s", err, out)
	}
}

// TestEnterpriseModuleInternalImportScannerRejectsInternalImport locks the
// source scanner that fails architecture CI when the fixture imports internal/.
func TestEnterpriseModuleInternalImportScannerRejectsInternalImport(t *testing.T) {
	t.Parallel()
	src := `package bad
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	err := scanFileForInternalImports(path)
	if err == nil {
		t.Fatal("expected internal import to be rejected")
	}
	if !strings.Contains(err.Error(), "internal/") {
		t.Fatalf("err=%v", err)
	}
}

func assertNoInternalImportsInDir(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		return scanFileForInternalImports(path)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scanFileForInternalImports(filePath string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
	if err != nil {
		return err
	}
	for _, imp := range f.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(impPath, "/internal/") || strings.HasSuffix(impPath, "/internal") {
			return &internalImportError{File: filePath, Import: impPath}
		}
	}
	return nil
}

type internalImportError struct {
	File   string
	Import string
}

func (e *internalImportError) Error() string {
	return e.File + ": forbidden internal/ import " + e.Import + " (requirement 12.6)"
}
