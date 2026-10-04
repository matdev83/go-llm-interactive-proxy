package archtest

// Task 10.3 consumer-neutrality architecture guards.
//
// Requirement 1.8 says existing coding-oriented features must "retain their
// existing behavior until their own implementation explicitly opts into
// classification gating", requirement 1.6 says a consumer must be able to decide
// from the immutable snapshot alone, and requirement 1.7 says classification is
// advisory derived metadata that must not by itself authorize a tool, grant a
// permission, establish identity, alter billing entitlement, or override routing
// authority.
//
// Those three requirements share ONE load-bearing structural fact: outside the
// sanctioned classification plumbing, no production code can even SEE the
// classification. This file pins that fact with a type-aware census rather than a
// name grep:
//
//   - a bare `.Classification` selector is not evidence of anything.
//     internal/plugins/features/reasoningpreservation has its own unrelated
//     `Classification` field on candidate structs, so a token scan both
//     over-reports and can be defeated by renaming. This census resolves each
//     selector's receiver type through go/types, so `reasoningpreservation`
//     candidate fields are correctly invisible while `SessionView.Classification`
//     is caught through a pointer, an alias, or a package-qualified reference.
//
// The census is repo-wide over internal/, pkg/ and cmd/. Task 6.2's
// TestSessionClassificationFieldHasNoExistingConsumer deliberately covered only
// runtime/execctx/pkg-lipsdk and deferred the repo-wide sweep; this file is that
// sweep, and its allowlist is CLOSED: a new reference outside the enumerated
// roles is a failure, not a new baseline.

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	// sessionSDKPackage is the canonical import path of the session view
	// contract that carries the Classification field.
	sessionSDKPackage = archTestModulePath + "/pkg/lipsdk/session"
)

// sessionClassificationPackages are the packages that own session
// classification. Any production use of an object declared in one of them is a
// classification reference, whatever the identifier is called: importing the
// SDK classifier contract is what would let a second consumer obtain its own
// decision for the same turn (requirement 1.6).
var sessionClassificationPackages = []string{
	archTestModulePath + "/pkg/lipsdk/sessionclassification",
	archTestModulePath + "/internal/plugins/features/sessionclassification",
	archTestModulePath + "/internal/standardplugins/featurehost/sessionclassification",
}

// sessionClassificationSessionSurface is the session-contract surface that
// exists only for classification. It is deliberately narrow: generic session
// names such as Kind or Validate are excluded because a reader of this table
// would have to keep it honest, and the SessionView field selector plus the
// IsCodingAgent method are what a real gate needs.
var sessionClassificationSessionSurface = []string{
	"Classification",
	"ClassificationSource",
	"ConfidenceBand",
	"ConfidenceHigh",
	"ConfidenceUnknown",
	"EvidenceCode",
	"IsCodingAgent",
	"KindCodingAgent",
	"KindUnknown",
	"MaxEvidenceCodeBytes",
	"SourceLocalIdentity",
	"SourceLocalTooling",
	"SourceRemote",
}

// sessionClassificationReference is one production file that can observe
// session classification, with the coarse reason kinds found in it.
type sessionClassificationReference struct {
	// File is the repo-relative slash path of the production file.
	File string
	// Reasons are sorted reason kinds: "contract-package" (uses an object from
	// a classification package), "session-surface" (uses a classification-only
	// session-contract name), "session-view-field" (selects
	// SessionView.Classification on a receiver whose own type IS SessionView) and
	// "classification-predicate" (calls IsCodingAgent/Validate on a
	// Classification). A read PROMOTED through an embedded session.SessionView is
	// reported as "session-view-field" only when the receiver resolves to
	// SessionView; receiverIsSessionView rejects a promoted receiver, so such a
	// read is reported as "session-surface" instead. Either way the file is
	// reported - only the reason kind differs.
	Reasons []string
}

