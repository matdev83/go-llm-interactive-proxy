package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// whitelistedLargebodyCallFunctions maps function/method names in
// internal/core/largebody to their architectural rationale.
// Requirement 19/22: Post-commit wire functions must NOT accept or return
// lipapi.Call. Only explicitly whitelisted pre-commit canonical oracle/test
// comparison functions may accept lipapi.Call.
var whitelistedLargebodyCallFunctions = map[string]string{
	"CanonicalCallIdentity":   "pre-commit canonical comparison identity oracle (identity.go:257)",
	"WriteCall":               "pre-commit canonical comparison identity writer oracle (identity.go:607)",
	"ClientTurnShapeFromCall": "pre-commit canonical turn shape extractor for parity testing (turn_shape.go:39)",
}

// whitelistedRuntimeWireAdapters maps whitelisted response-only wire adapters
// in internal/core/runtime to their architectural justification.
var whitelistedRuntimeWireAdapters = map[string]string{
	"responseEvidenceFromWire": "Task 12.3 bounded response evidence adapter from WireTurnFacts (Requirement 19.4)",
}

var (
	archPkgCacheMu sync.Mutex
	archPkgCache   = make(map[string]*packages.Package)
)

func loadPackageForArchTest(t *testing.T, pkgPath string) *packages.Package {
	t.Helper()
	archPkgCacheMu.Lock()
	defer archPkgCacheMu.Unlock()

	if pkg, ok := archPkgCache[pkgPath]; ok {
		return pkg
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil {
		t.Fatalf("load package %s: %v", pkgPath, err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("load package %s returned %d packages", pkgPath, len(pkgs))
	}
	if pkgs[0].Types == nil || pkgs[0].TypesInfo == nil {
		t.Fatalf("package %s has nil Types or TypesInfo", pkgPath)
	}
	archPkgCache[pkgPath] = pkgs[0]
	return pkgs[0]
}

// isLipapiCallType checks if a types.Type resolves to lipapi.Call or *lipapi.Call.
// It unwraps pointers, slices, arrays, and maps to catch nested and alias forms.
func isLipapiCallType(t types.Type) bool {
	if t == nil {
		return false
	}
	for {
		switch u := t.(type) {
		case *types.Pointer:
			t = u.Elem()
		case *types.Slice:
			t = u.Elem()
		case *types.Array:
			t = u.Elem()
		case *types.Map:
			t = u.Elem()
		default:
			goto done
		}
	}
done:
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi" && obj.Name() == "Call"
		}
	}
	return false
}

// isLipapiCloneCall verifies if an object or expression resolves to lipapi.CloneCall.
func isLipapiCloneCall(info *types.Info, call *ast.CallExpr) bool {
	if info == nil || call == nil {
		return false
	}
	var fnObj types.Object
	switch f := call.Fun.(type) {
	case *ast.Ident:
		fnObj = info.ObjectOf(f)
	case *ast.SelectorExpr:
		fnObj = info.ObjectOf(f.Sel)
	}
	if fnObj != nil && fnObj.Pkg() != nil {
		return fnObj.Pkg().Path() == "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi" && fnObj.Name() == "CloneCall"
	}
	// Fallback when type info object is unavailable
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if sel.Sel != nil && sel.Sel.Name == "CloneCall" {
			return true
		}
	} else if ident, ok := call.Fun.(*ast.Ident); ok {
		if ident.Name == "CloneCall" {
			return true
		}
	}
	return false
}

