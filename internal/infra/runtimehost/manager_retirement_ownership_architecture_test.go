package runtimehost

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Task 7.3 architecture gate (hardened): Manager is the sole production
// scheduler/caller of retireGeneration. The focused retirement policy call
// graph is rooted at retireGeneration (not every Manager method). Detection
// follows Generation provenance through aliases, method/function values,
// storage escapes, package globals/closures, callbacks, factories, nested
// structs, maps/slices/pointers/generic containers, make/new inference, and
// package-local helper call graphs. QuiesceCloser shape includes concrete
// and aliased method sets. Contested duplicate type/func declarations fail
// closed. Role/shape based — not an exact-name blocklist.
//
// Complements (does not replace) the DuplicateOnce lifecycle-owner detector
// in package archtest.

type retirementOwnershipFinding struct {
	Type   string
	Detail string
}

func (f retirementOwnershipFinding) String() string {
	return fmt.Sprintf("%s: %s", f.Type, f.Detail)
}

func retirementFindingsHave(findings []retirementOwnershipFinding, typ string) bool {
	for _, f := range findings {
		if f.Type == typ || strings.Contains(f.Type, typ) || strings.Contains(f.Detail, typ) {
			return true
		}
	}
	return false
}

type retirementOwnershipIndex struct {
	aliases        map[string]string
	structs        map[string]*ast.StructType
	interfaces     map[string]*ast.InterfaceType
	typeMethods    map[string][]*ast.FuncDecl
	pkgFuncs       map[string]*ast.FuncDecl // package-level func name -> decl
	typeDeclCounts map[string]int           // fail-closed duplicate type tracking
	funcDeclCounts map[string]int           // fail-closed duplicate package-func tracking
	pkgGlobalTypes map[string]string        // package-global name -> type string
	genGlobals     map[string]bool          // package globals holding Generation (direct/nested/container)
}

func buildRetirementOwnershipIndex(files map[string]*ast.File) *retirementOwnershipIndex {
	idx := &retirementOwnershipIndex{
		aliases:        map[string]string{},
		structs:        map[string]*ast.StructType{},
		interfaces:     map[string]*ast.InterfaceType{},
		typeMethods:    map[string][]*ast.FuncDecl{},
		pkgFuncs:       map[string]*ast.FuncDecl{},
		typeDeclCounts: map[string]int{},
		funcDeclCounts: map[string]int{},
		pkgGlobalTypes: map[string]string{},
		genGlobals:     map[string]bool{},
	}
	// Deterministic file order so contested first-wins indexing is stable.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file := files[name]
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					tname := ts.Name.Name
					idx.typeDeclCounts[tname]++
					if idx.typeDeclCounts[tname] > 1 {
						// Contested: keep first indexed shape; duplicate finding fails closed.
						continue
					}
					switch t := ts.Type.(type) {
					case *ast.StructType:
						idx.structs[tname] = t
					case *ast.InterfaceType:
						idx.interfaces[tname] = t
					default:
						// Aliases and any other named type used in ownership resolution.
						idx.aliases[tname] = retTypeString(ts.Type)
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) == 0 {
					if d.Name != nil {
						fname := d.Name.Name
						idx.funcDeclCounts[fname]++
						if idx.funcDeclCounts[fname] == 1 {
							idx.pkgFuncs[fname] = d
						}
					}
					continue
				}
				recv := strings.TrimPrefix(retRecvTypeName(d.Recv.List[0].Type), "*")
				idx.typeMethods[recv] = append(idx.typeMethods[recv], d)
			}
		}
	}
	for range 8 {
		changed := false
		for k, v := range idx.aliases {
			base := strings.TrimPrefix(v, "*")
			if next, ok := idx.aliases[base]; ok && next != v {
				if strings.HasPrefix(v, "*") && !strings.HasPrefix(next, "*") {
					next = "*" + next
				}
				idx.aliases[k] = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return idx
}

func (idx *retirementOwnershipIndex) resolve(typ string) string {
	seen := map[string]bool{}
	for {
		if seen[typ] {
			return typ
		}
		seen[typ] = true
		base := strings.TrimPrefix(typ, "*")
		next, ok := idx.aliases[base]
		if !ok {
			return typ
		}
		if strings.HasPrefix(typ, "*") && !strings.HasPrefix(next, "*") {
			next = "*" + next
		}
		typ = next
	}
}

func analyzeRetirementOwnership(files map[string]*ast.File) []retirementOwnershipFinding {
	idx := buildRetirementOwnershipIndex(files)
	var findings []retirementOwnershipFinding

	// Fail closed on contested production declarations that affect ownership resolution.
	dupTypes := make([]string, 0)
	for name, n := range idx.typeDeclCounts {
		if n > 1 {
			dupTypes = append(dupTypes, name)
		}
	}
	sort.Strings(dupTypes)
	for _, name := range dupTypes {
		findings = append(findings, retirementOwnershipFinding{
			Type:   name,
			Detail: fmt.Sprintf("duplicate production type declaration (%d); fail closed", idx.typeDeclCounts[name]),
		})
	}
	dupFuncs := make([]string, 0)
	for name, n := range idx.funcDeclCounts {
		if n > 1 {
			dupFuncs = append(dupFuncs, name)
		}
	}
	sort.Strings(dupFuncs)
	for _, name := range dupFuncs {
		findings = append(findings, retirementOwnershipFinding{
			Type:   name,
			Detail: fmt.Sprintf("duplicate production package function declaration (%d); fail closed", idx.funcDeclCounts[name]),
		})
	}

	findings = append(findings, idx.findRetireGenerationEscapes(files)...)
	findings = append(findings, idx.findCleanupPolicyOwners(files)...)
	findings = append(findings, idx.findRetirementStatusStorage(files)...)
	findings = append(findings, idx.findGenerationGlobalStorage(files)...)
	findings = append(findings, idx.findExternalQuiesceCloserCollaborators(files)...)
	findings = append(findings, idx.findNonManagerRetirementWorkflows(files)...)

	return findings
}

// ownerKey identifies a function/method for call-graph / escape attribution.
func retOwnerKey(fd *ast.FuncDecl) string {
	if fd == nil || fd.Name == nil {
		return ""
	}
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		recv := strings.TrimPrefix(retRecvTypeName(fd.Recv.List[0].Type), "*")
		return recv + "." + fd.Name.Name
	}
	return fd.Name.Name
}

func retIsManagerMethod(fd *ast.FuncDecl) bool {
	if fd == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	return strings.TrimPrefix(retRecvTypeName(fd.Recv.List[0].Type), "*") == "Manager"
}

// findRetireGenerationEscapes rejects any reference/storage/pass/call of
// retireGeneration outside its declaration that is not inside a Manager method.
func (idx *retirementOwnershipIndex) findRetireGenerationEscapes(files map[string]*ast.File) []retirementOwnershipFinding {
	var findings []retirementOwnershipFinding

	report := func(owner, detail string) {
		findings = append(findings, retirementOwnershipFinding{Type: owner, Detail: detail})
	}

	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name != nil && d.Name.Name == "retireGeneration" && d.Recv == nil {
					// Declaration name itself is not an escape; still scan body
					// for odd self-aliasing if needed (none expected).
					continue
				}
				if retIsManagerMethod(d) {
					continue
				}
				owner := retOwnerKey(d)
				if d.Body == nil {
					continue
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					id, ok := n.(*ast.Ident)
					if !ok || id.Name != "retireGeneration" {
						return true
					}
					report(owner, "references retireGeneration outside a Manager method (alias/call/callback/storage escape)")
					return false
				})
			case *ast.GenDecl:
				if d.Tok != token.VAR && d.Tok != token.CONST {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					names := make([]string, 0, len(vs.Names))
					for _, n := range vs.Names {
						if n != nil {
							names = append(names, n.Name)
						}
					}
					owner := "global:" + strings.Join(names, ",")
					ast.Inspect(vs, func(n ast.Node) bool {
						id, ok := n.(*ast.Ident)
						if !ok || id.Name != "retireGeneration" {
							return true
						}
						report(owner, "package-global references/stores retireGeneration (escape outside Manager)")
						return false
					})
				}
			}
		}
	}
	return findings
}

