package authority

import "fmt"

// Strength classifies whether an authority provider is required or advisory
// for a lifecycle stage (requirement 15.1).
type Strength string

const (
	StrengthRequired Strength = "required"
	StrengthAdvisory Strength = "advisory"
)

// IsKnown reports whether s is a documented strength.
func (s Strength) IsKnown() bool {
	switch s {
	case StrengthRequired, StrengthAdvisory:
		return true
	}
	return false
}

// Validate returns an error when s is not a known strength.
func (s Strength) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("authority: unknown strength %q", s)
	}
	return nil
}

// FailureBehavior classifies fail-open vs fail-closed infrastructure posture
// for a provider at a lifecycle stage (requirement 15.1). Distinct from
// deterministic capacity denials (requirement 15.4).
type FailureBehavior string

const (
	FailureFailClosed FailureBehavior = "fail_closed"
	FailureFailOpen   FailureBehavior = "fail_open"
)

// IsKnown reports whether b is a documented failure behavior.
func (b FailureBehavior) IsKnown() bool {
	switch b {
	case FailureFailClosed, FailureFailOpen:
		return true
	}
	return false
}

// Validate returns an error when b is not a known failure behavior.
func (b FailureBehavior) Validate() error {
	if !b.IsKnown() {
		return fmt.Errorf("authority: unknown failure behavior %q", b)
	}
	return nil
}

// StagePosture is one provider's declared posture for one lifecycle stage.
type StagePosture struct {
	Stage           Stage           `json:"stage"`
	Strength        Strength        `json:"strength"`
	FailureBehavior FailureBehavior `json:"failure_behavior"`
}

// Validate checks stage, strength, and failure behavior are known.
func (p StagePosture) Validate() error {
	if !p.Stage.IsKnown() {
		return fmt.Errorf("authority: unknown stage %q", p.Stage)
	}
	if err := p.Strength.Validate(); err != nil {
		return err
	}
	return p.FailureBehavior.Validate()
}

// ProviderDescriptor is the public registration/describe surface for request,
// attempt, or concurrency authority providers (requirements 12.1, 15.1).
// Coordinators consume descriptors; they are not store or executor types.
type ProviderDescriptor struct {
	ID      string         `json:"id"`
	Postures []StagePosture `json:"postures"`
}

// Validate requires a non-empty ID and validates each posture row.
func (d ProviderDescriptor) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("authority: provider id required")
	}
	if len(d.Postures) == 0 {
		return fmt.Errorf("authority: at least one stage posture required")
	}
	for i, p := range d.Postures {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("authority: postures[%d]: %w", i, err)
		}
	}
	return nil
}

// Describer exposes provider posture independently of Admit/Settle/Release so
// the design provider method sets stay focused while still satisfying 15.1.
type Describer interface {
	Describe() ProviderDescriptor
}