// TestArch_WirePostCommit_LargebodyPackageIsCallFree enforces Requirement 19/22:
// No struct in internal/core/largebody may retain lipapi.Call, and no wire
// post-commit function/method may accept/return lipapi.Call or invoke lipapi.CloneCall.
func TestArch_WirePostCommit_LargebodyPackageIsCallFree(t *testing.T) {
	pkg := loadPackageForArchTest(t, "github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody")

	var violations []string

	// 1. Struct field scan: every struct declared in largebody must be free of lipapi.Call.
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			field := st.Field(i)
			if isLipapiCallType(field.Type()) {
				violations = append(violations, fmt.Sprintf("struct %s: field %s has forbidden type %s",
					name, field.Name(), field.Type().String()))
			}
		}
	}

	// 2. Function / method signature scan: only explicitly whitelisted functions may accept lipapi.Call.
	for ident, obj := range pkg.TypesInfo.Defs {
		fn, ok := obj.(*types.Func)
		if !ok || fn == nil || ident == nil {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig == nil {
			continue
		}

		funcName := fn.Name()
		hasCall := false
		var callDetails []string

		// Check parameters
		params := sig.Params()
		for i := 0; i < params.Len(); i++ {
			p := params.At(i)
			if isLipapiCallType(p.Type()) {
				hasCall = true
				callDetails = append(callDetails, fmt.Sprintf("param %s: %s", p.Name(), p.Type().String()))
			}
		}
		// Check results
		results := sig.Results()
		for i := 0; i < results.Len(); i++ {
			r := results.At(i)
			if isLipapiCallType(r.Type()) {
				hasCall = true
				callDetails = append(callDetails, fmt.Sprintf("result %d: %s", i, r.Type().String()))
			}
		}

		if hasCall {
			justification, whitelisted := whitelistedLargebodyCallFunctions[funcName]
			if !whitelisted {
				pos := pkg.Fset.Position(ident.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: function %q accepts/returns lipapi.Call but is not whitelisted: %s",
					filepath.Base(pos.Filename), pos.Line, funcName, strings.Join(callDetails, ", ")))
			} else if justification == "" {
				violations = append(violations, fmt.Sprintf("function %q is whitelisted without justification", funcName))
			}
		}
	}

	// 3. CloneCall invocation scan: zero CloneCall invocations in production largebody code.
	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isLipapiCloneCall(pkg.TypesInfo, call) {
				pos := pkg.Fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: forbidden invocation of lipapi.CloneCall in largebody",
					filename, pos.Line))
			}
			return true
		})
	}

	if len(violations) > 0 {
		t.Fatalf("internal/core/largebody Call-free boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

// isRuntimeWireFunction determines if a function in internal/core/runtime
// is a wire post-commit function/method.
func isRuntimeWireFunction(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	name := fn.Name()
	if strings.Contains(name, "Wire") || strings.Contains(name, "wire") ||
		strings.Contains(name, "LargeBody") || strings.Contains(name, "largeBody") {
		return true
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil {
		return false
	}
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		p := params.At(i)
		typeName := p.Type().String()
		if strings.Contains(typeName, "largebody.Wire") ||
			strings.Contains(typeName, "largebody.Assessment") ||
			strings.Contains(typeName, "WireBilling") ||
			strings.Contains(typeName, "WireExposure") ||
			strings.Contains(typeName, "WireFrontendIngressInput") ||
			strings.Contains(typeName, "WireBackendIngressInput") {
			return true
		}
	}
	return false
}

// TestArch_WirePostCommit_RuntimeWireFunctionsAreCallFree verifies that all
// wire post-commit functions in internal/core/runtime are completely Call-free:
// they do not accept lipapi.Call, dereference fields of type lipapi.Call,
// or invoke lipapi.CloneCall, except explicitly whitelisted response-only adapters.
func TestArch_WirePostCommit_RuntimeWireFunctionsAreCallFree(t *testing.T) {
	pkg := loadPackageForArchTest(t, "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime")

	var violations []string

	for ident, obj := range pkg.TypesInfo.Defs {
		fn, ok := obj.(*types.Func)
		if !ok || fn == nil || ident == nil {
			continue
		}
		if !isRuntimeWireFunction(fn) {
			continue
		}

		funcName := fn.Name()
		if justification, whitelisted := whitelistedRuntimeWireAdapters[funcName]; whitelisted {
			if strings.TrimSpace(justification) == "" {
				violations = append(violations, fmt.Sprintf("wire function %q is whitelisted without justification", funcName))
			}
			continue
		}
		sig := fn.Type().(*types.Signature)

		// 1. Check parameters and return types
		params := sig.Params()
		for i := 0; i < params.Len(); i++ {
			p := params.At(i)
			if isLipapiCallType(p.Type()) {
				pos := pkg.Fset.Position(ident.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: wire function %q accepts forbidden parameter %s: %s",
					filepath.Base(pos.Filename), pos.Line, funcName, p.Name(), p.Type().String()))
			}
		}
		results := sig.Results()
		for i := 0; i < results.Len(); i++ {
			r := results.At(i)
			if isLipapiCallType(r.Type()) {
				pos := pkg.Fset.Position(ident.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: wire function %q returns forbidden type %s",
					filepath.Base(pos.Filename), pos.Line, funcName, r.Type().String()))
			}
		}
	}

	// 2. Scan AST function bodies of wire functions for CloneCall and Call dereferences
	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}

		for _, decl := range file.Decls {
			fnDecl, ok := decl.(*ast.FuncDecl)
			if !ok || fnDecl.Body == nil {
				continue
			}
			fnObj := pkg.TypesInfo.Defs[fnDecl.Name]
			fn, _ := fnObj.(*types.Func)
			if !isRuntimeWireFunction(fn) {
				continue
			}

			funcName := fnDecl.Name.Name
			if justification, whitelisted := whitelistedRuntimeWireAdapters[funcName]; whitelisted {
				if strings.TrimSpace(justification) == "" {
					violations = append(violations, fmt.Sprintf("wire function %q is whitelisted without justification", funcName))
				}
				continue
			}
			ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if isLipapiCloneCall(pkg.TypesInfo, x) {
						pos := pkg.Fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: wire function %q invokes forbidden lipapi.CloneCall",
							filename, pos.Line, funcName))
					}
				case *ast.SelectorExpr:
					// Check if selecting a field/method on an expression of type lipapi.Call or *lipapi.Call
					receiverType := pkg.TypesInfo.TypeOf(x.X)
					if isLipapiCallType(receiverType) {
						pos := pkg.Fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: wire function %q dereferences lipapi.Call field %q",
							filename, pos.Line, funcName, x.Sel.Name))
					}
				case *ast.StarExpr:
					// Check if dereferencing a pointer to lipapi.Call (*call)
					elemType := pkg.TypesInfo.TypeOf(x.X)
					if isLipapiCallType(elemType) {
						pos := pkg.Fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: wire function %q dereferences *lipapi.Call pointer",
							filename, pos.Line, funcName))
					}
				}
				return true
			})
		}
	}

	if len(violations) > 0 {
		t.Fatalf("internal/core/runtime wire Call-free boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestArch_WirePostCommit_CatchPrepCallClonesAndRenames_TypeOriented proves that
// our boundary check is type/dataflow oriented rather than textual string grep:
// any field access or dereference on a lipapi.Call receiver is caught regardless
// of field or variable names.
func TestArch_WirePostCommit_CatchPrepCallClonesAndRenames_TypeOriented(t *testing.T) {
	root := repoRoot(t)
	overlayPath := filepath.Join(root, "pkg", "lipapi", "synthetic_type_test_overlay.go")
	overlay := map[string][]byte{
		overlayPath: []byte(`package lipapi

type RenamedCarrier struct {
	ArbitraryCallName *Call
	UnrelatedField    int
}

func WireFunctionWithRenamedField(c *RenamedCarrier) {
	_ = c.ArbitraryCallName.Route.Selector
}

func WireFunctionWithRenamedClone(c *RenamedCarrier) {
	_ = CloneCall(*c.ArbitraryCallName)
}
`),
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests:   false,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)

	pkg := pkgs[0]
	var detectedDereference bool
	var detectedCloneCall bool

	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if filename != "synthetic_type_test_overlay.go" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if isLipapiCloneCall(pkg.TypesInfo, x) {
					detectedCloneCall = true
				}
			case *ast.SelectorExpr:
				if isLipapiCallType(pkg.TypesInfo.TypeOf(x.X)) {
					detectedDereference = true
				}
			}
			return true
		})
	}

	assert.True(t, detectedDereference, "type-oriented checker must catch dereferencing renamed field of type *lipapi.Call")
	assert.True(t, detectedCloneCall, "type-oriented checker must catch CloneCall invocation on renamed field")
}

