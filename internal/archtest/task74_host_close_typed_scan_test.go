package archtest

import (
	"go/ast"
	"strings"
)

// Typed/provenance helpers for Task 7.4: Manager, ProcessServices, Coordinator,
// and Host authority are resolved from imports, locals, aliases, nested fields,
// receivers, parameters, globals, factory returns, and structural method-set
// equivalents — not only Host fields or exact imported type names.

func (idx *hcIndex) isManagerType(typ string) bool {
	base := strings.TrimPrefix(typ, "*")
	return idx.managerTypes[base] || idx.managerTypes[typ]
}

func (idx *hcIndex) isProcessType(typ string) bool {
	base := strings.TrimPrefix(typ, "*")
	return idx.processTypes[base] || idx.processTypes[typ]
}

func (idx *hcIndex) isCoordinatorType(typ string) bool {
	base := strings.TrimPrefix(typ, "*")
	return idx.coordinatorTypes[base] || idx.coordinatorTypes[typ]
}

func (idx *hcIndex) authorityKindOfType(typ string) string {
	switch {
	case idx.isHostType(typ):
		return "host"
	case idx.isManagerType(typ):
		return "manager"
	case idx.isProcessType(typ):
		return "process"
	case idx.isCoordinatorType(typ):
		return "coordinator"
	default:
		return ""
	}
}

// authorityKindOfAST classifies a type expression, including inline/named
// interfaces and concrete local method sets.
func (idx *hcIndex) authorityKindOfAST(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch t := expr.(type) {
	case *ast.StarExpr:
		return idx.authorityKindOfAST(t.X)
	case *ast.ParenExpr:
		return idx.authorityKindOfAST(t.X)
	case *ast.InterfaceType:
		return idx.classifyMethodSet(idx.interfaceMethodSet(t, map[string]bool{}))
	case *ast.Ident:
		if kind := idx.authorityKindOfType(t.Name); kind != "" {
			return kind
		}
		if it, ok := idx.interfaces[t.Name]; ok {
			return idx.classifyMethodSet(idx.interfaceMethodSet(it, map[string]bool{}))
		}
		// Concrete renamed equivalents are pre-seeded into the type maps by
		// classifyStructuralEquivalents. Do not reclassify canonical
		// Manager/Coordinator/ProcessServices implementations on the fly —
		// those declaring packages must retain their method bodies.
		return ""
	case *ast.SelectorExpr:
		return idx.authorityKindOfType(hcTypeString(t))
	default:
		return idx.authorityKindOfType(hcTypeString(expr))
	}
}

func hcIsCleanupFuncType(typ string) bool {
	// func()(error) or func(context.Context)(error) — the pre-Host callback shape.
	return strings.HasPrefix(typ, "func(") && strings.Contains(typ, "(error)")
}

// hcIsPreHostRollbackShape reports whether fd is the structural pre-Host
// rollback owner: a package function that receives and invokes multiple
// cleanup callbacks without taking a Host parameter.
func (idx *hcIndex) hcIsPreHostRollbackShape(fd *ast.FuncDecl) bool {
	if fd == nil || fd.Recv != nil || fd.Type == nil || fd.Body == nil || fd.Name == nil {
		return false
	}
	if fd.Type.Params == nil {
		return false
	}
	var cleanupParams []string
	for _, f := range fd.Type.Params.List {
		typ := hcTypeString(f.Type)
		if idx.isHostType(typ) || idx.authorityKindOfAST(f.Type) == "host" {
			return false
		}
		if !hcIsCleanupFuncType(typ) {
			continue
		}
		for _, n := range f.Names {
			if n != nil && n.Name != "_" {
				cleanupParams = append(cleanupParams, n.Name)
			}
		}
	}
	if len(cleanupParams) < 2 {
		return false
	}
	called := map[string]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			for _, name := range cleanupParams {
				if id.Name == name {
					called[name] = true
				}
			}
		}
		return true
	})
	return len(called) >= 2
}