// sessionClassificationRoles is the CLOSED allowlist of sanctioned
// classification reference sites. Every entry is plumbing that either defines the
// contract, publishes the plane, carries bounded evidence, or projects the one
// immutable snapshot onto later consumers. None of them is a consumer: a
// consumer is a feature that decides something FROM the snapshot, and no role
// here does that.
var sessionClassificationRoles = []struct {
	Role   string
	Dirs   []string
	Files  []string
	Reason string
}{
	{
		Role:   "sdk-contract",
		Dirs:   []string{"pkg/lipsdk/sessionclassification"},
		Files:  []string{"pkg/lipsdk/session/classification.go", "pkg/lipsdk/session/view.go"},
		Reason: "the bounded snapshot and the Classifier contract themselves",
	},
	{
		Role:   "sdk-plane-registry",
		Files:  []string{"pkg/lipsdk/feature/plane_manifest.go", "pkg/lipsdk/feature/plane_generated.go"},
		Reason: "the exclusive classifier plane declaration and its generated registry",
	},
	{
		Role: "generic-core-stage",
		Files: []string{
			"internal/core/extensions/session_classification.go",
			"internal/core/extensions/snapshot.go",
			"internal/core/runtime/executor_session_classification.go",
			"internal/core/runtime/executor_session_classification_wire.go",
			"internal/core/runtime/executor_prepare_secure.go",
			"internal/core/runtime/executor_attempt_transform.go",
			"internal/core/execctx/submit_views.go",
		},
		Reason: "the two sanctioned stages plus the view-copy helpers that project one immutable snapshot",
	},
	{
		Role:   "wire-evidence-carrier",
		Dirs:   []string{"internal/core/largebody"},
		Reason: "bounded classification evidence on the large-payload proof carrier",
	},
	{
		Role:   "frontend-evidence-compiler",
		Files:  []string{"internal/plugins/frontends/frontendpipe/classification_evidence.go"},
		Reason: "the shared identity/tool-category compiler certified frontend profiles feed the stages",
	},
	{
		Role:   "feature-classifier-implementation",
		Dirs:   []string{"internal/plugins/features/sessionclassification"},
		Reason: "the feature that owns heuristic policy, state and remote flow",
	},
	{
		Role: "standard-featurehost-binding",
		Dirs: []string{"internal/standardplugins/featurehost/sessionclassification"},
		Files: []string{
			"internal/standardplugins/featurehost/process.go",
			"internal/standardplugins/featurehost/runtime.go",
			"internal/standardplugins/featurehost/sessionclassification_generation.go",
			"internal/standardplugins/featurehost/sessionclassification_lifecycle.go",
		},
		Reason: "process ownership, generation binding and the durable store/coordinator/remote adapter",
	},
	{
		Role:   "standard-feature-registration",
		Files:  []string{"internal/standardplugins/features_install.go", "internal/standardplugins/standard_table.go"},
		Reason: "the registration entry that names the feature id and binds its config factory",
	},
}

// sessionClassificationAuthorityDirs are the packages that own the authorities
// requirement 1.7 forbids classification from touching. The census asserts each
// of them was scanned and that none of them references classification, which is
// the direct statement "classification cannot authorize a tool, grant a
// permission, establish identity, alter billing entitlement, or override routing
// authority" - not by naming those verbs in a comment, but because the code that
// owns them has no way to read the fact.
var sessionClassificationAuthorityDirs = []string{
	"internal/core/accessmode",
	"internal/core/accounting",
	"internal/core/authorityattribution",
	"internal/core/auth",
	"internal/core/billing",
	"internal/core/identity",
	"internal/core/metering",
	"internal/core/policy",
	"internal/core/routeoverride",
	"internal/core/routing",
	"internal/core/safety",
	"internal/core/securesession",
	"internal/core/terminal",
	"internal/core/tokenaccounting",
	"internal/core/usageauthority",
	"pkg/lipsdk/auth",
	"pkg/lipsdk/billing",
	"pkg/lipsdk/terminaldecision",
	"pkg/lipsdk/toolpolicy",
	"pkg/lipsdk/usage",
}

// classificationPackageSet is the lookup form of
// sessionClassificationPackages, rebuilt per sweep so a caller cannot mutate the
// shared table.
func classificationPackageSet() map[string]bool {
	set := make(map[string]bool, len(sessionClassificationPackages))
	for _, path := range sessionClassificationPackages {
		set[path] = true
	}
	return set
}

