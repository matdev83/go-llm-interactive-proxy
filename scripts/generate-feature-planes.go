//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

type planeInfo struct {
	varName     string // e.g. PlaneSubmitHooks
	planeID     string // e.g. "submit_hooks"
	fieldName   string // e.g. submitHooks
	typeExpr    string // e.g. []hooks.SubmitHook
	isExclusive bool   // e.g. terminaldecision.Provider
	hasIdentity bool   // whether plane has an identity accessor
}

func main() {
	checkFlag := flag.Bool("check", false, "verify that generated output matches disk without modifying files")
	manifestPathFlag := flag.String("manifest", "", "path to plane_manifest.go (default: auto-detected)")
	outPathFlag := flag.String("out", "", "path to plane_generated.go (default: auto-detected)")
	flag.Parse()

	repoRoot := findRepoRoot()
	manifestPath := *manifestPathFlag
	if manifestPath == "" {
		manifestPath = filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_manifest.go")
	}
	outPath := *outPathFlag
	if outPath == "" {
		outPath = filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_generated.go")
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading manifest %s: %v\n", manifestPath, err)
		os.Exit(1)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, manifestPath, manifestBytes, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing manifest %s: %v\n", manifestPath, err)
		os.Exit(1)
	}

	planes, err := extractPlanes(file, manifestBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error extracting planes: %v\n", err)
		os.Exit(1)
	}

	sdkImports, err := deriveImports(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error deriving imports: %v\n", err)
		os.Exit(1)
	}

	generatedCode, err := generateCode(planes, sdkImports)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating code: %v\n", err)
		os.Exit(1)
	}

	formatted, err := format.Source(generatedCode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error formatting generated code: %v\n---\n%s\n---\n", err, string(generatedCode))
		os.Exit(1)
	}

	if *checkFlag {
		existing, err := os.ReadFile(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading existing generated file %s: %v\n", outPath, err)
			os.Exit(1)
		}
		if !bytes.Equal(existing, formatted) {
			fmt.Fprintf(os.Stderr, "ERROR: generated file %s is stale or differs from manifest\nRun 'go run ./scripts/generate-feature-planes.go' to regenerate.\n", outPath)
			os.Exit(1)
		}
		fmt.Println("plane_generated.go is up to date.")
		return
	}

	if err := os.WriteFile(outPath, formatted, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing generated file %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s successfully (%d planes).\n", outPath, len(planes))
}

func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func deriveImports(f *ast.File) ([]string, error) {
	var sdkImports []string
	seen := make(map[string]bool)

	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if !strings.Contains(path, "/") {
			continue
		}
		var importClause string
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" {
			importClause = fmt.Sprintf("%s %q", imp.Name.Name, path)
		} else {
			importClause = fmt.Sprintf("%q", path)
		}
		if !seen[importClause] {
			seen[importClause] = true
			sdkImports = append(sdkImports, importClause)
		}
	}

	if len(sdkImports) == 0 {
		return nil, fmt.Errorf("no SDK imports found in manifest")
	}

	slices.Sort(sdkImports)
	return sdkImports, nil
}

