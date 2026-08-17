package billingstore

import (
	"errors"
	"strings"

	"github.com/uptrace/bun/driver/pgdriver"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return true
		}
	}
	var pgErr pgdriver.Error
	if errors.As(err, &pgErr) {
		return pgErr.Field('C') == "23505"
	}
	return false
}

// isLegAttemptSeqConflict reports a unique violation raised by the
// (call_id, attempt_seq) uniqueness contract rather than the key or
// (call_id, b_leg_id) replay identities.
func isLegAttemptSeqConflict(err error) bool {
	if !isUniqueViolation(err) {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, usageLegCallAttemptSeqIndex) || strings.Contains(lower, "attempt_seq")
}
