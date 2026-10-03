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
	"strings"
)

// algPackageType is the structural fact set the census needs about one
// same-package type: the files that declare it, the methods it declares, and the
// same-package types it embeds.
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
	for _, spec := range decl.Specs {
		typeSpec, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		info := i.typesOfDecl[typeSpec.Name.Name]
		info.rels = appendUnique(info.rels, rel)
		structType, ok := unwrapParen(typeSpec.Type).(*ast.StructType)
		if !ok || structType.Fields == nil {
			i.typesOfDecl[typeSpec.Name.Name] = info
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
			i.embedOfType[embedded] = appendUnique(i.embedOfType[embedded], typeSpec.Name.Name)
		}
		i.typesOfDecl[typeSpec.Name.Name] = info
	}
}

// scanAlgTerminalOwnerCensus proves the feature exposes only the two approved
// terminal-policy receivers, each of which really implements the same generic
// terminaldecision.Provider seam, so neither strategy owns a separate terminal
// publication path (Requirement 9.5).
//
// The census is deliberately wider than "types that declare Decide":
//
//   - a receiver that INHERITS Decide through an embedded same-package type is
//     counted too, because it satisfies the same seam and would be a second
//     terminal owner the moment it is published;
//   - an approved receiver whose Decide is not the seam shape is reported, so
//     the claim that both implement terminaldecision.Provider is verified rather
//     than assumed. The seam shape is read syntactically: two parameters, an
//     error first result, and both the input and decision types qualified by the
//     import alias of pkg/lipsdk/terminaldecision.
func scanAlgTerminalOwnerCensus(files []algSourceFile) []RuleFinding {
	idx := newAlgPackageIndex(files)
	var findings []RuleFinding
	observed := idx.terminalReceiverFiles()
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
			if owner != want {
				findings = append(findings, RuleFinding{
					Rule:   RuleALGSecondTerminalOwner,
					Path:   owner,
					Detail: fmt.Sprintf("ALG terminal receiver %q is implemented in %s, want %s", recv, owner, want),
				})
			}
		}
	}
	for _, recv := range sortedKeys(observed) {
		if _, approved := algTerminalOwnerReceivers[recv]; approved {
			continue
		}
		findings = append(findings, RuleFinding{
			Rule:   RuleALGSecondTerminalOwner,
			Path:   strings.Join(observed[recv], ","),
			Detail: fmt.Sprintf("ALG declares unapproved terminal-policy receiver %q that implements %s; at most %d approved receivers may exist", recv, algDecideMethod, len(algTerminalOwnerReceivers)),
		})
	}
	findings = append(findings, idx.seamFindings()...)
	return findings
}

// seamFindings reports a declared Decide whose signature is not the generic
// terminal-decision seam, which would make the approved receiver an unverified
// terminal owner rather than a conforming one.
func (i *algPackageIndex) seamFindings() []RuleFinding {
	var findings []RuleFinding
	termAlias := i.terminalDecisionAlias()
	for _, recv := range sortedKeys(i.typesOfDecl) {
		info := i.typesOfDecl[recv]
		if !info.declaresMethod(algDecideMethod) {
			continue
		}
		seam := i.isTerminalDecisionSeam(recv, termAlias)
		if _, approved := algTerminalOwnerReceivers[recv]; !approved && seam {
			// Already reported as an unapproved owner; the seam check adds
			// nothing for it.
			continue
		}
		if !seam {
			findings = append(findings, RuleFinding{
				Rule:   RuleALGSecondTerminalOwner,
				Path:   strings.Join(info.rels, ","),
				Detail: fmt.Sprintf("ALG receiver %q declares %s with a signature that is not the generic terminal-decision seam, so the second-terminal-owner census cannot certify it", recv, algDecideMethod),
			})
		}
	}
	return findings
}

// terminalDecisionAlias returns the import alias the package uses for
// pkg/lipsdk/terminaldecision, or "" when the file set does not import it.
func (i *algPackageIndex) terminalDecisionAlias() string {
	path := algModulePath + "/pkg/lipsdk/terminaldecision"
	for _, aliases := range i.imports {
		for alias, imported := range aliases {
			if imported == path {
				return alias
			}
		}
	}
	return ""
}

// isTerminalDecisionSeam reports whether recv declares algDecideMethod with the
// generic seam shape: (context.Context, terminaldecision.Input)
// (terminaldecision.Decision, error).
func (i *algPackageIndex) isTerminalDecisionSeam(recv, termAlias string) bool {
	if termAlias == "" {
		return false
	}
	key := recv + "." + algDecideMethod
	fn, ok := i.decls[key]
	if !ok || fn.Type.Params == nil || fn.Type.Results == nil {
		return false
	}
	if len(fn.Type.Params.List) != 2 || len(fn.Type.Results.List) != 2 {
		return false
	}
	inParam := algTypeName(fn.Type.Params.List[1].Type)
	decision, errType := fn.Type.Results.List[0].Type, fn.Type.Results.List[1].Type
	return inParam == termAlias+".Input" &&
		algTypeName(decision) == termAlias+".Decision" &&
		algTypeName(errType) == "error"
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
