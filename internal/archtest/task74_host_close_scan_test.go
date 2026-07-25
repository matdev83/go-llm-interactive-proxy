package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

// Task 7.4 architecture gate: (*runtimebundle.ReloadHost).Close is the sole
// production coordinator of the manager-shutdown / process-close /
// tracing-shutdown ordering. Detection is role and shape based, following Host
// provenance through aliases, receivers, struct fields (direct, nested, and
// embedded), locals, package globals, function literals, and package-local
// helper call graphs. It is not an exact-name blocklist and grants no
// whole-file exemptions: unrelated resource Close, management-server Shutdown,
// http.Server Shutdown, and pre-Host validation cleanup stay valid negatives
// because none of them carry Host provenance.

type hcFinding struct {
	Scope  string
	Owner  string
	Detail string
}

func (f hcFinding) String() string {
	return fmt.Sprintf("%s|%s: %s", f.Scope, f.Owner, f.Detail)
}

func hcFindingsHave(findings []hcFinding, want string) bool {
	for _, f := range findings {
		if strings.Contains(f.Owner, want) || strings.Contains(f.Detail, want) || strings.Contains(f.Scope, want) {
			return true
		}
	}
	return false
}

// hcZone describes one production package under analysis.
type hcZone struct {
	// Scope is a stable label (usually the repo-relative directory).
	Scope string
	// HostOwner marks the package that declares the canonical Host type and
	// therefore may implement the shutdown ordering exactly once.
	HostOwner bool
}

// hcShutdownPrimitives are the terminal operations that make up process
// shutdown ordering. Reaching any of them from Host provenance outside the
// canonical Host.Close call graph is an ownership violation.
var (
	hcManagerShutdown = map[string]bool{
		"ShutdownDetached": true,
		"BeginShutdown":    true,
		"DetachActive":     true,
		"RetireGeneration": true,
		"SweepClosed":      true,
	}
	hcProcessShutdown = map[string]bool{
		"Close": true,
	}
	hcCoordinatorShutdown = map[string]bool{
		"BeginShutdown": true,
		"WaitForIdle":   true,
	}
	// hcHostShutdownFields are the Host members that expose shutdown authority.
	hcHostShutdownFields = map[string]string{
		"Manager":         "manager",
		"Process":         "process",
		"Coordinator":     "coordinator",
		"ShutdownTracing": "tracing",
		// Private ownership fields after Host encapsulation (PR B1).
		"manager":         "manager",
		"process":         "process",
		"coordinator":     "coordinator",
		"shutdownTracing": "tracing",
	}
)

type hcIndex struct {
	zone       hcZone
	aliases    map[string]string
	structs    map[string]*ast.StructType
	interfaces map[string]*ast.InterfaceType
	// hostTypes holds every local type string that resolves to the canonical Host.
	hostTypes map[string]bool
	// Contested typed shutdown authority (not only Host-field derived).
	managerTypes     map[string]bool
	processTypes     map[string]bool
	coordinatorTypes map[string]bool
	// globals maps package-global names to their declared/inferred type string.
	globals map[string]string
	// hostGlobals are package globals that hold Host provenance.
	hostGlobals map[string]bool
	// authorityGlobals maps package globals that hold typed shutdown authority.
	authorityGlobals map[string]string
	funcs            map[string]*ast.FuncDecl
	methods          map[string][]*ast.FuncDecl
	// contestedDeclCounts tracks duplicate contested type/func/method declarations.
	contestedDeclCounts map[string]int
	// typeDeclCounts counts every local type spec name (structs/interfaces) so
	// structural equivalents can fail closed on duplicates.
	typeDeclCounts map[string]int
	// funcDeclCounts tracks package-level function declarations for fail-closed
	// duplicate detection of contested owners (pre-Host rollback, etc.).
	funcDeclCounts map[string]int
}

