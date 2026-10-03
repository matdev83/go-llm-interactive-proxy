package archtest

// Agent-loop-guard (ALG) terminal-owner census (Requirement 9.5, design ratchet
// 5): the feature must expose exactly the two approved terminal-policy
// receivers, and each must really implement the generic terminaldecision.Provider
// seam, so neither strategy owns a separate terminal publication path.
//
// This lives apart from the strategy-isolation walks because it is a different
// question: the census is about WHICH types are terminal owners, while the walks
// are about what one strategy's construction graph may reach. Both are pure
// functions over the same explicit file set, so a fixture can overlay a
// deliberately violating miniature package and assert the verdict.

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

// algPackageType is the structural fact set the census needs about one type: the
// files that declare it, the methods it declares, and the same-package types it
// embeds.
type algPackageType struct {
	rels    []string
	methods []string
	embeds  []string
}

// recordTypeDecl records where a type is declared and which same-package types it
// embeds, so a receiver that inherits the terminal-decision method through an
// embedded type is still counted as a terminal owner.
func (i *algPackageIndex) recordTypeDecl(decl *ast.GenDecl, rel string) {
	if decl.Tok != token.TYPE {
		return
	}
	pkg := PackageDirFromRel(rel)
	for _, spec := range decl.Specs {
		typeSpec, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		id := algTypeID{Pkg: pkg, Name: typeSpec.Name.Name}
		info := i.typesOfDecl[id]
		info.rels = appendUnique(info.rels, rel)
		structType, ok := unwrapParen(typeSpec.Type).(*ast.StructType)
		if !ok || structType.Fields == nil {
			i.typesOfDecl[id] = info
			continue
		}
		for _, field := range structType.Fields.List {
			if len(field.Names) != 0 {
				continue
			}
			embedded := algTypeName(field.Type)
			if embedded == "" {
				continue
			}
			info.embeds = appendUnique(info.embeds, embedded)
			embeddedID := algTypeID{Pkg: pkg, Name: embedded}
			i.embedOfType[embeddedID] = appendUnique(i.embedOfType[embeddedID], id)
		}
		i.typesOfDecl[id] = info
	}
}

// scanAlgTerminalOwnerCensus proves the feature exposes only the two approved
// terminal-policy receivers, each of which really implements the same generic
// terminaldecision.Provider seam, so neither strategy owns a separate terminal
// publication path (Requirement 9.5).
//
// The file set is the WHOLE feature, root package and subpackages alike: a Decide
// receiver planted in a subpackage satisfies the same seam, so a census that read
// only the root package would certify a second terminal owner as absent.
//
// The census is deliberately wider than "types that declare Decide":
//
//   - a receiver that INHERITS Decide through an embedded same-package type is
//     counted too, because it satisfies the same seam and would be a second
//     terminal owner the moment it is published;
//   - an approved receiver whose Decide is not the seam shape is reported, so
//     the claim that both implement terminaldecision.Provider is verified rather
//     than assumed. The seam shape is read syntactically from the declaring
//     file: two parameters, an error first result, and both the input and
//     decision types qualified by the import alias of pkg/lipsdk/terminaldecision
//     in that same file.
func scanAlgTerminalOwnerCensus(files []algSourceFile) []RuleFinding {
	idx := newAlgPackageIndex(files)
	var findings []RuleFinding
	observed := idx.terminalOwners()
	for _, recv := range sortedKeys(algTerminalOwnerReceivers) {
		want := algTerminalOwnerReceivers[recv]
		owners := observed[recv]
		if len(owners) == 0 {
			findings = append(findings, RuleFinding{
				Rule:   RuleALGSecondTerminalOwner,
				Path:   algFeatureRootDir,
				Detail: fmt.Sprintf("approved ALG terminal receiver %q is absent, so the terminal-owner census is incomplete", recv),
			})
			continue
		}
		for _, owner := range owners {
			for _, rel := range owner.Rels {
				if rel != want {
					findings = append(findings, RuleFinding{
						Rule:   RuleALGSecondTerminalOwner,
						Path:   rel,
						Detail: fmt.Sprintf("ALG terminal receiver %q is implemented in %s, want %s", recv, rel, want),
					})
				}
			}
		}
	}
	for _, recv := range sortedKeys(observed) {
		if _, approved := algTerminalOwnerReceivers[recv]; approved {
			continue
		}
		for _, owner := range observed[recv] {
			findings = append(findings, RuleFinding{
				Rule: RuleALGSecondTerminalOwner,
				Path: strings.Join(owner.Rels, ","),
				Detail: fmt.Sprintf("ALG declares unapproved terminal-policy receiver %q in %s that implements %s; at most %d approved receivers may exist",
					recv, owner.Pkg, algDecideMethod, len(algTerminalOwnerReceivers)),
			})
		}
	}
	findings = append(findings, idx.seamFindings()...)
	return findings
}

