package lipsdk

import (
	"errors"
	"fmt"
)

var ErrDuplicateRegistration = errors.New("duplicate plugin registration")

// Requirement defines a mandatory plugin that must exist in a bundle.
type Requirement struct {
	Kind PluginKind
	ID   string
}

// DuplicateRegistrationError reports a conflicting plugin identity.
type DuplicateRegistrationError struct {
	Kind PluginKind
	ID   string
}

func (e *DuplicateRegistrationError) Error() string {
	return fmt.Sprintf("%v: %s/%s", ErrDuplicateRegistration, e.Kind, e.ID)
}

func (e *DuplicateRegistrationError) Unwrap() error {
	return ErrDuplicateRegistration
}

// MissingRequirementError reports a missing mandatory plugin.
type MissingRequirementError struct {
	Kind PluginKind
	ID   string
}

func (e *MissingRequirementError) Error() string {
	return fmt.Sprintf("missing mandatory plugin: %s/%s", e.Kind, e.ID)
}

// ValidateRegistrations checks duplicate IDs inside a kind and verifies mandatory entries.
func ValidateRegistrations(registrations []Registration, required []Requirement) error {
	seen := make(map[PluginKind]map[string]struct{}, len(required))

	for _, registration := range registrations {
		if registration.ID == "" {
			return fmt.Errorf("plugin registration id is required")
		}
		if registration.Kind == "" {
			return fmt.Errorf("plugin registration kind is required for %q", registration.ID)
		}

		ids := seen[registration.Kind]
		if ids == nil {
			ids = map[string]struct{}{}
			seen[registration.Kind] = ids
		}

		if _, exists := ids[registration.ID]; exists {
			return &DuplicateRegistrationError{Kind: registration.Kind, ID: registration.ID}
		}

		ids[registration.ID] = struct{}{}
	}

	for _, requirement := range required {
		ids := seen[requirement.Kind]
		if _, ok := ids[requirement.ID]; !ok {
			return &MissingRequirementError{Kind: requirement.Kind, ID: requirement.ID}
		}
	}

	return nil
}