// classificationSessionSurfaceSet is the lookup form of
// sessionClassificationSessionSurface.
func classificationSessionSurfaceSet() map[string]bool {
	set := make(map[string]bool, len(sessionClassificationSessionSurface))
	for _, name := range sessionClassificationSessionSurface {
		set[name] = true
	}
	return set
}

// sessionClassificationSweep is one repo-wide type-aware pass over every
// production package in internal/, pkg/ and cmd/.
type sessionClassificationSweep struct {
	// References are the production files that can observe classification,
	// sorted by path.
	References []sessionClassificationReference
	// Scanned counts every production file the pass type-checked, keyed by
	// repo-relative package directory. It exists so "the guard scanned nothing"
	// is a loud failure rather than a silent PASS.
	Scanned map[string]int
}

func (s sessionClassificationSweep) filesIn(dir string) []string {
	var out []string
	for _, ref := range s.References {
		if PackageDirFromRel(ref.File) == dir {
			out = append(out, ref.File)
		}
	}
	return out
}

// RESIDUAL LIMITATION, stated honestly: this sweep resolves types, so it sees
// direct field reads, pointer reads, aliases, promoted reads through an embedded
// session.SessionView, Classification as a parameter type, KindCodingAgent, and
// the IsCodingAgent/Validate predicates - verified by the detector controls. It
// does NOT see a string-keyed reflection read such as
// reflect.ValueOf(view).FieldByName("Classification"), which names no field and
// no package. No shipped code does this; a reviewer reading a reflection-heavy
// file under a non-allowlisted directory must check it by hand.

// sessionClassificationRoleOf reports the sanctioned role of one production file.
//
// A directory role matches the directory's OWN files only, never a subdirectory.
// A prefix match would silently sanction any new subpackage under a
// classifier-owned directory - for example
// internal/standardplugins/featurehost/sessionclassification/billing - which is
// exactly where a classification-reading authorization or billing path would be
// most tempting to add, and exactly what the advisory-only sweep (keyed on its
// own authority directory list) would not cover. A new subpackage must be
// enumerated in Dirs deliberately or it earns no role at all.
func sessionClassificationRoleOf(file string) []string {
	var roles []string
	for _, role := range sessionClassificationRoles {
		matched := slices.Contains(role.Files, file)
		if !matched {
			for _, dir := range role.Dirs {
				if strings.HasPrefix(file, dir+"/") &&
					!strings.Contains(strings.TrimPrefix(file, dir+"/"), "/") {
					matched = true
					break
				}
			}
		}
		if matched {
			roles = append(roles, role.Role)
		}
	}
	return roles
}

// sessionClassificationSweepOnce memoizes the repo-wide pass. The three
// certification tests that consume it would otherwise type-check every production
// package three times, which costs about nine seconds of the archtest package for
// one result, and the tree cannot change inside one test binary run. A pass that
// reported nothing is still memoized on purpose: each consumer runs its own
// coverage census, so an empty or partial sweep fails all three instead of being
// silently trusted by two of them.
var sessionClassificationSweepOnce struct {
	once  sync.Once
	sweep sessionClassificationSweep
}

// sharedSessionClassificationSweep returns the single repo-wide pass.
func sharedSessionClassificationSweep(t *testing.T) sessionClassificationSweep {
	t.Helper()
	sessionClassificationSweepOnce.once.Do(func() {
		sessionClassificationSweepOnce.sweep = sweepSessionClassificationReferences(t)
	})
	return sessionClassificationSweepOnce.sweep
}

