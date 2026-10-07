package journalstore

import (
	"errors"

	"github.com/uptrace/bun/driver/pgdriver"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isUniqueViolation reports whether err is a unique-constraint violation across
// the supported dialects. Used to turn source_event_key races into replay or
// ErrIdentityCollision instead of a leaked infrastructure error.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if sqliteErr, ok := errors.AsType[*sqlite.Error](err); ok {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return true
		}
	}
	if pgErr, ok := errors.AsType[pgdriver.Error](err); ok {
		return pgErr.Field('C') == "23505"
	}
	return false
}
