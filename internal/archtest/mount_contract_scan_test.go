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

// Desired StandardHTTPInput group names from approved design.md (Task 3.1 / 3.2).
var desiredStandardHTTPTypes = []string{
	"StandardHTTPInput",
	"HTTPCoreInput",
	"HTTPSecurityInput",
	"HTTPOperationsInput",
	"HTTPModelInput",
	"HTTPFrontendInput",
}

var desiredStandardHTTPGroupFields = map[string]string{
	"Core":       "HTTPCoreInput",
	"Security":   "HTTPSecurityInput",
	"Operations": "HTTPOperationsInput",
	"Models":     "HTTPModelInput",
	"Frontends":  "HTTPFrontendInput",
}

// mountHelpersStrictContract are Task 3.2 GREEN surfaces: production mount
// helpers plus the focused composer. Broad Built/RequestPlane on these fails
// the live Task 3.1→3.2 ratchet. Lifecycle owners are also prohibited here.
var mountHelpersStrictContract = map[string]bool{
	"mountMetrics":                   true,
	"mountDiagnostics":               true,
	"mountModelCatalogDiagnostics":   true,
	"mountModelInventoryDiagnostics": true,
	"mountSecureSessionDiagnostics":  true,
	"mountAccountingAdmin":           true,
	"mountControlPlaneQuery":         true,
	"mountAccountingAuthorityQuery":  true,
	"MountBundledFrontends":          true,
	"MountBundledFrontendsLegacy":    true,
	"mountALegCancel":                true,
	"stackHTTPHandler":               true,
	"prepareStandardHandler":         true,
	"ComposeStandardHTTP":            true,
}

// mountHelpersTransitionalAdapters are compatibility composition roots whose
// source input signature may remain broad until later tasks:
//   - NewStandardHandler: legacy *Built until Phase 4 (must project to
//     StandardHTTPInput before mounts after Task 3.2)
//
// ComposeRequestPlane was deleted in Task 3.5. Scanner still detects Built bags
// on NewStandardHandler; live Task 3.2 strict failure sets exclude it.
var mountHelpersTransitionalAdapters = map[string]bool{
	"NewStandardHandler": true,
}

// mountHelpersUnderContract is the full scanned set (strict ∪ transitional).
var mountHelpersUnderContract = func() map[string]bool {
	out := map[string]bool{}
	for name := range mountHelpersStrictContract {
		out[name] = true
	}
	for name := range mountHelpersTransitionalAdapters {
		out[name] = true
	}
	return out
}()

// mountHelperAllowedGroups maps each strict mount/composer surface to the
// cohesive groups it may accept after Task 3.2. Transitional adapters are also
// listed for post-projection shape checks when they already take focused inputs;
// their broad source signatures are not Task 3.2 strict failures.
var mountHelperAllowedGroups = map[string]map[string]bool{
	"mountMetrics":                   {"HTTPOperationsInput": true},
	"mountDiagnostics":               {"HTTPOperationsInput": true, "HTTPCoreInput": true},
	"mountModelCatalogDiagnostics":   {"HTTPModelInput": true},
	"mountModelInventoryDiagnostics": {"HTTPModelInput": true},
	"mountSecureSessionDiagnostics":  {"HTTPSecurityInput": true},
	"mountAccountingAdmin":           {"HTTPOperationsInput": true, "HTTPCoreInput": true},
	"mountControlPlaneQuery":         {"HTTPOperationsInput": true},
	"mountAccountingAuthorityQuery":  {"HTTPSecurityInput": true, "HTTPCoreInput": true},
	"MountBundledFrontends":          {"HTTPFrontendInput": true},
	"MountBundledFrontendsLegacy":    {"HTTPFrontendInput": true},
	"mountALegCancel":                {"HTTPFrontendInput": true, "HTTPCoreInput": true},
	"stackHTTPHandler":               {"HTTPSecurityInput": true, "HTTPOperationsInput": true},
	"prepareStandardHandler":         {"StandardHTTPInput": true},
	"NewStandardHandler":             {"StandardHTTPInput": true},
	"ComposeStandardHTTP":            {"StandardHTTPInput": true},
}

var prohibitedLifecycleFieldNames = map[string]bool{
	"Closers":          true,
	"Closer":           true,
	"Close":            true,
	"Shutdown":         true,
	"OnClose":          true,
	"OnShutdown":       true,
	"ReleaseClosers":   true,
	"ResourceLedger":   true,
	"Ledger":           true,
	"Host":             true,
	"Coordinator":      true,
	"DependencyGetter": true,
	"GetDependency":    true,
	"Lookup":           true,
	"Built":            true,
	"RequestPlane":     true,
}