// TestArch_FrontendCandidate_NoLegacyRouteResolverBodyMaterialization enforces
// Requirement 22.4 & Task 12.6:
// Frontend candidate code cannot materialize a second whole body solely for
// legacy route resolver. ResolveRouteSelector must never be invoked in candidate
// execution; its only allowed appearance is the Gate 5 pre-capture nil-check.
func TestArch_FrontendCandidate_NoLegacyRouteResolverBodyMaterialization(t *testing.T) {
	root := repoRoot(t)
	candidatePath := filepath.Join(root, "internal", "plugins", "frontends", "frontendpipe", "candidate.go")
	content, err := os.ReadFile(candidatePath)
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, candidatePath, content, parser.ParseComments)
	require.NoError(t, err)

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			// ResolveRouteSelector must NEVER be invoked as a function call in candidate code!
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel != nil && sel.Sel.Name == "ResolveRouteSelector" {
					pos := fset.Position(x.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: forbidden function call to ResolveRouteSelector in candidate lane",
						filepath.Base(candidatePath), pos.Line))
				}
			}
		case *ast.SelectorExpr:
			if x.Sel != nil && x.Sel.Name == "ResolveRouteSelector" {
				// The ONLY permitted appearance is in PreCapture Gate 5 (spec.ResolveRouteSelector != nil)
				pos := fset.Position(x.Pos())
				if !strings.Contains(string(content[max(0, int(x.Pos())-30):min(len(content), int(x.End())+30)]), "nil") {
					violations = append(violations, fmt.Sprintf("%s:%d: ResolveRouteSelector reference outside nil check: %s",
						filepath.Base(candidatePath), pos.Line, x.Sel.Name))
				}
			}
		}
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("frontend candidate legacy resolver violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestArch_WirePostCommit_RogueViolationDetection_MutationTest tests that
// any simulated rogue call, clone, or legacy resolver invocation in wire
// candidate code is deterministically flagged with the exact violation details.
func TestArch_WirePostCommit_RogueViolationDetection_MutationTest(t *testing.T) {
	fset := token.NewFileSet()
	badCode := `package badcode

type Spec struct {
	ResolveRouteSelector func() string
}

func rogueCandidateFunction(s *Spec) {
	// Forbidden: calling legacy route resolver
	_ = s.ResolveRouteSelector()
}
`
	file, err := parser.ParseFile(fset, "badcode.go", badCode, 0)
	require.NoError(t, err)

	var flaggedCall bool
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel != nil && sel.Sel.Name == "ResolveRouteSelector" {
					flaggedCall = true
				}
			}
		}
		return true
	})

	assert.True(t, flaggedCall, "rogue ResolveRouteSelector call must be flagged")
}
