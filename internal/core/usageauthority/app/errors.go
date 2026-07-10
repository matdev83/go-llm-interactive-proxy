package app

import (
	"errors"
	"fmt"
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
	ErrReservationConflict = &Error{kind: "reservation_conflict"}
	ErrDuplicateSettlement = &Error{kind: "duplicate_settlement"}
	ErrInvalidQuery        = &Error{kind: "invalid_query"}
	ErrUnsupportedFilter   = &Error{kind: "unsupported_filter"}
	ErrEvaluationTimeout   = &Error{kind: "evaluation_timeout"}
)

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

func isKind(err error, kind *Error) bool {
	return errors.Is(err, kind)
}
