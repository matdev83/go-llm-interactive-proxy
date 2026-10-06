package archtest

// Task 12.1 certification assertion for requirement 11.4 and 11.5: generic core
// (internal/core and pkg) must not branch on a concrete coding-client family or
// on the TypeSafe/Jev vendor.
//
// Coverage assessment before this file existed. Task 8.1 already guards vendor
// wire details repository-wide
// (TestSessionClassificationVendorDetailsStayInsideTheAdapterPackage), and that
// guard is complete for requirement 11.5's DTO half: it rejects a vendor-marked
// import, a vendor-named declared type, and a vendor+transport-shape literal
// anywhere outside the one adapter package. Two gaps remained:
//
//  1. that guard deliberately permits provider vocabulary outside the contract
//     packages, so `switch mode { case "jev": }` in generic core was unguarded;
//  2. the coding-client half of requirement 11.4 - no switch over Codex, Cline,
//     Roo, OpenCode, Droid, Hermes, Pi or any other concrete coding-client
//     identity in generic core - had no assertion anywhere in the tree.
//
// Both gaps are closed by the sweep below. The branch detector is pinned by a
// hostile-fixture self-test and by a sanctioned-package census, so neither a
// detector that rejects nothing nor one that rejects everything can produce a
// passing run. The sibling aggregate claim lives in
// sessionclassification_aggregate_token_test.go.

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// genericCoreNeutralityRoots are the generic-core trees requirement 11.4 governs:
// internal/core is generic runtime and pkg is the public/canonical contract
// surface. They are asserted to be non-empty by
// TestGenericCoreNeutralityScanIsNotVacuous.
var genericCoreNeutralityRoots = []string{"internal/core", "pkg"}

// genericCoreNeutralityExemptions are the only generic-core-surface packages
// allowed to name a concrete client family or the remote vendor, and each one is
// exempt for a stated reason:
//
//   - pkg/lipsdk/sessionclassification is the SDK evidence contract: requirement
//     11.2 and 11.3 make it the one place the bounded pure matcher catalog is
//     reachable, so it owns the accepted-identity vocabulary it is handed.
//
// The list is deliberately minimal. It holds exactly one entry, and that entry is
// the only generic-core package the live sweep actually flags, so an exemption is
// never a safety margin. It is exact and non-empty by assertion, so widening it
// is a deliberate edit rather than a prefix that silently admits new packages.
//
// pkg/lipsdk/backendplugin/contracttest is NOT exempt, and that is a measured
// decision rather than an oversight: its connector coverage manifest names module
// paths such as "codex" as keyed struct VALUES, which decide nothing, so the
// sweep passes over it unchanged.
var genericCoreNeutralityExemptions = map[string]string{
	"pkg/lipsdk/sessionclassification": "SDK evidence contract owns accepted client-identity vocabulary (requirements 11.2, 11.3)",
}

// genericCoreClientFamilyMarkers are the concrete coding-client identities
// requirement 11.4 names, plus the short spellings internal/agentfacts matches.
var genericCoreClientFamilyMarkers = []string{
	"codex", "cline", "opencode", "droid", "hermes", "pi", "roo",
}

// genericCoreShortFamilyMarkers never match inside a larger literal value, so
// "pipeline" or "room" cannot be reported as a client-family branch.
var genericCoreShortFamilyMarkers = []string{"pi", "roo"}

// genericCoreVendorMarkers are the remote-vendor identities requirement 11.5
// keeps out of public/canonical contracts.
var genericCoreVendorMarkers = []string{"typesafe", "jev"}

// genericCoreBranchCallFuncs are the identity-matching helpers whose string
// argument is a branch even though the literal is not an operand of ==.
var genericCoreBranchCallFuncs = []string{
	"EqualFold", "Contains", "HasPrefix", "HasSuffix", "ContainsAny",
}

// TestGenericCoreHasNoConcreteCodingClientOrVendorBranch is requirement 11.4 and
// 11.5 over the live tree. Generic core may carry an accepted client User-Agent
// as an opaque bounded string (requirement 11.1) and may hold a pluggable
// classifier port, but it may not switch on WHICH client sent it.
func TestGenericCoreHasNoConcreteCodingClientOrVendorBranch(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	scanned := 0
	if err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkgDir := PackageDirFromRel(rel)
		if !slices.ContainsFunc(genericCoreNeutralityRoots, func(prefix string) bool {
			return MatchPathPrefix(pkgDir, prefix)
		}) {
			return nil
		}
		if _, exempt := genericCoreNeutralityExemptions[pkgDir]; exempt {
			return nil
		}
		findings, err := genericCoreBranchFindings(pkgDir, rel, src)
		if err != nil {
			return err
		}
		scanned++
		violations = append(violations, findings...)
		return nil
	}); err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("generic core branches on a concrete coding-client family or the remote vendor (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
	if scanned < 100 {
		t.Fatalf("only %d generic-core production files were scanned; a census that small cannot support "+
			"a repository-wide neutrality claim", scanned)
	}
	t.Logf("%d generic-core production files scanned, zero client-family or vendor branches", scanned)
}