func (idx *hcIndex) callReturnsAuthorityKind(call *ast.CallExpr, fd *ast.FuncDecl, prov *hcProvenance) string {
	if call == nil {
		return ""
	}
	// make/new of a contested type: make([]*Manager, n) is not authority; new(Manager) is.
	if id, ok := call.Fun.(*ast.Ident); ok {
		switch id.Name {
		case "new":
			if len(call.Args) == 1 {
				if kind := idx.authorityKindOfAST(call.Args[0]); kind != "" && kind != "host" {
					return kind
				}
				if kind := idx.authorityKindOfAST(call.Args[0]); kind == "host" {
					return ""
				}
			}
		}
	}
	if typ := idx.callResultType(call, fd, prov); typ != "" {
		if kind := idx.authorityKindOfType(typ); kind != "" && kind != "host" {
			return kind
		}
		base := strings.TrimPrefix(hcUnwrapAuthorityType(typ), "*")
		if kind := idx.authorityKindOfType(base); kind != "" && kind != "host" {
			return kind
		}
		if it, ok := idx.interfaces[base]; ok {
			if kind := idx.classifyMethodSet(idx.interfaceMethodSet(it, map[string]bool{})); kind != "" {
				return kind
			}
		}
	}
	return ""
}

// callResultType returns the concrete first result type of a callee, when known.
func (idx *hcIndex) callResultType(call *ast.CallExpr, fd *ast.FuncDecl, prov *hcProvenance) string {
	if call == nil {
		return ""
	}
	if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "new" && len(call.Args) == 1 {
		return "*" + strings.TrimPrefix(hcTypeString(call.Args[0]), "*")
	}
	var decl *ast.FuncDecl
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		decl = idx.funcs[fun.Name]
	case *ast.SelectorExpr:
		base := idx.namedTypeOf(fun.X, fd, prov)
		if base == "" || fun.Sel == nil {
			return ""
		}
		for _, md := range idx.methods[base] {
			if md.Name != nil && md.Name.Name == fun.Sel.Name {
				decl = md
			}
		}
	}
	if decl == nil || decl.Type == nil || decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return ""
	}
	return hcTypeString(decl.Type.Results.List[0].Type)
}

func (idx *hcIndex) fieldAuthorityKind(typeName, field string) string {
	ft := idx.fieldType(typeName, field, map[string]bool{})
	if ft == "" {
		return ""
	}
	if kind := idx.authorityKindOfType(ft); kind != "" {
		return kind
	}
	// Named field type may be a local structural interface equivalent.
	base := strings.TrimPrefix(ft, "*")
	if it, ok := idx.interfaces[base]; ok {
		return idx.classifyMethodSet(idx.interfaceMethodSet(it, map[string]bool{}))
	}
	return ""
}

// classifyMethodSet maps a method set onto contested shutdown authority.
// Manager: ShutdownDetached(context.Context) error.
// Coordinator: BeginShutdown() plus WaitForIdle(context.Context) error.
// Process runtime: Close() error plus Closed() bool (not bare io.Closer).
func (idx *hcIndex) classifyMethodSet(methods map[string]string) string {
	if methods == nil {
		return ""
	}
	if sig, ok := methods["ShutdownDetached"]; ok && hcSigMatchesContextError(sig) {
		return "manager"
	}
	begin, hasBegin := methods["BeginShutdown"]
	wait, hasWait := methods["WaitForIdle"]
	if hasBegin && hcSigMatchesEmpty(begin) && hasWait && hcSigMatchesContextError(wait) {
		return "coordinator"
	}
	closeSig, hasClose := methods["Close"]
	closedSig, hasClosed := methods["Closed"]
	if hasClose && hcSigMatchesError(closeSig) && hasClosed && hcSigMatchesBool(closedSig) {
		return "process"
	}
	return ""
}

func hcSigMatchesContextError(sig string) bool {
	return sig == "(context.Context)(error)"
}

func hcSigMatchesEmpty(sig string) bool {
	return sig == "()()"
}

func hcSigMatchesError(sig string) bool {
	return sig == "()(error)"
}

func hcSigMatchesBool(sig string) bool {
	return sig == "()(bool)"
}

