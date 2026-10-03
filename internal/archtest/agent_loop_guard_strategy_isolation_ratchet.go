package archtest

// Agent-loop-guard (ALG) strategy-isolation ratchets: the two reachability
// walks that keep the preferred and legacy strategies apart (design ratchets 7
// and 8).
//
// These are the ALG ratchets that cannot be decided from a single file. Each walk
// starts at one strategy case of the single error-returning constructor and
// follows the feature's call graph, so a dependency cannot be smuggled in
// through a helper or through the subpackage the strategy delegates to.
//
// The multi-file terminal-owner census (Requirement 9.5) is a different concern
// and lives in agent_loop_guard_terminal_owner_census.go. The package-qualified
// index both judge over lives in agent_loop_guard_feature_index.go.
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
	"path/filepath"
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

// ScanAgentLoopGuardStrategyReachability walks the feature's call graph from one
// strategy branch of the ALG constructor and reports any reachable reference to
// that strategy's forbidden dependency.
//
// The index covers the WHOLE feature, root package and subpackages alike, so a
// strategy's construction graph is followed across the package boundary it
// actually crosses in the shipped layout: the preferred receiver delegates the
// decision to protocolpolicy, and the legacy receiver delegates to causepolicy
// and progress. A walk confined to one package would have stopped at the first
// of those calls.
//
// WHAT THE WALK COVERS, exactly:
//
//   - The receiver's own methods are seeded wholesale: every method declared on
//     the strategy's approved receiver type in the feature ROOT package is in
//     scope whether or not the strategy case body calls it.
//   - The statements of the strategy's case body inside the constructor.
//   - Every plain same-package function reachable from those seeds by repeated
//     plain-call hops (a bodyless identifier call resolved against the CALLING
//     file's own package), transitively.
//   - Every qualified call from those seeds into an INDEXED FEATURE PACKAGE
//     (protocolpolicy.Evaluate, progress.Evaluate, ...), resolved through the
//     calling file's import aliases, transitively. Each such call is resolved
//     against the qualified package's own exported function declarations, so
//     the hop chain continues there under the same rules.
//
// WHAT THE WALK DOES NOT COVER, and why that is a deliberate approximation:
//
//   - It does NOT follow a method reached through a receiver VARIABLE. A call
//     like e.judge(ctx) records pkgIdent="e", which resolves to no feature
//     package, so the walk stops there. Methods of a receiver type are covered
//     by the wholesale seeding above, not by following the qualifier.
//   - It does NOT follow method values (judge := verifier.Judge), composite
//     literals (verifier.Config{}), package-level function variables
//     (var z = verifier.New(...)), or any interface or struct-field indirection.
//     Those forms are references without an attributable callee.
//   - It does NOT resolve types, so it cannot tell which type a receiver
//     variable holds.
//   - It does NOT follow a qualified call into a package OUTSIDE the indexed
//     feature tree, even when that package is generic core or an SDK contract.
//     Such a package's own body is a different ratchet's subject; what is
//     checked at the hop itself is the package qualifier, which is the only
//     attributable fact a selector call carries here.
//   - It does NOT check the IMPORTS of a file it reaches; only the receiver's
//     own files are import-checked (see below).
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
// CALL appears in the strategy case body, in the receiver's own methods, in any
// indexed feature package those reach, or in the plain-function chains they
// reach. That is stronger than a bare grep of the receiver's file - it follows
// arbitrarily many hops and crosses the feature's own package boundaries - and
// it is the whole of what is claimed.
func ScanAgentLoopGuardStrategyReachability(files []algSourceFile, spec algStrategyIsolationSpec) []RuleFinding {
	idx := newAlgPackageIndex(files)
	unprovable := func(format string, args ...any) []RuleFinding {
		return []RuleFinding{{
			Rule:   spec.Rule,
			Path:   algFeatureRootDir,
			Detail: fmt.Sprintf("%s: %s", spec.Owner, fmt.Sprintf(format, args...)),
		}}
	}
	receiverKeys := idx.receiverDeclKeys(spec.Receiver)
	if len(receiverKeys) == 0 {
		return unprovable("receiver %q declares no %s method, so strategy isolation cannot be proven", spec.Receiver, algDecideMethod)
	}
	caseDecl, caseBody, ok := idx.strategyCaseBody(algStrategyConstructor, spec.StrategySelector)
	if !ok {
		return unprovable("%s has no %s case, so strategy isolation cannot be proven", algStrategyConstructor, spec.StrategySelector)
	}

	var findings []RuleFinding
	report := func(key algDeclID, hit algCallHit, detail string) {
		findings = append(findings, RuleFinding{
			Rule:   spec.Rule,
			Path:   fmt.Sprintf("%s:%d", idx.relOfDecl[key], algLine(idx.fsetOfDecl[key], hit.node)),
			Detail: fmt.Sprintf("%s: declaration %s %s", spec.Owner, key.Short, detail),
		})
	}

	// The receiver's own files must not import the forbidden dependency, so the
	// strategy's construction graph starts clean.
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

	visited := make(map[algDeclID]bool, len(receiverKeys))
	queue := make([]algDeclID, 0, len(receiverKeys)+1)
	queue = append(queue, receiverKeys...)
	queue = append(queue, idx.calleesOf(idx.relOfDecl[caseDecl], caseBody)...)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if visited[key] {
			continue
		}
		visited[key] = true
		rel := idx.relOfDecl[key]
		for _, hit := range idx.callHits(idx.decls[key]) {
			for name, reason := range spec.ForbiddenLocalIdents {
				if hit.matchesLocalName(name) {
					report(key, hit, fmt.Sprintf("reaches %q (%s)", name, reason))
				}
			}
			if alias, path, ok := idx.forbiddenAlias(rel, hit, spec.ForbiddenImportSuffixes); ok {
				report(key, hit, fmt.Sprintf("calls %s.%s from the forbidden dependency %q", alias, hit.selName, path))
			}
			if next, ok := idx.resolveCallee(rel, hit); ok {
				queue = append(queue, next)
			}
		}
	}
	return findings
}
