package archtest

// Agent-loop-guard (ALG) per-file ownership ratchets.
//
// Every ratchet in this file is split into a pure validator that accepts a file
// plus a thin repository walk. That is the established pattern in this package:
// closed_plane_ratchet.go pairs ScanFileClosedPlaneViolations with
// synthetic-source fixtures. The split is what lets the committed fixtures
// assert a validator's VERDICT on a deliberately violating sample instead of only
// asserting that a scan of the current tree finds nothing.
//
// The ALG ratchets that cannot be decided from a single file live in their own
// files, because this package enforces a hard 500-line cap per file:
//
//   - agent_loop_guard_terminal_owner_census.go - the Requirement 9.5
//     terminal-owner census and its seam verification;
//   - agent_loop_guard_strategy_isolation_ratchet.go - the two strategy-isolation
//     reachability walks;
//   - agent_loop_guard_ownership_ast.go - the shared AST primitives all of them
//     judge over, plus the fixture overlay parsers.
//
// Scope vocabulary:
//
//   - algFeatureRootDir is the concrete feature package. Its root files own
//     strategy construction (NewConfiguredProvider) and the proxy-owned
//     completion tool handle.
//   - algCoreDir is generic core. Requirement 12.1 forbids provider-name and
//     feature-name switches there, so the ALG vocabulary is rejected across all
//     of internal/core, not only internal/core/runtime.
//   - algControlToolSDK is the new provider-neutral SDK contract. It must stay
//     usable without importing ALG (Requirement 12.2), which is enforced by
//     forbidding every internal import rather than by enumerating ALG.

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

const (
	RuleALGCoreOwnership              = "alg_core_ownership"
	RuleALGControlToolSDKOwnership    = "alg_control_tool_sdk_ownership"
	RuleALGExclusiveControlToolPlane  = "alg_exclusive_control_tool_plane"
	RuleALGControlToolPlaneExecution  = "alg_control_tool_plane_execution"
	RuleALGClientCallAppend           = "alg_client_call_append"
	RuleALGSecondTerminalOwner        = "alg_second_terminal_owner"
	RuleALGHiddenGuardResurrection    = "alg_hidden_guard_resurrection"
	RuleALGPolicyStoreOwnership       = "alg_policy_store_ownership"
	RuleALGPreferredStrategyIsolation = "alg_preferred_strategy_isolation"
	RuleALGLegacyStrategyIsolation    = "alg_legacy_strategy_isolation"
)

const (
	algModulePath     = "github.com/matdev83/go-llm-interactive-proxy"
	algFeatureRootDir = "internal/plugins/features/agentloopguard"
	algFeatureImport  = algModulePath + "/" + algFeatureRootDir
	algCoreDir        = "internal/core"
	algControlToolSDK = "pkg/lipsdk/controltool"
	algFeaturePlane   = "pkg/lipsdk/feature"

	algLegacyProviderFile    = algFeatureRootDir + "/provider.go"
	algPreferredProviderFile = algFeatureRootDir + "/preferred_provider.go"

	algStrategyConstructor = "NewConfiguredProvider"
	algLegacyReceiver      = "provider"
	algPreferredReceiver   = "preferredProvider"

	// algDecideMethod is the generic terminal-decision receiver method. A type
	// that declares it is a terminal-policy receiver, so its census is the
	// no-second-terminal-owner ratchet.
	algDecideMethod = "Decide"

	// MultExclusive/MultOrdered, CombExclusive, NilReject and NilAllow mirror
	// the pkg/lipsdk/feature spellings so the plane-shape ratchet stays free of
	// generics while still reading the live declaration.
	algMultExclusive = "exclusive"
	algMultOrdered   = "ordered"
	algCombExclusive = "exclusive"
	algNilReject     = "reject"
)

// algForbiddenOwnershipIdents are concrete-feature identities and retired seams
// that must never appear as an identifier in generic core, in the generic
// control-tool SDK contract, or anywhere in the production tree (guardHidden).
var algForbiddenOwnershipIdents = map[string]string{
	"AgentLoopGuard":        "concrete ALG feature type name",
	"agentLoopGuard":        "concrete ALG feature identifier",
	"agentLoopGuardEnabled": "concrete ALG feature flag",
	"LoopGuard":             "retired ALG guard type name",
	"loopGuard":             "retired ALG guard identifier",
	"isLoopGuardEnabled":    "retired ALG guard predicate",
	"algProvider":           "concrete ALG provider field",
	"algProviderID":         "concrete ALG provider identity",
}