func hcBuildIndex(zone hcZone, files map[string]*ast.File) *hcIndex {
	idx := &hcIndex{
		zone:                zone,
		aliases:             map[string]string{},
		structs:             map[string]*ast.StructType{},
		interfaces:          map[string]*ast.InterfaceType{},
		hostTypes:           map[string]bool{},
		managerTypes:        map[string]bool{},
		processTypes:        map[string]bool{},
		coordinatorTypes:    map[string]bool{},
		globals:             map[string]string{},
		hostGlobals:         map[string]bool{},
		authorityGlobals:    map[string]string{},
		funcs:               map[string]*ast.FuncDecl{},
		methods:             map[string][]*ast.FuncDecl{},
		contestedDeclCounts: map[string]int{},
		typeDeclCounts:      map[string]int{},
		funcDeclCounts:      map[string]int{},
	}
	names := hcSortedKeys(files)

	// Canonical Host / Manager / Process / Coordinator spellings.
	if zone.HostOwner {
		idx.hostTypes["ReloadHost"] = true
		idx.hostTypes["Host"] = true
		idx.managerTypes["Manager"] = true
		idx.processTypes["ProcessServices"] = true
		idx.coordinatorTypes["Coordinator"] = true
	}
	for _, name := range names {
		file := files[name]
		for alias, path := range importAliasToPath(file) {
			switch path {
			case importRuntimebundle:
				idx.hostTypes[alias+".Host"] = true
				idx.hostTypes[alias+".ReloadHost"] = true
				idx.processTypes[alias+".ProcessServices"] = true
			case importRuntimehost:
				idx.managerTypes[alias+".Manager"] = true
				idx.coordinatorTypes[alias+".Coordinator"] = true
			}
		}
	}

	for _, name := range names {
		file := files[name]
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				switch d.Tok {
				case token.TYPE:
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok || ts.Name == nil {
							continue
						}
						switch t := ts.Type.(type) {
						case *ast.StructType:
							idx.structs[ts.Name.Name] = t
							idx.typeDeclCounts[ts.Name.Name]++
							switch ts.Name.Name {
							case "ReloadHost", "Host", "Manager", "ProcessServices", "Coordinator":
								idx.contestedDeclCounts["type:"+ts.Name.Name]++
							}
						case *ast.InterfaceType:
							idx.interfaces[ts.Name.Name] = t
							idx.typeDeclCounts[ts.Name.Name]++
						default:
							idx.aliases[ts.Name.Name] = hcTypeString(ts.Type)
							// A second declaration of the contested name itself
							// (not a distinct alias of it) is a fail-closed conflict.
							switch ts.Name.Name {
							case "ReloadHost", "Host", "Manager", "ProcessServices", "Coordinator":
								idx.contestedDeclCounts["type:"+ts.Name.Name]++
							}
						}
					}
				case token.VAR:
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for i, n := range vs.Names {
							if n == nil || n.Name == "_" {
								continue
							}
							typ := ""
							if vs.Type != nil {
								typ = hcTypeString(vs.Type)
							} else if i < len(vs.Values) {
								typ = idx.inferExprType(vs.Values[i], nil, nil)
							}
							if typ != "" {
								idx.globals[n.Name] = typ
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) == 0 {
					if d.Name != nil {
						idx.funcDeclCounts[d.Name.Name]++
						idx.funcs[d.Name.Name] = d
					}
					continue
				}
				recv := strings.TrimPrefix(hcTypeString(d.Recv.List[0].Type), "*")
				idx.methods[recv] = append(idx.methods[recv], d)
				if d.Name != nil && (recv == "ReloadHost" || recv == "Host") && d.Name.Name == "Close" {
					idx.contestedDeclCounts["method:"+recv+".Close"]++
				}
			}
		}
	}
	// Exact canonical identities declared in this package are seeded by name so
	// runtimehost (Manager/Coordinator) and runtimebundle (ProcessServices/Host)
	// retain typed authority without granting a whole-package exemption.
	idx.seedDeclaredCanonicalIdentities()
	idx.growAuthorityAliases()
	for name, typ := range idx.globals {
		if idx.isHostType(typ) {
			idx.hostGlobals[name] = true
		}
		if kind := idx.authorityKindOfType(typ); kind != "" && kind != "host" {
			idx.authorityGlobals[name] = kind
		}
	}
	idx.classifyStructuralEquivalents()
	// Re-grow aliases after structural classification marks renamed concretes.
	idx.growAuthorityAliases()
	// Re-infer and re-seed authority globals after structural classification
	// grows type maps (factory returns such as newPassiveShadow() resolve now).
	for _, name := range hcSortedKeys(files) {
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
				for i, n := range vs.Names {
					if n == nil || n.Name == "_" {
						continue
					}
					if vs.Type != nil {
						idx.globals[n.Name] = hcTypeString(vs.Type)
						continue
					}
					if i < len(vs.Values) {
						if typ := idx.inferExprType(vs.Values[i], nil, nil); typ != "" {
							idx.globals[n.Name] = typ
						}
					}
				}
			}
		}
	}
	for name, typ := range idx.globals {
		if kind := idx.authorityKindOfType(typ); kind != "" && kind != "host" {
			idx.authorityGlobals[name] = kind
		} else if idx.isRenamedAuthorityType(typ) {
			idx.authorityGlobals[name] = idx.authorityKindOfType(hcUnwrapAuthorityType(typ))
		}
	}
	return idx
}

// seedDeclaredCanonicalIdentities marks exact canonical declaration names when
// the package declares them with the matching contested method set. A type that
// merely reuses the name (e.g. continuity.Manager) is not seeded.
func (idx *hcIndex) seedDeclaredCanonicalIdentities() {
	seed := func(name string, set map[string]bool, want string) {
		var kind string
		switch {
		case idx.structs[name] != nil:
			kind = idx.classifyMethodSet(idx.concreteMethodSet(name))
		case idx.interfaces[name] != nil:
			kind = idx.classifyMethodSet(idx.interfaceMethodSet(idx.interfaces[name], map[string]bool{}))
		default:
			if target, ok := idx.aliases[name]; ok {
				base := strings.TrimPrefix(target, "*")
				if idx.structs[base] != nil {
					kind = idx.classifyMethodSet(idx.concreteMethodSet(base))
				} else if it, ok := idx.interfaces[base]; ok {
					kind = idx.classifyMethodSet(idx.interfaceMethodSet(it, map[string]bool{}))
				} else if k := idx.authorityKindOfType(base); k != "" {
					kind = k
				}
			}
		}
		if kind == want {
			set[name] = true
		}
	}
	seed("Manager", idx.managerTypes, "manager")
	seed("Coordinator", idx.coordinatorTypes, "coordinator")
	seed("ProcessServices", idx.processTypes, "process")
}