// TestGenericCoreNeutralityScanIsNotVacuous proves the sweep above is a
// measurement: every declared generic-core root exists and contributed files, and
// every declared exemption exists, is itself under a generic-core root, and is
// live in the tree. Without this, an exemption typo or a renamed root would turn
// the sweep into a pass over nothing.
func TestGenericCoreNeutralityScanIsNotVacuous(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	scanned := make(map[string]int)
	if err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		scanned[PackageDirFromRel(rel)]++
		return nil
	}); err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}

	for _, prefix := range genericCoreNeutralityRoots {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(prefix))); err != nil {
			t.Errorf("generic-core root %q does not exist: %v", prefix, err)
			continue
		}
		files := 0
		for dir, count := range scanned {
			if MatchPathPrefix(dir, prefix) {
				files += count
			}
		}
		if files == 0 {
			t.Errorf("generic-core root %q contributed no scanned production file; requirement 11.4 would be "+
				"unproven for it", prefix)
		}
	}

	if len(genericCoreNeutralityExemptions) == 0 {
		t.Fatal("no generic-core exemption is declared; the SDK contract package legitimately names client " +
			"families, so an empty exemption set means the sweep is mis-scoped")
	}
	for pkgDir, reason := range genericCoreNeutralityExemptions {
		if !slices.ContainsFunc(genericCoreNeutralityRoots, func(prefix string) bool {
			return MatchPathPrefix(pkgDir, prefix)
		}) {
			t.Errorf("exempt package %q is not under any generic-core root %v; an exemption outside the "+
				"governed surface authorizes nothing", pkgDir, genericCoreNeutralityRoots)
		}
		if scanned[pkgDir] == 0 {
			t.Errorf("exempt package %q contributed no scanned production file; the exemption is dead and "+
				"should be deleted (%s)", pkgDir, reason)
		}
	}
}

// TestGenericCoreCodingClientBranchDetectorRejectsHostileSources is the
// load-bearing self-test for genericCoreBranchFindings. Every fixture is a
// complete, otherwise-ordinary production file that smuggles exactly one
// concrete-client or vendor branch, so a passing live sweep cannot be explained
// by a detector that rejects nothing. Each control fixture proves the opposite
// direction: an opaque accepted User-Agent, a provider-mode string that is not a
// branch, and a same-word non-branch stay silent.
func TestGenericCoreCodingClientBranchDetectorRejectsHostileSources(t *testing.T) {
	t.Parallel()

	const genericCore = "internal/core/runtime"
	for _, tc := range genericCoreBranchHostileCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings, err := genericCoreBranchFindings(genericCore, genericCore+"/executor.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(findings) != tc.want {
				t.Fatalf("detector reported %d findings, want %d: %s", len(findings), tc.want,
					strings.Join(findings, "; "))
			}
		})
	}
}

// genericCoreBranchFindings reports every concrete coding-client or remote-vendor
// branch a generic-core production file contains.
//
// A finding is a BRANCH, not a mention. A concrete identity is reported only when
// a string literal carrying it sits in a position that decides control flow:
//
//  1. a case clause of a switch or type switch;
//  2. an operand of == or !=;
//  3. a key of a composite literal;
//  4. a string argument to strings.Contains / HasPrefix / HasSuffix /
//     EqualFold / ContainsAny, which match identity without an explicit operator.
//
// Plus one structural rule: an import whose path carries a concrete-client or
// vendor marker. Generic core must not be able to reach a coding-client module at
// all, so the import is reported independently of any literal.
//
// Residual limitation, stated honestly: this is a textual and syntactic
// tripwire, not a proof. A family identity assembled at runtime ("cod" + "ex"),
// carried in configuration, or hidden behind a helper in an exempt package is
// invisible here, and a marker-free alias for a coding-client module defeats the
// import rule exactly as it defeats sessionClassificationVendorImportPath. The
// stronger structural halves are elsewhere: internal/agentfacts is the single
// bounded matcher catalog (requirement 11.3), the generic aggregates carry no
// classifier field (TestGenericAggregatesContainNoPerFeatureFields), and vendor
// wire details are guarded repository-wide
// (TestSessionClassificationVendorDetailsStayInsideTheAdapterPackage). This
// layer must not be read as a complete account of where a client identity may
// appear.
func genericCoreBranchFindings(pkgDir, relPath string, src []byte) ([]string, error) {
	if _, exempt := genericCoreNeutralityExemptions[pkgDir]; exempt {
		return nil, nil
	}
	fset, file, err := ParseGoSource(relPath, src)
	if err != nil {
		return nil, err
	}
	violations := genericCoreIdentityImportFindings(relPath, file)
	return append(violations, genericCoreBranchLiteralFindings(fset, relPath, file)...), nil
}