func (idx *hcIndex) interfaceMethodSet(it *ast.InterfaceType, visiting map[string]bool) map[string]string {
	out := map[string]string{}
	if it == nil || it.Methods == nil {
		return out
	}
	for _, f := range it.Methods.List {
		if len(f.Names) == 0 {
			// Embedded interface (named or inline).
			switch emb := f.Type.(type) {
			case *ast.Ident:
				if visiting[emb.Name] {
					continue
				}
				visiting[emb.Name] = true
				if nested, ok := idx.interfaces[emb.Name]; ok {
					for k, v := range idx.interfaceMethodSet(nested, visiting) {
						out[k] = v
					}
				} else if alias, ok := idx.aliases[emb.Name]; ok {
					base := strings.TrimPrefix(alias, "*")
					if nested, ok := idx.interfaces[base]; ok {
						for k, v := range idx.interfaceMethodSet(nested, visiting) {
							out[k] = v
						}
					}
				}
			case *ast.InterfaceType:
				for k, v := range idx.interfaceMethodSet(emb, visiting) {
					out[k] = v
				}
			case *ast.SelectorExpr:
				// Imported embeddings are unresolved here; local shapes matter most.
			}
			continue
		}
		ft, ok := f.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		sig := hcFuncSignature(ft)
		for _, n := range f.Names {
			if n != nil && n.Name != "_" {
				out[n.Name] = sig
			}
		}
	}
	return out
}

func (idx *hcIndex) concreteMethodSet(typeName string) map[string]string {
	base := strings.TrimPrefix(typeName, "*")
	out := map[string]string{}
	for _, md := range idx.methods[base] {
		if md == nil || md.Name == nil || md.Type == nil {
			continue
		}
		out[md.Name.Name] = hcFuncSignature(md.Type)
	}
	return out
}

// hcIsCanonicalIdentityName reports exact canonical declaration identities.
// Only these names are excluded from renamed-equivalent classification — never
// a whole package that happens to declare them.
func hcIsCanonicalIdentityName(name string) bool {
	switch name {
	case "Manager", "Coordinator", "ProcessServices", "ReloadHost", "Host":
		return true
	default:
		return false
	}
}

// hcUnwrapAuthorityType peels pointers, slices, arrays, maps, chans, and
// one-level generic indexes down to a named type string suitable for
// authority classification (e.g. []*shadowScheduler → shadowScheduler).
func hcUnwrapAuthorityType(typ string) string {
	typ = strings.TrimSpace(typ)
	for typ != "" {
		switch {
		case strings.HasPrefix(typ, "*"):
			typ = strings.TrimPrefix(typ, "*")
		case strings.HasPrefix(typ, "[]"):
			typ = strings.TrimPrefix(typ, "[]")
		case strings.HasPrefix(typ, "..."):
			typ = strings.TrimPrefix(typ, "...")
		case strings.HasPrefix(typ, "chan "):
			typ = strings.TrimPrefix(typ, "chan ")
		case strings.HasPrefix(typ, "<-chan "):
			typ = strings.TrimPrefix(typ, "<-chan ")
		case strings.HasPrefix(typ, "chan<- "):
			typ = strings.TrimPrefix(typ, "chan<- ")
		case strings.HasPrefix(typ, "map["):
			if i := strings.LastIndex(typ, "]"); i >= 0 && i+1 < len(typ) {
				typ = typ[i+1:]
				continue
			}
			return typ
		default:
			if i := strings.Index(typ, "["); i > 0 && strings.HasSuffix(typ, "]") {
				typ = typ[:i]
				continue
			}
			return typ
		}
	}
	return typ
}

// isExactCanonicalType reports whether typ resolves to an exact canonical
// identity (Manager / Coordinator / ProcessServices), including aliases of them
// and imported selectors such as runtimehost.Manager.
func (idx *hcIndex) isExactCanonicalType(typ string) bool {
	base := strings.TrimPrefix(hcUnwrapAuthorityType(typ), "*")
	if base == "" {
		return false
	}
	visited := map[string]bool{}
	for range 8 {
		if visited[base] {
			break
		}
		visited[base] = true
		if hcIsCanonicalIdentityName(base) {
			switch base {
			case "Manager":
				return idx.managerTypes["Manager"]
			case "Coordinator":
				return idx.coordinatorTypes["Coordinator"]
			case "ProcessServices":
				return idx.processTypes["ProcessServices"]
			case "ReloadHost", "Host":
				return idx.isHostType(base)
			}
		}
		// Imported exact identities: alias.Manager / alias.Coordinator / ...
		if i := strings.LastIndex(base, "."); i >= 0 {
			name := base[i+1:]
			pkgAlias := base[:i]
			switch name {
			case "Manager":
				if idx.managerTypes[base] || idx.managerTypes[pkgAlias+".Manager"] {
					return true
				}
			case "Coordinator":
				if idx.coordinatorTypes[base] || idx.coordinatorTypes[pkgAlias+".Coordinator"] {
					return true
				}
			case "ProcessServices":
				if idx.processTypes[base] || idx.processTypes[pkgAlias+".ProcessServices"] {
					return true
				}
			case "ReloadHost", "Host":
				if idx.isHostType(base) {
					return true
				}
			}
		}
		alias, ok := idx.aliases[base]
		if !ok {
			break
		}
		base = strings.TrimPrefix(hcUnwrapAuthorityType(alias), "*")
	}
	return false
}

