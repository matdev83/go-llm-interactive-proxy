package archtest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MigrationWave represents the progressive wave boundary for plane consolidation.
type MigrationWave int

const (
	Wave0_Baseline           MigrationWave = 0
	Wave1_HookBus            MigrationWave = 1
	Wave2_Observers          MigrationWave = 2
	Wave3_RequestShaping     MigrationWave = 3
	Wave4_Tools              MigrationWave = 4
	Wave5a_GuardsCompaction  MigrationWave = 5
	Wave5b_LocalTurnTerminal MigrationWave = 6
	Wave5c_Residual          MigrationWave = 7
)

func (w MigrationWave) String() string {
	switch w {
	case Wave0_Baseline:
		return "W0_Baseline"
	case Wave1_HookBus:
		return "W1_HookBus"
	case Wave2_Observers:
		return "W2_Observers"
	case Wave3_RequestShaping:
		return "W3_RequestShaping"
	case Wave4_Tools:
		return "W4_Tools"
	case Wave5a_GuardsCompaction:
		return "W5a_GuardsCompaction"
	case Wave5b_LocalTurnTerminal:
		return "W5b_LocalTurnTerminal"
	case Wave5c_Residual:
		return "W5c_Residual"
	default:
		return fmt.Sprintf("Wave(%d)", int(w))
	}
}

// ActiveMigrationWave defines the currently active migration wave ratchet.
// As migration waves complete, advance this constant to lock in forbidden mirror rules.
const ActiveMigrationWave = Wave5b_LocalTurnTerminal

// MirrorShapeKind classifies the forbidden hand-authored mirror pattern.
type MirrorShapeKind string

const (
	MirrorFeatureBundleField          MirrorShapeKind = "FeatureBundleField"
	MirrorMergedSurfaceField          MirrorShapeKind = "MergedFeatureSurfaceField"
	MirrorAppendBranch                MirrorShapeKind = "AppendBranch"
	MirrorProjectionBranch            MirrorShapeKind = "ProjectionBranch"
	MirrorExtensionsOptionsField      MirrorShapeKind = "ExtensionsOptionsField"
	MirrorGenerationOpField           MirrorShapeKind = "GenerationOperationField"
	MirrorRequestRuntimeSnapshotField MirrorShapeKind = "RequestRuntimeSnapshotField"
	MirrorDiagArm                     MirrorShapeKind = "DiagnosticsArm"
	MirrorStageConsumer               MirrorShapeKind = "StageConsumer"
)

// MirrorFinding is one violation where a forbidden mirror exists past its wave.
type MirrorFinding struct {
	File       string
	Line       int
	ShapeKind  MirrorShapeKind
	PlaneID    string
	Identifier string
	Detail     string
	Wave       MigrationWave
}

func (f MirrorFinding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s (%s, wave %s): %s",
		f.File, f.Line, f.ShapeKind, f.Identifier, f.PlaneID, f.Wave, f.Detail)
}

// PlaneFieldMetadata holds canonical plane metadata for mirror detection.
type PlaneFieldMetadata struct {
	PlaneID string
	Wave    MigrationWave
}

// QualifiedSymbol returns the fully-qualified symbol name for a FuncDecl given its relative file path.
// For methods: "pkg/path.(*ReceiverType).MethodName" or "pkg/path.(ReceiverType).MethodName"
// For functions: "pkg/path.FunctionName"
func QualifiedSymbol(relPath string, fd *ast.FuncDecl) string {
	if fd == nil {
		return ""
	}
	dir := filepath.ToSlash(filepath.Dir(relPath))
	if dir == "." {
		dir = ""
	}
	var recvStr string
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		t := fd.Recv.List[0].Type
		if star, ok := t.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok {
				recvStr = "(*" + id.Name + ")."
			}
		} else if id, ok := t.(*ast.Ident); ok {
			recvStr = "(" + id.Name + ")."
		}
	}
	if dir != "" {
		return dir + "." + recvStr + fd.Name.Name
	}
	if recvStr != "" {
		return recvStr + fd.Name.Name
	}
	return fd.Name.Name
}

// ForbiddenMirrorPredicate inspects an AST node in a file and returns a finding if forbidden.
type ForbiddenMirrorPredicate func(relPath string, node ast.Node, fset *token.FileSet, maxCompletedWave MigrationWave) (MirrorFinding, bool)