// seamFindings reports a declared Decide whose signature is not the generic
// terminal-decision seam, which would make the approved receiver an unverified
// terminal owner rather than a conforming one.
//
// The seam alias is read from the file that declares the method, not from the
// file set as a whole: the index spans the whole feature, so one package's
// terminaldecision import must not vouch for another's Decide.
func (i *algPackageIndex) seamFindings() []RuleFinding {
	var findings []RuleFinding
	for _, id := range sortedAlgTypeIDs(i.typesOfDecl) {
		info := i.typesOfDecl[id]
		if !info.declaresMethod(algDecideMethod) {
			continue
		}
		seam := i.isTerminalDecisionSeam(id, info)
		if _, approved := algTerminalOwnerReceivers[id.Name]; !approved && seam {
			// Already reported as an unapproved owner; the seam check adds
			// nothing for it.
			continue
		}
		if !seam {
			findings = append(findings, RuleFinding{
				Rule:   RuleALGSecondTerminalOwner,
				Path:   strings.Join(info.rels, ","),
				Detail: fmt.Sprintf("ALG receiver %q declares %s with a signature that is not the generic terminal-decision seam, so the second-terminal-owner census cannot certify it", id.Name, algDecideMethod),
			})
		}
	}
	return findings
}

// terminalDecisionAlias returns the import alias the given file uses for
// pkg/lipsdk/terminaldecision, or "" when that file does not import it.
func (i *algPackageIndex) terminalDecisionAlias(rel string) string {
	path := algModulePath + "/pkg/lipsdk/terminaldecision"
	for alias, imported := range i.imports[rel] {
		if imported == path {
			return alias
		}
	}
	return ""
}

// isTerminalDecisionSeam reports whether the type declares algDecideMethod with
// the generic seam shape: (context.Context, terminaldecision.Input)
// (terminaldecision.Decision, error).
func (i *algPackageIndex) isTerminalDecisionSeam(id algTypeID, info algPackageType) bool {
	if len(info.rels) == 0 || i.terminalDecisionAlias(info.rels[0]) == "" {
		return false
	}
	key := algDeclID{Pkg: id.Pkg, Short: id.Name + "." + algDecideMethod}
	fn, ok := i.decls[key]
	if !ok || fn.Type.Params == nil || fn.Type.Results == nil {
		return false
	}
	termAlias := i.terminalDecisionAlias(info.rels[0])
	if len(fn.Type.Params.List) != 2 || len(fn.Type.Results.List) != 2 {
		return false
	}
	inParam := algTypeName(fn.Type.Params.List[1].Type)
	decision, errType := fn.Type.Results.List[0].Type, fn.Type.Results.List[1].Type
	return inParam == termAlias+".Input" &&
		algTypeName(decision) == termAlias+".Decision" &&
		algTypeName(errType) == "error"
}

// sortedAlgTypeIDs returns the indexed type ids in a stable order so census
// diagnostics are deterministic.
func sortedAlgTypeIDs(in map[algTypeID]algPackageType) []algTypeID {
	out := make([]algTypeID, 0, len(in))
	for id := range in {
		out = append(out, id)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Pkg != out[b].Pkg {
			return out[a].Pkg < out[b].Pkg
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// declaresMethod reports whether the receiver declares a method with name.
func (t algPackageType) declaresMethod(name string) bool {
	for _, key := range t.methods {
		if strings.HasSuffix(key, "."+name) {
			return true
		}
	}
	return false
}
