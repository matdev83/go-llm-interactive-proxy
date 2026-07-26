package backendplugin

import "strings"

// Validate reports whether the invocation is well-formed and within bounds.
func (inv Invocation) Validate() error {
	if strings.TrimSpace(inv.RequestID) == "" ||
		strings.TrimSpace(inv.AttemptID) == "" ||
		strings.TrimSpace(inv.ALegID) == "" ||
		strings.TrimSpace(inv.BLegID) == "" ||
		strings.TrimSpace(inv.CanonicalModelID) == "" {
		return ErrInvalidInvocation
	}
	if len(inv.Messages) == 0 {
		return ErrInvalidInvocation
	}
	max := DefaultMaxRawJSONBytes
	if err := inv.Options.ResponseSchemaJSON.Validate(max); err != nil {
		return err
	}
	for _, t := range inv.Tools {
		if err := t.ParametersJSON.Validate(max); err != nil {
			return err
		}
	}
	for _, m := range append(inv.Instructions, inv.Messages...) {
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
	return nil
}
