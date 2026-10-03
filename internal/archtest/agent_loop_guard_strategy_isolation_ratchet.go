package archtest

// Agent-loop-guard (ALG) strategy-isolation ratchets: the two reachability
// walks that keep the preferred and legacy strategies apart (design ratchets 7
// and 8).
//
// These are the ALG ratchets that cannot be decided from a single file. Each walk
// starts at one strategy case of the single error-returning constructor and
// follows the same-package call graph, so a dependency cannot be smuggled in
// through a helper.
//
// The multi-file terminal-owner census (Requirement 9.5) is a different concern
// and lives in agent_loop_guard_terminal_owner_census.go.
//
// Both are pure functions over an explicit file set. That is what lets the
// committed fixtures overlay a miniature feature package and assert the verdict,
// mirroring request_attempt_state_baseline_test.go's temporary AST overlay.
//
// The per-file vocabulary and repository walk live in
// agent_loop_guard_ownership_ratchet.go; the shared AST primitives live in
// agent_loop_guard_ownership_ast.go.

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

func ScanAgentLoopGuardFeaturePackage(files []algSourceFile) []RuleFinding {
	findings := scanAlgTerminalOwnerCensus(files)
	findings = append(findings, ScanAgentLoopGuardStrategyReachability(files, AlgPreferredStrategyIsolationSpec())...)
	findings = append(findings, ScanAgentLoopGuardStrategyReachability(files, AlgLegacyStrategyIsolationSpec())...)
	return findings
}

// AlgPreferredStrategyIsolationSpec is design ratchet 7: the preferred
// explicit-completion strategy must construct no verifier and reach no verifier
// call, because it judges a completion protocol rather than a semantic verdict
// (Requirement 9.4).
func AlgPreferredStrategyIsolationSpec() algStrategyIsolationSpec {
	return algStrategyIsolationSpec{
		Rule:                    RuleALGPreferredStrategyIsolation,
		Owner:                   "preferred explicit-completion strategy",
		StrategySelector:        "StrategyAttemptCompletion",
		Receiver:                algPreferredReceiver,
		ForbiddenImportSuffixes: []string{"/agentloopguard/verifier"},
	}
}

// AlgLegacyStrategyIsolationSpec is design ratchet 8: the legacy
// semantic-verifier strategy must not reach the proxy-owned control tool, so the
// two strategies keep their fixed vocabularies (Requirement 9.5).
func AlgLegacyStrategyIsolationSpec() algStrategyIsolationSpec {
	return algStrategyIsolationSpec{
		Rule:                    RuleALGLegacyStrategyIsolation,
		Owner:                   "legacy semantic-verifier strategy",
		StrategySelector:        "StrategySemanticVerifier",
		Receiver:                algLegacyReceiver,
		ForbiddenImportSuffixes: []string{"/pkg/lipsdk/controltool"},
		ForbiddenLocalIdents:    algLegacyForbiddenLocalIdents,
	}
}

// algStrategyIsolationSpec parameterizes one strategy-isolation ratchet: from
// the strategy case of the single error-returning constructor, no forbidden
// CALL may appear in that case body, in the receiver's own methods, or in the
// plain same-package function chain they reach, and the receiver's own files may
// not import the forbidden dependency. See
// ScanAgentLoopGuardStrategyReachability for the exact covered set.
type algStrategyIsolationSpec struct {
	Rule                    string
	Owner                   string
	StrategySelector        string
	Receiver                string
	ForbiddenImportSuffixes []string
	ForbiddenLocalIdents    map[string]string
}