// genericLookupMethodNames are vocabulary tokens for service-locator style
// methods on local interfaces referenced by desired HTTP group fields.
var genericLookupMethodNames = map[string]bool{
	"Get":        true,
	"Lookup":     true,
	"Resolve":    true,
	"Dependency": true,
	"Service":    true,
}

type mountContractFinding struct {
	Path   string
	Kind   string
	Detail string
	Helper string
}

func (f mountContractFinding) String() string {
	if f.Helper != "" {
		return fmt.Sprintf("%s: [%s] %s (%s)", f.Path, f.Kind, f.Detail, f.Helper)
	}
	return fmt.Sprintf("%s: [%s] %s", f.Path, f.Kind, f.Detail)
}

type mountContractScanResult struct {
	Findings                   []mountContractFinding
	DeclaredTypes              map[string]bool
	StandardHTTPFields         map[string]string // field name → type name
	StandardHTTPFieldIsPointer map[string]bool   // field name → pointer shape
	GroupFieldNames            map[string][]string
}

// broadBagFindingKinds are ratchet-gate kinds that must clear on strict
// surfaces after Task 3.2.
var broadBagFindingKinds = map[string]bool{
	"built_dependency":         true,
	"request_plane_dependency": true,
	"input_field_broad_bag":    true,
}

// task32StrictFailureKinds are live Task 3.2 failure kinds filtered to strict
// mount/composer surfaces (not transitional adapter source signatures).
var task32StrictFailureKinds = map[string]bool{
	"built_dependency":         true,
	"request_plane_dependency": true,
	"input_field_broad_bag":    true,
	"excess_group":             true,
}

func collectFindingsByKinds(fs []mountContractFinding, kinds map[string]bool) []string {
	var out []string
	for _, f := range fs {
		if kinds[f.Kind] {
			out = append(out, f.String())
		}
	}
	return out
}

// collectStrictTask32Findings returns Task 3.2 live-gate failures limited to
// strict mount/composer surfaces. Transitional adapter source-signature bags
// (NewStandardHandler Built) are tracked by the scanner but excluded from this
// strict failure set. ComposeRequestPlane was deleted in Task 3.5.
func collectStrictTask32Findings(fs []mountContractFinding, kinds map[string]bool) []string {
	var out []string
	for _, f := range fs {
		if !kinds[f.Kind] {
			continue
		}
		if f.Helper == "" {
			out = append(out, f.String())
			continue
		}
		if mountHelpersTransitionalAdapters[f.Helper] {
			continue
		}
		if !mountHelpersStrictContract[f.Helper] {
			continue
		}
		out = append(out, f.String())
	}
	return out
}

// collectTransitionalAdapterFindings returns broad-bag findings on transitional
// compatibility adapters. Useful for synthetic policy proofs and optional
// tracking; not a Task 3.2 strict failure set.
func collectTransitionalAdapterFindings(fs []mountContractFinding, kinds map[string]bool) []string {
	var out []string
	for _, f := range fs {
		if !kinds[f.Kind] {
			continue
		}
		if mountHelpersTransitionalAdapters[f.Helper] {
			out = append(out, f.String())
		}
	}
	return out
}

