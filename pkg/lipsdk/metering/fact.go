package metering

import (
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// MoneyObservation is an optional monetary observation attached to a fact.
// It is intentionally independent of pkg/lipsdk/economics.Money so metering
// does not import economics (import DAG: authority → economics → metering).
type MoneyObservation struct {
	NanoUnits int64  `json:"nano_units"`
	Currency  string `json:"currency,omitempty"`
	Present   bool   `json:"present"`
	Source    Source `json:"source,omitempty"`
}

// Fact is one idempotent metering journal record (requirements 3.1–3.5, 13.2).
//
// Idempotency: the same FactID within a StreamID must be treated as the same
// fact. Replaying Append with SameFactIdentity is a no-op at the store; a
// different Sequence or Kind for the same FactID is a contract violation for
// store implementations to reject.
type Fact struct {
	FactID         string                   `json:"fact_id"`
	StreamID       string                   `json:"stream_id"`
	Sequence       int64                    `json:"sequence"`
	Kind           FactKind                 `json:"kind"`
	Perspective    EconomicPerspective      `json:"perspective"`
	Boundary       Boundary                 `json:"boundary"`
	Lifecycle      LifecycleScope           `json:"lifecycle"`
	Correlation    Correlation              `json:"correlation"`
	Scope          scope.PrincipalScopeView `json:"scope"`
	FrontendID     string                   `json:"frontend_id,omitempty"`
	BackendID      string                   `json:"backend_id,omitempty"`
	Model          string                   `json:"model,omitempty"`
	AttemptOutcome AttemptOutcome           `json:"attempt_outcome,omitempty"`
	Surfaced       SurfacedState            `json:"surfaced,omitempty"`
	Quantities     []Quantity               `json:"quantities,omitempty"`
	Money          *MoneyObservation        `json:"money,omitempty"`
	Source         Source                   `json:"source"`
	Authority      Authority                `json:"authority"`
	Presence       Presence                 `json:"presence"`
	Supersedes     []string                 `json:"supersedes,omitempty"`
	PolicyVersion  VersionRef               `json:"policy_version,omitempty"`
	RecordedAt     time.Time                `json:"recorded_at"`
}

// IdempotencyKey returns the stable journal key for this fact identity
// (requirement 3.1): StreamID + FactID. It intentionally excludes quantities,
// money, and other payload fields so replays with identical identity collide.
// Sequence is checked separately via SameFactIdentity for ordered stream membership.
func (f Fact) IdempotencyKey() string {
	return strings.TrimSpace(f.StreamID) + "\x00" + strings.TrimSpace(f.FactID)
}

// SameFactIdentity reports whether a and b share FactID, StreamID, and Sequence.
// Stores treat matching identity as an idempotent replay of the same fact.
func SameFactIdentity(a, b Fact) bool {
	return strings.TrimSpace(a.FactID) == strings.TrimSpace(b.FactID) &&
		strings.TrimSpace(a.StreamID) == strings.TrimSpace(b.StreamID) &&
		a.Sequence == b.Sequence
}

// Validate checks required identity and enum fields, quantity rows, and
// supersession rules for correction/replacement kinds (requirements 3.2, 3.3).
func (f Fact) Validate() error {
	if strings.TrimSpace(f.FactID) == "" {
		return fmt.Errorf("metering: fact_id required")
	}
	if strings.TrimSpace(f.StreamID) == "" {
		return fmt.Errorf("metering: stream_id required")
	}
	if f.Sequence < 0 {
		return fmt.Errorf("metering: sequence must be non-negative")
	}
	if err := f.Kind.Validate(); err != nil {
		return err
	}
	if err := f.Perspective.Validate(); err != nil {
		return err
	}
	if err := f.Boundary.Validate(); err != nil {
		return err
	}
	if err := f.Lifecycle.Validate(); err != nil {
		return err
	}
	if err := f.Source.Validate(); err != nil {
		return err
	}
	if err := f.Authority.Validate(); err != nil {
		return err
	}
	if err := f.Presence.Validate(); err != nil {
		return err
	}
	if f.Surfaced != "" {
		if err := f.Surfaced.Validate(); err != nil {
			return err
		}
	}
	if f.AttemptOutcome != "" {
		if err := f.AttemptOutcome.Validate(); err != nil {
			return err
		}
	}
	for i, q := range f.Quantities {
		if err := q.Validate(); err != nil {
			return fmt.Errorf("metering: quantities[%d]: %w", i, err)
		}
	}
	if f.Kind.RequiresSupersedes() {
		if len(f.Supersedes) == 0 {
			return fmt.Errorf("metering: kind %q requires non-empty supersedes", f.Kind)
		}
		for i, id := range f.Supersedes {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("metering: supersedes[%d] empty", i)
			}
		}
	} else if len(f.Supersedes) > 0 {
		return fmt.Errorf("metering: kind %q must not set supersedes", f.Kind)
	}
	return nil
}
