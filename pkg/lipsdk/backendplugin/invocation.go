package backendplugin

import (
	"fmt"
	"slices"
	"strings"
)

// Validate reports whether the invocation is well-formed and within bounds.
func (inv Invocation) Validate() error {
	if strings.TrimSpace(inv.RequestID) == "" ||
		strings.TrimSpace(inv.AttemptID) == "" ||
		strings.TrimSpace(inv.ALegID) == "" ||
		strings.TrimSpace(inv.BLegID) == "" ||
		strings.TrimSpace(inv.CanonicalModelID) == "" {
		return ErrInvalidInvocation
	}
	if len(inv.Messages) == 0 && !(inv.ItemAuthority && len(inv.Items) > 0) {
		return ErrInvalidInvocation
	}
	max := DefaultMaxRawJSONBytes
	if err := inv.Options.ResponseSchemaJSON.Validate(max); err != nil {
		return err
	}
	for i, t := range inv.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("%w: Tools[%d].Name is required", ErrInvalidInvocation, i)
		}
		if t.Name != strings.TrimSpace(t.Name) {
			return fmt.Errorf("%w: Tools[%d].Name must not contain leading or trailing whitespace", ErrInvalidInvocation, i)
		}
		if err := t.ParametersJSON.Validate(max); err != nil {
			return err
		}
	}
	for _, m := range slices.Concat(inv.Instructions, inv.Messages) {
		if m.Role == RoleUnspecified {
			return ErrUnknownEnum
		}
		for _, p := range m.Parts {
			if p.Kind == PartKindUnspecified {
				return ErrUnknownEnum
			}
			if err := p.ToolArgsJSON.Validate(max); err != nil {
				return err
			}
		}
	}
	return validateInvocationWire(inv)
}