func extractPlanes(f *ast.File, src []byte) ([]planeInfo, error) {
	declaredPlanes := make(map[string]planeInfo)
	var standardPlanesElts []ast.Expr
	foundStandardPlanes := false

	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}
		for _, spec := range genDecl.Specs {
			valSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valSpec.Names {
				if i >= len(valSpec.Values) {
					continue
				}
				val := valSpec.Values[i]
				if name.Name == "StandardPlanes" {
					compLit, ok := val.(*ast.CompositeLit)
					if !ok {
						return nil, fmt.Errorf("StandardPlanes: expected CompositeLit, got %T", val)
					}
					standardPlanesElts = compLit.Elts
					foundStandardPlanes = true
					continue
				}
				if !strings.HasPrefix(name.Name, "Plane") || name.Name == "PlaneDeclaration" {
					continue
				}
				info, err := parsePlaneValue(name.Name, val, src)
				if err != nil {
					return nil, fmt.Errorf("plane %s: %w", name.Name, err)
				}
				declaredPlanes[name.Name] = info
			}
		}
	}

	if !foundStandardPlanes {
		return nil, fmt.Errorf("StandardPlanes slice declaration not found in manifest")
	}
	if len(standardPlanesElts) == 0 {
		return nil, fmt.Errorf("StandardPlanes composite literal is empty")
	}

	orderedPlanes := make([]planeInfo, 0, len(standardPlanesElts))
	seenVars := make(map[string]bool, len(standardPlanesElts))
	for _, elt := range standardPlanesElts {
		ident, ok := elt.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("expected identifier in StandardPlanes, got %T", elt)
		}
		if seenVars[ident.Name] {
			return nil, fmt.Errorf("duplicate plane %s in StandardPlanes", ident.Name)
		}
		seenVars[ident.Name] = true
		info, ok := declaredPlanes[ident.Name]
		if !ok {
			return nil, fmt.Errorf("plane %s referenced in StandardPlanes was not declared in manifest", ident.Name)
		}
		orderedPlanes = append(orderedPlanes, info)
	}

	for name := range declaredPlanes {
		if !seenVars[name] {
			return nil, fmt.Errorf("declared plane %s is not present in StandardPlanes", name)
		}
	}

	seenIDs := make(map[string]string, len(orderedPlanes))
	for _, info := range orderedPlanes {
		if prevVar, exists := seenIDs[info.planeID]; exists {
			return nil, fmt.Errorf("duplicate plane ID %q declared in %s and %s", info.planeID, prevVar, info.varName)
		}
		seenIDs[info.planeID] = info.varName
	}

	return orderedPlanes, nil
}

