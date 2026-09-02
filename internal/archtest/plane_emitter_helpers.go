package archtest

import (
	"bytes"
	"fmt"
	"strings"
)

// emitMapReplayFunctions generates map-backed candidate and replay helper functions.
func emitMapReplayFunctions(buf *bytes.Buffer, planes []planeInfo) {
	// contributeCandidateMapTo function
	buf.WriteString("// contributeCandidateMapTo contributes map-backed candidate values into dst.\n")
	buf.WriteString("func contributeCandidateMapTo(values map[string]any, dst *ContributionSet, source SourceKind, contributorID string) error {\n")
	buf.WriteString("\tif len(values) == 0 || dst == nil {\n\t\treturn nil\n\t}\n")
	for _, p := range planes {
		if !p.candidate {
			continue
		}
		fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok {\n", p.varName)
		buf.WriteString("\t\tif !isNilValue(v) {\n")
		fmt.Fprintf(buf, "\t\t\ttyped, ok := v.(%s)\n", p.typeExpr)
		buf.WriteString("\t\t\tif !ok {\n")
		fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: expected %s, got %%T\", ErrInvalidContribution, v),\n\t\t\t\t}\n", p.varName, p.typeExpr)
		buf.WriteString("\t\t\t}\n")
		fmt.Fprintf(buf, "\t\t\tif err := ContributeSource(dst, %s, source, contributorID, typed); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n", p.varName)
		buf.WriteString("\t\t}\n")
		buf.WriteString("\t}\n")
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")

	// validateAllPlanesMap function
	buf.WriteString("// validateAllPlanesMap validates map-backed plane values and identities without replaying.\n")
	buf.WriteString("func validateAllPlanesMap(values map[string]any, identities map[string]string) error {\n")
	buf.WriteString("\tif len(values) == 0 && len(identities) == 0 {\n\t\treturn nil\n\t}\n")
	for _, p := range planes {
		if strings.HasPrefix(p.typeExpr, "[]") {
			if p.hasIdentity {
				fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok {\n", p.varName)
				buf.WriteString("\t\tif !isNilValue(v) {\n")
				fmt.Fprintf(buf, "\t\t\ttyped, ok := v.(%s)\n", p.typeExpr)
				buf.WriteString("\t\t\tif !ok {\n")
				fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, fmt.Errorf(\"expected %s, got %%T\", v))\n", p.varName, p.typeExpr)
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t\t_ = typed\n")
				fmt.Fprintf(buf, "\t\t\tif %s.Validate != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, err)\n", p.varName)
				buf.WriteString("\t\t\t\t}\n")
				buf.WriteString("\t\t\t}\n")
				fmt.Fprintf(buf, "\t\t\tid, hasID := identities[%s.ID]\n", p.varName)
				buf.WriteString("\t\t\tif !hasID {\n")
				buf.WriteString("\t\t\t\tif id != \"\" || len(typed) > 0 {\n")
				fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"missing cached identity\"))\n", p.varName)
				buf.WriteString("\t\t\t\t}\n")
				buf.WriteString("\t\t\t} else {\n")
				buf.WriteString("\t\t\t\tif id == \"\" {\n")
				fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"missing cached identity\"))\n", p.varName)
				buf.WriteString("\t\t\t\t}\n")
				if p.hasValidateIdentity {
					fmt.Fprintf(buf, "\t\t\tif err := %s.ValidateIdentity(id); err != nil {\n", p.varName)
					fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, err)\n", p.varName)
					buf.WriteString("\t\t\t\t}\n")
				}
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t} else {\n")
				fmt.Fprintf(buf, "\t\t\tif id, hasID := identities[%s.ID]; hasID || id != \"\" {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"malformed metadata without value\"))\n", p.varName)
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t} else {\n")
				fmt.Fprintf(buf, "\t\tif id, hasID := identities[%s.ID]; hasID || id != \"\" {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"malformed metadata without value\"))\n", p.varName)
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t}\n")
			} else {
				fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok {\n", p.varName)
				buf.WriteString("\t\tif !isNilValue(v) {\n")
				fmt.Fprintf(buf, "\t\t\ttyped, ok := v.(%s)\n", p.typeExpr)
				buf.WriteString("\t\t\tif !ok {\n")
				fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, fmt.Errorf(\"expected %s, got %%T\", v))\n", p.varName, p.typeExpr)
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t\t_ = typed\n")
				fmt.Fprintf(buf, "\t\t\tif %s.Validate != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, err)\n", p.varName)
				buf.WriteString("\t\t\t\t}\n")
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t}\n")
			}
		} else if p.typeExpr == "int" {
			fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok {\n", p.varName)
			buf.WriteString("\t\tif !isNilValue(v) {\n")
			buf.WriteString("\t\t\ttyped, ok := v.(int)\n")
			buf.WriteString("\t\t\tif !ok {\n")
			fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, fmt.Errorf(\"expected int, got %%T\", v))\n", p.varName)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t\t_ = typed\n")
			buf.WriteString("\t\t\tif typed < 0 {\n")
			fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, fmt.Errorf(\"must be >= 0, got %%d\", typed))\n", p.varName)
			buf.WriteString("\t\t\t}\n")
			fmt.Fprintf(buf, "\t\t\tif typed > 0 && %s.Validate != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, err)\n", p.varName)
			buf.WriteString("\t\t\t\t}\n")
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t}\n")
		} else if p.isExclusive {
			fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok {\n", p.varName)
			buf.WriteString("\t\tif !isNilValue(v) {\n")
			fmt.Fprintf(buf, "\t\t\ttyped, ok := v.(%s)\n", p.typeExpr)
			buf.WriteString("\t\t\tif !ok {\n")
			fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, fmt.Errorf(\"expected %s, got %%T\", v))\n", p.varName, p.typeExpr)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t\t_ = typed\n")
			fmt.Fprintf(buf, "\t\tid, hasID := identities[%s.ID]\n", p.varName)
			buf.WriteString("\t\tif !hasID || id == \"\" {\n")
			fmt.Fprintf(buf, "\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"missing cached identity\"))\n", p.varName)
			buf.WriteString("\t\t}\n")
			if p.hasValidateIdentity {
				fmt.Fprintf(buf, "\t\t\tif err := %s.ValidateIdentity(id); err != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, err)\n", p.varName)
				buf.WriteString("\t\t\t}\n")
			}
			buf.WriteString("\t\t} else {\n")
			fmt.Fprintf(buf, "\t\t\tif id, hasID := identities[%s.ID]; hasID || id != \"\" {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"malformed metadata without value\"))\n", p.varName)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t} else {\n")
			fmt.Fprintf(buf, "\t\tif id, hasID := identities[%s.ID]; hasID || id != \"\" {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\treturn newPlaneValidationError(%s.ID, errors.New(\"malformed metadata without value\"))\n", p.varName)
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t}\n")
		} else {
			fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok {\n", p.varName)
			buf.WriteString("\t\tif !isNilValue(v) {\n")
			fmt.Fprintf(buf, "\t\t\ttyped, ok := v.(%s)\n", p.typeExpr)
			buf.WriteString("\t\t\tif !ok {\n")
			fmt.Fprintf(buf, "\t\t\t\treturn newPlaneValidationError(%s.ID, fmt.Errorf(\"expected %s, got %%T\", v))\n", p.varName, p.typeExpr)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t\t_ = typed\n")
			fmt.Fprintf(buf, "\t\t\tif %s.Validate != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\t\treturn newPlaneValidationError(%s.ID, err)\n", p.varName)
			buf.WriteString("\t\t\t\t}\n")
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t}\n")
		}
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")

	// mapHasIdentityReplayRule function
	buf.WriteString("// mapHasIdentityReplayRule reports whether map-backed values contains any present identity-bearing plane\n")
	buf.WriteString("// whose declared rule for source matches the given combination rule.\n")
	buf.WriteString("func mapHasIdentityReplayRule(values map[string]any, source SourceKind, rule Combination) (string, bool) {\n")
	buf.WriteString("\tif len(values) == 0 {\n\t\treturn \"\", false\n\t}\n")
	for _, p := range planes {
		if !p.hasIdentity {
			continue
		}
		fmt.Fprintf(buf, "\tif %s.Rules.RuleFor(source) == rule {\n", p.varName)
		fmt.Fprintf(buf, "\t\tif v, ok := values[%s.ID]; ok && !isNilValue(v) {\n", p.varName)
		if strings.HasPrefix(p.typeExpr, "[]") {
			fmt.Fprintf(buf, "\t\t\tif typed, ok := v.(%s); ok {\n", p.typeExpr)
			fmt.Fprintf(buf, "\t\t\t\tif len(typed) > 0 {\n\t\t\t\t\treturn %s.ID, true\n\t\t\t\t}\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t} else {\n\t\t\t\treturn %s.ID, true\n\t\t\t}\n", p.varName)
		} else {
			fmt.Fprintf(buf, "\t\t\treturn %s.ID, true\n", p.varName)
		}
		buf.WriteString("\t\t}\n")
		buf.WriteString("\t}\n")
	}
	buf.WriteString("\treturn \"\", false\n")
	buf.WriteString("}\n\n")

	// replayAllPlanesMapTo function
	buf.WriteString("// replayAllPlanesMapTo replays map-backed frozen values and identities into dst.\n")
	buf.WriteString("func replayAllPlanesMapTo(values map[string]any, identities map[string]string, dst *ContributionSet, source SourceKind, contributorID string) error {\n")
	buf.WriteString("\tif (len(values) == 0 && len(identities) == 0) || dst == nil {\n\t\treturn nil\n\t}\n")
	for _, p := range planes {
		if strings.HasPrefix(p.typeExpr, "[]") {
			if p.hasIdentity {
				fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok && !isNilValue(v) {\n", p.varName)
				fmt.Fprintf(buf, "\t\ttyped, ok := v.(%s)\n", p.typeExpr)
				buf.WriteString("\t\tif !ok {\n")
				fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: expected %s, got %%T\", ErrInvalidContribution, v),\n\t\t\t}\n", p.varName, p.typeExpr)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tsrcID := identities[%s.ID]\n", p.varName)
				fmt.Fprintf(buf, "\t\tvar current %s\n", p.typeExpr)
				buf.WriteString("\t\thadDestinationValue := false\n")
				buf.WriteString("\t\texistingID := \"\"\n")
				buf.WriteString("\t\tif dst.generated != nil {\n")
				fmt.Fprintf(buf, "\t\t\thadDestinationValue = len(dst.generated.%s) > 0\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\texistingID = dst.generated.%sID\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tcurrent = cloneSlice(dst.generated.%s)\n", p.fieldName)
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t\tincoming := cloneSlice(typed)\n")
				fmt.Fprintf(buf, "\t\tcombined, err := %s.Combine(source, current, incoming)\n", p.varName)
				buf.WriteString("\t\tif err != nil {\n")
				fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t}\n", p.varName)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tif (typed != nil || current != nil) && combined == nil {\n")
				fmt.Fprintf(buf, "\t\t\tcombined = make(%s, 0)\n", p.typeExpr)
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t\tclonedCombined := cloneSlice(combined)\n")
				buf.WriteString("\t\tfinalID := \"\"\n")
				buf.WriteString("\t\tfinalHasID := false\n")
				buf.WriteString("\t\tif len(clonedCombined) == 0 {\n")
				buf.WriteString("\t\t\tfinalID = \"\"\n")
				buf.WriteString("\t\t\tfinalHasID = false\n")
				buf.WriteString("\t\t} else if hadDestinationValue {\n")
				buf.WriteString("\t\t\tfinalID = existingID\n")
				buf.WriteString("\t\t\tfinalHasID = (existingID != \"\")\n")
				buf.WriteString("\t\t} else {\n")
				buf.WriteString("\t\t\tfinalID = srcID\n")
				buf.WriteString("\t\t\tfinalHasID = (srcID != \"\")\n")
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t\tif dst.generated != nil {\n")
				fmt.Fprintf(buf, "\t\t\tdst.generated.%s = clonedCombined\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tdst.generated.%sID = finalID\n", p.fieldName)
				fmt.Fprintf(buf, "\t\t\tdst.generated.%sHasID = finalHasID\n", p.fieldName)
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t\tif dst.pluginIDs != nil {\n")
				fmt.Fprintf(buf, "\t\t\tdst.pluginIDs[%s.ID] = contributorID\n", p.varName)
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t}\n")
			} else {
				fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok && !isNilValue(v) {\n", p.varName)
				fmt.Fprintf(buf, "\t\ttyped, ok := v.(%s)\n", p.typeExpr)
				buf.WriteString("\t\tif !ok {\n")
				fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: expected %s, got %%T\", ErrInvalidContribution, v),\n\t\t\t}\n", p.varName, p.typeExpr)
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tif %s.Validate != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", p.varName)
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t}\n")
				fmt.Fprintf(buf, "\t\tif dst.generated != nil && %s.generated.contribute != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\tif err := %s.generated.contribute(dst.generated, source, contributorID, typed); err != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", p.varName)
				buf.WriteString("\t\t\t}\n")
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t\tif dst.pluginIDs != nil {\n")
				fmt.Fprintf(buf, "\t\t\tdst.pluginIDs[%s.ID] = contributorID\n", p.varName)
				buf.WriteString("\t\t}\n")
				buf.WriteString("\t}\n")
			}
		} else if p.typeExpr == "int" {
			fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok && !isNilValue(v) {\n", p.varName)
			buf.WriteString("\t\ttyped, ok := v.(int)\n")
			buf.WriteString("\t\tif !ok {\n")
			fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: expected int, got %%T\", ErrInvalidContribution, v),\n\t\t\t}\n", p.varName)
			buf.WriteString("\t\t}\n")
			if p.hasValidateIdentity {
				fmt.Fprintf(buf, "\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
				fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", p.varName)
				buf.WriteString("\t\t\t}\n")
			}
			fmt.Fprintf(buf, "\t\tif dst.generated != nil && %s.generated.contribute != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\tif err := %s.generated.contribute(dst.generated, source, contributorID, typed); err != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", p.varName)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t\tif dst.pluginIDs != nil {\n")
			fmt.Fprintf(buf, "\t\t\tdst.pluginIDs[%s.ID] = contributorID\n", p.varName)
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t}\n")
		} else if p.isExclusive {
			fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok && !isNilValue(v) {\n", p.varName)
			fmt.Fprintf(buf, "\t\ttyped, ok := v.(%s)\n", p.typeExpr)
			buf.WriteString("\t\tif !ok {\n")
			fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: expected %s, got %%T\", ErrInvalidContribution, v),\n\t\t\t}\n", p.varName, p.typeExpr)
			buf.WriteString("\t\t}\n")
			fmt.Fprintf(buf, "\t\tsrcID, hasSrcID := identities[%s.ID]\n", p.varName)
			buf.WriteString("\t\tif !hasSrcID || srcID == \"\" {\n")
			fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: frozen exclusive identity is missing\", ErrInvalidContribution),\n\t\t\t}\n", p.varName)
			buf.WriteString("\t\t}\n")
			fmt.Fprintf(buf, "\t\tif dst.generated != nil && dst.generated.%sHasID {\n", p.fieldName)
			fmt.Fprintf(buf, "\t\t\treturn makeExclusiveConflictError(contributorID, %s.ID, %s.ExclusiveConflictError, dst.generated.%sID, srcID)\n", p.varName, p.varName, p.fieldName)
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t\tif dst.generated != nil {\n")
			fmt.Fprintf(buf, "\t\t\tdst.generated.%s = typed\n", p.fieldName)
			fmt.Fprintf(buf, "\t\t\tdst.generated.%sID = srcID\n", p.fieldName)
			fmt.Fprintf(buf, "\t\t\tdst.generated.%sHasID = true\n", p.fieldName)
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t\tif dst.pluginIDs != nil {\n")
			fmt.Fprintf(buf, "\t\t\tdst.pluginIDs[%s.ID] = contributorID\n", p.varName)
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t}\n")
		} else {
			fmt.Fprintf(buf, "\tif v, ok := values[%s.ID]; ok && !isNilValue(v) {\n", p.varName)
			fmt.Fprintf(buf, "\t\ttyped, ok := v.(%s)\n", p.typeExpr)
			buf.WriteString("\t\tif !ok {\n")
			fmt.Fprintf(buf, "\t\t\treturn &AttributedError{\n\t\t\t\tPluginID: contributorID,\n\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\tErr:      fmt.Errorf(\"%%w: expected %s, got %%T\", ErrInvalidContribution, v),\n\t\t\t}\n", p.varName, p.typeExpr)
			buf.WriteString("\t\t}\n")
			fmt.Fprintf(buf, "\t\tif %s.Validate != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\tif err := %s.Validate(typed); err != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", p.varName)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t}\n")
			fmt.Fprintf(buf, "\t\tif dst.generated != nil && %s.generated.contribute != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\tif err := %s.generated.contribute(dst.generated, source, contributorID, typed); err != nil {\n", p.varName)
			fmt.Fprintf(buf, "\t\t\t\treturn &AttributedError{\n\t\t\t\t\tPluginID: contributorID,\n\t\t\t\t\tPlaneID:  %s.ID,\n\t\t\t\t\tErr:      fmt.Errorf(\"%%w: %%w\", ErrInvalidContribution, err),\n\t\t\t\t}\n", p.varName)
			buf.WriteString("\t\t\t}\n")
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t\tif dst.pluginIDs != nil {\n")
			fmt.Fprintf(buf, "\t\t\tdst.pluginIDs[%s.ID] = contributorID\n", p.varName)
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t}\n")
		}
	}
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")
}

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