// algForbiddenOwnershipLiterals are provider-owned ALG strings that generic core
// and the generic control-tool contract must not embed: the feature identity in
// every spelling core ever carried, the ALG-only recovery overlay identity, the
// proxy-owned completion tool name, and both strategy selectors.
//
// There is no exemption list, no per-site waiver and no bounded-exception
// assertion: a discovered violation is fixed at its source, never allowlisted
// here. A vocabulary with a day-one exception proves nothing about the property
// it claims to enforce, and a per-file waiver is a partial exemption around an
// undetected remainder, which is worse than either.
//
// This ratchet therefore GRADES the current tree. Its one pre-existing
// violation - generic core embedding the ALG plugin identity and the ALG-only
// recovery overlay identity at internal/core/runtime/conversation_view.go - was
// resolved by DELETING that dead branch, not by introducing a generic seam for
// the identity. It was dead in two independent ways: the suppression guard
// spelled the identity "agent_loop_guard" while the only suppressor passes the
// frozen hyphenated "agent-loop-guard", so the guard could never match; and no
// producer ever published an "alg-rec" overlay, so the scan and the Deactivate
// could never fire. Generic core does not need the seam: the ALG feature's
// overlay lifecycle stays feature-owned through the neutral steering contract,
// and the generic continuation overlay has its own feature-neutral owner.
var algForbiddenOwnershipLiterals = map[string]string{
	"agent-loop-guard":   "concrete ALG feature identity",
	"agent_loop_guard":   "separated-underscore spelling of the concrete ALG feature identity",
	"agentloopguard":     "concrete ALG feature package or variable name",
	"alg-rec":            "ALG-only recovery steering overlay identity",
	"attempt_completion": "proxy-owned completion tool name owned by the ALG feature",
	"semantic_verifier":  "ALG legacy strategy selector",
}

// algRetiredHiddenGuardIdent is the removed hidden-content terminal owner. Its
// repository-wide resurrection is refused in production code.
const algRetiredHiddenGuardIdent = "guardHidden"

// algForbiddenClientAppendFields are the canonical client/A-leg call fields the
// feature must never append to directly (Requirement 3.1).
var algForbiddenClientAppendFields = map[string]bool{
	"Messages": true,
	"Items":    true,
}

// algTerminalOwnerReceivers pins the approved terminal-owner receiver census.
// Both approved receivers implement the same generic terminaldecision.Provider
// seam, so neither strategy owns a separate terminal publication path
// (Requirement 9.5). Any additional receiver declaring algDecideMethod is a
// second terminal owner.
var algTerminalOwnerReceivers = map[string]string{
	algLegacyReceiver:    algLegacyProviderFile,
	algPreferredReceiver: algPreferredProviderFile,
}

// algLegacyForbiddenLocalIdents are the feature-local control-tool entry points.
// The legacy strategy must not reach the proxy-owned completion tool through a
// local wrapper either, so a same-package indirection is caught as well as a
// direct SDK import.
var algLegacyForbiddenLocalIdents = map[string]string{
	"NewCompletionToolProvider":  "proxy-owned completion tool construction",
	"completionToolProvider":     "proxy-owned completion tool receiver",
	"completionToolSpec":         "proxy-owned completion tool spec",
	"PlaneControlToolProvider":   "proxy-owned control-tool plane",
	"PlaneControlToolProviderID": "proxy-owned control-tool plane identity",
}

