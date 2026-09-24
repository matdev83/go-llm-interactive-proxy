package archtest

import (
	"encoding/csv"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
)

// TestPhase1PublicOptionsStayNonMonetary protects the public construction
// boundary. Billing composition may be added through an explicit, typed opt-in
// seam later; ordinary Options and stock startup must remain non-money.
func TestPhase1PublicOptionsStayNonMonetary(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[lipruntime.Options]()
	containsTerm := func(value, term string) bool {
		for _, word := range identifierWords(value) {
			if word == term {
				return true
			}
		}
		return false
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		for _, term := range []string{
			"billing", "money", "currency", "tariff", "settlement", "exposure",
			"journal", "balance", "price", "charge", "credit", "debit",
		} {
			if containsTerm(field.Name, term) || containsTerm(field.Type.String(), term) {
				t.Fatalf("ordinary lipruntime.Options exposes monetary binding term %q through %s %s", term, field.Name, field.Type)
			}
		}
		if strings.Contains(strings.ToLower(field.Type.PkgPath()), "/internal/core/billing") || strings.Contains(strings.ToLower(field.Type.PkgPath()), "/internal/infra/billing") {
			t.Fatalf("ordinary lipruntime.Options field %s imports a monetary implementation type %s", field.Name, field.Type.PkgPath())
		}
	}
}

// phase1ConcreteProviderLiteral recognizes concrete provider names only. The
// generic word "provider" is deliberately not included: it is a neutral
// protocol field and is permitted in the core.
func phase1ConcreteProviderLiteral(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, provider := range []string{
		"openai", "anthropic", "gemini", "bedrock", "vertex", "codex", "cursor",
		"gitlab", "minimax", "mistral", "cohere", "commandcode",
	} {
		if value == provider || strings.HasPrefix(value, provider+":") || strings.HasPrefix(value, provider+"/") {
			return true
		}
	}
	return false
}

