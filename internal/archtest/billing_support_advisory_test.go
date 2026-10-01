package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Advice belongs to post-usage rating. Admission, terminal handoff and stream
// owners may carry generic evidence but must not interpret advisory metadata.
func TestBillingSupportAdvisoryMoneySeams(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, path := range []string{
		"internal/core/runtime", "internal/core/stream", "pkg/streampump",
		"internal/infra/billingadmission", "internal/core/billing/authorize.go",
		"internal/core/billing/credit_screen.go", "internal/core/billing/exposure.go",
		"internal/core/billing/exposure_recovery.go",
	} {
		err := filepath.WalkDir(filepath.Join(root, path), func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			assertAdvisoryBoundaryFile(t, name, false)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBillingSupportAdvisoryAssessorImports(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), "internal/core/billing/component_rater_advisory*.go"))
	if err != nil {
		t.Fatal(err)
	}
	production := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		production++
		assertAdvisoryBoundaryFile(t, path, true)
	}
	if production == 0 {
		t.Fatal("no production assessor source inspected")
	}
}

func TestBillingSupportAdvisoryBoundaryNegativeControls(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name, source       string
		imports, forbidden bool
	}{
		{"ordinary evidence", "package p; func f(v V) { _ = v.Observations }", false, false},
		{"report operand", "package p; func f(v V) { _ = v.SupportAdvisory }", false, true},
		{"context operand", "package p; func f(v V) { _ = v.SupportAdvisoryContexts }", false, true},
		{"assessor call", "package p; func f() { assessSupportUncertainty() }", false, true},
		{"wire interpretation", "package p; const key = `support_advisory`", false, true},
		{"stdlib", "package p; import `slices`", true, false},
		{"sdk contract", "package p; import `github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics`", true, false},
		{"SQL", "package p; import `database/sql`", true, true},
		{"SQL driver", "package p; import `database/sql/driver`", true, true},
		{"Bun", "package p; import `github.com/uptrace/bun`", true, true},
		{"provider SDK", "package p; import `github.com/openai/openai-go`", true, true},
		{"infra owner", "package p; import `github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore`", true, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			violations, err := advisoryBoundaryViolations([]byte(fixture.source), fixture.imports)
			if err != nil {
				t.Fatal(err)
			}
			if (len(violations) != 0) != fixture.forbidden {
				t.Fatalf("violations=%v, want forbidden=%v", violations, fixture.forbidden)
			}
		})
	}
}

func assertAdvisoryBoundaryFile(t *testing.T, path string, imports bool) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	violations, err := advisoryBoundaryViolations(source, imports)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, violation := range violations {
		t.Errorf("%s: %s", path, violation)
	}
}

func advisoryBoundaryViolations(source []byte, imports bool) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "boundary.go", source, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	if imports {
		const sdk = "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/"
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, err
			}
			if path == sdk+"economics" || path == sdk+"metering" {
				continue
			}
			first, _, _ := strings.Cut(path, "/")
			if path == "database/sql" || strings.HasPrefix(path, "database/sql/") || strings.Contains(first, ".") {
				violations = append(violations, "assessor dependency outside stdlib and typed economics/metering contracts: "+path)
			}
		}
		return violations, nil
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.Ident:
			if strings.HasPrefix(node.Name, "SupportAdvisory") || strings.HasPrefix(node.Name, "assessSupportUncertainty") {
				violations = append(violations, "advisory interpretation in money/stream owner: "+node.Name)
			}
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				value, err := strconv.Unquote(node.Value)
				if err == nil && strings.Contains(value, "support_advisory") {
					violations = append(violations, "advisory wire interpretation in money/stream owner")
				}
			}
		}
		return true
	})
	return violations, nil
}
