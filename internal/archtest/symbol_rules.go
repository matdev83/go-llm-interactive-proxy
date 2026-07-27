package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
)

// SymbolKind classifies a package-scope declaration.
type SymbolKind string

const (
	SymbolFunc   SymbolKind = "func"
	SymbolType   SymbolKind = "type"
	SymbolVar    SymbolKind = "var"
	SymbolConst  SymbolKind = "const"
	SymbolMethod SymbolKind = "method" // FuncDecl with receiver; Name is method name
)

// ForbiddenDeclRule forbids a package-scope declaration by exact name.
type ForbiddenDeclRule struct {
	Package string     // repo-relative package dir (e.g. internal/infra/runtimebundle)
	Kind    SymbolKind // declaration kind
	Name    string     // exact symbol name
	Reason  string     // short rationale
}

// RuleFinding is one violation of a permanent architecture rule.
type RuleFinding struct {
	Rule   string
	Path   string
	Detail string
}

func (f RuleFinding) String() string {
	return f.Path + ": " + f.Rule + ": " + f.Detail
}

// ForbiddenDeclarations is the permanent deleted-symbol inventory.
var ForbiddenDeclarations = []ForbiddenDeclRule{
	// Compatibility Built / Build surface
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "Built", Reason: "compatibility Built aggregate deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "Build", Reason: "compatibility runtimebundle.Build deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "requestPlaneAsBuilt", Reason: "canonical-to-legacy rehydration deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "RequestPlane", Reason: "broad RequestPlane aggregate deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "NewCompatRequestPlane", Reason: "RequestPlane compatibility ctor deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "ComposeRequestPlane", Reason: "ComposeRequestPlane deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "standardHTTPInputFromRequestPlane", Reason: "RequestPlane projector deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "CandidateRuntime", Reason: "use package-private candidateAssembly"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "ReloadHost", Reason: "renamed to concrete Host"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolMethod, Name: "LegacyClosers", Reason: "ResourceLedger closer projection deleted"},

	// Dual bootstrap / host attachment
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "BuildBootstrap", Reason: "dual bootstrap path deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "AttachReloadHost", Reason: "host-attachment dual path deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "BootstrapResult", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolType, Name: "BootstrapMode", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolConst, Name: "BootstrapUnspecified", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolConst, Name: "BootstrapServe", Reason: "bootstrap vocabulary deleted"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "LoadBootstrapEffective", Reason: "use LoadBootstrapEffectiveWithSource only"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "NewReloadHost", Reason: "use BuildHost / concrete Host"},
	{Package: "internal/infra/runtimebundle", Kind: SymbolFunc, Name: "bindReloadHost", Reason: "legacy host bind deleted"},

	// Deleted serve surface
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "RunWithRuntime", Reason: "deleted serve API"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "NewStandardHandler", Reason: "deleted Built-era handler"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "releaseBuiltResources", Reason: "deleted Built closer release"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "runClosers", Reason: "deleted closer runner"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "standardHTTPInputFromBuilt", Reason: "deleted Built projector"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "shutdownGenerationHost", Reason: "Host.Close owns shutdown"},
	{Package: "internal/stdhttp", Kind: SymbolFunc, Name: "closeProcessServices", Reason: "Host.Close owns process close"},

	// Deleted lipstd tracing boundary
	{Package: "cmd/lipstd", Kind: SymbolFunc, Name: "tracingDeferred", Reason: "Host.Close owns tracing shutdown"},
	{Package: "cmd/lipstd", Kind: SymbolFunc, Name: "deferHostTracingShutdown", Reason: "Host.Close owns tracing shutdown"},

	// Legacy options quarantine
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "adaptLegacyOptions", Reason: "legacy options adapter deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "prepareCanonicalProduction", Reason: "legacy options helper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "filterDescriptorsByFamily", Reason: "legacy options helper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "descriptorHasFamily", Reason: "legacy options helper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "legacyRequestRegistrations", Reason: "legacy options helper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "legacyAttemptRegistrations", Reason: "legacy options helper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "legacyConcurrencyRegistration", Reason: "legacy options helper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolConst, Name: "legacyProductionRaterID", Reason: "legacy options id deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "mapTriggerIn", Reason: "reload field mapper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "mapTriggerOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "mapCategoryOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "mapResultOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "mapHistoryOut", Reason: "reload field mapper deleted"},
	{Package: "pkg/lipruntime", Kind: SymbolFunc, Name: "mapStatusOut", Reason: "reload field mapper deleted"},

	// Internal reload contract ownership
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "TriggerKind", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "Trigger", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "Result", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "Status", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "HistoryEntry", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ReloadTrigger", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ReloadResult", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ReloadStatus", Reason: "contract owned by pkg/lipsdk/configreload"},
	{Package: "internal/core/configreload", Kind: SymbolType, Name: "ResultCategory", Reason: "contract owned by pkg/lipsdk/configreload"},
}

// AbsentFiles must not exist in the production tree.
var AbsentFiles = []string{
	"pkg/lipruntime/legacy_options.go",
	"pkg/lipruntime/reload_map.go",
	"cmd/lipstd/tracing_shutdown.go",
	"internal/infra/runtimebundle/built.go",
	"internal/infra/runtimebundle/build.go",
}

// ScanForbiddenDeclarations reports package-scope declarations matching ForbiddenDeclarations.
func ScanForbiddenDeclarations(root string) ([]RuleFinding, error) {
	index := make(map[string][]ForbiddenDeclRule)
	for _, r := range ForbiddenDeclarations {
		index[r.Package] = append(index[r.Package], r)
	}
	var out []RuleFinding
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		rules := matchingPackageRules(index, pkg)
		if len(rules) == 0 {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for _, r := range rules {
			if DeclExists(f, r) {
				out = append(out, RuleFinding{
					Rule:   "forbidden_decl",
					Path:   rel,
					Detail: string(r.Kind) + " " + r.Name + " (" + r.Reason + ")",
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func matchingPackageRules(index map[string][]ForbiddenDeclRule, pkg string) []ForbiddenDeclRule {
	var out []ForbiddenDeclRule
	for prefix, rules := range index {
		if MatchPathPrefix(pkg, prefix) {
			out = append(out, rules...)
		}
	}
	return out
}

// DeclExists reports whether rule matches a declaration in f.
func DeclExists(f *ast.File, r ForbiddenDeclRule) bool {
	switch r.Kind {
	case SymbolFunc:
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv == nil && fd.Name != nil && fd.Name.Name == r.Name {
				return true
			}
		}
	case SymbolMethod:
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv != nil && fd.Name != nil && fd.Name.Name == r.Name {
				return true
			}
		}
	case SymbolType:
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && ts.Name != nil && ts.Name.Name == r.Name {
					return true
				}
			}
		}
	case SymbolVar, SymbolConst:
		tok := token.VAR
		if r.Kind == SymbolConst {
			tok = token.CONST
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != tok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if name != nil && name.Name == r.Name {
						return true
					}
				}
			}
		}
	}
	return false
}

// ScanAbsentFiles reports paths from AbsentFiles that still exist.
func ScanAbsentFiles(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	for _, rel := range AbsentFiles {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			out = append(out, RuleFinding{Rule: "absent_file", Path: rel, Detail: "deleted production file must not reappear"})
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}
