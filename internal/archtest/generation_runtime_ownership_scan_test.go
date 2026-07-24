package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ownershipScanFile is one source unit for multi-file package-scoped scans.
type ownershipScanFile struct {
	Path string
	Src  string
}

const runtimebundleProductionPackage = "runtimebundle"

type runtimebundleProductionPackageIndex struct {
	Types    map[string]*ast.TypeSpec
	Aliases  map[string]ast.Expr
	Files    []ownershipScanFile
	Findings []generationRuntimeOwnershipFinding
}

func isRuntimebundleProductionGoPath(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go")
}

// indexRuntimebundleProductionPackage builds type/alias maps exclusively from
// non-test files whose package clause is exactly "runtimebundle". External
// packages and *_test.go files are ignored. Duplicate production type names
// fail closed with a deterministic finding and are removed from the index so
// contested names cannot satisfy canonical resolution.
func indexRuntimebundleProductionPackage(files []ownershipScanFile) (runtimebundleProductionPackageIndex, error) {
	sorted := append([]ownershipScanFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool {
		return slashPath(sorted[i].Path) < slashPath(sorted[j].Path)
	})

	idx := runtimebundleProductionPackageIndex{
		Types:   map[string]*ast.TypeSpec{},
		Aliases: map[string]ast.Expr{},
	}
	declaredAt := map[string]string{}

	for _, file := range sorted {
		rel := slashPath(file.Path)
		if !isRuntimebundleProductionGoPath(rel) {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, file.Src, parser.SkipObjectResolution)
		if err != nil {
			return runtimebundleProductionPackageIndex{}, err
		}
		if f.Name == nil || f.Name.Name != runtimebundleProductionPackage {
			continue
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				name := ts.Name.Name
				if prev, exists := declaredAt[name]; exists {
					paths := []string{prev, rel}
					sort.Strings(paths)
					detail := fmt.Sprintf("duplicate production type %q declared in %s and %s", name, paths[0], paths[1])
					if paths[0] == paths[1] {
						detail = fmt.Sprintf("duplicate production type %q declared multiple times in %s", name, paths[0])
					}
					idx.Findings = append(idx.Findings, generationRuntimeOwnershipFinding{
						Path:   "internal/infra/runtimebundle",
						Kind:   "duplicate_production_type",
						Detail: detail,
					})
					delete(idx.Types, name)
					delete(idx.Aliases, name)
					continue
				}
				declaredAt[name] = rel
				idx.Types[name] = ts
				if ts.Assign.IsValid() {
					idx.Aliases[name] = ts.Type
				}
			}
		}
		idx.Files = append(idx.Files, ownershipScanFile{Path: rel, Src: file.Src})
	}
	return idx, nil
}

// scanGenerationRuntimeOwnershipSources scans a synthetic or live multi-file
// package snapshot with production package isolation. Single-source fixtures
// should keep using scanGenerationRuntimeOwnershipSource.
func scanGenerationRuntimeOwnershipSources(files []ownershipScanFile) (generationRuntimeOwnershipScanResult, error) {
	idx, err := indexRuntimebundleProductionPackage(files)
	if err != nil {
		return generationRuntimeOwnershipScanResult{}, err
	}
	merged := generationRuntimeOwnershipScanResult{GroupFields: map[string]bool{}}
	merged.Findings = append(merged.Findings, idx.Findings...)

	for _, file := range idx.Files {
		got, scanErr := scanGenerationRuntimeOwnershipSourceWithTypes(file.Path, file.Src, idx.Types, idx.Aliases)
		if scanErr != nil {
			return generationRuntimeOwnershipScanResult{}, scanErr
		}
		mergeGenerationRuntimeOwnershipScan(&merged, got)
	}
	finalizeRuntimebundleOwnershipPackageScan(&merged)
	return merged, nil
}

func mergeGenerationRuntimeOwnershipScan(dst *generationRuntimeOwnershipScanResult, src generationRuntimeOwnershipScanResult) {
	dst.Findings = append(dst.Findings, src.Findings...)
	dst.GenerationRuntimeCount += src.GenerationRuntimeCount
	if src.GenerationBundleDeclared {
		dst.GenerationBundleDeclared = true
	}
	if src.HasCanonicalLedger {
		dst.HasCanonicalLedger = true
	}
	for k, v := range src.GroupFields {
		if v {
			dst.GroupFields[k] = true
		}
	}
}