// isRenamedAuthorityType reports contested shutdown authority that is not an
// exact canonical identity (structural equivalents and their aliases).
func (idx *hcIndex) isRenamedAuthorityType(typ string) bool {
	base := hcUnwrapAuthorityType(typ)
	if base == "" || idx.isExactCanonicalType(base) {
		return false
	}
	kind := idx.authorityKindOfType(base)
	if kind == "" || kind == "host" {
		// Container/generic spellings may not be pre-seeded; unwrap already
		// peeled to the element name — try the raw base maps.
		if idx.isManagerType(base) || idx.isProcessType(base) || idx.isCoordinatorType(base) {
			return !idx.isExactCanonicalType(base)
		}
		return false
	}
	return true
}

// typeHasFieldOfNamedType reports whether typeName declares a field whose
// (possibly container-wrapped) type refers to wantName.
func (idx *hcIndex) typeHasFieldOfNamedType(typeName, wantName string) bool {
	st := idx.structs[strings.TrimPrefix(typeName, "*")]
	if st == nil || st.Fields == nil {
		return false
	}
	want := strings.TrimPrefix(wantName, "*")
	for _, f := range st.Fields.List {
		ft := hcUnwrapAuthorityType(hcTypeString(f.Type))
		ft = strings.TrimPrefix(ft, "*")
		if ft == want {
			return true
		}
		if alias, ok := idx.aliases[ft]; ok {
			if strings.TrimPrefix(hcUnwrapAuthorityType(alias), "*") == want {
				return true
			}
		}
	}
	return false
}

// isCanonicalFocusedOwnedType reports renamed authority that exact canonical
// types may own as focused internals (Coordinator's attempt-gate shape).
func (idx *hcIndex) isCanonicalFocusedOwnedType(typ string) bool {
	base := strings.TrimPrefix(hcUnwrapAuthorityType(typ), "*")
	if base == "" || idx.isExactCanonicalType(base) {
		return false
	}
	kind := idx.authorityKindOfType(base)
	if kind == "" {
		return false
	}
	// Coordinator may own coordinator-shaped focused helpers declared as its fields.
	if kind == "coordinator" && idx.structs["Coordinator"] != nil && idx.coordinatorTypes["Coordinator"] {
		if idx.typeHasFieldOfNamedType("Coordinator", base) {
			return true
		}
	}
	return false
}

// hasMutualAuthorityFieldCoupling reports whether renamed type R and some other
// local struct S store each other — the attemptGate/attemptLease pattern —
// so S is an internal collaborator rather than an independent shutdown owner.
func (idx *hcIndex) hasMutualAuthorityFieldCoupling(renamedType string) bool {
	base := strings.TrimPrefix(hcUnwrapAuthorityType(renamedType), "*")
	if base == "" || !idx.isRenamedAuthorityType(base) {
		return false
	}
	for name, st := range idx.structs {
		if name == base || hcIsCanonicalIdentityName(name) || st == nil {
			continue
		}
		if !idx.typeHasFieldOfNamedType(name, base) {
			continue
		}
		if idx.typeHasFieldOfNamedType(base, name) {
			return true
		}
	}
	return false
}

