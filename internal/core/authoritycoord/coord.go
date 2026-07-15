package authoritycoord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

const defaultCleanupTimeout = 2 * time.Second

// PriorityClass is the deterministic evaluation order for request providers
// (requirement 8.1 / design priority classes — not registration order).
type PriorityClass int

const (
	PriorityConcurrency PriorityClass = iota
	PriorityCreditWallet
	PriorityQuotaBudgetRate
	PriorityAdvisory
)

// Compensator releases a prior successful admit hold.
type Compensator func(ctx context.Context) error

// StackEntry records one successful reservation for reverse compensation.
type StackEntry struct {
	ProviderID  string
	Handle      string
	Reservation authority.Reservation
	Compensate  Compensator
	Evidence    authority.SafeEvidence
}

// CompensationStack holds successful admits in acquisition order.
type CompensationStack struct {
	entries []StackEntry
}

// Push appends a successful hold.
func (s *CompensationStack) Push(e StackEntry) {
	if s == nil {
		return
	}
	s.entries = append(s.entries, e)
}

// Entries returns a copy of stack entries in acquisition order.
func (s *CompensationStack) Entries() []StackEntry {
	if s == nil || len(s.entries) == 0 {
		return nil
	}
	out := make([]StackEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Handles returns reservation handles in acquisition order.
func (s *CompensationStack) Handles() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		if strings.TrimSpace(e.Handle) != "" {
			out = append(out, e.Handle)
		}
	}
	return out
}

// CompensateFailed is recorded when reverse compensation fails (do not pretend released).
type CompensateFailed struct {
	ProviderID string
	Handle     string
	Err        error
	Evidence   authority.SafeEvidence
}

// ReverseCompensate releases entries in reverse order using a fresh bounded context
// derived from parent without inheriting cancellation (requirements 15.2, 15.3).
func (s *CompensationStack) ReverseCompensate(parent context.Context, timeout time.Duration) []CompensateFailed {
	if s == nil || len(s.entries) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	var failed []CompensateFailed
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if e.Compensate == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		err := invokeCompensate(ctx, e.Compensate)
		cancel()
		if err != nil {
			failed = append(failed, CompensateFailed{
				ProviderID: e.ProviderID,
				Handle:     e.Handle,
				Err:        err,
				Evidence:   e.Evidence,
			})
		}
	}
	return failed
}

// CompositeDecision is the aggregated admit result across providers.
type CompositeDecision struct {
	Kind               authority.DecisionKind
	Clamps             []authority.Clamp
	Stack              CompensationStack
	ProviderDecisions  []authority.Decision
	Readiness          authority.Readiness
	CompensateFailures []CompensateFailed
	Evidence           authority.SafeEvidence
	DeniedBy           string
	// Lease captures concurrency admit metadata for heartbeat/release (Phase 8).
	Lease authority.LeaseDecision
	// BoundVersions aggregates policy snapshot refs from providers and lease admit (11.2).
	BoundVersions []economics.PolicySnapshotRef
}

// ErrDenied is returned when a required provider denies admission.
type ErrDenied struct {
	ProviderID string
	Decision   authority.Decision
}

func (e *ErrDenied) Error() string {
	if e == nil {
		return "authoritycoord: denied"
	}
	return fmt.Sprintf("authoritycoord: denied by %q", e.ProviderID)
}

// ErrUnavailable is returned when required infrastructure fails closed.
type ErrUnavailable struct {
	ProviderID string
	Err        error
}

func (e *ErrUnavailable) Error() string {
	if e == nil {
		return "authoritycoord: unavailable"
	}
	if e.Err != nil {
		return fmt.Sprintf("authoritycoord: provider %q unavailable: %v", e.ProviderID, e.Err)
	}
	return fmt.Sprintf("authoritycoord: provider %q unavailable", e.ProviderID)
}

func (e *ErrUnavailable) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