// sweepSessionClassificationReferences resolves every production selector and
// identifier through go/types and reports the files that can observe session
// classification. Type resolution is what makes this guard honest: an unrelated
// `Classification` field on some other struct is invisible, while a
// SessionView.Classification reached through an alias, a pointer, or a struct
// that embeds a session view is caught.
func sweepSessionClassificationReferences(t *testing.T) sessionClassificationSweep {
	t.Helper()
	root := repoRoot(t)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports,
		Dir: root,
	}
	loaded, err := packages.Load(cfg, "./internal/...", "./pkg/...", "./cmd/...")
	if err != nil {
		t.Fatalf("load production packages: %v", err)
	}
	if len(loaded) == 0 {
		t.Fatal("no production package was loaded; the neutrality guard would scan nothing and pass")
	}

	classificationPkgs := classificationPackageSet()
	sessionSurface := classificationSessionSurfaceSet()

	var viewType *types.Named
	for _, p := range loaded {
		if p.PkgPath != sessionSDKPackage || p.Types == nil {
			continue
		}
		if obj := p.Types.Scope().Lookup("SessionView"); obj != nil {
			if named, ok := obj.Type().(*types.Named); ok {
				viewType = named
			}
		}
	}
	if viewType == nil {
		t.Fatalf("%s has no SessionView type; the field selector cannot be resolved", sessionSDKPackage)
	}

	sweep := sessionClassificationSweep{Scanned: map[string]int{}}
	for _, p := range loaded {
		if p.Types == nil || p.TypesInfo == nil {
			t.Errorf("package %s produced no type information; its files are unscanned", p.PkgPath)
			continue
		}
		if len(p.Errors) > 0 {
			t.Errorf("package %s failed to type-check (%v); its files are unscanned, so a reference there "+
				"would be invisible to the neutrality guard", p.PkgPath, p.Errors[0])
			continue
		}
		for _, file := range p.Syntax {
			position := p.Fset.Position(file.Pos())
			rel, err := filepath.Rel(root, position.Filename)
			if err != nil {
				t.Fatalf("relativize %s: %v", position.Filename, err)
			}
			rel = SlashPath(rel)
			sweep.Scanned[PackageDirFromRel(rel)]++
			reasons := sessionClassificationReasonsIn(p, file, classificationPkgs, sessionSurface, viewType)
			if len(reasons) == 0 {
				continue
			}
			sort.Strings(reasons)
			sweep.References = append(sweep.References, sessionClassificationReference{File: rel, Reasons: reasons})
		}
	}
	sort.Slice(sweep.References, func(i, j int) bool {
		return sweep.References[i].File < sweep.References[j].File
	})
	return sweep
}

func sessionClassificationReasonsIn(
	p *packages.Package,
	file *ast.File,
	classificationPkgs map[string]bool,
	sessionSurface map[string]bool,
	viewType *types.Named,
) []string {
	reasons := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			obj := p.TypesInfo.Uses[typed]
			if obj == nil || obj.Pkg() == nil {
				return true
			}
			switch {
			case classificationPkgs[obj.Pkg().Path()]:
				reasons["contract-package"] = true
			case obj.Pkg().Path() == sessionSDKPackage && sessionSurface[obj.Name()]:
				reasons["session-surface"] = true
			}
		case *ast.SelectorExpr:
			selection := p.TypesInfo.Selections[typed]
			if selection == nil || selection.Obj() == nil || selection.Obj().Pkg() == nil {
				return true
			}
			obj := selection.Obj()
			if classificationPkgs[obj.Pkg().Path()] {
				reasons["contract-package"] = true
			}
			if obj.Pkg().Path() != sessionSDKPackage {
				return true
			}
			if obj.Name() == "Classification" && receiverIsSessionView(selection.Recv(), viewType) {
				reasons["session-view-field"] = true
			}
			if obj.Name() == "IsCodingAgent" || obj.Name() == "Validate" {
				reasons["classification-predicate"] = true
			}
		}
		return true
	})
	return sortedKeys(reasons)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// receiverIsSessionView reports whether a resolved selector receiver is the
// canonical session view, through a pointer, an alias, or a named type whose
// underlying type is the view. Anything else - including reasoningpreservation's
// unrelated candidate Classification field - is deliberately not a match.
func receiverIsSessionView(recv types.Type, viewType *types.Named) bool {
	if recv == nil {
		return false
	}
	if pointer, ok := recv.(*types.Pointer); ok {
		recv = pointer.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj() == viewType.Obj()
}
