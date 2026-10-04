package archtest

// The whole-feature index shared by the ALG multi-file ratchets: the
// terminal-owner census (agent_loop_guard_terminal_owner_census.go) and the two
// strategy-isolation walks (agent_loop_guard_strategy_isolation_ratchet.go).
//
// It lives apart from both because it is neither a policy nor a validator: it is
// the fact set they judge over. It carries no ALG rule, so moving it out changes
// no verdict, and it keeps every ALG source file inside this package's
// 500-line cap.
//
// It is built from an EXPLICIT file set, which is what lets a fixture overlay a
// miniature feature tree. The repository walk feeds it the whole feature (see
// algFeatureTreeFile), and the fixtures feed it whatever files they choose.
//
// Every identity here is PACKAGE QUALIFIED. That is not decoration: the index
// spans a decomposed feature, so `Evaluate`, `Parse` or a receiver name can
// legitimately exist in two of its packages. A name-only key would silently keep
// one declaration and drop the other, which would make a subpackage
// declaration invisible exactly as it was before the index was widened.

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

// algDeclID identifies one indexed declaration: the repo-relative package
// directory that declares it plus the same-package short name (a plain function
// name, or "recv.Method").
//
// The package is part of the identity because the index now covers the whole
// feature tree, where the same short name legitimately exists in two packages
// (verifier.Parse and protocolpolicy.Evaluate-style helpers alike). A name-only
// key would silently drop one of the two declarations, so a subpackage
// declaration could become invisible exactly as it was before the index was
// widened.
type algDeclID struct {
	Pkg   string
	Short string
}

// algTypeID identifies one indexed type declaration, for the same reason
// algDeclID carries its package: two packages may declare a type of the same
// name, and a receiver-name collision must not hide a terminal owner.
type algTypeID struct {
	Pkg  string
	Name string
}

// algPackageIndex is the feature call-graph index used by the ALG ratchets. It
// is built from an explicit file set so a fixture can overlay a deliberately
// violating miniature feature tree.
type algPackageIndex struct {
	decls       map[algDeclID]*ast.FuncDecl
	relOfDecl   map[algDeclID]string
	fsetOfDecl  map[algDeclID]*token.FileSet
	recvOfDecl  map[algDeclID]string
	typesOfDecl map[algTypeID]algPackageType
	embedOfType map[algTypeID][]algTypeID
	imports     map[string]map[string]string
	pkgOfRel    map[string]string
}

func newAlgPackageIndex(files []algSourceFile) *algPackageIndex {
	idx := &algPackageIndex{
		decls:       make(map[algDeclID]*ast.FuncDecl),
		relOfDecl:   make(map[algDeclID]string),
		fsetOfDecl:  make(map[algDeclID]*token.FileSet),
		recvOfDecl:  make(map[algDeclID]string),
		typesOfDecl: make(map[algTypeID]algPackageType),
		embedOfType: make(map[algTypeID][]algTypeID),
		imports:     make(map[string]map[string]string),
		pkgOfRel:    make(map[string]string),
	}
	for _, file := range files {
		pkg := PackageDirFromRel(file.RelPath)
		idx.imports[file.RelPath] = algImportAliases(file.AST)
		idx.pkgOfRel[file.RelPath] = pkg
		for _, decl := range file.AST.Decls {
			switch typed := decl.(type) {
			case *ast.FuncDecl:
				fn := typed
				if fn.Name == nil || fn.Body == nil {
					continue
				}
				recv := algReceiverBaseName(fn.Recv)
				short := fn.Name.Name
				if recv != "" {
					short = recv + "." + fn.Name.Name
				}
				key := algDeclID{Pkg: pkg, Short: short}
				if _, exists := idx.decls[key]; exists {
					continue
				}
				idx.decls[key] = fn
				idx.relOfDecl[key] = file.RelPath
				idx.fsetOfDecl[key] = file.FSet
				idx.recvOfDecl[key] = recv
				if recv != "" {
					id := algTypeID{Pkg: pkg, Name: recv}
					info := idx.typesOfDecl[id]
					info.methods = append(info.methods, short)
					idx.typesOfDecl[id] = info
				}
			case *ast.GenDecl:
				idx.recordTypeDecl(typed, file.RelPath)
			}
		}
	}
	return idx
}

// receiverDeclKeys returns the declarations of every method on the named receiver
// in the feature ROOT package.
//
// The root package is the whole scope, not a narrowing: the terminal-owner
// census pins each approved receiver to its approved file, so a same-named
// receiver declared in a subpackage is already a census violation and is
// reported with the file that declares it. Seeding the walk from it as well
// would only duplicate that finding.
func (i *algPackageIndex) receiverDeclKeys(recv string) []algDeclID {
	var keys []algDeclID
	for key := range i.decls {
		if key.Pkg == algFeatureRootDir && i.recvOfDecl[key] == recv {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a].Short < keys[b].Short })
	return keys
}

// algTerminalOwner is one observed terminal-policy receiver: its bare name, the
// feature package that declares it, and the files that declare it.
type algTerminalOwner struct {
	Name string
	Pkg  string
	Rels []string
}