// growAuthorityAliases copies Host/Manager/Process/Coordinator authority through
// local type aliases (including aliases of renamed structural equivalents).
func (idx *hcIndex) growAuthorityAliases() {
	for range 8 {
		changed := false
		for name, target := range idx.aliases {
			if idx.isHostType(target) && !idx.hostTypes[name] {
				idx.hostTypes[name] = true
				changed = true
			}
			if idx.isManagerType(target) && !idx.managerTypes[name] {
				idx.managerTypes[name] = true
				changed = true
			}
			if idx.isProcessType(target) && !idx.processTypes[name] {
				idx.processTypes[name] = true
				changed = true
			}
			if idx.isCoordinatorType(target) && !idx.coordinatorTypes[name] {
				idx.coordinatorTypes[name] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
}

func (idx *hcIndex) inferExprType(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) string {
	switch x := expr.(type) {
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			if t := idx.inferExprType(x.X, fd, prov); t != "" {
				if strings.HasPrefix(t, "*") {
					return t
				}
				return "*" + t
			}
		}
	case *ast.CompositeLit:
		return hcTypeString(x.Type)
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
			return "*" + strings.TrimPrefix(hcTypeString(x.Args[0]), "*")
		}
		if typ := idx.callResultType(x, fd, prov); typ != "" {
			return typ
		}
		if idx.callReturnsHost(x, fd, prov) {
			for t := range idx.hostTypes {
				return "*" + strings.TrimPrefix(t, "*")
			}
		}
	case *ast.Ident:
		if prov != nil {
			if t := prov.types[x.Name]; t != "" {
				return t
			}
		}
		if t, ok := idx.globals[x.Name]; ok {
			return t
		}
	}
	return ""
}

func hcSortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (idx *hcIndex) isHostType(typ string) bool {
	base := strings.TrimPrefix(typ, "*")
	return idx.hostTypes[base] || idx.hostTypes[typ]
}

// namedTypeOf resolves the named type string of a struct field, following
// aliases, embedded fields, and nested structs.
func (idx *hcIndex) fieldType(typeName, field string, visiting map[string]bool) string {
	base := strings.TrimPrefix(typeName, "*")
	if base == "" || visiting[base] {
		return ""
	}
	visiting[base] = true
	st, ok := idx.structs[base]
	if !ok {
		if alias, ok := idx.aliases[base]; ok {
			return idx.fieldType(alias, field, visiting)
		}
		return ""
	}
	if st.Fields == nil {
		return ""
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			emb := strings.TrimPrefix(hcTypeString(f.Type), "*")
			if t := idx.fieldType(emb, field, visiting); t != "" {
				return t
			}
			continue
		}
		for _, n := range f.Names {
			if n != nil && n.Name == field {
				return hcTypeString(f.Type)
			}
		}
	}
	return ""
}

// hcOwner is one analyzed function, method, or function literal.
type hcOwner struct {
	key  string
	decl *ast.FuncDecl
	// parent links a function literal to its enclosing owner for blessing.
	parent string
}

