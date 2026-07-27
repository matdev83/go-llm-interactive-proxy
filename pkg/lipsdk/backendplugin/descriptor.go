package backendplugin

import "strings"

// Validate reports whether the descriptor is a valid v1 export table.
func (d PluginDescriptor) Validate() error {
	if d.ProtocolMajor != ProtocolMajorV1 {
		return ErrInvalidDescriptor
	}
	if strings.TrimSpace(d.PluginID) == "" || strings.TrimSpace(d.Version) == "" {
		return ErrInvalidDescriptor
	}
	if len(d.Factories) == 0 {
		return ErrInvalidDescriptor
	}
	if _, err := indexFeatures(d.Features); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, f := range d.Factories {
		kind := strings.TrimSpace(f.Kind)
		if kind == "" {
			return ErrInvalidDescriptor
		}
		if _, ok := seen[kind]; ok {
			return ErrInvalidDescriptor
		}
		seen[kind] = struct{}{}
		if err := f.CredentialMode.Validate(); err != nil {
			return err
		}
		if err := f.AccessScope.Validate(); err != nil {
			return err
		}
		if err := f.ProcessSharing.Validate(); err != nil {
			return err
		}
	}
	return nil
}