// findCleanupPolicyOwners rejects CleanupPolicy fields/globals outside Manager
// and outside retireGeneration's parameter list (locals of retireGeneration are
// not fields). Nested non-Manager holders are rejected.
func (idx *retirementOwnershipIndex) findCleanupPolicyOwners(files map[string]*ast.File) []retirementOwnershipFinding {
	var findings []retirementOwnershipFinding

	for name, st := range idx.structs {
		if name == "Manager" || name == "CleanupPolicy" {
			continue
		}
		// Direct or nested CleanupPolicy ownership, but do not treat a *Manager
		// field as nested policy ownership (Manager is the canonical owner).
		if idx.structHasFieldOfType(st, "CleanupPolicy", map[string]bool{"Manager": true}) {
			findings = append(findings, retirementOwnershipFinding{
				Type:   name,
				Detail: "non-Manager struct owns CleanupPolicy (retirement policy ownership is Manager field / retireGeneration param only)",
			})
		}
	}

	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				hold := false
				if vs.Type != nil && idx.typeMentionsCleanupPolicy(vs.Type) {
					hold = true
				}
				for _, v := range vs.Values {
					if idx.exprInfersNamedHold(v, "CleanupPolicy") || idx.exprMentionsCleanupPolicyComposite(v) {
						hold = true
					}
				}
				if !hold {
					continue
				}
				n := "embedded"
				if len(vs.Names) > 0 {
					n = vs.Names[0].Name
				}
				findings = append(findings, retirementOwnershipFinding{
					Type:   "global:" + n,
					Detail: "package-global CleanupPolicy owner",
				})
			}
		}
	}
	return findings
}

func (idx *retirementOwnershipIndex) typeMentionsCleanupPolicy(expr ast.Expr) bool {
	resolved := idx.resolve(retTypeString(expr))
	base := strings.TrimPrefix(resolved, "*")
	if base == "CleanupPolicy" {
		return true
	}
	switch t := expr.(type) {
	case *ast.ArrayType:
		return idx.typeMentionsCleanupPolicy(t.Elt)
	case *ast.MapType:
		return idx.typeMentionsCleanupPolicy(t.Key) || idx.typeMentionsCleanupPolicy(t.Value)
	case *ast.IndexExpr:
		return idx.typeMentionsCleanupPolicy(t.Index) || idx.typeMentionsCleanupPolicy(t.X)
	case *ast.IndexListExpr:
		if idx.typeMentionsCleanupPolicy(t.X) {
			return true
		}
		if slices.ContainsFunc(t.Indices, idx.typeMentionsCleanupPolicy) {
			return true
		}
	}
	return false
}

func (idx *retirementOwnershipIndex) exprMentionsCleanupPolicyComposite(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.CompositeLit:
		if t.Type != nil && idx.typeMentionsCleanupPolicy(t.Type) {
			return true
		}
		if slices.ContainsFunc(t.Elts, idx.exprMentionsCleanupPolicyComposite) {
			return true
		}
	case *ast.KeyValueExpr:
		return idx.exprMentionsCleanupPolicyComposite(t.Value)
	case *ast.UnaryExpr:
		return idx.exprMentionsCleanupPolicyComposite(t.X)
	case *ast.ParenExpr:
		return idx.exprMentionsCleanupPolicyComposite(t.X)
	}
	return false
}

// exprInfersNamedHold resolves make/new/conversion/composite/address/alias
// initializer expressions that infer a named hold of wantType.
func (idx *retirementOwnershipIndex) exprInfersNamedHold(expr ast.Expr, wantType string) bool {
	if expr == nil {
		return false
	}
	switch t := expr.(type) {
	case *ast.CallExpr:
		switch fun := t.Fun.(type) {
		case *ast.Ident:
			switch fun.Name {
			case "make":
				if len(t.Args) >= 1 && idx.fieldTypeHoldsNamed(t.Args[0], wantType, map[string]bool{}) {
					return true
				}
			case "new":
				if len(t.Args) == 1 && idx.fieldTypeHoldsNamed(t.Args[0], wantType, map[string]bool{}) {
					return true
				}
			default:
				// Type conversion: CleanupPolicy(x) / RetirementStatus(x)
				if idx.fieldTypeHoldsNamed(fun, wantType, map[string]bool{}) {
					return true
				}
			}
		default:
			// Conversion with non-ident type expression.
			if idx.fieldTypeHoldsNamed(t.Fun, wantType, map[string]bool{}) {
				return true
			}
		}
	case *ast.CompositeLit:
		if t.Type != nil && idx.fieldTypeHoldsNamed(t.Type, wantType, map[string]bool{}) {
			return true
		}
		for _, elt := range t.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if idx.exprInfersNamedHold(kv.Value, wantType) {
					return true
				}
				continue
			}
			if idx.exprInfersNamedHold(elt, wantType) {
				return true
			}
		}
	case *ast.UnaryExpr:
		if t.Op == token.AND {
			return idx.exprInfersNamedHold(t.X, wantType)
		}
	case *ast.ParenExpr:
		return idx.exprInfersNamedHold(t.X, wantType)
	case *ast.StarExpr:
		return idx.exprInfersNamedHold(t.X, wantType)
	case *ast.Ident:
		// Alias to a package-level name is not followed here; typed/inferred
		// make/new/composite forms cover the required evasions.
		return false
	}
	return false
}

func (idx *retirementOwnershipIndex) structHasFieldOfType(st *ast.StructType, wantType string, visiting map[string]bool) bool {
	if st == nil || st.Fields == nil {
		return false
	}
	for _, f := range st.Fields.List {
		if idx.fieldTypeHoldsNamed(f.Type, wantType, visiting) {
			return true
		}
	}
	return false
}

func (idx *retirementOwnershipIndex) fieldTypeHoldsNamed(expr ast.Expr, wantType string, visiting map[string]bool) bool {
	resolved := idx.resolve(retTypeString(expr))
	base := strings.TrimPrefix(resolved, "*")
	if base == wantType {
		return true
	}
	switch t := expr.(type) {
	case *ast.ArrayType:
		return idx.fieldTypeHoldsNamed(t.Elt, wantType, visiting)
	case *ast.MapType:
		return idx.fieldTypeHoldsNamed(t.Key, wantType, visiting) || idx.fieldTypeHoldsNamed(t.Value, wantType, visiting)
	case *ast.IndexExpr:
		return idx.fieldTypeHoldsNamed(t.Index, wantType, visiting) || idx.fieldTypeHoldsNamed(t.X, wantType, visiting)
	case *ast.IndexListExpr:
		if idx.fieldTypeHoldsNamed(t.X, wantType, visiting) {
			return true
		}
		for _, ind := range t.Indices {
			if idx.fieldTypeHoldsNamed(ind, wantType, visiting) {
				return true
			}
		}
		return false
	}
	if nested, ok := idx.structs[base]; ok && !visiting[base] {
		visiting[base] = true
		found := idx.structHasFieldOfType(nested, wantType, visiting)
		delete(visiting, base)
		return found
	}
	return false
}