// ScanAgentLoopGuardStrategyReachability walks the same-package call graph from
// one strategy branch of the ALG constructor and reports any reachable
// reference to that strategy's forbidden dependency.
//
// WHAT THE WALK COVERS, exactly:
//
//   - The receiver's own methods are seeded wholesale: every method declared on
//     the strategy's approved receiver type is in scope whether or not the
//     strategy case body calls it.
//   - The statements of the strategy's case body inside the constructor.
//   - Every plain same-package function reachable from those seeds by repeated
//     plain-call hops (a bodyless identifier call resolved against the same
//     package's declared functions), transitively.
//
// WHAT THE WALK DOES NOT COVER, and why that is a deliberate approximation:
//
//   - It does NOT follow a method reached through a receiver VARIABLE. A call
//     like e.judge(ctx) records pkgIdent="e", so it resolves to no declaration
//     and the walk stops there. Methods of a receiver type are covered by the
//     wholesale seeding above, not by following the qualifier.
//   - It does NOT follow method values (judge := verifier.Judge), composite
//     literals (verifier.Config{}), package-level function variables
//     (var z = verifier.New(...)), or any interface or struct-field indirection.
//     Those forms are references without an attributable callee.
//   - It does NOT resolve types, so it cannot tell which type a receiver
//     variable holds.
//
// Fail-open on unresolvable calls is the correct choice here, not a shortcut. A
// strict variant that fails closed on every unresolvable call - or that checks
// the IMPORTS of every file the walk reaches - is NOT green on the live tree:
// the preferred strategy's seeded methods and the shared allowStop helper in
// provider.go pull that file in, and provider.go legitimately imports the
// verifier for the legacy strategy. Failing closed would therefore report the
// approved live layout as a violation. Closing the remaining forms needs type
// resolution (go/types), which this syntax-only index does not carry.
//
// Consequently the enforced guarantee is narrow and stated as such: no forbidden
// CALL appears in the strategy case body, in the receiver's own methods, or in
// the plain-function chain they reach. That is stronger than a bare grep of the
// receiver's file - it follows arbitrarily many hops - and it is the whole of
// what is claimed.
func ScanAgentLoopGuardStrategyReachability(files []algSourceFile, spec algStrategyIsolationSpec) []RuleFinding {
	idx := newAlgPackageIndex(files)
	unprovable := func(format string, args ...any) []RuleFinding {
		return []RuleFinding{{
			Rule:   spec.Rule,
			Path:   algLegacyProviderFile,
			Detail: fmt.Sprintf("%s: %s", spec.Owner, fmt.Sprintf(format, args...)),
		}}
	}
	receiverKeys := idx.receiverDeclKeys(spec.Receiver)
	if len(receiverKeys) == 0 {
		return unprovable("receiver %q declares no %s method, so strategy isolation cannot be proven", spec.Receiver, algDecideMethod)
	}
	caseBody, ok := idx.strategyCaseBody(algStrategyConstructor, spec.StrategySelector)
	if !ok {
		return unprovable("%s has no %s case, so strategy isolation cannot be proven", algStrategyConstructor, spec.StrategySelector)
	}

	var findings []RuleFinding
	report := func(key string, hit algCallHit, detail string) {
		findings = append(findings, RuleFinding{
			Rule:   spec.Rule,
			Path:   fmt.Sprintf("%s:%d", idx.relOfDecl[key], algLine(idx.fsetOfDecl[key], hit.node)),
			Detail: fmt.Sprintf("%s: declaration %s %s", spec.Owner, key, detail),
		})
	}

	// The receiver's own methods must live in a file that does not import the
	// forbidden dependency, so the strategy's construction graph starts clean.
	for _, key := range receiverKeys {
		rel := idx.relOfDecl[key]
		for _, path := range sortedImportPaths(idx.imports[rel]) {
			if algForbiddenImport(path, spec.ForbiddenImportSuffixes) {
				findings = append(findings, RuleFinding{
					Rule:   spec.Rule,
					Path:   rel,
					Detail: fmt.Sprintf("%s: receiver %q is implemented in %s, which imports the forbidden dependency %q", spec.Owner, spec.Receiver, filepath.Base(rel), path),
				})
			}
		}
	}

	visited := make(map[string]bool, len(receiverKeys))
	queue := make([]string, 0, len(receiverKeys))
	queue = append(queue, receiverKeys...)
	queue = append(queue, idx.calleesOf(caseBody)...)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if visited[key] {
			continue
		}
		visited[key] = true
		for _, hit := range idx.callHits(idx.decls[key]) {
			for name, reason := range spec.ForbiddenLocalIdents {
				if hit.matchesLocalName(name) {
					report(key, hit, fmt.Sprintf("reaches %q (%s)", name, reason))
				}
			}
			if alias, path, ok := idx.forbiddenAlias(hit, spec.ForbiddenImportSuffixes); ok {
				report(key, hit, fmt.Sprintf("calls %s.%s from the forbidden dependency %q", alias, hit.selName, path))
			}
			if next, ok := idx.resolveCallee(hit); ok {
				queue = append(queue, next)
			}
		}
	}
	return findings
}

// algPackageIndex is the same-package call-graph index used by the ALG
// ratchets. It is built from an explicit file set so a fixture can overlay a
// deliberately violating miniature package.
type algPackageIndex struct {
	decls       map[string]*ast.FuncDecl
	relOfDecl   map[string]string
	fsetOfDecl  map[string]*token.FileSet
	recvOfDecl  map[string]string
	typesOfDecl map[string]algPackageType
	embedOfType map[string][]string
	imports     map[string]map[string]string
}