func hcOwnerKey(fd *ast.FuncDecl) string {
	if fd == nil || fd.Name == nil {
		return ""
	}
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		return strings.TrimPrefix(hcTypeString(fd.Recv.List[0].Type), "*") + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// hcProvenance records which identifiers hold Host, Manager, ProcessServices,
// Coordinator, or tracing-shutdown values — including whether a kind was
// derived from a Host field extraction.
type hcProvenance struct {
	host        map[string]bool
	kinds       map[string]string // ident -> manager|process|coordinator|tracing
	types       map[string]string // ident -> concrete type string when known
	hostDerived map[string]bool   // ident assigned from a Host shutdown field
}

func newHCProvenance(outer *hcProvenance) *hcProvenance {
	p := &hcProvenance{host: map[string]bool{}, kinds: map[string]string{}, types: map[string]string{}, hostDerived: map[string]bool{}}
	if outer != nil {
		for k, v := range outer.host {
			p.host[k] = v
		}
		for k, v := range outer.kinds {
			p.kinds[k] = v
		}
		for k, v := range outer.types {
			p.types[k] = v
		}
		for k, v := range outer.hostDerived {
			p.hostDerived[k] = v
		}
	}
	return p
}

// hcReport carries findings plus the ownership graph used by uniqueness gates.
type hcReport struct {
	Findings []hcFinding
	// Touching maps every owner that reaches a Host shutdown primitive to the
	// primitive kinds it reaches.
	Touching map[string][]string
	// CallGraph maps owner -> called owner keys within the package.
	CallGraph map[string]map[string]bool
	Blessed   map[string]bool
}

// coverage returns, for every owner, the union of shutdown-primitive kinds it
// reaches directly or through its package-local callees.
func (r hcReport) coverage() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for owner, kinds := range r.Touching {
		if out[owner] == nil {
			out[owner] = map[string]bool{}
		}
		for _, k := range kinds {
			out[owner][k] = true
		}
	}
	owners := map[string]bool{}
	for owner := range r.CallGraph {
		owners[owner] = true
	}
	for owner := range r.Touching {
		owners[owner] = true
	}
	for range len(owners) + 1 {
		changed := false
		for owner := range owners {
			for callee := range r.CallGraph[owner] {
				for k := range out[callee] {
					if out[owner] == nil {
						out[owner] = map[string]bool{}
					}
					if !out[owner][k] {
						out[owner][k] = true
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return out
}

// Roots returns the production entry points whose call closure covers the full
// shutdown ordering and that no other such entry point calls. A converged
// codebase has exactly one.
func (r hcReport) Roots() []string {
	cov := r.coverage()
	full := map[string]bool{}
	for owner, kinds := range cov {
		if strings.HasPrefix(owner, "funcLit:") {
			continue
		}
		if kinds["manager"] && kinds["process"] && kinds["tracing"] {
			full[owner] = true
		}
	}
	called := map[string]bool{}
	for owner := range full {
		for callee := range r.CallGraph[owner] {
			called[callee] = true
		}
	}
	var out []string
	for owner := range full {
		if called[owner] {
			continue
		}
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

// analyzeHostCloseOwnership reports Host shutdown-primitive escapes for one
// production package.
func analyzeHostCloseOwnership(zone hcZone, files map[string]*ast.File) []hcFinding {
	return reportHostCloseOwnership(zone, files).Findings
}

func reportHostCloseOwnership(zone hcZone, files map[string]*ast.File) hcReport {
	idx := hcBuildIndex(zone, files)
	owners := map[string]*hcOwner{}
	callGraph := map[string]map[string]bool{}
	violations := map[string][]string{}
	touching := map[string]map[string]bool{}

	var findings []hcFinding
	for name, n := range idx.contestedDeclCounts {
		if n > 1 {
			findings = append(findings, hcFinding{
				Scope:  zone.Scope,
				Owner:  "package",
				Detail: "duplicate contested declaration " + name + " used by ownership resolution",
			})
		}
	}
	findings = append(findings, idx.scanPassiveRenamedStorage(zone)...)

	preHostOwners := map[string]bool{}
	for _, name := range hcSortedKeys(idx.funcs) {
		fd := idx.funcs[name]
		if idx.hcIsPreHostRollbackShape(fd) {
			preHostOwners[name] = true
			if n := idx.funcDeclCounts[name]; n > 1 {
				findings = append(findings, hcFinding{
					Scope:  zone.Scope,
					Owner:  "package",
					Detail: "duplicate contested declaration func:" + name + " used by ownership resolution",
				})
			}
		}
	}
	if zone.HostOwner && len(preHostOwners) > 1 {
		findings = append(findings, hcFinding{
			Scope:  zone.Scope,
			Owner:  "package",
			Detail: "multiple pre-Host rollback owners " + strings.Join(hcSortedKeys(preHostOwners), ","),
		})
	}

	for _, name := range hcSortedKeys(files) {
		for _, decl := range files[name].Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := hcOwnerKey(fd)
			if key == "" {
				continue
			}
			owners[key] = &hcOwner{key: key, decl: fd}
		}
	}

	// Mark FuncLit arguments to the sole pre-Host owner as blessed feeders only
	// when their authority is pre-Host typed provenance — never when the parent
	// carries complete Host provenance or the literal captures Host-derived
	// shutdown fields.
	blessedFeederLits := map[*ast.FuncLit]bool{}
	if len(preHostOwners) == 1 {
		var preHostName string
		for name := range preHostOwners {
			preHostName = name
		}
		for _, key := range hcSortedKeys(owners) {
			o := owners[key]
			if o.decl.Body == nil {
				continue
			}
			parentProv := idx.buildProvenance(o.decl, o.decl.Body, nil)
			parentHasHost := provenanceHasHost(parentProv)
			ast.Inspect(o.decl.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != preHostName {
					return true
				}
				if parentHasHost {
					return true
				}
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.FuncLit)
					if !ok {
						continue
					}
					if !idx.funcLitUsesHostProvenance(lit, parentProv) {
						blessedFeederLits[lit] = true
					}
				}
				return true
			})
		}
	}

	litSeq := 0
	blessedFeederKeys := map[string]bool{}
	var analyze func(key string, fd *ast.FuncDecl, body *ast.BlockStmt, outer *hcProvenance, blessedFeed bool)
	analyze = func(key string, fd *ast.FuncDecl, body *ast.BlockStmt, outer *hcProvenance, blessedFeed bool) {
		if body == nil {
			return
		}
		if callGraph[key] == nil {
			callGraph[key] = map[string]bool{}
		}
		prov := idx.buildProvenance(fd, body, outer)
		report := func(detail, kind string) {
			if blessedFeed {
				return
			}
			violations[key] = append(violations[key], detail)
			if kind == "" {
				return
			}
			owner := key
			if parent, ok := owners[key]; ok && parent.parent != "" {
				owner = parent.parent
			}
			if touching[owner] == nil {
				touching[owner] = map[string]bool{}
			}
			touching[owner][kind] = true
		}
		var preHostName string
		for name := range preHostOwners {
			preHostName = name
			break
		}
		parentHasHost := provenanceHasHost(prov)
		skipSelector := map[*ast.SelectorExpr]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				litSeq++
				litKey := fmt.Sprintf("funcLit:%s#%d", key, litSeq)
				synth := &ast.FuncDecl{Name: ast.NewIdent("lit"), Type: x.Type, Body: x.Body}
				owners[litKey] = &hcOwner{key: litKey, decl: synth, parent: key}
				feedBlessed := blessedFeed || blessedFeederLits[x]
				if blessedFeederLits[x] {
					blessedFeederKeys[litKey] = true
				}
				analyze(litKey, synth, x.Body, prov, feedBlessed)
				return false
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name != "" {
					callGraph[key][id.Name] = true
				}
				feedingPreHost := false
				if id, ok := x.Fun.(*ast.Ident); ok && preHostName != "" && id.Name == preHostName {
					feedingPreHost = true
				}
				if kind := idx.shutdownKindOf(x.Fun, fd, prov); kind == "tracing" {
					report("Host tracing shutdown invoked outside Host.Close", kind)
				}
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil {
					if fd != nil && fd.Recv != nil && hcIdentName(sel.X) != "" {
						if recvName := hcRecvName(fd); recvName != "" && hcIdentName(sel.X) == recvName {
							recvType := strings.TrimPrefix(hcTypeString(fd.Recv.List[0].Type), "*")
							callGraph[key][recvType+"."+sel.Sel.Name] = true
						}
					}
				}
				for _, arg := range x.Args {
					if feedingPreHost {
						idx.reportPreHostFeeder(arg, fd, prov, parentHasHost, skipSelector, report)
						continue
					}
					if detail, kind := idx.escapeDetail(arg, fd, prov); detail != "" {
						report(detail+" passed as an argument", kind)
					}
				}
			case *ast.SelectorExpr:
				if skipSelector[x] {
					return true
				}
				if detail, kind := idx.primitiveDetail(x, fd, prov); detail != "" {
					report(detail, kind)
				}
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					if kind := idx.shutdownKindOf(lhs, fd, prov); kind == "tracing" {
						report("Host tracing shutdown field mutated outside Host", kind)
					}
				}
				for _, rhs := range x.Rhs {
					if detail, kind := idx.escapeDetail(rhs, fd, prov); detail != "" {
						report(detail+" stored in an assignment", kind)
					}
				}
			case *ast.ReturnStmt:
				for _, res := range x.Results {
					if detail, kind := idx.escapeDetail(res, fd, prov); detail != "" {
						report(detail+" returned to a caller", kind)
					}
				}
			case *ast.CompositeLit:
				litType := strings.TrimPrefix(hcTypeString(x.Type), "*")
				for _, elt := range x.Elts {
					value := elt
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						value = kv.Value
					}
					if detail, kind := idx.escapeDetail(value, fd, prov); detail != "" {
						// Exact canonical composite literals may receive exact
						// canonical fields and Coordinator's focused gate.
						if idx.isExactCanonicalType(litType) {
							if idx.exprIsExactCanonical(value, fd, prov) {
								continue
							}
							if renamed := idx.renamedAuthorityExprType(value, fd, prov); renamed != "" &&
								idx.allowsRenamedFieldStorage(litType, renamed) {
								continue
							}
						}
						report(detail+" stored in a composite literal", kind)
					}
				}
			}
			return true
		})
	}

	for _, key := range hcSortedKeys(owners) {
		o := owners[key]
		analyze(key, o.decl, o.decl.Body, nil, false)
	}

	blessed := map[string]bool{}
	// Narrow canonical-owner blessing: only focused shutdown roots and their
	// actual call-graph descendants. Do not bless every Manager/Coordinator
	// method — arbitrary additions such as Manager.Rogue / Coordinator.Rogue
	// must remain contested.
	for _, root := range []string{
		"Manager.ShutdownDetached",
		"Coordinator.BeginShutdown",
		"Coordinator.WaitForIdle",
		"ProcessServices.Close",
	} {
		if _, ok := owners[root]; ok {
			blessed[root] = true
		}
	}
	if zone.HostOwner {
		for _, root := range []string{"ReloadHost.Close", "Host.Close"} {
			if _, ok := owners[root]; ok {
				blessed[root] = true
			}
		}
		for name := range preHostOwners {
			blessed[name] = true
		}
		// Only feeder lits whose authority is pre-Host typed (not Host-derived)
		// are blessed for uniqueness accounting.
		for litKey := range blessedFeederKeys {
			blessed[litKey] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for key := range blessed {
			for callee := range callGraph[key] {
				candidates := []string{callee}
				if zone.HostOwner {
					candidates = append(candidates, "ReloadHost."+callee, "Host."+callee)
				}
				for _, candidate := range candidates {
					if !blessed[candidate] {
						if _, ok := owners[candidate]; ok {
							blessed[candidate] = true
							changed = true
						}
					}
				}
			}
		}
		for litKey, o := range owners {
			if o.parent != "" && blessed[o.parent] && !blessed[litKey] {
				blessed[litKey] = true
				changed = true
			}
		}
	}

	for _, key := range hcSortedKeys(violations) {
		if blessed[key] {
			continue
		}
		for _, detail := range violations[key] {
			findings = append(findings, hcFinding{Scope: zone.Scope, Owner: key, Detail: detail})
		}
	}
	kinds := map[string][]string{}
	for owner, set := range touching {
		kinds[owner] = hcSortedKeys(set)
	}
	return hcReport{Findings: findings, Touching: kinds, CallGraph: callGraph, Blessed: blessed}
}