// algFeatureImportAllowlist is the complete permitted non-stdlib import closure
// of the ALG feature root package: its own subpackages plus the canonical and
// SDK contracts it is designed against. Anything else - a composition root, an
// HTTP endpoint, a policy store, a generic runtime seam - would give the
// feature an owner outside its exclusive planes and would violate the boundary
// commitment that the feature stays removable.
//
// pkg/lipsdk/steering is the provider-neutral overlay contract, and it stays in
// the closure so the feature's recovery overlay can remain feature-owned policy
// over a neutral SDK contract instead of a core-owned seam. The feature declares
// no steering import today, and the entry is deliberately not a channel for core
// to learn a feature-owned overlay identity: core no longer knows that identity
// at all, which is exactly what algForbiddenOwnershipLiterals now enforces.
// pkg/lipsdk/auxiliary and pkg/lipsdk/feature are permitted
// because the legacy strategy's bounded verifier request and the feature
// contribution plane are part of the shipped design; both are provider-neutral
// SDK contracts, and the ratchet forbids every other internal import, so the
// feature still cannot reach a composition root, an HTTP endpoint, or a generic
// runtime seam.
var algFeatureImportAllowlist = map[string]bool{
	algFeatureImport + "/causepolicy":              true,
	algFeatureImport + "/progress":                 true,
	algFeatureImport + "/protocolpolicy":           true,
	algFeatureImport + "/protocolstate":            true,
	algFeatureImport + "/verifier":                 true,
	algModulePath + "/pkg/lipapi":                  true,
	algModulePath + "/pkg/lipsdk/auxiliary":        true,
	algModulePath + "/pkg/lipsdk/controltool":      true,
	algModulePath + "/pkg/lipsdk/feature":          true,
	algModulePath + "/pkg/lipsdk/scope":            true,
	algModulePath + "/pkg/lipsdk/session":          true,
	algModulePath + "/pkg/lipsdk/steering":         true,
	algModulePath + "/pkg/lipsdk/terminaldecision": true,
	algModulePath + "/pkg/lipsdk/workspace":        true,
	"gopkg.in/yaml.v3":                             true,
}

// algSourceFile is one parsed production source file participating in the ALG
// ownership ratchets. It is the overlay shape the committed negative fixtures
type algSourceFile struct {
	RelPath string
	AST     *ast.File
	FSet    *token.FileSet
}

// algControlToolPlaneShape is the plain projection of the control-tool plane
// declaration. Reading the live declaration into this shape keeps the
// exclusive-slot ratchet free of the pkg/lipsdk/feature generics while still
// letting a fixture mutate any single field and observe the verdict.
type algControlToolPlaneShape struct {
	ID                string
	Multiplicity      string
	FeatureRule       string
	NilPolicy         string
	HasIdentity       bool
	StandardPlaneUses int
}

// ValidateAlgControlToolExclusivePlane proves that the control-tool plane admits
// at most one occupant, admits it only from the feature source, and refuses a
// typed nil before publication. One finding is reported per violated property so
// a fixture can assert its own specific defect.
func ValidateAlgControlToolExclusivePlane(shape algControlToolPlaneShape) []RuleFinding {
	const path = algFeaturePlane + "/plane_manifest.go"
	var findings []RuleFinding
	add := func(format string, args ...any) {
		findings = append(findings, RuleFinding{
			Rule:   RuleALGExclusiveControlToolPlane,
			Path:   path,
			Detail: fmt.Sprintf(format, args...),
		})
	}
	if strings.TrimSpace(shape.ID) == "" {
		add("control-tool plane must declare a stable plane ID")
	}
	if shape.Multiplicity != algMultExclusive {
		add("control-tool plane multiplicity = %q, want %q: at most one control-tool occupant per generation", shape.Multiplicity, algMultExclusive)
	}
	if shape.FeatureRule != algCombExclusive {
		add("control-tool plane feature combine rule = %q, want %q: a second feature contribution must be refused", shape.FeatureRule, algCombExclusive)
	}
	if shape.NilPolicy != algNilReject {
		add("control-tool plane nil policy = %q, want %q: a typed-nil provider must be refused before publication", shape.NilPolicy, algNilReject)
	}
	if !shape.HasIdentity {
		add("control-tool plane must require a stable identity so an exclusive occupant stays attributable")
	}
	if shape.StandardPlaneUses != 1 {
		add("control-tool plane appears %d times in the standard plane list, want exactly 1", shape.StandardPlaneUses)
	}
	return findings
}

