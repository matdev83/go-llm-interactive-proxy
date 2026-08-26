package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

func inspectAppendBody(fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd.Body == nil {
		return
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if meta, exists := KnownPlaneFields[name]; exists && meta.Wave <= maxCompletedWave {
			pos := fset.Position(sel.Pos())
			record(MirrorAppendBranch, name, meta.PlaneID,
				fmt.Sprintf("hand-authored Append branch for plane %q is forbidden in wave %s", name, meta.Wave),
				pos.Line, meta.Wave)
		}
		return true
	})
}

func inspectProjectionBody(fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd.Body == nil {
		return
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			if k, ok := node.Key.(*ast.Ident); ok {
				if meta, exists := KnownPlaneFields[k.Name]; exists && meta.Wave <= maxCompletedWave {
					pos := fset.Position(k.Pos())
					record(MirrorProjectionBranch, k.Name, meta.PlaneID,
						fmt.Sprintf("projection branch %q in %q is forbidden in wave %s", k.Name, fd.Name.Name, meta.Wave),
						pos.Line, meta.Wave)
				}
			}
		case *ast.SelectorExpr:
			name := node.Sel.Name
			if meta, exists := KnownPlaneFields[name]; exists && meta.Wave <= maxCompletedWave {
				pos := fset.Position(node.Pos())
				record(MirrorProjectionBranch, name, meta.PlaneID,
					fmt.Sprintf("projection branch %q in %q is forbidden in wave %s", name, fd.Name.Name, meta.Wave),
					pos.Line, meta.Wave)
			}
		}
		return true
	})
}

func receiverTypeName(t ast.Expr) string {
	switch expr := t.(type) {
	case *ast.StarExpr:
		if id, ok := expr.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return expr.Name
	}
	return ""
}

func isGenerationOpMethod(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	typeName := receiverTypeName(fd.Recv.List[0].Type)
	return typeName == "GenerationBundle" || typeName == "generationOperations"
}

func inspectGenerationOpMethod(relPath string, f *ast.File, fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	meta, exists := KnownPlaneFields[fd.Name.Name]
	if !exists {
		return
	}
	if meta.Wave > maxCompletedWave {
		return
	}
	if IsThinDelegate(relPath, fd, f) {
		return
	}
	pos := fset.Position(fd.Pos())
	record(MirrorGenerationOpField, fd.Name.Name, meta.PlaneID,
		fmt.Sprintf("generation accessor %q does not delegate to Get in wave %s", fd.Name.Name, meta.Wave),
		pos.Line, meta.Wave)
}

func resolvePlaneMeta(expr ast.Expr) (PlaneFieldMetadata, bool) {
	var identName string
	switch e := expr.(type) {
	case *ast.Ident:
		identName = e.Name
	case *ast.SelectorExpr:
		identName = e.Sel.Name
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			rawID := strings.Trim(e.Value, `"`)
			for _, meta := range KnownPlaneFields {
				if meta.PlaneID == rawID {
					return meta, true
				}
			}
		}
		return PlaneFieldMetadata{}, false
	default:
		return PlaneFieldMetadata{}, false
	}

	if meta, exists := KnownPlaneFields[identName]; exists {
		return meta, true
	}
	if trimmed, ok := strings.CutPrefix(identName, "Plane"); ok {
		if meta, exists := KnownPlaneFields[trimmed]; exists {
			return meta, true
		}
	}
	for _, meta := range KnownPlaneFields {
		trimmed, _ := strings.CutPrefix(identName, "Plane")
		if strings.EqualFold(meta.PlaneID, identName) || strings.EqualFold(meta.PlaneID, trimmed) {
			return meta, true
		}
	}
	return PlaneFieldMetadata{}, false
}

func extractPlaneMetaFromCall(call *ast.CallExpr) (PlaneFieldMetadata, bool) {
	for _, arg := range call.Args {
		if meta, ok := resolvePlaneMeta(arg); ok {
			return meta, true
		}
	}
	return PlaneFieldMetadata{}, false
}

func inspectStageConsumers(relPath string, f *ast.File, fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd == nil || fd.Body == nil {
		return
	}

	qualSym := QualifiedSymbol(relPath, fd)
	isWhitelisted := IsAllowedStageConsumer(qualSym)

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isGetCall(relPath, call, f) {
			return true
		}
		meta, ok := extractPlaneMetaFromCall(call)
		if !ok {
			return true
		}
		if meta.Wave > maxCompletedWave {
			return true
		}

		pos := fset.Position(call.Pos())
		if !isWhitelisted {
			record(MirrorStageConsumer, fd.Name.Name, meta.PlaneID,
				fmt.Sprintf("stage consumer %q is not in AllowedStageConsumers allowlist in wave %s", qualSym, meta.Wave),
				pos.Line, meta.Wave)
			return true
		}
		if !IsThinDelegate(relPath, fd, f) {
			record(MirrorStageConsumer, fd.Name.Name, meta.PlaneID,
				fmt.Sprintf("stage consumer %q does not thinly delegate to Get in wave %s", qualSym, meta.Wave),
				pos.Line, meta.Wave)
		}
		return true
	})
}

func inspectDiagnosticsBody(fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd.Body == nil || maxCompletedWave < Wave5c_Residual {
		return
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if meta, exists := KnownPlaneFields[name]; exists && meta.Wave <= maxCompletedWave {
			pos := fset.Position(sel.Pos())
			record(MirrorDiagArm, name, meta.PlaneID,
				fmt.Sprintf("diagnostics arm accessing plane field %q is forbidden in wave %s", name, meta.Wave),
				pos.Line, meta.Wave)
		}
		return true
	})
}