// reportPreHostFeeder validates an argument passed to the sole pre-Host rollback
// owner. Pre-Host typed method values/callbacks are skipped; Host-derived
// authority, complete Host values, and feeders from a Host-holding parent are not.
func (idx *hcIndex) reportPreHostFeeder(
	arg ast.Expr,
	fd *ast.FuncDecl,
	prov *hcProvenance,
	parentHasHost bool,
	skipSelector map[*ast.SelectorExpr]bool,
	report func(string, string),
) {
	switch a := arg.(type) {
	case *ast.FuncLit:
		// Function literals are analyzed on their own; blessing is decided up front.
		return
	case *ast.Ident:
		if prov.host[a.Name] {
			report("complete Host passed into pre-Host rollback owner", "host")
			return
		}
		if prov.hostDerived[a.Name] {
			kind := prov.kinds[a.Name]
			if kind == "" {
				kind = "manager"
			}
			report("Host "+kind+" shutdown authority passed as an argument", kind)
		}
		return
	case *ast.SelectorExpr:
		if a.Sel != nil {
			if kind, ok := hcHostShutdownFields[a.Sel.Name]; ok && idx.isHostExpr(a.X, fd, prov) {
				report("Host "+kind+" shutdown authority passed as an argument", kind)
				return
			}
		}
		hostDerived := parentHasHost ||
			idx.isHostDerivedAuthority(a, fd, prov) ||
			idx.isHostDerivedAuthority(a.X, fd, prov) ||
			idx.isHostExpr(a.X, fd, prov)
		if id, ok := a.X.(*ast.Ident); ok && (prov.hostDerived[id.Name] || prov.host[id.Name]) {
			hostDerived = true
		}
		if detail, kind := idx.primitiveDetail(a, fd, prov); detail != "" {
			if hostDerived {
				report(detail+" passed as an argument", kind)
				return
			}
			skipSelector[a] = true
			return
		}
		if detail, kind := idx.escapeDetail(a, fd, prov); detail != "" {
			report(detail+" passed as an argument", kind)
			return
		}
		if !hostDerived {
			skipSelector[a] = true
		}
		return
	default:
		if detail, kind := idx.escapeDetail(arg, fd, prov); detail != "" {
			report(detail+" passed as an argument", kind)
		}
	}
}