// findRetirementStatusStorage rejects persistent RetirementStatus storage:
// direct/nested fields, map/slice/pointer/generic containers, package globals,
// and aliases. Per-attempt locals/returns remain allowed.
func (idx *retirementOwnershipIndex) findRetirementStatusStorage(files map[string]*ast.File) []retirementOwnershipFinding {
	var findings []retirementOwnershipFinding

	for name, st := range idx.structs {
		if st.Fields == nil {
			continue
		}
		for _, f := range st.Fields.List {
			if !idx.fieldTypeHoldsNamed(f.Type, "RetirementStatus", map[string]bool{}) {
				continue
			}
			fieldNames := "embedded"
			if len(f.Names) > 0 {
				names := make([]string, 0, len(f.Names))
				for _, n := range f.Names {
					names = append(names, n.Name)
				}
				fieldNames = strings.Join(names, ",")
			}
			findings = append(findings, retirementOwnershipFinding{
				Type:   name,
				Detail: fmt.Sprintf("field %s persistently stores RetirementStatus (fresh per-attempt return/local only)", fieldNames),
			})
		}
	}

	for _, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				hold := false
				if vs.Type != nil && idx.fieldTypeHoldsNamed(vs.Type, "RetirementStatus", map[string]bool{}) {
					hold = true
				}
				for _, v := range vs.Values {
					if idx.exprInfersNamedHold(v, "RetirementStatus") {
						hold = true
					}
				}
				if !hold {
					continue
				}
				n := "embedded"
				if len(vs.Names) > 0 {
					n = vs.Names[0].Name
				}
				findings = append(findings, retirementOwnershipFinding{
					Type:   "global:" + n,
					Detail: "package-global RetirementStatus storage",
				})
			}
		}
	}
	return findings
}

// findGenerationGlobalStorage rejects package-global Generation ownership:
// direct *Generation, aliases, and nested/container-shaped holders. Seeds
// genGlobals/pkgGlobalTypes so references like global.Close() and holder.g.BeginClose()
// are treated as Generation provenance. Manager's active field is not a package global.
func (idx *retirementOwnershipIndex) findGenerationGlobalStorage(files map[string]*ast.File) []retirementOwnershipFinding {
	var findings []retirementOwnershipFinding

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		file := files[name]
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				// Compile-time interface assertions (var _ T = ...) are not storage.
				onlyBlank := len(vs.Names) > 0
				for _, n := range vs.Names {
					if n != nil && n.Name != "_" {
						onlyBlank = false
						break
					}
				}
				if onlyBlank {
					continue
				}
				hold := false
				typeStr := ""
				if vs.Type != nil {
					typeStr = retTypeString(vs.Type)
					if idx.fieldTypeHoldsNamed(vs.Type, "Generation", map[string]bool{}) {
						hold = true
					}
				}
				for _, v := range vs.Values {
					if idx.exprInfersNamedHold(v, "Generation") {
						hold = true
						if typeStr == "" {
							typeStr = idx.inferExprTypeString(v)
						}
					}
				}
				if typeStr == "" && vs.Type != nil {
					typeStr = retTypeString(vs.Type)
				}
				for _, n := range vs.Names {
					if n == nil || n.Name == "" || n.Name == "_" {
						continue
					}
					if typeStr != "" {
						idx.pkgGlobalTypes[n.Name] = typeStr
					}
					if hold {
						idx.genGlobals[n.Name] = true
					}
				}
				if !hold {
					continue
				}
				n := "embedded"
				if len(vs.Names) > 0 && vs.Names[0] != nil && vs.Names[0].Name != "_" {
					n = vs.Names[0].Name
				}
				findings = append(findings, retirementOwnershipFinding{
					Type:   "global:" + n,
					Detail: "package-global Generation storage/provenance (direct/nested/aliased/container)",
				})
			}
		}
	}
	return findings
}

func (idx *retirementOwnershipIndex) inferExprTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.CompositeLit:
		if t.Type != nil {
			return retTypeString(t.Type)
		}
	case *ast.CallExpr:
		switch fun := t.Fun.(type) {
		case *ast.Ident:
			switch fun.Name {
			case "new":
				if len(t.Args) == 1 {
					return "*" + retTypeString(t.Args[0])
				}
			case "make":
				if len(t.Args) >= 1 {
					return retTypeString(t.Args[0])
				}
			default:
				return retTypeString(fun)
			}
		default:
			return retTypeString(t.Fun)
		}
	case *ast.UnaryExpr:
		if t.Op == token.AND {
			inner := idx.inferExprTypeString(t.X)
			if inner != "" {
				return "*" + strings.TrimPrefix(inner, "*")
			}
		}
	case *ast.ParenExpr:
		return idx.inferExprTypeString(t.X)
	case *ast.StarExpr:
		return "*" + idx.inferExprTypeString(t.X)
	}
	return ""
}

// isQuiesceCloserShape reports whether typ resolves to an interface OR a
// concrete/aliased named type whose method set includes Close() error and
// Quiesce(context.Context) error (exact name QuiesceCloser not required).
func (idx *retirementOwnershipIndex) isQuiesceCloserShape(typ string) bool {
	base := strings.TrimPrefix(idx.resolve(typ), "*")
	methods := idx.collectTypeMethodSet(base, map[string]bool{})
	hasClose, hasQuiesce := false, false
	for name, sig := range methods {
		switch name {
		case "Close":
			hasClose = retSigIsClose(sig)
		case "Quiesce":
			hasQuiesce = retSigIsQuiesce(sig)
		}
	}
	return hasClose && hasQuiesce
}

func (idx *retirementOwnershipIndex) collectTypeMethodSet(name string, visiting map[string]bool) map[string]*ast.FuncType {
	out := map[string]*ast.FuncType{}
	if name == "" || visiting[name] {
		return out
	}
	if iface, ok := idx.interfaces[name]; ok {
		return idx.collectIfaceMethods(name, iface, visiting)
	}
	visiting[name] = true

	// Concrete named struct (or empty named type): declared methods + embedded.
	for _, md := range idx.typeMethods[name] {
		if md.Name == nil || md.Type == nil {
			continue
		}
		out[md.Name.Name] = md.Type
	}
	if st, ok := idx.structs[name]; ok && st.Fields != nil {
		for _, f := range st.Fields.List {
			if len(f.Names) != 0 {
				continue // only embedded fields contribute methods
			}
			emb := strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
			for k, v := range idx.collectTypeMethodSet(emb, visiting) {
				if _, exists := out[k]; !exists {
					out[k] = v
				}
			}
		}
	}
	// Alias to another named type: follow.
	if alias, ok := idx.aliases[name]; ok {
		emb := strings.TrimPrefix(idx.resolve(alias), "*")
		if emb != name {
			for k, v := range idx.collectTypeMethodSet(emb, visiting) {
				if _, exists := out[k]; !exists {
					out[k] = v
				}
			}
		}
	}
	return out
}

func (idx *retirementOwnershipIndex) collectIfaceMethods(name string, iface *ast.InterfaceType, visiting map[string]bool) map[string]*ast.FuncType {
	out := map[string]*ast.FuncType{}
	if iface == nil || iface.Methods == nil || visiting[name] {
		return out
	}
	visiting[name] = true
	for _, f := range iface.Methods.List {
		switch {
		case len(f.Names) > 0:
			if ft, ok := f.Type.(*ast.FuncType); ok {
				out[f.Names[0].Name] = ft
			}
		default:
			emb := strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
			maps.Copy(out, idx.collectTypeMethodSet(emb, visiting))
		}
	}
	return out
}

func retSigIsClose(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil || ft.Results == nil {
		return false
	}
	if len(retFlattenParams(ft.Params)) != 0 {
		return false
	}
	res := retFlattenParams(ft.Results)
	return len(res) == 1 && retTypeString(res[0]) == "error"
}

