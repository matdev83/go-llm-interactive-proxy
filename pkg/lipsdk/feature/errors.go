package feature

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidPlane indicates an invalid or incomplete plane declaration.
	ErrInvalidPlane = errors.New("feature: invalid plane declaration")

	// ErrInvalidContribution indicates an invalid contribution to a plane.
	ErrInvalidContribution = errors.New("feature: invalid contribution")

	// ErrUnsupportedSource indicates a contribution from a source not supported by the plane.
	ErrUnsupportedSource = errors.New("feature: unsupported contribution source")

	// ErrExclusiveConflict reports an attempted second contribution to an exclusive plane slot.
	ErrExclusiveConflict = errors.New("feature: exclusive slot conflict")

	// ErrNilContribution reports a nil contribution rejected by plane nil policy.
	ErrNilContribution = errors.New("feature: nil contribution")
)

// AttributedError wraps an underlying error with the plugin ID and plane ID
// where the contribution or validation failure occurred.
type AttributedError struct {
	PluginID string
	PlaneID  string
	Err      error
}

func (e *AttributedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.PluginID != "" && e.PlaneID != "" {
		return fmt.Sprintf("feature: plugin %q plane %q: %v", e.PluginID, e.PlaneID, e.Err)
	}
	if e.PlaneID != "" {
		return fmt.Sprintf("feature: plane %q: %v", e.PlaneID, e.Err)
	}
	if e.PluginID != "" {
		return fmt.Sprintf("feature: plugin %q: %v", e.PluginID, e.Err)
	}
	return fmt.Sprintf("feature: %v", e.Err)
}

func (e *AttributedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *AttributedError) Is(target error) bool {
	if target == nil {
		return false
	}
	return errors.Is(e.Err, target)
}
