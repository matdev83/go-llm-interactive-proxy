package app

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
)

// Error is the stable application error wrapper used for authority
// orchestration failures.
type Error struct {
	kind string
	op   string
	err  error
}

func (e *Error) Error() string {
	switch {
	case e == nil:
		return "<nil>"
	case e.err == nil && e.op == "":
		return "usage authority " + e.kind
	case e.err == nil:
		return fmt.Sprintf("usage authority %s: %s", e.op, e.kind)
	default:
		return fmt.Sprintf("usage authority %s: %s: %v", e.op, e.kind, e.err)
	}
}

func (e *Error) Unwrap() error { return e.err }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || e == nil || t == nil {
		return false
	}
	return e.kind != "" && e.kind == t.kind
}

var (
	ErrDisabled            = &Error{kind: "disabled"}
	ErrDegraded            = &Error{kind: "degraded"}
	ErrUnavailable         = &Error{kind: "unavailable"}
	ErrCapacityExceeded    = &Error{kind: "capacity_exceeded"}
	ErrReservationConflict = &Error{kind: "reservation_conflict"}
	ErrDuplicateSettlement = &Error{kind: "duplicate_settlement"}
	ErrInvalidQuery        = &Error{kind: "invalid_query"}
	ErrUnsupportedFilter   = &Error{kind: "unsupported_filter"}
	ErrEvaluationTimeout   = &Error{kind: "evaluation_timeout"}
	// ErrRequiredEvidence identifies a recorder failure that must prevent
	// protected pre-work from starting. Best-effort evidence failures use the
	// ordinary unavailable/error paths instead.
	ErrRequiredEvidence = &Error{kind: "required_evidence"}
)

// ReservationCapacityError reports a successful atomic determination that a
// strict window lacks capacity. It remains a reservation conflict for older
// callers, but admission must never treat it as unavailable infrastructure.
type ReservationCapacityError struct {
	Requested domain.Amount
	Remaining domain.Amount
}

func (e *ReservationCapacityError) Error() string {
	if e == nil {
		return "usage authority capacity exceeded"
	}
	return fmt.Sprintf("usage authority capacity exceeded: requested %s, remaining %s", e.Requested.String(), e.Remaining.String())
}

func (e *ReservationCapacityError) Is(target error) bool {
	return target == ErrCapacityExceeded || target == ErrReservationConflict
}

// WrapError annotates an authority error with operation context while
// preserving sentinel matching through errors.Is.
func WrapError(kind *Error, op string, err error) error {
	if err == nil {
		return nil
	}
	if kind == nil {
		return err
	}
	return &Error{kind: kind.kind, op: op, err: err}
}

// RuleReservationError identifies the rule whose reservation descriptor
// failed while preserving the underlying error's sentinel identity.
type RuleReservationError struct {
	RuleID string
	Err    error
}

func (e *RuleReservationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("usage authority reservation rule %q: %v", e.RuleID, e.Err)
}

func (e *RuleReservationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ReservationFailureRuleID returns the originating rule for an attributed
// reservation failure. Store-wide failures intentionally return no rule ID.
func ReservationFailureRuleID(err error) string {
	var ruleErr *RuleReservationError
	if errors.As(err, &ruleErr) && ruleErr != nil {
		return ruleErr.RuleID
	}
	return ""
}

func isKind(err error, kind *Error) bool {
	return errors.Is(err, kind)
}