func retSigIsQuiesce(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil || ft.Results == nil {
		return false
	}
	params := retFlattenParams(ft.Params)
	if len(params) != 1 {
		return false
	}
	if !strings.HasSuffix(retTypeString(params[0]), "context.Context") &&
		!strings.Contains(retTypeString(params[0]), "Context") {
		return false
	}
	res := retFlattenParams(ft.Results)
	return len(res) == 1 && retTypeString(res[0]) == "error"
}

// findExternalQuiesceCloserCollaborators rejects functions/methods whose
// parameter list (not receiver) contains both a Generation and a
// QuiesceCloser-shaped collaborator.
func (idx *retirementOwnershipIndex) findExternalQuiesceCloserCollaborators(files map[string]*ast.File) []retirementOwnershipFinding {
	var findings []retirementOwnershipFinding
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name == "retireGeneration" || fd.Type == nil {
				continue
			}
			params := retFlattenParams(fd.Type.Params)
			hasGeneration := false
			hasCloser := false
			for _, p := range params {
				resolved := idx.resolve(retTypeString(p))
				base := strings.TrimPrefix(resolved, "*")
				if base == "Generation" {
					hasGeneration = true
				}
				if idx.isQuiesceCloserShape(resolved) {
					hasCloser = true
				}
			}
			if hasGeneration && hasCloser {
				findings = append(findings, retirementOwnershipFinding{
					Type:   retOwnerKey(fd),
					Detail: "accepts both *Generation and QuiesceCloser-shaped collaborator (owned must derive from Generation)",
				})
			}
		}
	}
	return findings
}

// findNonManagerRetirementWorkflows detects renamed non-Manager retirement
// workflows by Generation provenance reaching BeginQuiesce/MarkQuiesced/
// BeginClose/Close through direct calls, method values, aliases, callbacks,
// factories, nested FuncLits, package globals, or package helper chains. Only
// the canonical retireGeneration policy call graph (and Generation's own
// lifecycle methods) are allowed — Manager methods may schedule/call
// retireGeneration but must not directly drive lifecycle or bless arbitrary
// duplicate policy helpers.
func (idx *retirementOwnershipIndex) findNonManagerRetirementWorkflows(files map[string]*ast.File) []retirementOwnershipFinding {
	var findings []retirementOwnershipFinding

	callGraph := map[string]map[string]bool{}
	owners := map[string]*ast.FuncDecl{}
	lifecycleDrivers := map[string]bool{}
	funcLitParent := map[string]string{} // nested FuncLit owner -> enclosing owner

	registerOwner := func(fd *ast.FuncDecl) {
		key := retOwnerKey(fd)
		if key == "" {
			return
		}
		owners[key] = fd
		if callGraph[key] == nil {
			callGraph[key] = map[string]bool{}
		}
	}

	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)

	for _, name := range fileNames {
		file := files[name]
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			registerOwner(fd)
		}
	}

	lifecycleNames := map[string]bool{
		"BeginQuiesce": true,
		"MarkQuiesced": true,
		"BeginClose":   true,
		"Close":        true,
	}

	funcLitSeq := 0

	var markLifecycleFromBody func(owner string, fd *ast.FuncDecl, body *ast.BlockStmt, outerProv map[string]bool)
	markLifecycleFromBody = func(owner string, fd *ast.FuncDecl, body *ast.BlockStmt, outerProv map[string]bool) {
		if body == nil {
			return
		}
		if callGraph[owner] == nil {
			callGraph[owner] = map[string]bool{}
		}
		prov := idx.buildGenerationProvenance(fd, body, outerProv)
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				// Every FuncLit is a deterministic synthetic owner with its own
				// params plus captured outer Generation provenance.
				funcLitSeq++
				litOwner := fmt.Sprintf("funcLit:%s#%d", owner, funcLitSeq)
				funcLitParent[litOwner] = owner
				synthName := strings.ReplaceAll(litOwner, ":", "_")
				synthName = strings.ReplaceAll(synthName, "#", "_")
				synth := &ast.FuncDecl{Name: ast.NewIdent(synthName), Type: x.Type, Body: x.Body}
				owners[litOwner] = synth
				markLifecycleFromBody(litOwner, synth, x.Body, prov)
				return false // do not attribute lit body to enclosing owner
			case *ast.CallExpr:
				switch fun := x.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel != nil && lifecycleNames[fun.Sel.Name] {
						if idx.exprIsGenerationDerived(fun.X, fd, body, prov) {
							lifecycleDrivers[owner] = true
						}
					}
				case *ast.Ident:
					if fun.Name != "" {
						callGraph[owner][fun.Name] = true
					}
				}
			case *ast.SelectorExpr:
				// Method value / callback registration: g.BeginQuiesce, g.Close
				if x.Sel != nil && lifecycleNames[x.Sel.Name] {
					if idx.exprIsGenerationDerived(x.X, fd, body, prov) {
						lifecycleDrivers[owner] = true
					}
				}
			}
			return true
		})
	}

	// Deterministic owner scan order.
	ownerKeys := make([]string, 0, len(owners))
	for key := range owners {
		ownerKeys = append(ownerKeys, key)
	}
	sort.Strings(ownerKeys)
	for _, key := range ownerKeys {
		fd := owners[key]
		markLifecycleFromBody(key, fd, fd.Body, nil)
	}

	// Package-global function literals (closures) that drive Generation lifecycle.
	for _, name := range fileNames {
		file := files[name]
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, v := range vs.Values {
					fl, ok := v.(*ast.FuncLit)
					if !ok || fl.Body == nil {
						continue
					}
					n := "embedded"
					if i < len(vs.Names) && vs.Names[i] != nil {
						n = vs.Names[i].Name
					} else if len(vs.Names) > 0 && vs.Names[0] != nil {
						n = vs.Names[0].Name
					}
					owner := "global:" + n
					synth := &ast.FuncDecl{Name: ast.NewIdent(n), Type: fl.Type, Body: fl.Body}
					owners[owner] = synth
					markLifecycleFromBody(owner, synth, fl.Body, nil)
				}
			}
		}
	}

	// Blessed roots: only the focused policy implementation retireGeneration and
	// its actual helper descendants — not every Manager method/callee.
	blessed := map[string]bool{}
	if _, ok := owners["retireGeneration"]; ok {
		blessed["retireGeneration"] = true
	}
	changed := true
	for changed {
		changed = false
		for key := range blessed {
			for cal := range callGraph[key] {
				if !blessed[cal] {
					if _, ok := owners[cal]; ok {
						blessed[cal] = true
						changed = true
					}
				}
			}
		}
	}
	// FuncLits nested under a blessed owner inherit blessing (e.g. recover wrappers).
	changed = true
	for changed {
		changed = false
		for lit, parent := range funcLitParent {
			if blessed[parent] && !blessed[lit] {
				blessed[lit] = true
				changed = true
			}
		}
	}

	driverKeys := make([]string, 0, len(lifecycleDrivers))
	for key := range lifecycleDrivers {
		driverKeys = append(driverKeys, key)
	}
	sort.Strings(driverKeys)
	for _, key := range driverKeys {
		if blessed[key] {
			continue
		}
		fd := owners[key]
		// Allow Generation's own canonical lifecycle implementation methods.
		if fd != nil && fd.Recv != nil && len(fd.Recv.List) > 0 {
			recv := strings.TrimPrefix(retRecvTypeName(fd.Recv.List[0].Type), "*")
			if recv == "Generation" {
				continue
			}
		}
		findings = append(findings, retirementOwnershipFinding{
			Type:   key,
			Detail: "non-canonical retirement workflow: Generation provenance reaches BeginQuiesce/MarkQuiesced/BeginClose/Close outside retireGeneration policy call graph",
		})
	}
	return findings
}