// ScanAgentLoopGuardOwnershipViolations walks the live production tree and
// reports every ALG ownership and strategy-isolation violation.
func ScanAgentLoopGuardOwnershipViolations(repoRoot string) ([]RuleFinding, error) {
	var findings []RuleFinding
	var featureFiles []algSourceFile
	err := WalkProductionGoFiles(repoRoot, func(rel, abs string, src []byte) error {
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		findings = append(findings, ScanFileAgentLoopGuardOwnership(rel, fset, f)...)
		if algFeatureRootFile(rel) {
			featureFiles = append(featureFiles, algSourceFile{RelPath: rel, AST: f, FSet: fset})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	findings = append(findings, ScanAgentLoopGuardFeaturePackage(featureFiles)...)
	return findings, nil
}

// algFeatureRootFile reports whether rel is a production file directly in the
// ALG feature root package, excluding tests and excluding its subpackages.
func algFeatureRootFile(rel string) bool {
	rel = SlashPath(rel)
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	return PackageDirFromRel(rel) == algFeatureRootDir
}

// algFeatureTreePackage reports whether pkg is the ALG feature root package or
// one of its subpackages, which is the whole feature.
func algFeatureTreePackage(pkg string) bool {
	return pkg == algFeatureRootDir || strings.HasPrefix(pkg, algFeatureRootDir+"/")
}

// ScanFileAgentLoopGuardOwnership applies every per-file ALG ratchet. The
// repository walk and the negative fixtures call this same function, so a
// fixture can never prove a different rule than the ratchet runs.
func ScanFileAgentLoopGuardOwnership(relPath string, fset *token.FileSet, f *ast.File) []RuleFinding {
	rel := SlashPath(relPath)
	var findings []RuleFinding
	emit := func(rule string, node ast.Node, format string, args ...any) {
		findings = append(findings, RuleFinding{
			Rule:   rule,
			Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, node)),
			Detail: fmt.Sprintf(format, args...),
		})
	}
	pkg := PackageDirFromRel(rel)

	switch {
	case MatchPathPrefix(pkg, algCoreDir):
		findings = append(findings, scanAlgCoreOwnership(rel, f, fset)...)
	case pkg == algControlToolSDK:
		findings = append(findings, scanAlgControlToolSDKOwnership(rel, f, fset)...)
	case pkg == algFeaturePlane:
		findings = append(findings, scanAlgControlToolPlaneExecution(rel, f, fset)...)
	case algFeatureTreePackage(pkg):
		findings = append(findings, scanAlgFeatureOwnership(rel, f, fset)...)
	}

	// The hidden-content terminal owner is retired repository-wide, so its
	// resurrection is refused in every production package, not only in core.
	ast.Inspect(f, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != algRetiredHiddenGuardIdent {
			return true
		}
		emit(RuleALGHiddenGuardResurrection, ident,
			"resurrected retired hidden-content terminal owner %q; the canonical SDK writer is the only writer seam", algRetiredHiddenGuardIdent)
		return true
	})
	return findings
}

// scanAlgCoreOwnership rejects every concrete ALG import, name, or switch in
// generic core production code (Requirement 12.1, design ratchet 1).
func scanAlgCoreOwnership(rel string, f *ast.File, fset *token.FileSet) []RuleFinding {
	var findings []RuleFinding
	for _, imp := range FileImportPaths(f) {
		if imp == algFeatureImport || strings.HasPrefix(imp, algFeatureImport+"/") {
			findings = append(findings, RuleFinding{
				Rule:   RuleALGCoreOwnership,
				Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, f)),
				Detail: fmt.Sprintf("generic core imports the concrete ALG feature %q; core must stay provider-neutral", imp),
			})
		}
	}
	findings = append(findings, scanAlgOwnershipVocabulary(rel, f, fset, RuleALGCoreOwnership, "generic core")...)
	return findings
}

// scanAlgControlToolSDKOwnership keeps the new generic contract usable without
// importing ALG (Requirement 12.2, design ratchet 2). Rejecting every internal
// import is stronger than enumerating ALG: an SDK package that cannot reach the
// internal tree cannot name the feature.
func scanAlgControlToolSDKOwnership(rel string, f *ast.File, fset *token.FileSet) []RuleFinding {
	var findings []RuleFinding
	for _, spec := range f.Imports {
		if spec.Path == nil {
			continue
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasPrefix(path, algModulePath+"/internal/") {
			continue
		}
		findings = append(findings, RuleFinding{
			Rule:   RuleALGControlToolSDKOwnership,
			Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, spec)),
			Detail: fmt.Sprintf("the generic control-tool SDK contract imports internal package %q; it must stay usable without importing ALG", path),
		})
	}
	findings = append(findings, scanAlgOwnershipVocabulary(rel, f, fset, RuleALGControlToolSDKOwnership, "the generic control-tool SDK contract")...)
	return findings
}