// primitiveDetail reports a shutdown-primitive invocation or method value
// reached from Host provenance.
func (idx *hcIndex) primitiveDetail(sel *ast.SelectorExpr, fd *ast.FuncDecl, prov *hcProvenance) (string, string) {
	if sel == nil || sel.Sel == nil {
		return "", ""
	}
	kind := idx.shutdownKindOf(sel.X, fd, prov)
	if kind == "" {
		return "", ""
	}
	name := sel.Sel.Name
	contested := false
	switch kind {
	case "manager":
		contested = hcManagerShutdown[name]
	case "process":
		contested = hcProcessShutdown[name]
	case "coordinator":
		contested = hcCoordinatorShutdown[name]
	}
	if !contested {
		return "", ""
	}
	// Exact Manager methods may drive their own contested primitives (retirement
	// / shutdown helpers). Renamed receivers are never covered by this rule.
	if kind == "manager" && idx.ownerRecvExactCanonical(fd) == "Manager" && idx.exprIsExactCanonical(sel.X, fd, prov) {
		return "", ""
	}
	// Exact Coordinator methods may drive the focused attempt-gate field; other
	// renamed coordinator equivalents remain contested.
	if kind == "coordinator" && idx.ownerRecvExactCanonical(fd) == "Coordinator" {
		if renamed := idx.renamedAuthorityExprType(sel.X, fd, prov); renamed != "" && idx.isCanonicalFocusedOwnedType(renamed) {
			return "", ""
		}
	}
	switch kind {
	case "manager":
		return "Host-owned manager shutdown primitive " + name + " invoked or method-valued outside Host.Close", kind
	case "process":
		return "Host-owned process runtime " + name + " invoked or method-valued outside Host.Close", kind
	case "coordinator":
		return "Host-owned coordinator shutdown primitive " + name + " invoked or method-valued outside Host.Close", kind
	}
	return "", ""
}

// scanPassiveRenamedStorage reports package globals and struct fields that
// store renamed structural shutdown authority without requiring method
// invocation. Exact canonical identities and narrowly focused Coordinator gate
// ownership (including mutually-coupled internal collaborators) stay valid.
func (idx *hcIndex) scanPassiveRenamedStorage(zone hcZone) []hcFinding {
	var out []hcFinding
	seenGlobal := map[string]bool{}
	reportGlobal := func(name, typ string) {
		if seenGlobal[name] || name == "" {
			return
		}
		if !idx.isRenamedAuthorityType(typ) {
			return
		}
		seenGlobal[name] = true
		kind := idx.authorityKindOfType(hcUnwrapAuthorityType(typ))
		if kind == "" {
			kind = "typed"
		}
		out = append(out, hcFinding{
			Scope:  zone.Scope,
			Owner:  "package",
			Detail: "renamed " + kind + " shutdown authority stored in package global " + name,
		})
	}
	for _, name := range hcSortedKeys(idx.globals) {
		reportGlobal(name, idx.globals[name])
	}
	for _, typeName := range hcSortedKeys(idx.structs) {
		if !idx.structHoldsDisallowedRenamedAuthority(typeName, map[string]bool{}) {
			continue
		}
		st := idx.structs[typeName]
		reported := false
		if st != nil && st.Fields != nil {
			for _, f := range st.Fields.List {
				ft := hcTypeString(f.Type)
				if idx.isRenamedAuthorityType(ft) && !idx.allowsRenamedFieldStorage(typeName, ft) {
					kind := idx.authorityKindOfType(hcUnwrapAuthorityType(ft))
					fieldName := "(embedded)"
					if len(f.Names) > 0 && f.Names[0] != nil {
						fieldName = f.Names[0].Name
					}
					out = append(out, hcFinding{
						Scope:  zone.Scope,
						Owner:  typeName,
						Detail: "renamed " + kind + " shutdown authority stored in struct field " + typeName + "." + fieldName,
					})
					reported = true
				}
			}
		}
		if !reported {
			out = append(out, hcFinding{
				Scope:  zone.Scope,
				Owner:  typeName,
				Detail: "renamed shutdown authority stored in nested struct holder " + typeName,
			})
		}
	}
	return out
}

// structHoldsDisallowedRenamedAuthority reports whether typeName transitively
// stores renamed contested authority outside the focused canonical exemptions.
func (idx *hcIndex) structHoldsDisallowedRenamedAuthority(typeName string, visiting map[string]bool) bool {
	base := strings.TrimPrefix(typeName, "*")
	if base == "" || visiting[base] {
		return false
	}
	visiting[base] = true
	st := idx.structs[base]
	if st == nil || st.Fields == nil {
		if alias, ok := idx.aliases[base]; ok {
			return idx.structHoldsDisallowedRenamedAuthority(alias, visiting)
		}
		return false
	}
	for _, f := range st.Fields.List {
		ft := hcTypeString(f.Type)
		if idx.isRenamedAuthorityType(ft) {
			if !idx.allowsRenamedFieldStorage(base, ft) {
				return true
			}
			continue
		}
		nested := strings.TrimPrefix(hcUnwrapAuthorityType(ft), "*")
		if nested == "" || nested == base {
			continue
		}
		if idx.structs[nested] == nil {
			if _, ok := idx.aliases[nested]; !ok {
				continue
			}
		}
		if idx.structHoldsDisallowedRenamedAuthority(nested, visiting) {
			return true
		}
	}
	return false
}

// escapeDetail reports Host shutdown authority leaving its owner through
// Host-field extraction (Manager/Process/Coordinator/ShutdownTracing) or
// shutdown method values. Exact canonical identities may be wired inside the
// package that declares them; renamed structural equivalents are contested
// everywhere unless they are narrowly focused canonical internals (for example
// Coordinator's attempt-gate field) used inside exact canonical wiring.
func (idx *hcIndex) escapeDetail(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) (string, string) {
	// Method values such as host.Process.Close / m.ShutdownDetached handed off.
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if detail, kind := idx.primitiveDetail(sel, fd, prov); detail != "" {
			return detail, kind
		}
	}
	kind := idx.shutdownKindOf(expr, fd, prov)
	if kind == "" {
		return "", ""
	}
	if idx.isHostDerivedAuthority(expr, fd, prov) {
		return "Host " + kind + " shutdown authority", kind
	}
	// Renamed structural authority is contested even in declaring packages.
	if renamed := idx.renamedAuthorityExprType(expr, fd, prov); renamed != "" {
		if idx.allowsRenamedWiringInOwner(fd, renamed) {
			return "", ""
		}
		return "Host " + kind + " shutdown authority", kind
	}
	// Exact canonical identities: declaring-package wiring is expected; consumer
	// packages may not pass/store/return them outside Host.Close.
	if !idx.zone.HostOwner && (kind == "manager" || kind == "process" || kind == "coordinator" || kind == "tracing") {
		if idx.exprIsExactCanonical(expr, fd, prov) && idx.declaresCanonicalForKind(kind) {
			return "", ""
		}
		return "Host " + kind + " shutdown authority", kind
	}
	return "", ""
}