// buildGenerationProvenance tracks identifiers that are Generation-derived via
// params, receiver, outer captured provenance, package-global Generation
// storage, local assignments/aliases, and factory/method returns.
func (idx *retirementOwnershipIndex) buildGenerationProvenance(fd *ast.FuncDecl, body *ast.BlockStmt, outerProv map[string]bool) map[string]bool {
	prov := map[string]bool{}
	for k, v := range outerProv {
		if v {
			prov[k] = true
		}
	}
	for k := range idx.genGlobals {
		prov[k] = true
	}
	if fd == nil {
		return prov
	}
	seedParam := func(f *ast.Field) {
		if f == nil || f.Type == nil {
			return
		}
		base := strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
		for _, n := range f.Names {
			if n == nil || n.Name == "" || n.Name == "_" {
				continue
			}
			if base == "Generation" {
				prov[n.Name] = true
			} else {
				// Shadow outer/global capture with a non-Generation binding.
				delete(prov, n.Name)
			}
		}
	}
	if fd.Type != nil {
		for _, f := range flattenFields(fd.Type.Params) {
			seedParam(f)
		}
	}
	if fd.Recv != nil {
		for _, f := range flattenFields(fd.Recv) {
			seedParam(f)
		}
	}
	if body == nil {
		return prov
	}
	// Fixed-point local alias / short-decl propagation (cycle-safe via set growth).
	for range 16 {
		changed := false
		ast.Inspect(body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false // nested lit has its own provenance frame
			}
			switch x := n.(type) {
			case *ast.AssignStmt:
				if len(x.Lhs) != len(x.Rhs) {
					return true
				}
				for i := range x.Lhs {
					id, ok := x.Lhs[i].(*ast.Ident)
					if !ok || id.Name == "_" || id.Name == "" {
						continue
					}
					if idx.exprIsGenerationDerived(x.Rhs[i], fd, body, prov) && !prov[id.Name] {
						prov[id.Name] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				for i, name := range x.Names {
					if name == nil || name.Name == "_" {
						continue
					}
					var rhs ast.Expr
					if i < len(x.Values) {
						rhs = x.Values[i]
					} else if len(x.Values) == 1 {
						rhs = x.Values[0]
					}
					if rhs != nil && idx.exprIsGenerationDerived(rhs, fd, body, prov) && !prov[name.Name] {
						prov[name.Name] = true
						changed = true
					}
					if x.Type != nil {
						base := strings.TrimPrefix(idx.resolve(retTypeString(x.Type)), "*")
						if base == "Generation" && !prov[name.Name] {
							prov[name.Name] = true
							changed = true
						}
					}
				}
			}
			return true
		})
		if !changed {
			break
		}
	}
	return prov
}

func (idx *retirementOwnershipIndex) exprIsGenerationDerived(expr ast.Expr, fd *ast.FuncDecl, body *ast.BlockStmt, prov map[string]bool) bool {
	if expr == nil {
		return false
	}
	switch x := expr.(type) {
	case *ast.Ident:
		if prov[x.Name] || idx.genGlobals[x.Name] {
			return true
		}
		// Fallback typed param/receiver match when provenance set not yet seeded.
		if fd != nil && fd.Type != nil {
			for _, f := range flattenFields(fd.Type.Params) {
				base := strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
				if base != "Generation" {
					continue
				}
				for _, n := range f.Names {
					if n != nil && n.Name == x.Name {
						return true
					}
				}
			}
			if fd.Recv != nil {
				for _, f := range flattenFields(fd.Recv) {
					base := strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
					if base != "Generation" {
						continue
					}
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return true
						}
					}
				}
			}
		}
		return false
	case *ast.ParenExpr:
		return idx.exprIsGenerationDerived(x.X, fd, body, prov)
	case *ast.StarExpr:
		return idx.exprIsGenerationDerived(x.X, fd, body, prov)
	case *ast.SelectorExpr:
		return idx.selectorHoldsGeneration(x, fd, body, prov, map[string]bool{})
	case *ast.IndexExpr:
		// Container element of a Generation-holding value (e.g. globals[i]).
		return idx.exprIsGenerationDerived(x.X, fd, body, prov)
	case *ast.CallExpr:
		return idx.callReturnsGeneration(x, fd, body, prov)
	case *ast.UnaryExpr:
		if x.Op == token.AND || x.Op == token.MUL {
			return idx.exprIsGenerationDerived(x.X, fd, body, prov)
		}
	}
	return false
}

func (idx *retirementOwnershipIndex) selectorHoldsGeneration(sel *ast.SelectorExpr, fd *ast.FuncDecl, body *ast.BlockStmt, prov map[string]bool, visiting map[string]bool) bool {
	if sel == nil || sel.Sel == nil {
		return false
	}
	// Resolve the type of the selector base, then the named field.
	baseType := idx.exprNamedType(sel.X, fd, body, prov, visiting)
	if baseType == "" {
		return false
	}
	fieldType := idx.structFieldType(baseType, sel.Sel.Name, map[string]bool{})
	if fieldType == "" {
		return false
	}
	return strings.TrimPrefix(idx.resolve(fieldType), "*") == "Generation"
}

func (idx *retirementOwnershipIndex) exprNamedType(expr ast.Expr, fd *ast.FuncDecl, body *ast.BlockStmt, prov map[string]bool, visiting map[string]bool) string {
	if expr == nil {
		return ""
	}
	switch x := expr.(type) {
	case *ast.Ident:
		// Receiver or param typed name.
		if fd != nil {
			if fd.Recv != nil {
				for _, f := range flattenFields(fd.Recv) {
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
						}
					}
				}
			}
			if fd.Type != nil {
				for _, f := range flattenFields(fd.Type.Params) {
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
						}
					}
				}
			}
		}
		if prov[x.Name] || idx.genGlobals[x.Name] {
			if t, ok := idx.pkgGlobalTypes[x.Name]; ok {
				return strings.TrimPrefix(idx.resolve(t), "*")
			}
			return "Generation"
		}
		if t, ok := idx.pkgGlobalTypes[x.Name]; ok {
			return strings.TrimPrefix(idx.resolve(t), "*")
		}
		return ""
	case *ast.ParenExpr:
		return idx.exprNamedType(x.X, fd, body, prov, visiting)
	case *ast.StarExpr:
		return idx.exprNamedType(x.X, fd, body, prov, visiting)
	case *ast.SelectorExpr:
		base := idx.exprNamedType(x.X, fd, body, prov, visiting)
		if base == "" || x.Sel == nil {
			return ""
		}
		ft := idx.structFieldType(base, x.Sel.Name, map[string]bool{})
		return strings.TrimPrefix(idx.resolve(ft), "*")
	case *ast.IndexExpr:
		base := idx.exprNamedType(x.X, fd, body, prov, visiting)
		if base == "" {
			return ""
		}
		// Slice/array/map element: peel one container layer when typed.
		resolved := idx.resolve(base)
		if after, ok := strings.CutPrefix(resolved, "[]"); ok {
			return strings.TrimPrefix(idx.resolve(after), "*")
		}
		if strings.HasPrefix(resolved, "map[") {
			// map[K]V — recover V via structFieldType-unavailable; use pkg type string.
			if t, ok := idx.pkgGlobalTypes[retIdentName(x.X)]; ok {
				if mt, ok := idx.mapValueTypeString(t); ok {
					return strings.TrimPrefix(idx.resolve(mt), "*")
				}
			}
		}
		if idx.genGlobals[retIdentName(x.X)] || prov[retIdentName(x.X)] {
			return "Generation"
		}
		return ""
	case *ast.CallExpr:
		if idx.callReturnsGeneration(x, fd, body, prov) {
			return "Generation"
		}
	}
	return ""
}