func finalizeRuntimebundleOwnershipPackageScan(merged *generationRuntimeOwnershipScanResult) {
	if merged.GroupFields == nil {
		merged.GroupFields = map[string]bool{}
	}
	if merged.GenerationRuntimeCount != 1 {
		merged.Findings = append(merged.Findings, generationRuntimeOwnershipFinding{
			Path: "internal/infra/runtimebundle", Kind: "generation_runtime_count",
			Detail: fmt.Sprintf("want exactly one GenerationRuntime contract, found %d", merged.GenerationRuntimeCount),
		})
	}
	if !merged.GenerationBundleDeclared {
		merged.Findings = append(merged.Findings, generationRuntimeOwnershipFinding{
			Path: "internal/infra/runtimebundle", Kind: "generation_bundle_missing",
			Detail: "GenerationBundle concrete type missing",
		})
	}
	if !merged.HasCanonicalLedger {
		merged.Findings = append(merged.Findings, generationRuntimeOwnershipFinding{
			Path: "internal/infra/runtimebundle", Kind: "missing_canonical_ledger",
			Detail: "GenerationBundle missing canonical ledger *ResourceLedger field",
		})
	}
	for _, g := range requiredGenerationRuntimeGroups {
		if !merged.GroupFields[g] {
			merged.Findings = append(merged.Findings, generationRuntimeOwnershipFinding{
				Path: "internal/infra/runtimebundle", Kind: "missing_group_field",
				Detail: fmt.Sprintf("GenerationBundle missing cohesive group field %q", g),
			})
		}
	}
}

// generationRuntimeOwnershipFinding is a deterministic, site-unique architecture finding.
type generationRuntimeOwnershipFinding struct {
	Path   string
	Kind   string
	Detail string
}

func (f generationRuntimeOwnershipFinding) String() string {
	return fmt.Sprintf("%s: [%s] %s", f.Path, f.Kind, f.Detail)
}

type generationRuntimeOwnershipScanResult struct {
	Findings                 []generationRuntimeOwnershipFinding
	GenerationRuntimeCount   int
	GenerationBundleDeclared bool
	HasCanonicalLedger       bool
	GroupFields              map[string]bool
}

var requiredGenerationRuntimeGroups = []string{
	"execution", "publication", "models", "operations",
}

var forbiddenGenerationBundleFieldNames = map[string]bool{
	"owner": true, "Owner": true,
	"candidate": true, "Candidate": true, "cand": true,
	"quiesceOnce": true, "closeOnce": true,
	"process": true, "Process": true, "processCloser": true, "ProcessServices": true,
	"cfg": true, "config": true, "Config": true,
	"built": true, "Built": true,
	"app": true, "App": true,
	"requestPlane": true, "RequestPlane": true,
	"deps": true, "dependencies": true, "dependencyMap": true, "services": true,
	"handler": true, "executor": true, "routing": true, "frontends": true,
	"registrations": true, "httpAuth": true, "backendIDs": true,
	"terminalProviders": true, "readiness": true, "catalog": true,
	"ownership": true, // Task 7.2: duplicate lifecycle shell deleted; ledger is direct
}

var forbiddenGenerationRuntimeMethodNames = map[string]bool{
	"Get": true, "Lookup": true, "Resolve": true,
	"GetDependency": true, "LookupDependency": true, "ResolveDependency": true,
	"Dependencies": true, "DependencyMap": true, "Services": true,
}

var broadGenerationDependencyGetters = map[string]bool{
	"GetExecutor": true, "GetStore": true, "GetMetrics": true, "GetLedger": true,
	"GetOwner": true, "GetCandidate": true, "GetProcess": true, "GetBuilt": true,
	"GetRequestPlane": true, "GetApp": true, "GetConfig": true,
	"GetPluginRegistry": true, "GetDatabasePools": true, "GetClosers": true,
}