// allowsRenamedFieldStorage reports whether struct ownerType may store a field
// of renamed authority type fieldType without becoming an independent owner.
func (idx *hcIndex) allowsRenamedFieldStorage(ownerType, fieldType string) bool {
	owner := strings.TrimPrefix(ownerType, "*")
	field := strings.TrimPrefix(hcUnwrapAuthorityType(fieldType), "*")
	if owner == "" || field == "" || !idx.isRenamedAuthorityType(field) {
		return true
	}
	// Exact canonical types may own exact canonical fields (handled elsewhere)
	// and Coordinator may own its focused coordinator-shaped gate field.
	if idx.isExactCanonicalType(owner) {
		if idx.isExactCanonicalType(field) {
			return true
		}
		if owner == "Coordinator" && idx.isCanonicalFocusedOwnedType(field) {
			return true
		}
		// Exact Manager/ProcessServices/Coordinator ordinary fields that are
		// not renamed authority are already non-findings; renamed siblings on
		// Manager/ProcessServices are independent owners.
		return false
	}
	// Internal collaborator of a focused-owned renamed type (attemptLease).
	if idx.isCanonicalFocusedOwnedType(field) && idx.typeHasFieldOfNamedType(field, owner) && idx.typeHasFieldOfNamedType(owner, field) {
		return true
	}
	return false
}

// renamedAuthorityExprType resolves the static type string of an expression
// that carries renamed (non-exact) contested authority, if any.
func (idx *hcIndex) renamedAuthorityExprType(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) string {
	if expr == nil {
		return ""
	}
	if typ := idx.inferExprType(expr, fd, prov); typ != "" && idx.isRenamedAuthorityType(typ) {
		return typ
	}
	if typ := idx.namedTypeOf(expr, fd, prov); typ != "" && idx.isRenamedAuthorityType(typ) {
		return typ
	}
	if id, ok := expr.(*ast.Ident); ok && prov != nil {
		if t := prov.types[id.Name]; t != "" && idx.isRenamedAuthorityType(t) {
			return t
		}
	}
	kind := idx.shutdownKindOf(expr, fd, prov)
	if kind == "" || kind == "tracing" || kind == "host" {
		return ""
	}
	// Kind-only provenance without a concrete renamed type is ambiguous in
	// packages that declare the exact canonical identity (locals of Manager
	// lose their spelling). Do not treat those as renamed.
	if idx.exprIsExactCanonical(expr, fd, prov) {
		return ""
	}
	if idx.declaresCanonicalForKind(kind) {
		return ""
	}
	return kind
}

// exprIsExactCanonical reports whether expr refers to an exact canonical
// Manager/Coordinator/ProcessServices identity (not a renamed equivalent).
func (idx *hcIndex) exprIsExactCanonical(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) bool {
	if typ := idx.inferExprType(expr, fd, prov); typ != "" {
		return idx.isExactCanonicalType(typ)
	}
	if typ := idx.namedTypeOf(expr, fd, prov); typ != "" {
		return idx.isExactCanonicalType(typ)
	}
	if id, ok := expr.(*ast.Ident); ok && prov != nil {
		if t := prov.types[id.Name]; t != "" {
			return idx.isExactCanonicalType(t)
		}
	}
	switch x := expr.(type) {
	case *ast.Ident:
		if t, ok := idx.globals[x.Name]; ok {
			return idx.isExactCanonicalType(t)
		}
		if fd != nil {
			if fd.Recv != nil {
				for _, f := range fd.Recv.List {
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return idx.isExactCanonicalType(hcTypeString(f.Type))
						}
					}
				}
			}
			if fd.Type != nil && fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return idx.isExactCanonicalType(hcTypeString(f.Type))
						}
					}
				}
			}
		}
	case *ast.ParenExpr:
		return idx.exprIsExactCanonical(x.X, fd, prov)
	case *ast.StarExpr:
		return idx.exprIsExactCanonical(x.X, fd, prov)
	case *ast.CallExpr:
		if typ := idx.callResultType(x, fd, prov); typ != "" {
			return idx.isExactCanonicalType(typ)
		}
	}
	return false
}