func scanMountContractSource(filename, src string) (mountContractScanResult, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return mountContractScanResult{}, err
	}
	aliases, dotImportRuntimebundle := importAliases(f)
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

	out := mountContractScanResult{
		DeclaredTypes:              map[string]bool{},
		StandardHTTPFields:         map[string]string{},
		StandardHTTPFieldIsPointer: map[string]bool{},
		GroupFieldNames:            map[string][]string{},
	}
	for name, ts := range localTypes {
		for _, want := range desiredStandardHTTPTypes {
			if name == want {
				out.DeclaredTypes[name] = true
			}
		}
		if name == "StandardHTTPInput" {
			if st, ok := ts.Type.(*ast.StructType); ok {
				shapes := structFieldShapes(st)
				out.StandardHTTPFields = shapes.Types
				out.StandardHTTPFieldIsPointer = shapes.Pointers
				for field, wantType := range desiredStandardHTTPGroupFields {
					gotType := shapes.Types[field]
					if gotType == wantType && shapes.Pointers[field] {
						out.Findings = append(out.Findings, mountContractFinding{
							Path: filename, Kind: "pointer_group_field",
							Detail: fmt.Sprintf("StandardHTTPInput.%s must be value %s, not *%s", field, wantType, wantType),
						})
					}
				}
			}
		}
		for _, want := range desiredStandardHTTPTypes {
			if name != want || want == "StandardHTTPInput" {
				continue
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				var fields []string
				for _, field := range st.Fields.List {
					for _, n := range field.Names {
						fields = append(fields, n.Name)
						if prohibitedLifecycleFieldNames[n.Name] || typeLooksLikeLifecycle(field.Type, aliases, typeAliases, localTypes, dotImportRuntimebundle) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "lifecycle_field",
								Detail: fmt.Sprintf("%s.%s prohibited lifecycle/broad-bag field", name, n.Name),
							})
						}
						if isAnyOrEmptyInterface(field.Type) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "arbitrary_any_field",
								Detail: fmt.Sprintf("%s.%s uses any/interface{} (arbitrary bag)", name, n.Name),
							})
						}
						if isMapType(field.Type) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "arbitrary_map_field",
								Detail: fmt.Sprintf("%s.%s uses map type (arbitrary lookup bag)", name, n.Name),
							})
						}
						if localIfaceLooksLikeServiceLocator(field.Type, localTypes) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "service_locator",
								Detail: fmt.Sprintf("%s.%s references generic dependency getter/service locator interface", name, n.Name),
							})
						}
						if fieldNameLooksLikeGenericGetter(n.Name) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "generic_getter_field",
								Detail: fmt.Sprintf("%s.%s name looks like a generic dependency getter", name, n.Name),
							})
						}
					}
					if len(field.Names) == 0 {
						// embedded
						if typeLooksLikeLifecycle(field.Type, aliases, typeAliases, localTypes, dotImportRuntimebundle) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "lifecycle_field",
								Detail: fmt.Sprintf("%s embeds prohibited lifecycle/broad-bag type", name),
							})
						}
						if isAnyOrEmptyInterface(field.Type) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "arbitrary_any_field",
								Detail: fmt.Sprintf("%s embeds any/interface{}", name),
							})
						}
						if isMapType(field.Type) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "arbitrary_map_field",
								Detail: fmt.Sprintf("%s embeds map type", name),
							})
						}
						if localIfaceLooksLikeServiceLocator(field.Type, localTypes) {
							out.Findings = append(out.Findings, mountContractFinding{
								Path: filename, Kind: "service_locator",
								Detail: fmt.Sprintf("%s embeds generic dependency getter/service locator interface", name),
							})
						}
					}
				}
				out.GroupFieldNames[name] = fields
			}
		}
	}

	// Function signatures / input structs for mount helpers.
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv != nil {
			continue
		}
		name := fd.Name.Name
		if !mountHelpersUnderContract[name] && !isStdhttpMountHelperName(name) {
			continue
		}
		if fd.Type == nil || fd.Type.Params == nil {
			continue
		}
		for _, field := range fd.Type.Params.List {
			findings := scanExprForBroadBag(filename, name, field.Type, aliases, typeAliases, localTypes, dotImportRuntimebundle)
			out.Findings = append(out.Findings, findings...)

			// Direct parameter allowlist: desired group types passed as params.
			groupType := resolvedTypeName(field.Type, aliases, typeAliases)
			if allowed := mountHelperAllowedGroups[name]; allowed != nil && isDesiredHTTPGroupType(groupType) {
				if !allowed[groupType] {
					out.Findings = append(out.Findings, mountContractFinding{
						Path: filename, Kind: "excess_group", Helper: name,
						Detail: fmt.Sprintf("direct param type %s but helper may only accept %v", groupType, sortedKeys(allowed)),
					})
				}
			}

			// Resolve local mount-input bags and scan their fields. Desired cohesive
			// group types passed directly are allowlist-checked above; do not treat
			// StandardHTTPInput / HTTP*Input themselves as mount input bags (their
			// nested group fields would false-positive as excess_group).
			base := unwrapTypeExpr(field.Type)
			if id, ok := base.(*ast.Ident); ok && !isDesiredHTTPGroupType(id.Name) {
				if ts, ok := localTypes[id.Name]; ok {
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, sf := range st.Fields.List {
							findings := scanExprForBroadBag(filename, name, sf.Type, aliases, typeAliases, localTypes, dotImportRuntimebundle)
							for i := range findings {
								findings[i].Kind = "input_field_broad_bag"
								if len(sf.Names) > 0 {
									findings[i].Detail = fmt.Sprintf("input %s.%s: %s", id.Name, sf.Names[0].Name, findings[i].Detail)
								}
							}
							out.Findings = append(out.Findings, findings...)
							// Disallow accepting all groups by default: if a mount input
							// references a desired group type not in its allowlist, report.
							groupType := resolvedTypeName(sf.Type, aliases, typeAliases)
							if allowed := mountHelperAllowedGroups[name]; allowed != nil && isDesiredHTTPGroupType(groupType) {
								if !allowed[groupType] {
									out.Findings = append(out.Findings, mountContractFinding{
										Path: filename, Kind: "excess_group", Helper: name,
										Detail: fmt.Sprintf("input references %s but helper may only accept %v", groupType, sortedKeys(allowed)),
									})
								}
							}
						}
					}
				}
			}
		}
	}
	return out, nil
}