// Exported Generation/Candidate ownership-transfer or ledger-escape methods are
// forbidden. Package-private transfer (e.g. transferLedgerOwnership) is allowed.
var forbiddenOwnershipEscapeMethods = map[string]bool{
	"TransferLedgerOwnership": true,
	"TransferOwnership":       true,
	"TakeLedger":              true,
	"DetachLedger":            true,
	"ReleaseLedger":           true,
	"StealLedger":             true,
	"GetLedger":               true,
	"Ledger":                  true,
	"ResourceLedger":          true,
	"OwnedLedger":             true,
}

var ownershipBoundaryReceivers = map[string]bool{
	"GenerationBundle":  true,
	"GenerationRuntime": true,
	"CandidateRuntime":  true,
}

func scanGenerationRuntimeOwnershipSource(filename, src string) (generationRuntimeOwnershipScanResult, error) {
	return scanGenerationRuntimeOwnershipSourceWithTypes(filename, src, nil, nil)
}

func scanGenerationRuntimeOwnershipSourceWithTypes(
	filename, src string,
	pkgTypes map[string]*ast.TypeSpec,
	pkgAliases map[string]ast.Expr,
) (generationRuntimeOwnershipScanResult, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return generationRuntimeOwnershipScanResult{}, err
	}
	out := generationRuntimeOwnershipScanResult{GroupFields: map[string]bool{}}
	rel := slashPath(filename)

	localTypes := map[string]*ast.TypeSpec{}
	typeAliases := map[string]ast.Expr{}
	// Seed with package-scope maps when provided (production walk). File-local
	// declarations override so single-file synthetic fixtures stay isolated.
	for k, v := range pkgTypes {
		localTypes[k] = v
	}
	for k, v := range pkgAliases {
		typeAliases[k] = v
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				continue
			}
			localTypes[ts.Name.Name] = ts
			if ts.Assign.IsValid() {
				typeAliases[ts.Name.Name] = ts.Type
			} else {
				delete(typeAliases, ts.Name.Name)
			}
		}
	}

	for name, ts := range localTypes {
		// Only inspect types declared in this file to avoid cross-file
		// GenerationBundle re-scans when using package-scope maps.
		if !typeDeclInFile(f, name) {
			continue
		}
		switch name {
		case "GenerationRuntime":
			out.GenerationRuntimeCount++
			if _, ok := ts.Type.(*ast.InterfaceType); !ok {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "generation_runtime_not_interface",
					Detail: "GenerationRuntime must be an interface contract",
				})
			} else {
				inspectGenerationRuntimeInterface(rel, ts.Type.(*ast.InterfaceType), &out)
			}
		case "generationOwner":
			out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
				Path: rel, Kind: "generation_owner_delegate",
				Detail: "generationOwner lifecycle delegate is forbidden on the canonical generation path",
			})
		case "GenerationBundle":
			out.GenerationBundleDeclared = true
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "generation_bundle_not_struct",
					Detail: "GenerationBundle must be a struct",
				})
				continue
			}
			inspectGenerationBundleStruct(rel, st, localTypes, typeAliases, &out)
		}
	}

	// Methods on GenerationBundle / GenerationRuntime / CandidateRuntime receivers.
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		recv := receiverTypeName(fd.Recv.List[0].Type)
		if !ownershipBoundaryReceivers[recv] {
			continue
		}
		name := fd.Name.Name
		if recv == "GenerationBundle" || recv == "GenerationRuntime" {
			if forbiddenGenerationRuntimeMethodNames[name] || broadGenerationDependencyGetters[name] {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "generic_dependency_lookup",
					Detail: fmt.Sprintf("%s.%s is a forbidden generic/broad dependency lookup", recv, name),
				})
			}
		}
		if ast.IsExported(name) && (forbiddenOwnershipEscapeMethods[name] || methodReturnsResourceLedger(fd)) {
			out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
				Path: rel, Kind: "exported_ownership_transfer",
				Detail: fmt.Sprintf("%s.%s exports ownership transfer or ledger escape", recv, name),
			})
		}
	}

	return out, nil
}