// scanAlgOwnershipVocabulary is the shared identifier and literal rejection used
// by the core and SDK ratchets. It waives nothing: neither an identifier nor a
// literal has an approved carrier.
func scanAlgOwnershipVocabulary(
	rel string,
	f *ast.File,
	fset *token.FileSet,
	rule, owner string,
) []RuleFinding {
	var findings []RuleFinding
	for _, hit := range algIdentifierHits(f, algForbiddenOwnershipIdents) {
		findings = append(findings, RuleFinding{
			Rule:   rule,
			Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, hit.node)),
			Detail: fmt.Sprintf("%s names the concrete ALG seam %q: %s", owner, hit.name, algForbiddenOwnershipIdents[hit.name]),
		})
	}
	for _, hit := range algStringLiteralHits(f, algForbiddenOwnershipLiterals) {
		findings = append(findings, RuleFinding{
			Rule:   rule,
			Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, hit.node)),
			Detail: fmt.Sprintf("%s embeds the concrete ALG literal %q: %s", owner, hit.text, algForbiddenOwnershipLiterals[hit.text]),
		})
	}
	return findings
}

// scanAlgControlToolPlaneExecution keeps the control-tool plane out of the
// request-execution view that the frontend/client tool path consumes
// (Requirement 3.5, design ratchet 3). Reading the plane stays legal only
// through the admitted-attempt transform, which is not part of that view.
func scanAlgControlToolPlaneExecution(rel string, f *ast.File, fset *token.FileSet) []RuleFinding {
	var findings []RuleFinding
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || algReceiverBaseName(fn.Recv) != "RequestExecutionView" || fn.Type.Results == nil {
			continue
		}
		for _, result := range fn.Type.Results.List {
			if algIsControlToolProviderType(result.Type) {
				findings = append(findings, RuleFinding{
					Rule:   RuleALGControlToolPlaneExecution,
					Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, fn)),
					Detail: fmt.Sprintf("RequestExecutionView.%s exposes the proxy-owned control-tool provider to the frontend execution path; the control-tool plane must never be request-executed", fn.Name.Name),
				})
			}
		}
	}
	return findings
}

// scanAlgFeatureOwnership rejects a direct ALG append to the canonical
// client/A-leg call (design ratchet 4) and any import outside the feature's own
// closure, which is what would let the feature acquire a second policy
// endpoint/store or composition authority (design ratchet 6).
//
// It runs on EVERY file of the feature, root package and subpackages alike. The
// closure is a property of the feature, not of one directory: a composition or
// policy import smuggled into agentloopguard/verifier would give the feature a
// second owner exactly as an import in the root package would.
func scanAlgFeatureOwnership(rel string, f *ast.File, fset *token.FileSet) []RuleFinding {
	var findings []RuleFinding
	for _, direct := range algAppendAssignments(f) {
		findings = append(findings, RuleFinding{
			Rule:   RuleALGClientCallAppend,
			Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, direct.node)),
			Detail: fmt.Sprintf("ALG appends directly to the canonical client/A-leg field %q; proxy-owned completion must use the canonical SDK writer", direct.field),
		})
	}
	allowed := algAllowedFeatureImports()
	for _, spec := range f.Imports {
		if spec.Path == nil {
			continue
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || algFeatureImportAllowed(path, allowed) {
			continue
		}
		findings = append(findings, RuleFinding{
			Rule:   RuleALGPolicyStoreOwnership,
			Path:   fmt.Sprintf("%s:%d", rel, algLine(fset, spec)),
			Detail: fmt.Sprintf("ALG feature root imports %q outside its permitted closure %v; a policy endpoint/store or composition dependency would be a second owner", path, allowed),
		})
	}
	return findings
}