// declaresCanonicalForKind reports whether this package declares the exact
// canonical identity for the contested kind (shape-seeded). Used only together
// with expression-level exact-identity checks — never as a package-wide
// renamed-authority exemption.
func (idx *hcIndex) declaresCanonicalForKind(kind string) bool {
	switch kind {
	case "manager":
		return idx.managerTypes["Manager"]
	case "coordinator":
		return idx.coordinatorTypes["Coordinator"]
	case "process":
		return idx.processTypes["ProcessServices"]
	default:
		return false
	}
}

func (idx *hcIndex) isHostDerivedAuthority(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return prov.hostDerived[x.Name]
	case *ast.ParenExpr:
		return idx.isHostDerivedAuthority(x.X, fd, prov)
	case *ast.StarExpr:
		return idx.isHostDerivedAuthority(x.X, fd, prov)
	case *ast.SelectorExpr:
		if x.Sel == nil {
			return false
		}
		if _, ok := hcHostShutdownFields[x.Sel.Name]; ok && idx.isHostExpr(x.X, fd, prov) {
			return true
		}
		if _, ok := hcHostShutdownFields[x.Sel.Name]; ok && idx.selectorIsHost(x.X, fd, prov) {
			return true
		}
	}
	return false
}

// shutdownKindOf classifies an expression as manager/process/coordinator/tracing
// authority from Host fields or direct typed provenance.
func (idx *hcIndex) shutdownKindOf(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) string {
	switch x := expr.(type) {
	case *ast.Ident:
		if kind := prov.kinds[x.Name]; kind != "" {
			return kind
		}
		return idx.authorityGlobals[x.Name]
	case *ast.ParenExpr:
		return idx.shutdownKindOf(x.X, fd, prov)
	case *ast.StarExpr:
		return idx.shutdownKindOf(x.X, fd, prov)
	case *ast.CallExpr:
		return idx.callReturnsAuthorityKind(x, fd, prov)
	case *ast.SelectorExpr:
		if x.Sel == nil {
			return ""
		}
		if kind, ok := hcHostShutdownFields[x.Sel.Name]; ok && idx.isHostExpr(x.X, fd, prov) {
			return kind
		}
		// Nested holder: cfg.host.Manager where cfg.host is Host provenance.
		if kind, ok := hcHostShutdownFields[x.Sel.Name]; ok && idx.selectorIsHost(x.X, fd, prov) {
			return kind
		}
		// Typed nested/aliased fields: bag.Manager, outer.process, etc.
		if base := idx.namedTypeOf(x.X, fd, prov); base != "" {
			if kind := idx.fieldAuthorityKind(base, x.Sel.Name); kind != "" && kind != "host" {
				return kind
			}
		}
		// Ident container with typed field through Host-like name without type info:
		// already covered by fieldAuthorityKind when namedTypeOf works.
	}
	return ""
}

func (idx *hcIndex) isHostExpr(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return prov.host[x.Name] || idx.hostGlobals[x.Name]
	case *ast.ParenExpr:
		return idx.isHostExpr(x.X, fd, prov)
	case *ast.StarExpr:
		return idx.isHostExpr(x.X, fd, prov)
	case *ast.SelectorExpr:
		return idx.selectorIsHost(x, fd, prov)
	case *ast.CallExpr:
		return idx.callReturnsHost(x, fd, prov)
	}
	return false
}

func (idx *hcIndex) selectorIsHost(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	base := idx.namedTypeOf(sel.X, fd, prov)
	if base == "" {
		return false
	}
	ft := idx.fieldType(base, sel.Sel.Name, map[string]bool{})
	return ft != "" && idx.isHostType(ft)
}

func (idx *hcIndex) namedTypeOf(expr ast.Expr, fd *ast.FuncDecl, prov *hcProvenance) string {
	switch x := expr.(type) {
	case *ast.Ident:
		if prov != nil {
			if t := prov.types[x.Name]; t != "" {
				return strings.TrimPrefix(t, "*")
			}
		}
		if fd != nil {
			if fd.Recv != nil {
				for _, f := range fd.Recv.List {
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return strings.TrimPrefix(hcTypeString(f.Type), "*")
						}
					}
				}
			}
			if fd.Type != nil && fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					for _, n := range f.Names {
						if n != nil && n.Name == x.Name {
							return strings.TrimPrefix(hcTypeString(f.Type), "*")
						}
					}
				}
			}
		}
		if t, ok := idx.globals[x.Name]; ok {
			return strings.TrimPrefix(t, "*")
		}
		return ""
	case *ast.ParenExpr:
		return idx.namedTypeOf(x.X, fd, prov)
	case *ast.StarExpr:
		return idx.namedTypeOf(x.X, fd, prov)
	case *ast.SelectorExpr:
		base := idx.namedTypeOf(x.X, fd, prov)
		if base == "" || x.Sel == nil {
			return ""
		}
		return strings.TrimPrefix(idx.fieldType(base, x.Sel.Name, map[string]bool{}), "*")
	}
	return ""
}

func (idx *hcIndex) callReturnsHost(call *ast.CallExpr, fd *ast.FuncDecl, prov *hcProvenance) bool {
	var decl *ast.FuncDecl
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		decl = idx.funcs[fun.Name]
	case *ast.SelectorExpr:
		base := idx.namedTypeOf(fun.X, fd, prov)
		if base == "" || fun.Sel == nil {
			return false
		}
		for _, md := range idx.methods[base] {
			if md.Name != nil && md.Name.Name == fun.Sel.Name {
				decl = md
			}
		}
	}
	if decl == nil || decl.Type == nil || decl.Type.Results == nil {
		return false
	}
	for _, f := range decl.Type.Results.List {
		if idx.isHostType(hcTypeString(f.Type)) {
			return true
		}
	}
	return false
}