func typeDeclInFile(f *ast.File, name string) bool {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if ok && ts.Name != nil && ts.Name.Name == name {
				return true
			}
		}
	}
	return false
}

func methodReturnsResourceLedger(fd *ast.FuncDecl) bool {
	if fd.Type == nil || fd.Type.Results == nil {
		return false
	}
	for _, field := range fd.Type.Results.List {
		if exprTypeName(field.Type) == "ResourceLedger" {
			return true
		}
	}
	return false
}

func inspectGenerationRuntimeInterface(rel string, it *ast.InterfaceType, out *generationRuntimeOwnershipScanResult) {
	if it.Methods == nil {
		return
	}
	for _, field := range it.Methods.List {
		if len(field.Names) == 0 {
			// Embedded interface — allowed (PublishedRequestPlane, etc.).
			continue
		}
		for _, n := range field.Names {
			if forbiddenGenerationRuntimeMethodNames[n.Name] || broadGenerationDependencyGetters[n.Name] {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "generic_dependency_lookup",
					Detail: fmt.Sprintf("GenerationRuntime.%s is a forbidden generic/broad dependency lookup", n.Name),
				})
			}
		}
	}
}

func inspectGenerationBundleStruct(
	rel string,
	st *ast.StructType,
	localTypes map[string]*ast.TypeSpec,
	typeAliases map[string]ast.Expr,
	out *generationRuntimeOwnershipScanResult,
) {
	if st.Fields == nil {
		return
	}
	var canonicalLedgerFields []string
	for _, field := range st.Fields.List {
		names := field.Names
		if len(names) == 0 {
			// Embedded field.
			typeName := exprTypeName(field.Type)
			if typeName == "CandidateRuntime" || typeName == "generationOwner" {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "candidate_owner_field",
					Detail: fmt.Sprintf("embedded %s is forbidden on GenerationBundle", typeName),
				})
			}
			if isCanonicalResourceLedgerType(field.Type, localTypes, typeAliases) ||
				typeName == "ResourceLedger" ||
				typeContainsResourceLedger(field.Type, localTypes, typeAliases, map[string]bool{}) {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "nested_ledger_owner",
					Detail: fmt.Sprintf("embedded %s is forbidden on GenerationBundle; canonical owner is a single ledger *ResourceLedger field", typeName),
				})
			}
			continue
		}
		for _, n := range names {
			typeName := resolveLocalTypeName(field.Type, localTypes, typeAliases)
			if forbiddenGenerationBundleFieldNames[n.Name] {
				kind := "forbidden_flat_or_owner_field"
				switch n.Name {
				case "owner", "Owner", "candidate", "Candidate", "cand":
					kind = "candidate_owner_field"
				case "quiesceOnce", "closeOnce":
					kind = "dual_once_lifecycle"
				case "cfg", "config", "Config", "built", "Built", "app", "App",
					"requestPlane", "RequestPlane", "process", "Process",
					"processCloser", "ProcessServices":
					kind = "mutable_or_process_owner_field"
				case "deps", "dependencies", "dependencyMap", "services":
					kind = "generic_dependency_map"
				}
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: kind,
					Detail: fmt.Sprintf("GenerationBundle.%s (%s) is forbidden", n.Name, typeName),
				})
			}
			for _, g := range requiredGenerationRuntimeGroups {
				if n.Name != g {
					continue
				}
				// Cohesive groups must be private struct values, not flat
				// dependency pointers that happen to reuse the group name.
				if _, ok := field.Type.(*ast.StructType); ok {
					out.GroupFields[g] = true
					continue
				}
				if id, ok := field.Type.(*ast.Ident); ok {
					if ts, exists := localTypes[id.Name]; exists {
						if _, isStruct := ts.Type.(*ast.StructType); isStruct {
							out.GroupFields[g] = true
						}
					}
				}
			}
			if typeName == "CandidateRuntime" || strings.Contains(typeName, "CandidateRuntime") {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "candidate_owner_field",
					Detail: fmt.Sprintf("GenerationBundle.%s retains CandidateRuntime (%s)", n.Name, typeName),
				})
			}
			if typeName == "generationOwner" {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "generation_owner_delegate",
					Detail: fmt.Sprintf("GenerationBundle.%s retains generationOwner delegate", n.Name),
				})
			}

			canonicalPtr := isCanonicalResourceLedgerPtr(field.Type, localTypes, typeAliases)
			nestedLedger := !canonicalPtr && typeContainsResourceLedger(field.Type, localTypes, typeAliases, map[string]bool{})
			looksLikeLedgerName := n.Name == "ledger" || strings.Contains(strings.ToLower(n.Name), "ledger")
			if canonicalPtr {
				canonicalLedgerFields = append(canonicalLedgerFields, n.Name)
				if n.Name != "ledger" {
					out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
						Path: rel, Kind: "non_canonical_ledger",
						Detail: fmt.Sprintf("GenerationBundle.%s is *ResourceLedger but canonical field name must be ledger", n.Name),
					})
				}
			} else if nestedLedger {
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "nested_ledger_owner",
					Detail: fmt.Sprintf("GenerationBundle.%s nests ResourceLedger ownership (%s)", n.Name, typeName),
				})
			} else if looksLikeLedgerName || strings.HasSuffix(typeName, "ResourceLedger") {
				// Named like a ledger or suffix-colliding type that is not the
				// package ResourceLedger pointer (Fake/Alternate/interface/etc).
				out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
					Path: rel, Kind: "non_canonical_ledger",
					Detail: fmt.Sprintf("GenerationBundle.%s (%s) is not canonical *ResourceLedger", n.Name, typeName),
				})
			}
		}
	}
	switch {
	case len(canonicalLedgerFields) == 1 && canonicalLedgerFields[0] == "ledger":
		out.HasCanonicalLedger = true
	case len(canonicalLedgerFields) > 1:
		out.Findings = append(out.Findings, generationRuntimeOwnershipFinding{
			Path: rel, Kind: "duplicate_canonical_ledger",
			Detail: fmt.Sprintf("GenerationBundle has %d direct *ResourceLedger fields %v; want exactly one ledger field", len(canonicalLedgerFields), canonicalLedgerFields),
		})
	}
}