func retIdentName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func (idx *retirementOwnershipIndex) mapValueTypeString(typeStr string) (string, bool) {
	// Best-effort parse of map[K]V from retTypeString form.
	if !strings.HasPrefix(typeStr, "map[") {
		return "", false
	}
	rest := strings.TrimPrefix(typeStr, "map[")
	depth := 1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return rest[i+1:], true
			}
		}
	}
	return "", false
}

func (idx *retirementOwnershipIndex) structFieldType(typeName, field string, visiting map[string]bool) string {
	base := strings.TrimPrefix(idx.resolve(typeName), "*")
	if base == "" || visiting[base] {
		return ""
	}
	visiting[base] = true
	st, ok := idx.structs[base]
	if !ok || st.Fields == nil {
		// Follow alias to struct.
		if alias, ok := idx.aliases[base]; ok {
			return idx.structFieldType(alias, field, visiting)
		}
		return ""
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			// Embedded: search nested.
			emb := strings.TrimPrefix(idx.resolve(retTypeString(f.Type)), "*")
			if t := idx.structFieldType(emb, field, visiting); t != "" {
				return t
			}
			continue
		}
		for _, n := range f.Names {
			if n != nil && n.Name == field {
				return retTypeString(f.Type)
			}
		}
	}
	return ""
}

func (idx *retirementOwnershipIndex) callReturnsGeneration(call *ast.CallExpr, fd *ast.FuncDecl, body *ast.BlockStmt, prov map[string]bool) bool {
	if call == nil {
		return false
	}
	cal := idx.resolveCallFunc(call, fd, body, prov)
	if cal == nil || cal.Type == nil || cal.Type.Results == nil {
		return false
	}
	results := retFlattenParams(cal.Type.Results)
	for _, r := range results {
		if strings.TrimPrefix(idx.resolve(retTypeString(r)), "*") == "Generation" {
			// Package functions and methods returning *Generation (including
			// zero-argument factories and selector calls like m.Active()) are
			// Generation provenance at the call site.
			return true
		}
	}
	return false
}

func (idx *retirementOwnershipIndex) resolveCallFunc(call *ast.CallExpr, fd *ast.FuncDecl, body *ast.BlockStmt, prov map[string]bool) *ast.FuncDecl {
	if call == nil {
		return nil
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return idx.pkgFuncs[fun.Name]
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return nil
		}
		base := idx.exprNamedType(fun.X, fd, body, prov, map[string]bool{})
		if base == "" {
			return nil
		}
		base = strings.TrimPrefix(idx.resolve(base), "*")
		for _, md := range idx.typeMethods[base] {
			if md.Name != nil && md.Name.Name == fun.Sel.Name {
				return md
			}
		}
	}
	return nil
}

func flattenFields(fl *ast.FieldList) []*ast.Field {
	if fl == nil {
		return nil
	}
	return fl.List
}

func retFlattenParams(fl *ast.FieldList) []ast.Expr {
	var out []ast.Expr
	if fl == nil {
		return out
	}
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, f.Type)
		}
	}
	return out
}

func retTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + retTypeString(t.X)
	case *ast.SelectorExpr:
		return retTypeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + retTypeString(t.Elt)
	case *ast.MapType:
		return "map[" + retTypeString(t.Key) + "]" + retTypeString(t.Value)
	case *ast.ChanType:
		return "chan " + retTypeString(t.Value)
	case *ast.FuncType:
		return "func"
	case *ast.InterfaceType:
		return "interface"
	case *ast.StructType:
		return "struct"
	case *ast.Ellipsis:
		return "..." + retTypeString(t.Elt)
	case *ast.IndexExpr:
		return retTypeString(t.X) + "[" + retTypeString(t.Index) + "]"
	case *ast.IndexListExpr:
		parts := make([]string, 0, len(t.Indices))
		for _, ind := range t.Indices {
			parts = append(parts, retTypeString(ind))
		}
		return retTypeString(t.X) + "[" + strings.Join(parts, ",") + "]"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func retRecvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + retRecvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

func TestManagerRetirementOwnership_ProductionTargetIsGreen(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	got := analyzeRetirementOwnership(files)
	if len(got) != 0 {
		joined := make([]string, 0, len(got))
		for _, f := range got {
			joined = append(joined, f.String())
		}
		t.Fatalf("retirement scheduling ownership violations (%d):\n%s", len(got), strings.Join(joined, "\n"))
	}
}