// buildProvenance seeds Host and typed shutdown-authority identifiers from the
// receiver, parameters, captured outer scope, package globals, and local
// assignments (fixed point over aliases).
func (idx *hcIndex) buildProvenance(fd *ast.FuncDecl, body *ast.BlockStmt, outer *hcProvenance) *hcProvenance {
	prov := newHCProvenance(outer)
	for name := range idx.hostGlobals {
		prov.host[name] = true
	}
	for name, kind := range idx.authorityGlobals {
		prov.kinds[name] = kind
	}
	for name, typ := range idx.globals {
		if typ != "" {
			prov.types[name] = typ
		}
	}
	seed := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, f := range fields.List {
			typ := hcTypeString(f.Type)
			isHost := idx.isHostType(typ) || idx.authorityKindOfAST(f.Type) == "host"
			kind := idx.authorityKindOfAST(f.Type)
			if kind == "" {
				kind = idx.authorityKindOfType(typ)
			}
			for _, n := range f.Names {
				if n == nil || n.Name == "_" {
					continue
				}
				if typ != "" {
					prov.types[n.Name] = typ
				}
				if isHost {
					prov.host[n.Name] = true
					continue
				}
				delete(prov.host, n.Name)
				if kind != "" && kind != "host" {
					prov.kinds[n.Name] = kind
				} else {
					delete(prov.kinds, n.Name)
				}
			}
		}
	}
	if fd != nil {
		seed(fd.Recv)
		if fd.Type != nil {
			seed(fd.Type.Params)
		}
	}
	if body == nil {
		return prov
	}
	for range 8 {
		changed := false
		ast.Inspect(body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			var lhs []ast.Expr
			var rhs []ast.Expr
			switch x := n.(type) {
			case *ast.AssignStmt:
				lhs, rhs = x.Lhs, x.Rhs
			case *ast.ValueSpec:
				for _, name := range x.Names {
					lhs = append(lhs, name)
				}
				rhs = x.Values
				if x.Type != nil && len(rhs) == 0 {
					declTyp := hcTypeString(x.Type)
					kind := idx.authorityKindOfType(declTyp)
					for _, name := range x.Names {
						if name == nil || name.Name == "_" {
							continue
						}
						if declTyp != "" && prov.types[name.Name] != declTyp {
							prov.types[name.Name] = declTyp
							changed = true
						}
						if idx.isHostType(declTyp) && !prov.host[name.Name] {
							prov.host[name.Name] = true
							changed = true
						}
						if kind != "" && kind != "host" && prov.kinds[name.Name] != kind {
							prov.kinds[name.Name] = kind
							changed = true
						}
					}
					return true
				}
			default:
				return true
			}
			if len(lhs) != len(rhs) {
				return true
			}
			for i := range lhs {
				id, ok := lhs[i].(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				if idx.isHostExpr(rhs[i], fd, prov) && !prov.host[id.Name] {
					prov.host[id.Name] = true
					changed = true
				}
				if kind := idx.shutdownKindOf(rhs[i], fd, prov); kind != "" && prov.kinds[id.Name] != kind {
					prov.kinds[id.Name] = kind
					changed = true
				}
				if typ := idx.inferExprType(rhs[i], fd, prov); typ != "" && prov.types[id.Name] != typ {
					prov.types[id.Name] = typ
					changed = true
				} else if typ := idx.namedTypeOf(rhs[i], fd, prov); typ != "" {
					full := typ
					if !strings.HasPrefix(full, "*") {
						// Keep pointer shape when RHS is a pointer field/ident.
						if inferred := idx.inferExprType(rhs[i], fd, prov); strings.HasPrefix(inferred, "*") {
							full = inferred
						} else if sel, ok := rhs[i].(*ast.SelectorExpr); ok {
							if base := idx.namedTypeOf(sel.X, fd, prov); base != "" && sel.Sel != nil {
								if ft := idx.fieldType(base, sel.Sel.Name, map[string]bool{}); strings.HasPrefix(ft, "*") {
									full = ft
								}
							}
						}
					}
					if prov.types[id.Name] != full && full != "" {
						prov.types[id.Name] = full
						changed = true
					}
				}
				if idx.isHostDerivedAuthority(rhs[i], fd, prov) && !prov.hostDerived[id.Name] {
					prov.hostDerived[id.Name] = true
					changed = true
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

func hcRecvName(fd *ast.FuncDecl) string {
	if fd == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	for _, n := range fd.Recv.List[0].Names {
		if n != nil {
			return n.Name
		}
	}
	return ""
}

func hcIdentName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func hcTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + hcTypeString(t.X)
	case *ast.SelectorExpr:
		return hcTypeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + hcTypeString(t.Elt)
	case *ast.MapType:
		return "map[" + hcTypeString(t.Key) + "]" + hcTypeString(t.Value)
	case *ast.ChanType:
		return "chan " + hcTypeString(t.Value)
	case *ast.FuncType:
		return "func" + hcFuncSignature(t)
	case *ast.InterfaceType:
		return "interface"
	case *ast.StructType:
		return "struct"
	case *ast.Ellipsis:
		return "..." + hcTypeString(t.Elt)
	case *ast.IndexExpr:
		return hcTypeString(t.X) + "[" + hcTypeString(t.Index) + "]"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func hcFuncSignature(ft *ast.FuncType) string {
	params := hcFieldTypes(ft.Params)
	results := hcFieldTypes(ft.Results)
	return "(" + strings.Join(params, ",") + ")(" + strings.Join(results, ",") + ")"
}

func hcFieldTypes(fl *ast.FieldList) []string {
	var out []string
	if fl == nil {
		return out
	}
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			out = append(out, hcTypeString(f.Type))
		}
	}
	return out
}