func parsePlaneValue(varName string, expr ast.Expr, src []byte) (planeInfo, error) {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return planeInfo{}, fmt.Errorf("expected CompositeLit, got %T", expr)
	}

	// Type of CompositeLit is Plane[T] (IndexExpr or IndexListExpr)
	var typeArgStr string
	switch t := compLit.Type.(type) {
	case *ast.IndexExpr:
		typeArgStr = string(src[t.Index.Pos()-1 : t.Index.End()-1])
	case *ast.IndexListExpr:
		if len(t.Indices) > 0 {
			typeArgStr = string(src[t.Indices[0].Pos()-1 : t.Indices[len(t.Indices)-1].End()-1])
		}
	default:
		return planeInfo{}, fmt.Errorf("expected IndexExpr on Plane[T], got %T", compLit.Type)
	}

	typeArgStr = strings.TrimSpace(typeArgStr)

	fieldName := strings.TrimPrefix(varName, "Plane")
	if len(fieldName) > 0 {
		runes := []rune(fieldName)
		runes[0] = unicode.ToLower(runes[0])
		fieldName = string(runes)
	}

	var planeID string
	var multiplicity string
	var hasRules bool
	var hasFeatureRule bool
	var featureRule string
	var hasHostRule bool
	var hostRule string
	var hasGenBinderRule bool
	var genBinderRule string
	var hasCombine bool
	var hasIdentity bool
	var hasDiagStageID bool
	var hasDiagMaterialize bool
	var hasDiagPrivileges bool
	var hasDiagCoalesce bool

	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		kIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch kIdent.Name {
		case "ID":
			if basicLit, ok := kv.Value.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
				planeID = strings.Trim(basicLit.Value, `"`)
			} else {
				planeID = strings.Trim(strings.TrimSpace(string(src[kv.Value.Pos()-1:kv.Value.End()-1])), `"`)
			}
		case "Multiplicity":
			multiplicity = string(src[kv.Value.Pos()-1 : kv.Value.End()-1])
		case "Identity":
			if ident, ok := kv.Value.(*ast.Ident); ok && ident.Name == "nil" {
				hasIdentity = false
			} else {
				hasIdentity = true
			}
		case "Combine":
			if ident, ok := kv.Value.(*ast.Ident); ok && ident.Name == "nil" {
				hasCombine = false
			} else {
				hasCombine = true
			}
		case "Rules":
			hasRules = true
			if rulesLit, ok := kv.Value.(*ast.CompositeLit); ok {
				for _, rElt := range rulesLit.Elts {
					if rKV, ok := rElt.(*ast.KeyValueExpr); ok {
						if rKIdent, ok := rKV.Key.(*ast.Ident); ok {
							valStr := string(src[rKV.Value.Pos()-1 : rKV.Value.End()-1])
							switch rKIdent.Name {
							case "Feature":
								hasFeatureRule = true
								featureRule = valStr
							case "Host":
								hasHostRule = true
								hostRule = valStr
							case "GenerationBinder":
								hasGenBinderRule = true
								genBinderRule = valStr
							}
						}
					}
				}
			}
		case "Diagnostics":
			if diagLit, ok := kv.Value.(*ast.CompositeLit); ok {
				for _, dElt := range diagLit.Elts {
					if dKV, ok := dElt.(*ast.KeyValueExpr); ok {
						if dKIdent, ok := dKV.Key.(*ast.Ident); ok {
							switch dKIdent.Name {
							case "StageID":
								if basicLit, ok := dKV.Value.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
									val := strings.Trim(basicLit.Value, `"`)
									if val != "" {
										hasDiagStageID = true
									}
								} else if ident, ok := dKV.Value.(*ast.Ident); ok && ident.Name == "nil" {
									hasDiagStageID = false
								} else {
									valStr := strings.Trim(strings.TrimSpace(string(src[dKV.Value.Pos()-1:dKV.Value.End()-1])), `"`)
									if valStr != "" && valStr != `""` {
										hasDiagStageID = true
									}
								}
							case "Materialize":
								if ident, ok := dKV.Value.(*ast.Ident); ok && ident.Name == "nil" {
									hasDiagMaterialize = false
								} else {
									hasDiagMaterialize = true
								}
							case "Privileges":
								if ident, ok := dKV.Value.(*ast.Ident); ok && ident.Name == "nil" {
									hasDiagPrivileges = false
								} else {
									hasDiagPrivileges = true
								}
							case "CoalesceGroup":
								if basicLit, ok := dKV.Value.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
									val := strings.Trim(basicLit.Value, `"`)
									if val != "" {
										hasDiagCoalesce = true
									}
								} else if ident, ok := dKV.Value.(*ast.Ident); ok && ident.Name == "nil" {
									hasDiagCoalesce = false
								} else {
									valStr := strings.Trim(strings.TrimSpace(string(src[dKV.Value.Pos()-1:dKV.Value.End()-1])), `"`)
									if valStr != "" && valStr != `""` {
										hasDiagCoalesce = true
									}
								}
							}
						}
					}
				}
			}
		}
	}

	if planeID == "" {
		return planeInfo{}, fmt.Errorf("plane ID is required and must not be empty")
	}

	isExclusive := strings.Contains(multiplicity, "MultExclusive")
	isOrdered := strings.Contains(multiplicity, "MultOrdered")

	if !isExclusive && !isOrdered {
		return planeInfo{}, fmt.Errorf("invalid or missing Multiplicity: %s", multiplicity)
	}

	if !hasRules || (!hasFeatureRule && !hasHostRule && !hasGenBinderRule) {
		return planeInfo{}, fmt.Errorf("at least one source rule must be specified in Rules")
	}

	if isExclusive {
		if strings.Contains(featureRule, "CombConcatenate") || strings.Contains(featureRule, "CombReduce") {
			return planeInfo{}, fmt.Errorf("exclusive plane cannot use concatenate or reduce rule on feature source")
		}
		if !strings.Contains(featureRule, "CombExclusive") {
			return planeInfo{}, fmt.Errorf("exclusive plane must use CombExclusive on feature source")
		}
	}

	if isOrdered {
		if strings.Contains(featureRule, "CombExclusive") {
			return planeInfo{}, fmt.Errorf("ordered plane cannot use CombExclusive rule on feature source")
		}
	}

	requiresIdentity := isExclusive ||
		strings.Contains(featureRule, "CombReplaceByIdentity") ||
		strings.Contains(hostRule, "CombReplaceByIdentity") ||
		strings.Contains(genBinderRule, "CombReplaceByIdentity")

	if requiresIdentity && !hasIdentity {
		return planeInfo{}, fmt.Errorf("identity function is required for exclusive or replace-by-identity plane")
	}

	if !hasCombine {
		return planeInfo{}, fmt.Errorf("combine function is required")
	}

	if hasDiagStageID && !hasDiagMaterialize {
		return planeInfo{}, fmt.Errorf("diagnostics StageID is set but Materialize function is missing")
	}
	if !hasDiagStageID && (hasDiagMaterialize || hasDiagPrivileges || hasDiagCoalesce) {
		return planeInfo{}, fmt.Errorf("diagnostics StageID must not be empty when diagnostics metadata is provided")
	}

	return planeInfo{
		varName:     varName,
		planeID:     planeID,
		fieldName:   fieldName,
		typeExpr:    typeArgStr,
		isExclusive: isExclusive,
		hasIdentity: hasIdentity,
	}, nil
}