// isCanonicalResourceLedgerPtr reports whether expr resolves (via package-scope
// aliases / defined aliases) to exactly *ResourceLedger. Cycle-safe. Bare
// ResourceLedger, FakeResourceLedger, interfaces, and unrelated pointers are false.
func isCanonicalResourceLedgerPtr(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr) bool {
	return resolveCanonicalResourceLedgerPtr(expr, localTypes, typeAliases, map[string]bool{})
}

func isCanonicalResourceLedgerType(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr) bool {
	seen := map[string]bool{}
	cur := expr
	for cur != nil {
		switch t := cur.(type) {
		case *ast.StarExpr:
			return resolveCanonicalResourceLedgerNamed(t.X, localTypes, typeAliases, seen)
		case *ast.Ident:
			if t.Name == "ResourceLedger" {
				return true
			}
			if seen[t.Name] {
				return false
			}
			seen[t.Name] = true
			if alt, ok := typeAliases[t.Name]; ok {
				cur = alt
				continue
			}
			if ts, ok := localTypes[t.Name]; ok && ts.Assign.IsValid() {
				cur = ts.Type
				continue
			}
			return false
		default:
			return false
		}
	}
	return false
}

func resolveCanonicalResourceLedgerPtr(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr, seen map[string]bool) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return resolveCanonicalResourceLedgerNamed(t.X, localTypes, typeAliases, seen)
	case *ast.Ident:
		if seen[t.Name] {
			return false
		}
		seen[t.Name] = true
		if alt, ok := typeAliases[t.Name]; ok {
			return resolveCanonicalResourceLedgerPtr(alt, localTypes, typeAliases, seen)
		}
		if ts, ok := localTypes[t.Name]; ok && ts.Assign.IsValid() {
			return resolveCanonicalResourceLedgerPtr(ts.Type, localTypes, typeAliases, seen)
		}
		return false
	default:
		return false
	}
}