// IsGeneratedFile reports whether path or content indicates a generated file.
func IsGeneratedFile(path string, src []byte, f *ast.File) bool {
	if strings.HasSuffix(path, "_generated.go") || filepath.Base(path) == "plane_generated.go" {
		return true
	}
	if len(src) > 0 {
		head := src
		if len(head) > 1024 {
			head = head[:1024]
		}
		if bytes.Contains(head, []byte("Code generated")) && bytes.Contains(head, []byte("DO NOT EDIT")) {
			return true
		}
	}
	if f != nil {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "Code generated") && strings.Contains(c.Text, "DO NOT EDIT") {
					return true
				}
			}
		}
	}
	return false
}

// IsThinDelegate reports whether a function/method is a strictly thin delegate
// whose body contains exactly one return statement calling Get or FrozenIdentity
// from pkg/lipsdk/feature, with no other statements or branching logic.
func IsThinDelegate(relPath string, fd *ast.FuncDecl, files ...*ast.File) bool {
	if fd == nil || fd.Body == nil || len(fd.Body.List) != 1 {
		return false
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	var f *ast.File
	if len(files) > 0 {
		f = files[0]
	}
	return isGetCall(relPath, ret.Results[0], f)
}

func isGetCall(relPath string, expr ast.Expr, f *ast.File) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if isGetOrFrozenIdentity(relPath, call.Fun, f) {
		return true
	}
	return isRequestExecutionViewCall(relPath, call, f)
}

func isFeaturePkgImport(imp *ast.ImportSpec) bool {
	if imp == nil || imp.Path == nil {
		return false
	}
	path := strings.Trim(imp.Path.Value, `"`)
	return path == "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
}

func isFeaturePkgDotImportOrSamePkg(relPath string, f *ast.File) bool {
	if f == nil {
		return false
	}
	if f.Name != nil && f.Name.Name == "feature" {
		slash := filepath.ToSlash(relPath)
		dir := path.Dir(slash)
		if dir == "pkg/lipsdk/feature" {
			return true
		}
	}
	for _, imp := range f.Imports {
		if isFeaturePkgImport(imp) && imp.Name != nil && imp.Name.Name == "." {
			return true
		}
	}
	return false
}

func isFeaturePkgIdent(relPath string, expr ast.Expr, f *ast.File) bool {
	pkgIdent, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	pkgName := pkgIdent.Name
	if f == nil {
		return false
	}
	for _, imp := range f.Imports {
		if isFeaturePkgImport(imp) {
			if imp.Name != nil {
				if imp.Name.Name == pkgName {
					return true
				}
			} else if pkgName == "feature" {
				return true
			}
		}
	}
	return false
}

func isGetOrFrozenIdentity(relPath string, fn ast.Expr, f *ast.File) bool {
	switch e := fn.(type) {
	case *ast.IndexExpr:
		return isGetOrFrozenIdentity(relPath, e.X, f)
	case *ast.IndexListExpr:
		return isGetOrFrozenIdentity(relPath, e.X, f)
	case *ast.ParenExpr:
		return isGetOrFrozenIdentity(relPath, e.X, f)
	case *ast.Ident:
		if e.Name != "Get" && e.Name != "FrozenIdentity" {
			return false
		}
		return isFeaturePkgDotImportOrSamePkg(relPath, f)
	case *ast.SelectorExpr:
		if e.Sel.Name != "Get" && e.Sel.Name != "FrozenIdentity" {
			return false
		}
		return isFeaturePkgIdent(relPath, e.X, f)
	default:
		return false
	}
}

var allowedRequestExecutionMethods = map[string]bool{
	"ToolCallPolicies":   true,
	"ToolCallFinalizers": true,
	"SecretGuards":       true,
	"LocalTurnHandlers":  true,
}

func isRequestExecutionViewCall(relPath string, call *ast.CallExpr, f *ast.File) bool {
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if !allowedRequestExecutionMethods[sel.Sel.Name] {
		return false
	}
	innerCall, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := innerCall.Fun.(type) {
	case *ast.SelectorExpr:
		if fn.Sel.Name != "RequestExecution" {
			return false
		}
		return isFeaturePkgIdent(relPath, fn.X, f)
	case *ast.Ident:
		if fn.Name != "RequestExecution" {
			return false
		}
		return isFeaturePkgDotImportOrSamePkg(relPath, f)
	default:
		return false
	}
}

func isDiagnosticsTargetFile(relPath string) bool {
	return filepath.ToSlash(relPath) == "internal/core/diag/inventory_extensions.go"
}

