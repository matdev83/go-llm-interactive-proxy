package archtest

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"unicode"
)

type planeInfo struct {
	varName                string // e.g. PlaneSubmitHooks
	planeID                string // e.g. "submit_hooks"
	fieldName              string // e.g. submitHooks
	typeExpr               string // e.g. []hooks.SubmitHook
	isExclusive            bool   // e.g. terminaldecision.Provider
	hasIdentity            bool   // whether plane has an identity accessor
	hasGenBinderRule       bool   // whether GenerationBinder rule is declared
	genBinderRule          string // e.g. CombReplaceByIdentity
	candidate              bool   // whether plane allows candidate overlay contribution
	hasRequestMaterializer bool   // whether plane has a RequestMaterializer
	requestBorrow          bool   // whether plane exposes RequestExecutionView method
	hasDiagStageID         bool
	diagStageID            string
	diagOrder              int
	diagCoalesceGroup      string
	hasDiagMaterialize     bool
	hasDiagPrivileges      bool
}

// GenerateFeaturePlanesCode parses plane_manifest.go source bytes and returns the formatted Go code for plane_generated.go.
func GenerateFeaturePlanesCode(manifestBytes []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plane_manifest.go", manifestBytes, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	planes, err := extractPlanes(file, manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("extract planes: %w", err)
	}

	sdkImports, err := deriveImports(file)
	if err != nil {
		return nil, fmt.Errorf("derive imports: %w", err)
	}

	generatedCode, err := generatePlanesCode(planes, sdkImports)
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}

	formatted, err := format.Source(generatedCode)
	if err != nil {
		return nil, fmt.Errorf("format generated code: %w\n---\n%s\n---", err, string(generatedCode))
	}

	return formatted, nil
}

// GenerateFeatureBundleProjectionCode parses plane_manifest.go source bytes and returns the formatted Go code for internal/featurebundle/bundle_projection_generated.go.
func GenerateFeatureBundleProjectionCode(manifestBytes []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plane_manifest.go", manifestBytes, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	planes, err := extractPlanes(file, manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("extract planes: %w", err)
	}

	generatedCode, err := generateBundleProjectionCode(planes)
	if err != nil {
		return nil, fmt.Errorf("generate bundle projection code: %w", err)
	}

	formatted, err := format.Source(generatedCode)
	if err != nil {
		return nil, fmt.Errorf("format generated bundle projection code: %w\n---\n%s\n---", err, string(generatedCode))
	}

	return formatted, nil
}