func isStdhttpMountHelperName(name string) bool {
	if name == "stackHTTPHandler" || name == "prepareStandardHandler" || name == "ComposeStandardHTTP" || name == "NewStandardHandler" {
		return true
	}
	return strings.HasPrefix(name, "mount") || strings.HasPrefix(name, "Mount")
}

func importAliases(f *ast.File) (map[string]string, bool) {
	aliases := map[string]string{}
	dotRB := false
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
		}
		if name == "." && path == importRuntimebundle {
			dotRB = true
			continue
		}
		if name == "_" {
			continue
		}
		aliases[name] = path
	}
	return aliases, dotRB
}

func scanExprForBroadBag(filename, helper string, expr ast.Expr, aliases map[string]string, typeAliases map[string]ast.Expr, localTypes map[string]*ast.TypeSpec, dotRB bool) []mountContractFinding {
	var out []mountContractFinding
	if typeLooksLikeBuiltOrRequestPlane(expr, aliases, typeAliases, localTypes, dotRB) {
		kind := "built_dependency"
		detail := typeString(expr) + " broad Built/RequestPlane dependency"
		if typeLooksLikeRequestPlane(expr, aliases) || resolvesToName(expr, aliases, typeAliases, "RequestPlane") {
			kind = "request_plane_dependency"
			detail = typeString(expr) + " RequestPlane dependency"
		}
		out = append(out, mountContractFinding{Path: filename, Kind: kind, Helper: helper, Detail: detail})
	}
	if typeLooksLikeLifecycle(expr, aliases, typeAliases, localTypes, dotRB) && !typeLooksLikeBuiltOrRequestPlane(expr, aliases, typeAliases, localTypes, dotRB) {
		out = append(out, mountContractFinding{
			Path: filename, Kind: "lifecycle_owner", Helper: helper,
			Detail: typeString(expr) + " lifecycle/closer owner at mount boundary",
		})
	}
	return out
}

func typeLooksLikeBuiltOrRequestPlane(expr ast.Expr, aliases map[string]string, typeAliases map[string]ast.Expr, localTypes map[string]*ast.TypeSpec, dotRB bool) bool {
	expr = unwrapTypeExpr(expr)
	switch t := expr.(type) {
	case *ast.Ident:
		if alt, ok := typeAliases[t.Name]; ok {
			return typeLooksLikeBuiltOrRequestPlane(alt, aliases, typeAliases, localTypes, dotRB)
		}
		if ts, ok := localTypes[t.Name]; ok {
			if ts.Assign.IsValid() {
				return typeLooksLikeBuiltOrRequestPlane(ts.Type, aliases, typeAliases, localTypes, dotRB)
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				for _, f := range st.Fields.List {
					if typeLooksLikeBuiltOrRequestPlane(f.Type, aliases, typeAliases, localTypes, dotRB) {
						return true
					}
				}
			}
			// Local type named Built/RequestPlane that is not a runtimebundle alias
			// (e.g. unrelated empty struct) is not a broad-bag hit.
			return false
		}
		// Unqualified Built/RequestPlane only counts with a dot-import of runtimebundle.
		if (t.Name == "Built" || t.Name == "RequestPlane") && dotRB {
			return true
		}
		return false
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok || t.Sel == nil {
			return false
		}
		path := aliases[pkg.Name]
		if path != importRuntimebundle {
			return false
		}
		return t.Sel.Name == "Built" || t.Sel.Name == "RequestPlane"
	case *ast.StarExpr:
		return typeLooksLikeBuiltOrRequestPlane(t.X, aliases, typeAliases, localTypes, dotRB)
	default:
		return false
	}
}