// funcReturnsExactCanonical reports whether fd's results include an exact
// canonical identity (constructor/wiring functions).
func (idx *hcIndex) funcReturnsExactCanonical(fd *ast.FuncDecl) string {
	if fd == nil || fd.Type == nil || fd.Type.Results == nil {
		return ""
	}
	for _, f := range fd.Type.Results.List {
		typ := hcTypeString(f.Type)
		if idx.isExactCanonicalType(typ) {
			base := strings.TrimPrefix(hcUnwrapAuthorityType(typ), "*")
			if alias, ok := idx.aliases[base]; ok {
				base = strings.TrimPrefix(hcUnwrapAuthorityType(alias), "*")
			}
			if hcIsCanonicalIdentityName(base) {
				return base
			}
		}
	}
	return ""
}

// ownerRecvExactCanonical returns the exact canonical receiver type name for fd.
func (idx *hcIndex) ownerRecvExactCanonical(fd *ast.FuncDecl) string {
	if fd == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	typ := hcTypeString(fd.Recv.List[0].Type)
	if !idx.isExactCanonicalType(typ) {
		return ""
	}
	base := strings.TrimPrefix(hcUnwrapAuthorityType(typ), "*")
	if alias, ok := idx.aliases[base]; ok {
		base = strings.TrimPrefix(hcUnwrapAuthorityType(alias), "*")
	}
	if hcIsCanonicalIdentityName(base) {
		return base
	}
	return ""
}

// allowsRenamedWiringInOwner reports whether renamed authority may appear as
// local ephemeral wiring inside this function (canonical constructors/methods
// or the focused helper type's own methods / mutual collaborators).
func (idx *hcIndex) allowsRenamedWiringInOwner(fd *ast.FuncDecl, renamedTyp string) bool {
	if fd == nil {
		return false
	}
	base := strings.TrimPrefix(hcUnwrapAuthorityType(renamedTyp), "*")
	kindOnly := base == "manager" || base == "process" || base == "coordinator"
	if !idx.isRenamedAuthorityType(renamedTyp) && !kindOnly {
		return false
	}
	recvName := ""
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		recvName = strings.TrimPrefix(hcUnwrapAuthorityType(hcTypeString(fd.Recv.List[0].Type)), "*")
		if alias, ok := idx.aliases[recvName]; ok {
			recvName = strings.TrimPrefix(hcUnwrapAuthorityType(alias), "*")
		}
	}
	if kindOnly {
		switch base {
		case "coordinator":
			if recvName == "Coordinator" || idx.funcReturnsExactCanonical(fd) == "Coordinator" {
				return true
			}
			if recvName != "" && idx.isCanonicalFocusedOwnedType(recvName) {
				return true
			}
			if recvName != "" && idx.hasMutualAuthorityFieldCoupling(recvName) {
				return true
			}
		}
		return false
	}
	focused := idx.isCanonicalFocusedOwnedType(base)
	if recvName != "" {
		if recvName == "Coordinator" && focused {
			return true
		}
		// Focused helper type methods may construct/return themselves.
		if focused && recvName == base {
			return true
		}
		// Mutual collaborators (attemptLease) may wire the focused gate.
		if focused && idx.typeHasFieldOfNamedType(recvName, base) && idx.typeHasFieldOfNamedType(base, recvName) {
			return true
		}
		if idx.isCanonicalFocusedOwnedType(recvName) && (recvName == base || focused) {
			return true
		}
	}
	if ret := idx.funcReturnsExactCanonical(fd); ret == "Coordinator" && focused {
		return true
	}
	// Factory for the focused owned type itself (newAttemptGate).
	if fd.Recv == nil && fd.Type != nil && fd.Type.Results != nil {
		for _, f := range fd.Type.Results.List {
			rt := hcTypeString(f.Type)
			if idx.isCanonicalFocusedOwnedType(rt) &&
				strings.TrimPrefix(hcUnwrapAuthorityType(rt), "*") == base {
				return true
			}
		}
	}
	return false
}

func hcContestedMethodsForKind(kind string) map[string]bool {
	switch kind {
	case "manager":
		return map[string]bool{"ShutdownDetached": true}
	case "coordinator":
		return map[string]bool{"BeginShutdown": true, "WaitForIdle": true}
	case "process":
		return map[string]bool{"Close": true, "Closed": true}
	default:
		return nil
	}
}