func extractPlanes(f *ast.File, src []byte) ([]planeInfo, error) {
	declaredPlanes := make(map[string]planeInfo)
	var standardPlanesElts []ast.Expr
	foundStandardPlanes := false
	var standardCandidatePlanesElts []ast.Expr
	foundStandardCandidatePlanes := false

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
				if name.Name == "StandardCandidatePlanes" {
					compLit, ok := val.(*ast.CompositeLit)
					if !ok {
						return nil, fmt.Errorf("StandardCandidatePlanes: expected CompositeLit, got %T", val)
					}
					standardCandidatePlanesElts = compLit.Elts
					foundStandardCandidatePlanes = true
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

	if foundStandardCandidatePlanes {
		candidatePlaneIDs := make(map[string]bool)
		for _, elt := range standardCandidatePlanesElts {
			basicLit, ok := elt.(*ast.BasicLit)
			if !ok || basicLit.Kind != token.STRING {
				return nil, fmt.Errorf("expected string literal in StandardCandidatePlanes, got %T", elt)
			}
			candID := strings.Trim(basicLit.Value, `"`)
			if candID == "" {
				return nil, fmt.Errorf("StandardCandidatePlanes contains empty string")
			}
			if candidatePlaneIDs[candID] {
				return nil, fmt.Errorf("duplicate candidate plane ID %q in StandardCandidatePlanes", candID)
			}
			candidatePlaneIDs[candID] = true
		}

		for candID := range candidatePlaneIDs {
			if _, exists := seenIDs[candID]; !exists {
				return nil, fmt.Errorf("candidate plane ID %q in StandardCandidatePlanes was not declared in manifest", candID)
			}
		}

		for i := range orderedPlanes {
			if candidatePlaneIDs[orderedPlanes[i].planeID] {
				orderedPlanes[i].candidate = true
			}
		}
	}

	if err := validateDiagnosticsCrossPlane(orderedPlanes); err != nil {
		return nil, err
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
	var hasRequestMaterializer bool
	var requestBorrow bool
	var diagStageID string
	var diagOrder int
	var diagCoalesceGroup string
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
		case "RequestMaterializer":
			if ident, ok := kv.Value.(*ast.Ident); ok && ident.Name == "nil" {
				hasRequestMaterializer = false
			} else {
				if err := validateRequestMaterializerExpr(kv.Value, varName); err != nil {
					return planeInfo{}, err
				}
				hasRequestMaterializer = true
			}
		case "RequestBorrow":
			if ident, ok := kv.Value.(*ast.Ident); ok && ident.Name == "true" {
				requestBorrow = true
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
										diagStageID = val
									}
								} else if ident, ok := dKV.Value.(*ast.Ident); ok && ident.Name == "nil" {
									hasDiagStageID = false
								} else {
									valStr := strings.Trim(strings.TrimSpace(string(src[dKV.Value.Pos()-1:dKV.Value.End()-1])), `"`)
									if valStr != "" && valStr != `""` {
										hasDiagStageID = true
										diagStageID = valStr
									}
								}
							case "Order":
								if basicLit, ok := dKV.Value.(*ast.BasicLit); ok && basicLit.Kind == token.INT {
									var ord int
									if _, err := fmt.Sscanf(basicLit.Value, "%d", &ord); err == nil {
										diagOrder = ord
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
									if err := validatePrivilegesFunc(varName, dKV.Value); err != nil {
										return planeInfo{}, err
									}
									hasDiagPrivileges = true
								}
							case "CoalesceGroup":
								if basicLit, ok := dKV.Value.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
									val := strings.Trim(basicLit.Value, `"`)
									if val != "" {
										hasDiagCoalesce = true
										diagCoalesceGroup = val
									}
								} else if ident, ok := dKV.Value.(*ast.Ident); ok && ident.Name == "nil" {
									hasDiagCoalesce = false
								} else {
									valStr := strings.Trim(strings.TrimSpace(string(src[dKV.Value.Pos()-1:dKV.Value.End()-1])), `"`)
									if valStr != "" && valStr != `""` {
										hasDiagCoalesce = true
										diagCoalesceGroup = valStr
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

	if requestBorrow {
		if !hasRequestMaterializer {
			return planeInfo{}, fmt.Errorf("plane %s: RequestBorrow requires non-nil RequestMaterializer", varName)
		}
		if !strings.HasPrefix(typeArgStr, "[]") {
			return planeInfo{}, fmt.Errorf("plane %s: RequestBorrow cannot be used on non-slice plane type %s", varName, typeArgStr)
		}
	}

	if hasDiagStageID {
		if !hasDiagMaterialize {
			return planeInfo{}, fmt.Errorf("diagnostics StageID is set but Materialize function is missing")
		}
		if diagOrder <= 0 {
			return planeInfo{}, fmt.Errorf("diagnostics StageID is set but Order must be > 0 (got %d)", diagOrder)
		}
	}
	if !hasDiagStageID && (hasDiagMaterialize || hasDiagPrivileges || hasDiagCoalesce || diagOrder != 0) {
		return planeInfo{}, fmt.Errorf("diagnostics StageID must not be empty when diagnostics metadata is provided")
	}

	return planeInfo{
		varName:                varName,
		planeID:                planeID,
		fieldName:              fieldName,
		typeExpr:               typeArgStr,
		isExclusive:            isExclusive,
		hasIdentity:            hasIdentity,
		hasGenBinderRule:       hasGenBinderRule,
		genBinderRule:          genBinderRule,
		hasRequestMaterializer: hasRequestMaterializer,
		requestBorrow:          requestBorrow,
		hasDiagStageID:         hasDiagStageID,
		diagStageID:            diagStageID,
		diagOrder:              diagOrder,
		diagCoalesceGroup:      diagCoalesceGroup,
		hasDiagMaterialize:     hasDiagMaterialize,
		hasDiagPrivileges:      hasDiagPrivileges,
	}, nil
}