func TestManagerRetirementOwnership_SyntheticFixtures(t *testing.T) {
	t.Parallel()

	t.Run("rejects_non_manager_caller_of_retireGeneration", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
func retireGeneration() (RetirementStatus, error) { return RetirementStatus{}, nil }
`,
			"rogue.go": `
package runtimehost
type rogueScheduler struct{}
func (r *rogueScheduler) Sweep() { _, _ = retireGeneration() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "rogueScheduler.Sweep") {
			t.Fatalf("expected non-Manager caller rejection; got %v", got)
		}
	})

	t.Run("rejects_function_alias_global_escape", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
func retireGeneration() (RetirementStatus, error) { return RetirementStatus{}, nil }
var run = retireGeneration
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "global:run") {
			t.Fatalf("expected global alias escape rejection; got %v", got)
		}
	})

	t.Run("rejects_local_alias_and_callback_escape", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
func retireGeneration() (RetirementStatus, error) { return RetirementStatus{}, nil }
func register(fn func() (RetirementStatus, error)) {}
func rogue() {
	f := retireGeneration
	f()
	register(retireGeneration)
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "rogue") {
			t.Fatalf("expected local alias/callback escape rejection; got %v", got)
		}
	})

	t.Run("accepts_manager_as_sole_caller", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
func retireGeneration() (RetirementStatus, error) { return RetirementStatus{}, nil }
`,
			"manager.go": `
package runtimehost
type Manager struct{ cleanupPolicy CleanupPolicy }
func (m *Manager) RetireGeneration() (RetirementStatus, error) { return retireGeneration() }
func (m *Manager) scheduleRetire() { _, _ = retireGeneration() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("Manager-only callers must pass; got %v", got)
		}
	})

	t.Run("rejects_renamed_two_arg_scheduler_with_policy", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import "context"
type Generation struct{}
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
type alternate struct{ policy CleanupPolicy }
func (*alternate) Retire(ctx context.Context, g *Generation) (RetirementStatus, error) { return RetirementStatus{}, nil }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "alternate") {
			t.Fatalf("expected renamed two-arg scheduler rejection; got %v", got)
		}
	})

	t.Run("rejects_nested_cleanup_policy_owner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type CleanupPolicy struct{ MaxAttempts int }
type policyHalf struct { budget CleanupPolicy }
type splitWorker struct { half policyHalf }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "policyHalf") && !retirementFindingsHave(got, "splitWorker") {
			t.Fatalf("expected nested CleanupPolicy owner rejection; got %v", got)
		}
	})

	t.Run("rejects_retirement_status_cache_field", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
type Manager struct {
	last RetirementStatus
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "Manager") {
			t.Fatalf("expected RetirementStatus cache field rejection; got %v", got)
		}
	})

	t.Run("rejects_status_map_slice_pointer_global_alias", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
type StatusAlias = RetirementStatus
type statusBox struct{ byID map[int64]RetirementStatus }
type hist struct{ items []RetirementStatus }
type ptrHold struct{ p *RetirementStatus }
var last RetirementStatus
var history []RetirementStatus
type observer struct{ snap StatusAlias }
`,
		})
		got := analyzeRetirementOwnership(files)
		for _, want := range []string{"statusBox", "hist", "ptrHold", "global:last", "global:history", "observer"} {
			if !retirementFindingsHave(got, want) {
				t.Fatalf("expected %s rejection; got %v", want, got)
			}
		}
	})

	t.Run("rejects_external_stopper_collaborator", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import "context"
type Generation struct{}
type Stopper interface{ Close() error; Quiesce(context.Context) error }
func retireElsewhere(ctx context.Context, g *Generation, s Stopper) error { return nil }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "retireElsewhere") {
			t.Fatalf("expected Stopper collaborator rejection; got %v", got)
		}
	})

	t.Run("rejects_renamed_workflow_via_call_graph", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import "context"
type Generation struct{}
func (g *Generation) BeginQuiesce() error { return nil }
func (g *Generation) MarkQuiesced() error { return nil }
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
func (g *Generation) Drained() <-chan struct{} { return nil }
func helper(g *Generation) error {
	if err := g.BeginQuiesce(); err != nil { return err }
	_ = g.MarkQuiesced()
	<-g.Drained()
	_ = g.BeginClose()
	return g.Close()
}
func retireElsewhere(ctx context.Context, g *Generation) error { return helper(g) }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "helper") && !retirementFindingsHave(got, "retireElsewhere") {
			t.Fatalf("expected renamed workflow rejection; got %v", got)
		}
	})

	t.Run("rejects_duplicate_retireGeneration_declaration", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"a.go": `
package runtimehost
type RetirementStatus struct{}
func retireGeneration() (RetirementStatus, error) { return RetirementStatus{}, nil }
`,
			"b.go": `
package runtimehost
func retireGeneration() (RetirementStatus, error) { return RetirementStatus{}, nil }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "retireGeneration") {
			t.Fatalf("expected duplicate declaration rejection; got %v", got)
		}
	})

	t.Run("accepts_canonical_retireGeneration_and_manager_policy", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"retire.go": `
package runtimehost
import "context"
type Generation struct{}
type QuiesceCloser interface{ Close() error; Quiesce(context.Context) error }
type CleanupPolicy struct{ MaxAttempts int }
type ReloadObserver struct{}
type RetirementStatus struct{ Outcome string }
func retireGeneration(ctx context.Context, g *Generation, policy CleanupPolicy, observer *ReloadObserver) (RetirementStatus, error) {
	_ = runCleanup(g, policy)
	st := RetirementStatus{Outcome: "ok"}
	return st, nil
}
func (g *Generation) BeginQuiesce() error { return nil }
func (g *Generation) MarkQuiesced() error { return nil }
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
func runCleanup(g *Generation, policy CleanupPolicy) error { return g.Close() }
`,
			"manager.go": `
package runtimehost
import "context"
type Manager struct{ cleanupPolicy CleanupPolicy }
func (m *Manager) RetireGeneration(ctx context.Context, g *Generation) (RetirementStatus, error) {
	return retireGeneration(ctx, g, m.cleanupPolicy, nil)
}
func (m *Manager) scheduleRetire(g *Generation) { _, _ = m.RetireGeneration(context.Background(), g) }
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("canonical Manager/retireGeneration must pass; got %v", got)
		}
	})

	t.Run("accepts_unrelated_negatives", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import "context"
import "time"
type Generation struct{}
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
type QuiesceCloser interface{ Close() error; Quiesce(context.Context) error }
type Manager struct{ cleanupPolicy CleanupPolicy }
func (g *Generation) Lifecycle() int { return 0 }
func (g *Generation) Status() string { return "" }
func inspect(g *Generation) string { return g.Status() }
func quiesceOnly(c QuiesceCloser) error { return c.Close() }
type telemetry struct {
	outcome string
	dur time.Duration
}
func emit() RetirementStatus { return RetirementStatus{Outcome: "ok"} }
func shutdown(ctx context.Context, done chan struct{}) {
	select {
	case <-ctx.Done():
	case <-done:
	}
}
type unrelatedCloser struct{}
func (u *unrelatedCloser) Close() error { return nil }
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("unrelated negatives must pass; got %v", got)
		}
	})

	t.Run("accepts_retirement_status_as_return_value", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
type Manager struct{}
func (m *Manager) RetireGeneration() (RetirementStatus, error) {
	st := RetirementStatus{Outcome: "ok"}
	return st, nil
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if retirementFindingsHave(got, "Manager") {
			t.Fatalf("return-value status must pass; got %v", got)
		}
	})

	// --- A: blessed call graph rooted at retireGeneration only ---

	t.Run("rejects_manager_direct_lifecycle_drive", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) BeginQuiesce() error { return nil }
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
type Manager struct{}
func (m *Manager) Drive(g *Generation) {
	_ = g.BeginQuiesce()
	_ = g.BeginClose()
	_ = g.Close()
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "Manager.Drive") {
			t.Fatalf("expected Manager direct lifecycle drive rejection; got %v", got)
		}
	})

	t.Run("rejects_manager_rogue_policy_helper", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) BeginQuiesce() error { return nil }
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
func retireGeneration(g *Generation) error { return nil }
type Manager struct{}
func (m *Manager) Other(g *Generation) { roguePolicy(g) }
func roguePolicy(g *Generation) {
	_ = g.BeginQuiesce()
	_ = g.BeginClose()
	_ = g.Close()
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "roguePolicy") {
			t.Fatalf("expected Manager→rogue helper rejection; got %v", got)
		}
		if retirementFindingsHave(got, "Manager.Other") && !retirementFindingsHave(got, "roguePolicy") {
			t.Fatalf("rogue policy helper must be the ownership violation; got %v", got)
		}
	})

	t.Run("accepts_manager_scheduling_retireGeneration_only", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"retire.go": `
package runtimehost
type Generation struct{}
type CleanupPolicy struct{}
type RetirementStatus struct{}
func (g *Generation) BeginQuiesce() error { return nil }
func (g *Generation) MarkQuiesced() error { return nil }
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
func retireGeneration(g *Generation, policy CleanupPolicy) (RetirementStatus, error) {
	_ = g.BeginQuiesce()
	_ = g.MarkQuiesced()
	_ = g.BeginClose()
	return RetirementStatus{}, runCleanup(g)
}
func runCleanup(g *Generation) error { return g.Close() }
`,
			"manager.go": `
package runtimehost
type Manager struct{ cleanupPolicy CleanupPolicy }
func (m *Manager) RetireGeneration(g *Generation) (RetirementStatus, error) {
	return retireGeneration(g, m.cleanupPolicy)
}
func (m *Manager) scheduleRetire(g *Generation) { _, _ = m.RetireGeneration(g) }
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("canonical Manager→retireGeneration must pass; got %v", got)
		}
	})

	// --- B: Generation provenance evasions ---

	t.Run("rejects_receiver_field_generation_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
type holder struct{ g *Generation }
func (r *holder) Run() { _ = r.g.Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "holder.Run") {
			t.Fatalf("expected receiver-field Generation Close rejection; got %v", got)
		}
	})

	t.Run("rejects_nested_aliased_struct_generation_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
type core struct{ g *Generation }
type coreAlias = core
type wrap struct{ coreAlias }
func (w *wrap) Run() { _ = w.g.Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "wrap.Run") {
			t.Fatalf("expected nested/aliased struct Generation Close rejection; got %v", got)
		}
	})

	t.Run("rejects_local_alias_generation_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
func aliasClose(g *Generation) { x := g; _ = x.Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "aliasClose") {
			t.Fatalf("expected local alias Generation Close rejection; got %v", got)
		}
	})

	t.Run("rejects_method_value_callback_registration", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
func register(fn func() error) {}
func storeClose(g *Generation) {
	cb := g.Close
	register(cb)
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "storeClose") {
			t.Fatalf("expected method-value callback rejection; got %v", got)
		}
	})

	t.Run("rejects_package_global_generation_closure", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
var drive = func(g *Generation) { _ = g.BeginClose(); _ = g.Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "global:drive") {
			t.Fatalf("expected package-global Generation closure rejection; got %v", got)
		}
	})

	t.Run("accepts_generation_metadata_with_unrelated_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Status() string { return "" }
func (g *Generation) ID() int64 { return 0 }
type resource struct{}
func (r *resource) Close() error { return nil }
func inspect(g *Generation, r *resource) error {
	_ = g.Status()
	_ = g.ID()
	return r.Close()
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if retirementFindingsHave(got, "inspect") {
			t.Fatalf("Generation metadata + unrelated Close must pass; got %v", got)
		}
	})

	// --- C: inferred package-global make/new storage ---

	t.Run("rejects_inferred_make_new_global_status_and_policy", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
type CleanupPolicy struct{ MaxAttempts int }
var history = make([]RetirementStatus, 0)
var byID = make(map[int64]RetirementStatus)
var last = new(RetirementStatus)
var policies = make([]CleanupPolicy, 0)
`,
		})
		got := analyzeRetirementOwnership(files)
		for _, want := range []string{"global:history", "global:byID", "global:last", "global:policies"} {
			if !retirementFindingsHave(got, want) {
				t.Fatalf("expected %s rejection; got %v", want, got)
			}
		}
	})

	t.Run("accepts_unrelated_global_telemetry_containers", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type RetirementStatus struct{ Outcome string }
type CleanupPolicy struct{ MaxAttempts int }
var outcomes = make([]string, 0)
var counts = make(map[string]int)
var lastErr = new(string)
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("unrelated telemetry globals must pass; got %v", got)
		}
	})

	// --- D: concrete structural QuiesceCloser collaborator ---

	t.Run("rejects_concrete_quiesce_closer_collaborator", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
import "context"
type Generation struct{}
type plane struct{}
func (p *plane) Close() error { return nil }
func (p *plane) Quiesce(ctx context.Context) error { return nil }
type planeAlias = plane
func retireDirect(g *Generation, p plane) error { return nil }
func retirePtr(g *Generation, p *plane) error { return nil }
func retireAlias(g *Generation, p *planeAlias) error { return nil }
`,
		})
		got := analyzeRetirementOwnership(files)
		for _, want := range []string{"retireDirect", "retirePtr", "retireAlias"} {
			if !retirementFindingsHave(got, want) {
				t.Fatalf("expected %s concrete collaborator rejection; got %v", want, got)
			}
		}
	})

	t.Run("accepts_closer_missing_quiesce_beside_generation", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
type onlyClose struct{}
func (o *onlyClose) Close() error { return nil }
func attach(g *Generation, o *onlyClose) error { return nil }
`,
		})
		got := analyzeRetirementOwnership(files)
		if retirementFindingsHave(got, "attach") {
			t.Fatalf("closer lacking Quiesce must pass; got %v", got)
		}
	})

	// --- E: duplicate production declarations fail closed ---

	t.Run("rejects_duplicate_generation_and_policy_types", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"a.go": `
package runtimehost
type Generation struct{ id int64 }
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
type QuiesceCloser interface{ Close() error }
`,
			"b.go": `
package runtimehost
type Generation struct{ id int64 }
type CleanupPolicy struct{ MaxAttempts int }
type RetirementStatus struct{ Outcome string }
type QuiesceCloser interface{ Close() error; Quiesce() error }
`,
		})
		got := analyzeRetirementOwnership(files)
		for _, want := range []string{"Generation", "CleanupPolicy", "RetirementStatus", "QuiesceCloser"} {
			if !retirementFindingsHave(got, want) {
				t.Fatalf("expected duplicate %s rejection; got %v", want, got)
			}
		}
	})

	t.Run("rejects_duplicate_package_func_order_independent", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"z_last.go": `
package runtimehost
func helper() {}
`,
			"a_first.go": `
package runtimehost
func helper() {}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "helper") {
			t.Fatalf("expected duplicate package func rejection; got %v", got)
		}
	})

	// --- F: nested FuncLit / factory / package-global Generation provenance ---

	t.Run("rejects_returned_local_closure_own_generation_param", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
func factory() func(*Generation) error {
	return func(g *Generation) error { return g.Close() }
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "funcLit:factory") {
			t.Fatalf("expected returned local closure rejection; got %v", got)
		}
	})

	t.Run("rejects_registered_or_invoked_local_funclit_own_generation_param", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) BeginClose() error { return nil }