// terminalOwners maps each receiver NAME whose method set contains the generic
// terminal-decision method to every package of the indexed feature that declares
// it. A type qualifies by declaring algDecideMethod itself or by inheriting it
// through a chain of same-package embedded types, because embedding satisfies
// the same seam; the inheritance is closed transitively, so the chain claim in
// the census contract is the implemented one. Receivers without that method - the
// feature config value, the control-tool handle - are not terminal owners and are
// deliberately excluded.
//
// The index covers the whole feature tree, so the search is not limited to the
// root package: a Decide receiver smuggled into a subpackage satisfies the same
// terminaldecision.Provider seam and would be a second terminal owner the moment
// it was published.
func (i *algPackageIndex) terminalOwners() map[string][]algTerminalOwner {
	grouped := make(map[string]map[string][]string)
	add := func(recv, pkg, rel string) {
		byPkg := grouped[recv]
		if byPkg == nil {
			byPkg = make(map[string][]string)
			grouped[recv] = byPkg
		}
		byPkg[pkg] = appendUnique(byPkg[pkg], rel)
	}

	var queue []algTypeID
	declaring := make(map[algTypeID]bool)
	for key, recv := range i.recvOfDecl {
		if recv == "" || !strings.HasSuffix(key.Short, "."+algDecideMethod) {
			continue
		}
		add(recv, key.Pkg, i.relOfDecl[key])
		id := algTypeID{Pkg: key.Pkg, Name: recv}
		if !declaring[id] {
			declaring[id] = true
			queue = append(queue, id)
		}
	}
	visited := make(map[algTypeID]bool, len(queue))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		for _, embedder := range i.embedOfType[id] {
			for _, rel := range i.typesOfDecl[embedder].rels {
				add(embedder.Name, embedder.Pkg, rel)
			}
			if !visited[embedder] {
				queue = append(queue, embedder)
			}
		}
	}

	owners := make(map[string][]algTerminalOwner, len(grouped))
	for _, recv := range sortedKeys(grouped) {
		byPkg := grouped[recv]
		for _, pkg := range sortedKeys(byPkg) {
			owners[recv] = append(owners[recv], algTerminalOwner{
				Name: recv,
				Pkg:  pkg,
				Rels: algSortedUnique(byPkg[pkg]),
			})
		}
	}
	return owners
}

// strategyCaseBody returns the id of the named package-level function and the
// statements of the switch case selected by the named strategy constant inside
// it. A missing case is reported as unprovable isolation rather than silently
// skipped.
func (i *algPackageIndex) strategyCaseBody(fnName, selector string) (algDeclID, []ast.Stmt, bool) {
	var constructor algDeclID
	for key := range i.decls {
		if key.Pkg == algFeatureRootDir && key.Short == fnName {
			constructor = key
			break
		}
	}
	fn, ok := i.decls[constructor]
	if !ok || fn.Body == nil {
		return constructor, nil, false
	}
	var found []ast.Stmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SwitchStmt:
			for _, stmt := range node.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok || len(clause.List) == 0 {
					continue
				}
				if ident, ok := clause.List[0].(*ast.Ident); ok && ident.Name == selector {
					found = clause.Body
					return false
				}
			}
		case *ast.FuncLit:
			// A closure body is not the constructor's own dispatch.
			return false
		}
		return true
	})
	if found == nil {
		return constructor, nil, false
	}
	return constructor, found, true
}

// forbiddenAlias reports whether the call's package qualifier is an import alias
// in the CALLING file for one of the forbidden dependency suffixes. Resolution
// is per file, so an alias that some other indexed file happens to use cannot
// make an unrelated call look forbidden.
func (i *algPackageIndex) forbiddenAlias(rel string, hit algCallHit, suffixes []string) (string, string, bool) {
	if hit.pkgIdent == "" {
		return "", "", false
	}
	path, ok := i.imports[rel][hit.pkgIdent]
	if !ok || !algForbiddenImport(path, suffixes) {
		return "", "", false
	}
	return hit.pkgIdent, path, true
}

// resolveCallee maps an observed call in rel to the next indexed declaration key.
//
// Two shapes resolve, and both are resolved AGAINST THE CALLING FILE so the
// result cannot depend on which other packages happen to be indexed:
//
//   - a plain same-package call, resolved against the calling package's declared
//     functions;
//   - a qualified call whose qualifier is an import alias of an INDEXED FEATURE
//     PACKAGE, resolved against that package's exported functions. This is the
//     hop the shipped layout crosses (preferredProvider.Decide ->
//     protocolpolicy.Evaluate), and without it the subpackages the feature is
//     actually decomposed into would sit outside the walk's horizon.
//
// Every other selector call does not resolve: the walk carries no type
// information and therefore cannot attribute pkgIdent.Sel to a declared method
// (see the walk's coverage contract above), nor to a package outside the
// feature. Selector calls are still checked for a forbidden import alias
// through forbiddenAlias, which is the only shape a forbidden cross-package
// reference takes.
func (i *algPackageIndex) resolveCallee(rel string, hit algCallHit) (algDeclID, bool) {
	pkg := i.pkgOfRel[rel]
	if hit.selName == "" {
		id := algDeclID{Pkg: pkg, Short: hit.identName}
		if _, ok := i.decls[id]; ok {
			return id, true
		}
		return algDeclID{}, false
	}
	imported, ok := i.imports[rel][hit.pkgIdent]
	if !ok {
		return algDeclID{}, false
	}
	target := algFeaturePackageDir(imported)
	if target == "" {
		return algDeclID{}, false
	}
	id := algDeclID{Pkg: target, Short: hit.selName}
	if _, ok := i.decls[id]; ok {
		return id, true
	}
	return algDeclID{}, false
}

// calleesOf resolves the indexed declarations called directly from stmts of rel.
func (i *algPackageIndex) calleesOf(rel string, stmts []ast.Stmt) []algDeclID {
	var out []algDeclID
	for _, hit := range algCallHits(stmts) {
		if next, ok := i.resolveCallee(rel, hit); ok {
			out = append(out, next)
		}
	}
	return out
}

func (i *algPackageIndex) callHits(fn *ast.FuncDecl) []algCallHit {
	if fn == nil || fn.Body == nil {
		return nil
	}
	return algCallHits(fn.Body.List)
}
