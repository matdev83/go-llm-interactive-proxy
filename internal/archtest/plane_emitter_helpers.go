package archtest

import (
	"bytes"
	"fmt"
	"strings"
)

// emitRequestExecutionView generates RequestExecutionView struct and accessor methods.
func emitRequestExecutionView(buf *bytes.Buffer, planes []planeInfo) {
	buf.WriteString("// RequestExecutionView is an immutable borrowed view over request-materialized planes.\n")
	buf.WriteString("// Its returned slices must not be mutated.\n")
	buf.WriteString("type RequestExecutionView struct {\n")
	buf.WriteString("\tfrozen FrozenPlaneSet\n")
	buf.WriteString("}\n\n")

	buf.WriteString("// RequestExecution constructs a RequestExecutionView over the request-materialized planes in in.\n")
	buf.WriteString("func RequestExecution(in FrozenPlaneSet) RequestExecutionView {\n")
	buf.WriteString("\treturn RequestExecutionView{frozen: in}\n")
	buf.WriteString("}\n\n")

	for _, p := range planes {
		if !p.requestBorrow {
			continue
		}
		pascalName := strings.TrimPrefix(p.varName, "Plane")
		fmt.Fprintf(buf, "// %s returns the request-materialized %s without cloning.\n", pascalName, pascalName)
		buf.WriteString("// The returned slice is immutable borrowed storage and MUST NOT be mutated.\n")
		fmt.Fprintf(buf, "func (v RequestExecutionView) %s() %s {\n", pascalName, p.typeExpr)
		buf.WriteString("\tif v.frozen.frozen == nil {\n\t\treturn nil\n\t}\n")
		fmt.Fprintf(buf, "\treturn v.frozen.frozen.%s\n", p.fieldName)
		buf.WriteString("}\n\n")
	}
}

// emitGenerationBinderMethods generates Bind<Plane> and Replace<Plane> convenience methods on ContributionSet.
func emitGenerationBinderMethods(buf *bytes.Buffer, planes []planeInfo) {
	for _, p := range planes {
		if p.genBinderRule == "CombReplaceByIdentity" {
			pascalName := strings.TrimPrefix(p.varName, "Plane")
			fmt.Fprintf(buf, "// Bind%s replaces %s under SourceGenerationBinder semantics.\n", pascalName, pascalName)
			fmt.Fprintf(buf, "func (s *ContributionSet) Bind%s(contributorID string, v %s) error {\n", pascalName, p.typeExpr)
			fmt.Fprintf(buf, "\treturn ContributeSource(s, %s, SourceGenerationBinder, contributorID, v)\n", p.varName)
			buf.WriteString("}\n\n")

			fmt.Fprintf(buf, "// Replace%s replaces %s under SourceGenerationBinder semantics.\n", pascalName, pascalName)
			fmt.Fprintf(buf, "func (s *ContributionSet) Replace%s(contributorID string, v %s) error {\n", pascalName, p.typeExpr)
			fmt.Fprintf(buf, "\treturn ContributeSource(s, %s, SourceGenerationBinder, contributorID, v)\n", p.varName)
			buf.WriteString("}\n\n")
		}
	}
}