func typeLooksLikeLifecycle(expr ast.Expr, aliases map[string]string, typeAliases map[string]ast.Expr, localTypes map[string]*ast.TypeSpec, dotRB bool) bool {
	if typeLooksLikeBuiltOrRequestPlane(expr, aliases, typeAliases, localTypes, dotRB) {
		return true
	}
	expr = unwrapTypeExpr(expr)
	switch t := expr.(type) {
	case *ast.Ident:
		if prohibitedLifecycleFieldNames[t.Name] {
			return true
		}
		if alt, ok := typeAliases[t.Name]; ok {
			return typeLooksLikeLifecycle(alt, aliases, typeAliases, localTypes, dotRB)
		}
		return false
	case *ast.SelectorExpr:
		if t.Sel == nil {
			return false
		}
		if t.Sel.Name == "Closer" {
			if pkg, ok := t.X.(*ast.Ident); ok && aliases[pkg.Name] == "io" {
				return true
			}
		}
		if t.Sel.Name == "ResourceLedger" || t.Sel.Name == "Built" || t.Sel.Name == "RequestPlane" {
			return true
		}
		return false
	case *ast.ArrayType:
		return typeLooksLikeLifecycle(t.Elt, aliases, typeAliases, localTypes, dotRB)
	case *ast.FuncType:
		// Fail closed: zero-argument callbacks returning nothing or error are
		// close/shutdown/lifecycle callbacks regardless of field name.
		if fieldParamCount(t.Params) != 0 {
			return false
		}
		nRes := fieldParamCount(t.Results)
		if nRes == 0 {
			return true // func()
		}
		if nRes == 1 {
			res := t.Results.List[0].Type
			if id, ok := unwrapTypeExpr(res).(*ast.Ident); ok && id.Name == "error" {
				return true // func() error
			}
		}
		return false
	case *ast.StarExpr:
		return typeLooksLikeLifecycle(t.X, aliases, typeAliases, localTypes, dotRB)
	default:
		return false
	}
}

func fieldParamCount(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++
			continue
		}
		n += len(f.Names)
	}
	return n
}

func localIfaceLooksLikeServiceLocator(expr ast.Expr, localTypes map[string]*ast.TypeSpec) bool {
	id, ok := unwrapTypeExpr(expr).(*ast.Ident)
	if !ok {
		return false
	}
	ts, ok := localTypes[id.Name]
	if !ok || ts.Type == nil {
		return false
	}
	it, ok := ts.Type.(*ast.InterfaceType)
	if !ok || it.Methods == nil {
		return false
	}
	for _, m := range it.Methods.List {
		ft, ok := m.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		for _, n := range m.Names {
			if methodLooksLikeGenericLookup(n.Name, ft) {
				return true
			}
		}
	}
	return false
}

func methodLooksLikeGenericLookup(name string, ft *ast.FuncType) bool {
	if !genericLookupMethodName(name) {
		return false
	}
	if ft.Params == nil || fieldParamCount(ft.Params) == 0 {
		return false
	}
	hasStringKey := false
	for _, p := range ft.Params.List {
		if id, ok := unwrapTypeExpr(p.Type).(*ast.Ident); ok && id.Name == "string" {
			hasStringKey = true
			break
		}
	}
	if !hasStringKey {
		return false
	}
	if ft.Results == nil || fieldParamCount(ft.Results) == 0 {
		return false
	}
	for _, r := range ft.Results.List {
		if isAnyOrEmptyInterface(r.Type) {
			return true
		}
	}
	return false
}

func genericLookupMethodName(name string) bool {
	if genericLookupMethodNames[name] {
		return true
	}
	// Similarly justified compound names (GetDependency, LookupService, …).
	for token := range genericLookupMethodNames {
		if strings.Contains(name, token) {
			return true
		}
	}
	return false
}

func isAnyOrEmptyInterface(expr ast.Expr) bool {
	expr = unwrapTypeExpr(expr)
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "any"
	case *ast.InterfaceType:
		return t.Methods == nil || len(t.Methods.List) == 0
	default:
		return false
	}
}

func isMapType(expr ast.Expr) bool {
	_, ok := unwrapTypeExpr(expr).(*ast.MapType)
	return ok
}