func newAlgPackageIndex(files []algSourceFile) *algPackageIndex {
	idx := &algPackageIndex{
		decls:       make(map[string]*ast.FuncDecl),
		relOfDecl:   make(map[string]string),
		fsetOfDecl:  make(map[string]*token.FileSet),
		recvOfDecl:  make(map[string]string),
		typesOfDecl: make(map[string]algPackageType),
		embedOfType: make(map[string][]string),
		imports:     make(map[string]map[string]string),
	}
	for _, file := range files {
		idx.imports[file.RelPath] = algImportAliases(file.AST)
		for _, decl := range file.AST.Decls {
			switch typed := decl.(type) {
			case *ast.FuncDecl:
				fn := typed
				if fn.Name == nil || fn.Body == nil {
					continue
				}
				recv := algReceiverBaseName(fn.Recv)
				key := fn.Name.Name
				if recv != "" {
					key = recv + "." + fn.Name.Name
				}
				if _, exists := idx.decls[key]; exists {
					continue
				}
				idx.decls[key] = fn
				idx.relOfDecl[key] = file.RelPath
				idx.fsetOfDecl[key] = file.FSet
				idx.recvOfDecl[key] = recv
				if recv != "" {
					info := idx.typesOfDecl[recv]
					info.methods = append(info.methods, key)
					idx.typesOfDecl[recv] = info
				}
			case *ast.GenDecl:
				idx.recordTypeDecl(typed, file.RelPath)
			}
		}
	}
	return idx
}

func (i *algPackageIndex) receiverDeclKeys(recv string) []string {
	var keys []string
	for key := range i.decls {
		if i.recvOfDecl[key] == recv {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// terminalReceiverFiles maps each receiver type whose method set contains the
// generic terminal-decision method to the files declaring it. A type qualifies
// by declaring algDecideMethod itself or by inheriting it through a chain of
// same-package embedded types, because embedding satisfies the same seam.
// Receivers without that method - the feature config value, the control-tool
// handle - are not terminal owners and are deliberately excluded.
func (i *algPackageIndex) terminalReceiverFiles() map[string][]string {
	owners := make(map[string][]string)
	for key, recv := range i.recvOfDecl {
		if recv == "" || !strings.HasSuffix(key, "."+algDecideMethod) {
			continue
		}
		owners[recv] = appendUnique(owners[recv], i.relOfDecl[key])
	}
	for _, recv := range sortedKeys(owners) {
		for _, embedder := range i.embedOfType[recv] {
			if _, already := owners[embedder]; already {
				continue
			}
			owners[embedder] = i.typesOfDecl[embedder].rels
		}
	}
	return owners
}

// strategyCaseBody returns the statements of the switch case selected by the
// named strategy constant inside the constructor. A missing case is reported as
// unprovable isolation rather than silently skipped.
func (i *algPackageIndex) strategyCaseBody(fnName, selector string) ([]ast.Stmt, bool) {
	fn, ok := i.decls[fnName]
	if !ok || fn.Body == nil {
		return nil, false
	}
	var found []ast.Stmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SwitchStmt:
			for _, stmt := range node.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok || len(clause.List) == 0 {
					continue
				}
				if ident, ok := clause.List[0].(*ast.Ident); ok && ident.Name == selector {
					found = clause.Body
					return false
				}
			}
		case *ast.FuncLit:
			// A closure body is not the constructor's own dispatch.
			return false
		}
		return true
	})
	if found == nil {
		return nil, false
	}
	return found, true
}

// forbiddenAlias reports whether the call's package qualifier is an import alias
// for one of the forbidden dependency suffixes.
func (i *algPackageIndex) forbiddenAlias(hit algCallHit, suffixes []string) (string, string, bool) {
	if hit.pkgIdent == "" {
		return "", "", false
	}
	for rel, aliases := range i.imports {
		path, ok := aliases[hit.pkgIdent]
		if !ok || !algForbiddenImport(path, suffixes) {
			continue
		}
		return hit.pkgIdent, path, rel != ""
	}
	return "", "", false
}

// resolveCallee maps an observed call to the next same-package declaration key.
//
// Only a plain same-package function call resolves: an identifier call whose
// name is declared in the indexed package. A selector call never resolves,
// because the walk carries no type information and therefore cannot attribute
// pkgIdent.Sel to a declared method (see the walk's coverage contract above).
// Selector calls are still checked for a forbidden import alias through
// forbiddenAlias, which is the only shape a cross-package reference takes.
func (i *algPackageIndex) resolveCallee(hit algCallHit) (string, bool) {
	if hit.pkgIdent != "" || hit.selName != "" {
		return "", false
	}
	if _, ok := i.decls[hit.identName]; ok {
		return hit.identName, true
	}
	return "", false
}

// calleesOf resolves the same-package declarations called directly from stmts.
func (i *algPackageIndex) calleesOf(stmts []ast.Stmt) []string {
	var out []string
	for _, hit := range algCallHits(stmts) {
		if next, ok := i.resolveCallee(hit); ok {
			out = append(out, next)
		}
	}
	return out
}

func (i *algPackageIndex) callHits(fn *ast.FuncDecl) []algCallHit {
	if fn == nil || fn.Body == nil {
		return nil
	}
	return algCallHits(fn.Body.List)
}