// classifyStructuralEquivalents grows manager/process/coordinator type maps
// from local interface and concrete method sets in every package, and records
// contested declaration counts for fail-closed duplicate detection. Exact
// canonical identities are excluded; renamed concretes beside them are not.
func (idx *hcIndex) classifyStructuralEquivalents() {
	mark := func(name, kind string) {
		if name == "" || strings.Contains(name, ".") || kind == "" {
			return
		}
		if hcIsCanonicalIdentityName(name) || idx.isHostType(name) {
			return
		}
		switch kind {
		case "manager":
			idx.managerTypes[name] = true
		case "process":
			idx.processTypes[name] = true
		case "coordinator":
			idx.coordinatorTypes[name] = true
		default:
			return
		}
		key := "type:" + name
		if n := idx.typeDeclCounts[name]; n > idx.contestedDeclCounts[key] {
			idx.contestedDeclCounts[key] = n
		} else if idx.contestedDeclCounts[key] == 0 {
			idx.contestedDeclCounts[key] = 1
		}
		idx.recordStructuralMethodDuplicates(name, kind)
	}
	for name, it := range idx.interfaces {
		if hcIsCanonicalIdentityName(name) {
			continue
		}
		mark(name, idx.classifyMethodSet(idx.interfaceMethodSet(it, map[string]bool{})))
	}
	for name := range idx.structs {
		if hcIsCanonicalIdentityName(name) {
			continue
		}
		mark(name, idx.classifyMethodSet(idx.concreteMethodSet(name)))
	}
	for name, target := range idx.aliases {
		if hcIsCanonicalIdentityName(name) {
			continue
		}
		base := strings.TrimPrefix(target, "*")
		if it, ok := idx.interfaces[base]; ok {
			mark(name, idx.classifyMethodSet(idx.interfaceMethodSet(it, map[string]bool{})))
			continue
		}
		if kind := idx.authorityKindOfType(base); kind != "" && kind != "host" {
			mark(name, kind)
			continue
		}
		if kind := idx.classifyMethodSet(idx.concreteMethodSet(base)); kind != "" {
			mark(name, kind)
		}
	}
}

// recordStructuralMethodDuplicates fails closed when a renamed equivalent
// declares the same contested method more than once (method-set resolution
// would otherwise silently pick one).
func (idx *hcIndex) recordStructuralMethodDuplicates(typeName, kind string) {
	contested := hcContestedMethodsForKind(kind)
	if contested == nil {
		return
	}
	counts := map[string]int{}
	for _, md := range idx.methods[typeName] {
		if md == nil || md.Name == nil {
			continue
		}
		counts[md.Name.Name]++
	}
	for name, n := range counts {
		if !contested[name] || n <= 1 {
			continue
		}
		key := "method:" + typeName + "." + name
		if n > idx.contestedDeclCounts[key] {
			idx.contestedDeclCounts[key] = n
		}
	}
}

// funcLitUsesHostProvenance reports whether a function literal reaches Host or
// Host-derived shutdown authority from its outer provenance.
func (idx *hcIndex) funcLitUsesHostProvenance(lit *ast.FuncLit, outer *hcProvenance) bool {
	if lit == nil {
		return false
	}
	synth := &ast.FuncDecl{Name: ast.NewIdent("lit"), Type: lit.Type, Body: lit.Body}
	prov := idx.buildProvenance(synth, lit.Body, outer)
	found := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.Ident:
			if prov.host[x.Name] || prov.hostDerived[x.Name] {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if x.Sel != nil {
				if _, ok := hcHostShutdownFields[x.Sel.Name]; ok && idx.isHostExpr(x.X, synth, prov) {
					found = true
					return false
				}
			}
			if idx.isHostDerivedAuthority(x, synth, prov) || idx.isHostDerivedAuthority(x.X, synth, prov) {
				found = true
				return false
			}
			if kind := idx.shutdownKindOf(x.X, synth, prov); kind != "" {
				if idx.isHostExpr(x.X, synth, prov) || idx.isHostDerivedAuthority(x.X, synth, prov) {
					found = true
					return false
				}
				// Ident whose kind was seeded from a Host field extraction.
				if id, ok := x.X.(*ast.Ident); ok && prov.hostDerived[id.Name] {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// provenanceHasHost reports whether any complete Host value is in scope.
func provenanceHasHost(prov *hcProvenance) bool {
	if prov == nil {
		return false
	}
	return len(prov.host) > 0
}