// fieldNameLooksLikeGenericGetter rejects one-getter-per-dependency bag vocabulary
// on focused HTTP group fields (GetX / LookupX / ResolveX / Dependency).
func fieldNameLooksLikeGenericGetter(name string) bool {
	switch name {
	case "Get", "Lookup", "Resolve", "Dependency", "GetDependency", "LookupService", "ResolveDependency":
		return true
	}
	for _, prefix := range []string{"Get", "Lookup", "Resolve"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return strings.Contains(name, "Dependency") && (strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "Lookup"))
}

func unwrapTypeExpr(expr ast.Expr) ast.Expr {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		default:
			return expr
		}
	}
}

func typeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok && t.Sel != nil {
			return pkg.Name + "." + t.Sel.Name
		}
	case *ast.ArrayType:
		return "[]" + typeString(t.Elt)
	}
	return fmt.Sprintf("%T", expr)
}

func resolvedTypeName(expr ast.Expr, aliases map[string]string, typeAliases map[string]ast.Expr) string {
	expr = unwrapTypeExpr(expr)
	switch t := expr.(type) {
	case *ast.Ident:
		if alt, ok := typeAliases[t.Name]; ok {
			return resolvedTypeName(alt, aliases, typeAliases)
		}
		return t.Name
	case *ast.SelectorExpr:
		if t.Sel != nil {
			return t.Sel.Name
		}
	}
	return ""
}

func resolvesToName(expr ast.Expr, aliases map[string]string, typeAliases map[string]ast.Expr, name string) bool {
	return resolvedTypeName(expr, aliases, typeAliases) == name
}

type structFieldShapeSet struct {
	Types    map[string]string
	Pointers map[string]bool
}

func structFieldShapes(st *ast.StructType) structFieldShapeSet {
	out := structFieldShapeSet{
		Types:    map[string]string{},
		Pointers: map[string]bool{},
	}
	if st == nil || st.Fields == nil {
		return out
	}
	for _, f := range st.Fields.List {
		_, isPtr := f.Type.(*ast.StarExpr)
		tn := resolvedTypeName(f.Type, nil, nil)
		for _, n := range f.Names {
			out.Types[n.Name] = tn
			out.Pointers[n.Name] = isPtr
		}
	}
	return out
}

func isDesiredHTTPGroupType(name string) bool {
	switch name {
	case "StandardHTTPInput", "HTTPCoreInput", "HTTPSecurityInput", "HTTPOperationsInput", "HTTPModelInput", "HTTPFrontendInput":
		return true
	default:
		return false
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stdhttpMountContractSourceDirs are scanned to assemble one aggregate mount
// contract view. Task 3.4 moved the StandardHTTPInput/HTTP*Input group
// declarations into the cycle-neutral internal/stdhttp/contract package;
// root stdhttp holds only aliases and mount/composer function signatures, so
// both directories must be scanned to see the real group shapes and the
// production mount surface together.
var stdhttpMountContractSourceDirs = []string{
	filepath.Join("internal", "stdhttp"),
	filepath.Join("internal", "stdhttp", "contract"),
}

func scanStdhttpMountContract(t *testing.T) mountContractScanResult {
	t.Helper()
	root := repoRoot(t)
	agg := mountContractScanResult{
		DeclaredTypes:              map[string]bool{},
		StandardHTTPFields:         map[string]string{},
		StandardHTTPFieldIsPointer: map[string]bool{},
		GroupFieldNames:            map[string][]string{},
	}
	for _, relDir := range stdhttpMountContractSourceDirs {
		dir := filepath.Join(root, relDir)
		entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, abs := range entries {
			base := filepath.Base(abs)
			if strings.HasSuffix(base, "_test.go") {
				continue
			}
			src, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			rel := filepath.ToSlash(filepath.Join(relDir, base))
			got, err := scanMountContractSource(rel, string(src))
			if err != nil {
				t.Fatalf("scan %s: %v", rel, err)
			}
			agg.Findings = append(agg.Findings, got.Findings...)
			for k, v := range got.DeclaredTypes {
				agg.DeclaredTypes[k] = v
			}
			for k, v := range got.StandardHTTPFields {
				agg.StandardHTTPFields[k] = v
			}
			for k, v := range got.StandardHTTPFieldIsPointer {
				agg.StandardHTTPFieldIsPointer[k] = v
			}
			for k, v := range got.GroupFieldNames {
				agg.GroupFieldNames[k] = v
			}
		}
	}
	sort.Slice(agg.Findings, func(i, j int) bool {
		return agg.Findings[i].String() < agg.Findings[j].String()
	})
	return agg
}