// genericCoreIdentityImportFindings reports every import that lets generic core
// reach a concrete coding-client or remote-vendor package. Both halves of the
// import spec are inspected for the same reason
// sessionClassificationImportFindings inspects both: an ordinary aliased import
// would otherwise enter a generic-core file under a marker-free local name.
func genericCoreIdentityImportFindings(relPath string, file *ast.File) []string {
	var findings []string
	for _, spec := range file.Imports {
		if spec.Path == nil {
			continue
		}
		importPath := strings.Trim(spec.Path.Value, `"`)
		if genericCoreMarkerSegment(importPath) {
			findings = append(findings, fmt.Sprintf("%s: imports a concrete coding-client or vendor package %s; "+
				"generic core reaches clients through accepted opaque identity only (requirements 11.1, 11.4, 11.5)",
				relPath, importPath))
			continue
		}
		if spec.Name != nil && genericCoreVendorName(spec.Name.Name) {
			findings = append(findings, fmt.Sprintf("%s: imports a package under a client-family or vendor local alias %q",
				relPath, spec.Name.Name))
		}
	}
	return findings
}

// genericCoreBranchLiteralFindings walks every string literal and reports the
// ones that carry a concrete client family or the remote vendor while sitting in
// a branching position.
func genericCoreBranchLiteralFindings(fset *token.FileSet, relPath string, file *ast.File) []string {
	branching := make(map[*ast.BasicLit]bool)
	mark := func(exprs []ast.Expr) {
		for _, expr := range exprs {
			if literal, ok := expr.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				branching[literal] = true
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CaseClause:
			mark(n.List)
		case *ast.BinaryExpr:
			switch n.Op {
			case token.EQL, token.NEQ:
				mark([]ast.Expr{n.X, n.Y})
			}
		case *ast.KeyValueExpr:
			mark([]ast.Expr{n.Key})
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			if !slices.Contains(genericCoreBranchCallFuncs, sel.Sel.Name) {
				return true
			}
			// Only a strings.* receiver matches identity textually; a
			// same-named method on an unrelated type is not a branch on a
			// client identity.
			if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != "strings" {
				return true
			}
			for _, arg := range n.Args {
				mark([]ast.Expr{arg})
			}
		}
		return true
	})

	var findings []string
	for literal := range branching {
		value, err := strconv.Unquote(literal.Value)
		if err != nil || !genericCoreBranchIdentity(value) {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s:%d: branches on concrete coding-client or vendor identity %q; "+
			"requirement 11.4 keeps generic core free of client-family switches and requirement 11.5 keeps the "+
			"remote vendor out of canonical contracts",
			relPath, fset.Position(literal.Pos()).Line, value))
	}
	slices.Sort(findings)
	return findings
}

// genericCoreBranchIdentity reports whether a literal value names a concrete
// coding-client family or the remote vendor. A short marker must be the whole
// value so an ordinary English word is not a finding; a longer marker also
// matches inside a larger value, which is what catches a versioned User-Agent
// such as "codex_cli_rs/1.2.3" used as a branch.
func genericCoreBranchIdentity(value string) bool {
	lowered := strings.ToLower(strings.TrimSpace(value))
	if lowered == "" {
		return false
	}
	if genericCoreVendorName(lowered) {
		return true
	}
	for _, marker := range genericCoreClientFamilyMarkers {
		if lowered == marker {
			return true
		}
		if slices.Contains(genericCoreShortFamilyMarkers, marker) {
			continue
		}
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// genericCoreMarkerSegment reports whether any slash-separated segment of an
// import path names a concrete coding-client family or the remote vendor.
func genericCoreMarkerSegment(importPath string) bool {
	for _, segment := range strings.Split(strings.ToLower(importPath), "/") {
		if genericCoreVendorName(segment) {
			return true
		}
		for _, marker := range genericCoreClientFamilyMarkers {
			if segment == marker {
				return true
			}
		}
	}
	return false
}

// genericCoreVendorName reports whether a name carries the remote-vendor marker.
// The markers are reused from the task 8.1 vendor guard so the two layers cannot
// drift: one list decides where vendor wire details may live, this one decides
// where a vendor identity may be branched on.
func genericCoreVendorName(name string) bool {
	lowered := strings.ToLower(name)
	return slices.ContainsFunc(genericCoreVendorMarkers, func(marker string) bool {
		return strings.Contains(lowered, marker)
	})
}