// ScanForbiddenMirrors walks all production files and finds forbidden mirrors past maxCompletedWave.
func ScanForbiddenMirrors(root string, maxCompletedWave MigrationWave) ([]MirrorFinding, error) {
	var findings []MirrorFinding
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		fileFindings := ScanFileForForbiddenMirrors(rel, src, fset, f, maxCompletedWave)
		findings = append(findings, fileFindings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].PlaneID < findings[j].PlaneID
	})
	return findings, nil
}

// ScanFileForForbiddenMirrors inspects a single parsed Go file.
func ScanFileForForbiddenMirrors(relPath string, src []byte, fset *token.FileSet, f *ast.File, maxCompletedWave MigrationWave) []MirrorFinding {
	if IsGeneratedFile(relPath, src, f) {
		return nil
	}

	var findings []MirrorFinding
	seen := make(map[string]bool)
	addFinding := func(kind MirrorShapeKind, ident, planeID, detail string, line int, wave MigrationWave) {
		key := fmt.Sprintf("%s:%d:%s:%s", relPath, line, kind, planeID)
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, MirrorFinding{
			File:       relPath,
			Line:       line,
			ShapeKind:  kind,
			PlaneID:    planeID,
			Identifier: ident,
			Detail:     detail,
			Wave:       wave,
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.TypeSpec:
			st, ok := node.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			structName := node.Name.Name
			var allowed map[string]bool
			var shapeKind MirrorShapeKind
			switch structName {
			case "FeatureBundle":
				if maxCompletedWave < Wave5c_Residual {
					return true
				}
				allowed = AllowedFeatureBundleFields
				shapeKind = MirrorFeatureBundleField
			case "MergedFeatureSurface":
				allowed = AllowedMergedSurfaceFields
				shapeKind = MirrorMergedSurfaceField
			case "ExtensionsOptions":
				allowed = AllowedExtensionsOptionsFields
				shapeKind = MirrorExtensionsOptionsField
			case "generationOperations":
				allowed = AllowedGenerationOperationsFields
				shapeKind = MirrorGenerationOpField
			case "RequestRuntimeSnapshot":
				allowed = AllowedRequestRuntimeSnapshotFields
				shapeKind = MirrorRequestRuntimeSnapshotField
			default:
				return true
			}

			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if allowed[name.Name] {
						continue
					}
					meta, exists := KnownPlaneFields[name.Name]
					if !exists && len(name.Name) > 0 {
						exported := strings.ToUpper(name.Name[:1]) + name.Name[1:]
						meta, exists = KnownPlaneFields[exported]
					}
					if exists && meta.Wave <= maxCompletedWave {
						pos := fset.Position(name.Pos())
						addFinding(shapeKind, name.Name, meta.PlaneID,
							fmt.Sprintf("hand-authored plane field %q on struct %q is forbidden in wave %s", name.Name, structName, meta.Wave),
							pos.Line, meta.Wave)
					}
				}
			}

		case *ast.FuncDecl:
			funcName := node.Name.Name
			qualSym := QualifiedSymbol(relPath, node)
			if isDiagnosticsTargetFile(relPath) {
				inspectDiagnosticsBody(f, node, fset, maxCompletedWave, addFinding)
			} else if funcName == "Append" {
				inspectAppendBody(node, fset, maxCompletedWave, addFinding)
			} else if IsAllowedHookProjection(qualSym) {
				// Exact qualified symbol allowlist: Hook-bus view projection via Get is allowed
			} else if IsAllowedObserverProjection(qualSym) {
				// Exact qualified symbol allowlist: Observer view projection via Get is allowed
			} else if funcName == "extensionsFromMerged" || funcName == "overlayExtensions" ||
				strings.Contains(strings.ToLower(funcName), "frommerged") ||
				strings.Contains(strings.ToLower(funcName), "hooksconfig") ||
				strings.Contains(relPath, "internal/infra/runtimebundle/build_feature_hooks.go") {
				inspectProjectionBody(node, fset, maxCompletedWave, addFinding)
			} else if isGenerationOpMethod(node) {
				inspectGenerationOpMethod(relPath, f, node, fset, maxCompletedWave, addFinding)
			} else if strings.Contains(relPath, "internal/testkit/") || strings.Contains(relPath, "internal/refbackend/") || strings.Contains(relPath, "internal/refclient/") {
				// Test harness and emulator packages are allowed to read planes via Get
			} else {
				inspectStageConsumers(relPath, f, node, fset, maxCompletedWave, addFinding)
			}
		}
		return true
	})

	return findings
}