func (g *Generation) Close() error { return nil }
func register(fn func(*Generation)) {}
func registerRogue() {
	register(func(g *Generation) { _ = g.BeginClose(); _ = g.Close() })
}
func invokeRogue() {
	func(g *Generation) { _ = g.Close() }(nil)
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "funcLit:registerRogue") {
			t.Fatalf("expected registered local FuncLit rejection; got %v", got)
		}
		if !retirementFindingsHave(got, "funcLit:invokeRogue") {
			t.Fatalf("expected immediately invoked local FuncLit rejection; got %v", got)
		}
	})

	t.Run("rejects_zero_arg_package_factory_begin_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) BeginClose() error { return nil }
func currentGeneration() *Generation { return nil }
func rogue() { _ = currentGeneration().BeginClose() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "rogue") {
			t.Fatalf("expected zero-arg factory BeginClose rejection; got %v", got)
		}
	})

	t.Run("rejects_method_selector_factory_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
type Manager struct{}
func (m *Manager) Active() *Generation { return nil }
func rogue2(m *Manager) { _ = m.Active().Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if !retirementFindingsHave(got, "rogue2") {
			t.Fatalf("expected method/selector factory Close rejection; got %v", got)
		}
	})

	t.Run("rejects_direct_and_nested_global_generation_storage", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Close() error { return nil }
func (g *Generation) BeginClose() error { return nil }
type GenPtr = *Generation
type holder struct{ g *Generation }
type wrap struct{ inner holder }
var current *Generation
var aliased GenPtr
var h holder
var w wrap
func rogue() { _ = current.Close() }
func rogue2() { _ = h.g.BeginClose() }
func rogue3() { _ = aliased.Close() }
func rogue4() { _ = w.inner.g.Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		for _, want := range []string{"global:current", "global:aliased", "global:h", "global:w", "rogue", "rogue2", "rogue3", "rogue4"} {
			if !retirementFindingsHave(got, want) {
				t.Fatalf("expected %s rejection; got %v", want, got)
			}
		}
	})

	t.Run("accepts_local_callback_unrelated_resource_close", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Status() string { return "" }
type resource struct{}
func (r *resource) Close() error { return nil }
func register(fn func() error) {}
func wire(g *Generation, r *resource) {
	_ = g.Status()
	register(func() error { return r.Close() })
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("unrelated local callback Close must pass; got %v", got)
		}
	})

	t.Run("accepts_factory_returning_unrelated_closer", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
type resource struct{}
func (r *resource) Close() error { return nil }
func newResource() *resource { return &resource{} }
func use() { _ = newResource().Close() }
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("unrelated factory Close must pass; got %v", got)
		}
	})

	t.Run("accepts_global_telemetry_not_generation_or_status", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
type RetirementStatus struct{ Outcome string }
type CleanupPolicy struct{ MaxAttempts int }
var lastOutcome string
var counts = map[string]int{}
type telemetry struct{ n int }
var snap telemetry
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("unrelated telemetry globals must pass; got %v", got)
		}
	})

	t.Run("accepts_manager_active_readonly_without_lifecycle", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"gen.go": `
package runtimehost
type Generation struct{}
func (g *Generation) Status() string { return "" }
func (g *Generation) ID() int64 { return 0 }
type Manager struct{}
func (m *Manager) Active() *Generation { return nil }
func peek(m *Manager) string {
	g := m.Active()
	if g == nil {
		return ""
	}
	_ = g.ID()
	return g.Status()
}
`,
		})
		got := analyzeRetirementOwnership(files)
		if len(got) != 0 {
			t.Fatalf("Manager.Active read-only use must pass; got %v", got)
		}
	})
}