func phase1StringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// TestPhase1CoreHasNoProviderNamedBranches ensures future economic/core code
// cannot grow a provider-specific branch. Provider semantics belong in the
// adapter/plugin zones; the core may branch only on neutral contracts.
func TestPhase1CoreHasNoProviderNamedBranches(t *testing.T) {
	t.Parallel()
	repo := repoRoot(t)
	// Route-override contract fixtures intentionally use concrete selectors to
	// exercise latest-wins semantics. All other core production packages are
	// guarded against provider-specific economic behavior.
	root := filepath.Join(repo, "internal", "core")
	var offenders []string
	scan := func() error {
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "routeoverride/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.SwitchStmt:
					if n.Body == nil {
						return true
					}
					for _, stmt := range n.Body.List {
						clause, ok := stmt.(*ast.CaseClause)
						if !ok {
							continue
						}
						for _, expr := range clause.List {
							if value, ok := phase1StringLiteral(expr); ok && phase1ConcreteProviderLiteral(value) {
								offenders = append(offenders, filepath.ToSlash(filepath.Join("internal", "core", rel))+": provider switch literal "+value)
							}
						}
					}
				case *ast.BinaryExpr:
					if n.Op != token.EQL && n.Op != token.NEQ {
						return true
					}
					for _, expr := range []ast.Expr{n.X, n.Y} {
						if value, ok := phase1StringLiteral(expr); ok && phase1ConcreteProviderLiteral(value) {
							offenders = append(offenders, filepath.ToSlash(filepath.Join("internal", "core", rel))+": provider comparison literal "+value)
						}
					}
				}
				return true
			})
			return nil
		})
	}
	if err := scan(); err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("core contains provider-named branch; move provider semantics to an adapter:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestPhase1SingleMonetaryWriterAndNoShadowPosting keeps the only production
// monetary writer in the billing-store adapter. Runtime, metering, and other
// infrastructure may request a billing operation through the core port but may
// not add a second journal writer or shadow posting path.
func TestPhase1SingleMonetaryWriterAndNoShadowPosting(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allowed := []string{
		"internal/core/billing/",
		"internal/infra/billingstore/",
	}
	forbidden := map[string]struct{}{
		"ApplyCallBillingResult":       {},
		"ApplyProviderCost":            {},
		"MarkProviderCostUnreconciled": {},
		"postJournalInTx":              {},
		"postJournalTransaction":       {},
		"ShadowPost":                   {},
		"shadowPost":                   {},
		"ShadowPosting":                {},
		"shadowPosting":                {},
		"ShadowJournal":                {},
		"shadowJournal":                {},
		"postJournalShadow":            {},
	}
	var offenders []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, prefix := range allowed {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			id, ok := node.(*ast.Ident)
			if ok {
				if _, blocked := forbidden[id.Name]; blocked {
					offenders = append(offenders, rel+": "+id.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("second monetary writer or shadow posting outside billing ownership:\n%s", strings.Join(offenders, "\n"))
	}

	// Keep the legacy ownership chokepoints singular even inside the allowed
	// billing-store zone; an allowlisted package must not hide a second writer.
	writerDefinitions := map[string]int{
		"ApplyCallBillingResult": 0,
		"ApplyProviderCost":      0,
		"postJournalInTx":        0,
		"postJournalTransaction": 0,
	}
	err = filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.FuncDecl)
			if ok {
				if _, required := writerDefinitions[decl.Name.Name]; required {
					writerDefinitions[decl.Name.Name]++
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, count := range writerDefinitions {
		if count != 1 {
			t.Fatalf("monetary ownership chokepoint %s definitions = %d, want exactly one", name, count)
		}
	}
}

func TestPhase1FingerprintSourcesCarryExplicitVersionMarkers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	required := map[string][]string{
		"internal/core/billing/records.go":                   {"CurrentRecordSchemaVersion", "workload:v1"},
		"internal/core/billing/call_usage.go":                {"c.string(\"cur\")", "c.string(\"clur\")"},
		"internal/core/billing/commands.go":                  {"trusted-command:v1"},
		"internal/core/billing/exposure.go":                  {"exposure:v1"},
		"internal/core/billing/maintenance.go":               {"provider-maintenance:v1"},
		"internal/core/billing/provider_cost_fingerprint.go": {"provider-cost:v1"},
		"internal/core/billing/journal.go":                   {"JournalFingerprintPrefix", "journal-fp:v2"},
	}
	for rel, markers := range required {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(src)
		if rel != "internal/core/billing/call_usage.go" && !strings.Contains(text, "sha256.Sum256") {
			t.Fatalf("%s no longer exposes the expected SHA-256 fingerprint implementation", rel)
		}
		for _, marker := range markers {
			if !strings.Contains(text, marker) {
				t.Fatalf("%s lost explicit schema/fingerprint version marker %q", rel, marker)
			}
		}
	}
}

// TestPhase1ProducerConsumerCensusIsExactAndDispositioned validates the
// source-anchor census used by the Phase 1 review. A category roll-up is not a
// completion claim: every row must name an existing source path, exact symbol,
// owner, protocol family, and a status plus parent task disposition.
func TestPhase1ProducerConsumerCensusIsExactAndDispositioned(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(".kiro/specs/archive/usage-economics-b-leg-multimodal-refinement/evidence/phase1-producer-consumer-census.tsv"))
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || len(rows[0]) != 7 {
		t.Fatalf("census header = %#v, want seven TSV columns", rows)
	}
	wantHeader := []string{"category", "path", "symbol_anchor", "symbols_or_boundary", "protocol_family", "owner", "disposition"}
	for i, want := range wantHeader {
		if rows[0][i] != want {
			t.Fatalf("census header column %d = %q, want %q", i, rows[0][i], want)
		}
	}
	if len(rows)-1 != 69 {
		t.Fatalf("census exact source-anchor rows = %d, want frozen 69", len(rows)-1)
	}
	wantCategories := map[string]int{
		"compaction": 2, "metering-journal": 4, "monetary-rating": 3,
		"monetary-store": 5, "monetary-worker": 3, "prompt-cache": 6,
		"provider-producer": 16, "report": 7, "sideband-finalizer": 9,
		"token-contract": 6, "token-reducer": 2, "token-runtime": 6,
	}
	gotCategories := make(map[string]int, len(wantCategories))
	seen := make(map[string]struct{}, len(rows)-1)
	allowedStatuses := map[string]struct{}{
		"bridge-v1": {}, "bridge estimated": {}, "bridge fixture": {},
		"pending": {}, "unsupported": {}, "RED": {},
		"v2-certified": {}, "lossless-v1-bridge": {}, "unsupported advanced evidence": {},
		"removed": {},
	}
	for lineNo, row := range rows[1:] {
		if len(row) != 7 {
			t.Fatalf("census line %d has %d columns, want 7", lineNo+2, len(row))
		}
		for i, value := range row {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("census line %d column %d is blank", lineNo+2, i+1)
			}
		}
		category, relPath, anchor, boundary, protocol, owner, disposition := row[0], row[1], row[2], row[3], row[4], row[5], row[6]
		if strings.Contains(relPath, "\\") || filepath.IsAbs(relPath) || !strings.HasSuffix(relPath, ".go") {
			t.Fatalf("census line %d path %q is not a repository-relative Go path", lineNo+2, relPath)
		}
		if _, ok := wantCategories[category]; !ok {
			t.Fatalf("census line %d has unknown category %q", lineNo+2, category)
		}
		gotCategories[category]++
		identity := relPath + "#" + anchor
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("census line %d duplicates exact owner anchor %q", lineNo+2, identity)
		}
		seen[identity] = struct{}{}
		sourcePath := filepath.Join(root, filepath.FromSlash(relPath))
		src, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("census line %d source %s: %v", lineNo+2, relPath, err)
		}
		parts := strings.SplitN(disposition, ";", 2)
		status := strings.TrimSpace(parts[0])
		if _, ok := allowedStatuses[status]; !ok {
			t.Fatalf("census line %d disposition status %q is not explicit", lineNo+2, status)
		}
		if status == "removed" {
			// Task 18.2 (Migration Strategy step 8): retired superseded paths
			// keep their historical anchor name but must be absent from the
			// final tree. The explicit replacement must be present instead.
			if strings.Contains(string(src), anchor) {
				t.Fatalf("census line %d retired anchor %q still present in %s; removal is not complete", lineNo+2, anchor, relPath)
			}
			if relPath == "internal/core/runtime/billing_leg.go" && anchor == "mergeStreamCostOntoLeg" {
				if !strings.Contains(string(src), "projectV1BillingEvidence") {
					t.Fatalf("census line %d retired mergeStreamCostOntoLeg lacks replacement projectV1BillingEvidence in %s", lineNo+2, relPath)
				}
			}
		} else if !strings.Contains(string(src), anchor) {
			t.Fatalf("census line %d source %s lacks exact symbol anchor %q", lineNo+2, relPath, anchor)
		}
		if strings.TrimSpace(boundary) == category || strings.TrimSpace(protocol) == category {
			t.Fatalf("census line %d collapses boundary/protocol to category only", lineNo+2)
		}
		// The Phase 8 disposition may contain a bounded semicolon-delimited
		// reason before the frozen parent-task marker. Keep the status in the
		// first field while accepting the richer evidence text.
		if len(parts) != 2 || !strings.Contains(disposition, "; parent ") {
			t.Fatalf("census line %d disposition %q lacks parent task", lineNo+2, disposition)
		}
		if strings.TrimSpace(owner) == category {
			t.Fatalf("census line %d owner is only the category %q", lineNo+2, owner)
		}
	}
	if len(gotCategories) != len(wantCategories) {
		t.Fatalf("census categories = %#v, want %#v", gotCategories, wantCategories)
	}
	for category, want := range wantCategories {
		if gotCategories[category] != want {
			t.Fatalf("census category %q rows = %d, want %d", category, gotCategories[category], want)
		}
	}
	if _, err := reader.Read(); err != io.EOF {
		t.Fatalf("census reader did not end at EOF: %v", err)
	}
}
