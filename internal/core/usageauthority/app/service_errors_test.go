package app

import (
	"errors"
	"testing"
)

func TestAuthorityErrorsMatchSentinels(t *testing.T) {
	t.Parallel()

	err := WrapError(ErrReservationConflict, "reserve", errors.New("duplicate reservation"))
	if !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("wrapped error must match reservation conflict sentinel: %v", err)
	}

	err = WrapError(ErrDuplicateSettlement, "settle", errors.New("duplicate settlement"))
	if !errors.Is(err, ErrDuplicateSettlement) {
		t.Fatalf("wrapped error must match duplicate settlement sentinel: %v", err)
	}
}