func generateCode(planes []planeInfo, sdkImports []string) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("// Code generated by scripts/generate-feature-planes.go. DO NOT EDIT.\n\n")
	buf.WriteString("package feature\n\n")
	buf.WriteString("import (\n")
	buf.WriteString("\t\"slices\"\n\n")
	for _, imp := range sdkImports {
		fmt.Fprintf(&buf, "\t%s\n", imp)
	}
	buf.WriteString(")\n\n")

	// 1. generatedContributions struct
	buf.WriteString("// generatedContributions holds typed contribution storage for all declared feature planes.\n")
	buf.WriteString("type generatedContributions struct {\n")
	buf.WriteString("\tfreeze func() *generatedFrozen\n\n")
	for _, p := range planes {
		fmt.Fprintf(&buf, "\t%s %s\n", p.fieldName, p.typeExpr)
		if p.isExclusive {
			fmt.Fprintf(&buf, "\t%sID string\n", p.fieldName)
			fmt.Fprintf(&buf, "\t%sHasID bool\n", p.fieldName)
		}
	}
	buf.WriteString("}\n\n")

	// 2. generatedFrozen struct
	buf.WriteString("// generatedFrozen holds immutable typed snapshot storage for all declared feature planes.\n")
	buf.WriteString("type generatedFrozen struct {\n")
	for _, p := range planes {
		fmt.Fprintf(&buf, "\t%s %s\n", p.fieldName, p.typeExpr)
		if p.isExclusive {
			fmt.Fprintf(&buf, "\t%sID string\n", p.fieldName)
			fmt.Fprintf(&buf, "\t%sHasID bool\n", p.fieldName)
		}
	}
	buf.WriteString("}\n\n")

	// 3. newGeneratedContributions constructor
	buf.WriteString("// newGeneratedContributions constructs a new generatedContributions with an immutable snapshot freeze function.\n")
	buf.WriteString("func newGeneratedContributions() *generatedContributions {\n")
	buf.WriteString("\tgc := &generatedContributions{}\n")
	buf.WriteString("\tgc.freeze = func() *generatedFrozen {\n")
	buf.WriteString("\t\tgf := &generatedFrozen{\n")
	for _, p := range planes {
		if strings.HasPrefix(p.typeExpr, "[]") {
			fmt.Fprintf(&buf, "\t\t\t%s: slices.Clone(gc.%s),\n", p.fieldName, p.fieldName)
		} else {
			fmt.Fprintf(&buf, "\t\t\t%s: gc.%s,\n", p.fieldName, p.fieldName)
		}
		if p.isExclusive {
			fmt.Fprintf(&buf, "\t\t\t%sID: gc.%sID,\n", p.fieldName, p.fieldName)
			fmt.Fprintf(&buf, "\t\t\t%sHasID: gc.%sHasID,\n", p.fieldName, p.fieldName)
		}
	}
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\treturn gf\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn gc\n")
	buf.WriteString("}\n\n")

	// 4. init() binding closures
	buf.WriteString("func init() {\n")
	for _, p := range planes {
		fmt.Fprintf(&buf, "\t%s.generated = generatedAccess[%s]{\n", p.varName, p.typeExpr)

		// contribute closure
		fmt.Fprintf(&buf, "\t\tcontribute: func(gc *generatedContributions, pluginID string, v %s) error {\n", p.typeExpr)
		fmt.Fprintf(&buf, "\t\t\tcombined, err := %s.Combine(SourceFeature, gc.%s, v)\n", p.varName, p.fieldName)
		buf.WriteString("\t\t\tif err != nil {\n")
		buf.WriteString("\t\t\t\treturn err\n")
		buf.WriteString("\t\t\t}\n")
		fmt.Fprintf(&buf, "\t\t\tgc.%s = combined\n", p.fieldName)
		if p.isExclusive {
			fmt.Fprintf(&buf, "\t\t\tid, hasID := %s.Identity(v)\n", p.varName)
			fmt.Fprintf(&buf, "\t\t\tgc.%sID = id\n", p.fieldName)
			fmt.Fprintf(&buf, "\t\t\tgc.%sHasID = hasID\n", p.fieldName)
		}
		buf.WriteString("\t\t\treturn nil\n")
		buf.WriteString("\t\t},\n")

		// get closure
		fmt.Fprintf(&buf, "\t\tget: func(gf *generatedFrozen) %s {\n", p.typeExpr)
		buf.WriteString("\t\t\tif gf == nil {\n")
		if strings.HasPrefix(p.typeExpr, "[]") {
			buf.WriteString("\t\t\t\treturn nil\n")
		} else if p.typeExpr == "int" {
			buf.WriteString("\t\t\t\treturn 0\n")
		} else {
			buf.WriteString("\t\t\t\treturn nil\n")
		}
		buf.WriteString("\t\t\t}\n")
		fmt.Fprintf(&buf, "\t\t\treturn gf.%s\n", p.fieldName)
		buf.WriteString("\t\t},\n")

		// identity closure
		if p.isExclusive {
			buf.WriteString("\t\tidentity: func(gf *generatedFrozen) (string, bool) {\n")
			buf.WriteString("\t\t\tif gf == nil {\n")
			buf.WriteString("\t\t\t\treturn \"\", false\n")
			buf.WriteString("\t\t\t}\n")
			fmt.Fprintf(&buf, "\t\t\treturn gf.%sID, gf.%sHasID\n", p.fieldName, p.fieldName)
			buf.WriteString("\t\t},\n")
		} else if p.hasIdentity {
			buf.WriteString("\t\tidentity: func(gf *generatedFrozen) (string, bool) {\n")
			buf.WriteString("\t\t\tif gf == nil {\n")
			buf.WriteString("\t\t\t\treturn \"\", false\n")
			buf.WriteString("\t\t\t}\n")
			fmt.Fprintf(&buf, "\t\t\treturn %s.Identity(gf.%s)\n", p.varName, p.fieldName)
			buf.WriteString("\t\t},\n")
		}

		buf.WriteString("\t}\n")
	}
	buf.WriteString("}\n")

	return buf.Bytes(), nil
}