func resolveCanonicalResourceLedgerNamed(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr, seen map[string]bool) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		if t.Name == "ResourceLedger" {
			return true
		}
		if seen[t.Name] {
			return false
		}
		seen[t.Name] = true
		if alt, ok := typeAliases[t.Name]; ok {
			return resolveCanonicalResourceLedgerNamed(alt, localTypes, typeAliases, seen)
		}
		if ts, ok := localTypes[t.Name]; ok && ts.Assign.IsValid() {
			return resolveCanonicalResourceLedgerNamed(ts.Type, localTypes, typeAliases, seen)
		}
		return false
	case *ast.SelectorExpr:
		// pkg.ResourceLedger is not the package-local canonical type.
		return false
	default:
		return false
	}
}

// typeContainsResourceLedger reports nested/embedded ResourceLedger ownership
// inside named local structs (wrappers/shells). Direct canonical *ResourceLedger
// pointers are handled separately and should not call this with the direct field.
func typeContainsResourceLedger(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr, seen map[string]bool) bool {
	if expr == nil {
		return false
	}
	if isCanonicalResourceLedgerPtr(expr, localTypes, typeAliases) || isCanonicalResourceLedgerType(expr, localTypes, typeAliases) {
		return true
	}
	switch t := expr.(type) {
	case *ast.StarExpr:
		return typeContainsResourceLedger(t.X, localTypes, typeAliases, seen)
	case *ast.Ident:
		if t.Name == "ResourceLedger" {
			return true
		}
		if seen[t.Name] {
			return false
		}
		seen[t.Name] = true
		if alt, ok := typeAliases[t.Name]; ok {
			return typeContainsResourceLedger(alt, localTypes, typeAliases, seen)
		}
		ts, ok := localTypes[t.Name]
		if !ok {
			return false
		}
		if ts.Assign.IsValid() {
			return typeContainsResourceLedger(ts.Type, localTypes, typeAliases, seen)
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			// Interfaces / defined non-struct types that merely suffix-match are
			// not nested ledger owners by containment.
			return false
		}
		for _, field := range st.Fields.List {
			if typeContainsResourceLedger(field.Type, localTypes, typeAliases, seen) {
				return true
			}
		}
		return false
	case *ast.StructType:
		if t.Fields == nil {
			return false
		}
		for _, field := range t.Fields.List {
			if typeContainsResourceLedger(field.Type, localTypes, typeAliases, seen) {
				return true
			}
		}
		return false
	case *ast.ArrayType:
		return typeContainsResourceLedger(t.Elt, localTypes, typeAliases, seen)
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == "ResourceLedger"
	default:
		return false
	}
}

func resolveLocalTypeName(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr) string {
	return resolveLocalTypeNameCycle(expr, localTypes, typeAliases, map[string]bool{})
}

func resolveLocalTypeNameCycle(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr, seen map[string]bool) string {
	name := exprTypeName(expr)
	if name == "" {
		return name
	}
	if seen[name] {
		return name
	}
	seen[name] = true
	if alt, ok := typeAliases[name]; ok {
		return resolveLocalTypeNameCycle(alt, localTypes, typeAliases, seen)
	}
	if ts, ok := localTypes[name]; ok && ts.Assign.IsValid() {
		return resolveLocalTypeNameCycle(ts.Type, localTypes, typeAliases, seen)
	}
	return name
}

func exprTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return exprTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return exprTypeName(t.X)
	case *ast.ArrayType:
		return "[]" + exprTypeName(t.Elt)
	case *ast.MapType:
		return "map"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

func scanRuntimebundleGenerationOwnership(t *testing.T) generationRuntimeOwnershipScanResult {
	t.Helper()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimebundle")
	var files []ownershipScanFile

	// Collect every .go file (including _test.go and any non-runtimebundle
	// package clause). Package isolation + production filtering happens in
	// indexRuntimebundleProductionPackage so test/external declarations cannot
	// influence production type/alias resolution.
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		files = append(files, ownershipScanFile{Path: slashPath(rel), Src: string(src)})
		return nil
	})
	if err != nil {
		t.Fatalf("walk runtimebundle: %v", err)
	}

	got, scanErr := scanGenerationRuntimeOwnershipSources(files)
	if scanErr != nil {
		t.Fatalf("scan runtimebundle ownership: %v", scanErr)
	}
	return got
}
