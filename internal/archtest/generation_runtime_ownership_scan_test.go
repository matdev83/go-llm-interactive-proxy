package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	GroupFields              map[string]bool
}

var requiredGenerationRuntimeGroups = []string{
	"execution", "publication", "models", "operations", "ownership",
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
	"registrations": true, "httpAuth": true, "backendIDs": true, "ledger": true,
	"terminalProviders": true, "readiness": true, "catalog": true,
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
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return generationRuntimeOwnershipScanResult{}, err
	}
	out := generationRuntimeOwnershipScanResult{GroupFields: map[string]bool{}}
	rel := slashPath(filename)

	localTypes := map[string]*ast.TypeSpec{}
	typeAliases := map[string]ast.Expr{}
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
			}
		}
	}

	for name, ts := range localTypes {
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
		}
	}
}

func resolveLocalTypeName(expr ast.Expr, localTypes map[string]*ast.TypeSpec, typeAliases map[string]ast.Expr) string {
	name := exprTypeName(expr)
	if alt, ok := typeAliases[name]; ok {
		return exprTypeName(alt)
	}
	if ts, ok := localTypes[name]; ok && ts.Assign.IsValid() {
		return exprTypeName(ts.Type)
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
	merged := generationRuntimeOwnershipScanResult{GroupFields: map[string]bool{}}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
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
		got, scanErr := scanGenerationRuntimeOwnershipSource(slashPath(rel), string(src))
		if scanErr != nil {
			return scanErr
		}
		merged.Findings = append(merged.Findings, got.Findings...)
		merged.GenerationRuntimeCount += got.GenerationRuntimeCount
		if got.GenerationBundleDeclared {
			merged.GenerationBundleDeclared = true
		}
		for k, v := range got.GroupFields {
			if v {
				merged.GroupFields[k] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk runtimebundle: %v", err)
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
	for _, g := range requiredGenerationRuntimeGroups {
		if !merged.GroupFields[g] {
			merged.Findings = append(merged.Findings, generationRuntimeOwnershipFinding{
				Path: "internal/infra/runtimebundle", Kind: "missing_group_field",
				Detail: fmt.Sprintf("GenerationBundle missing cohesive group field %q", g),
			})
		}
	}
	return merged
}
