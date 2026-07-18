package journalstore

import (
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ErrIdentityCollision is returned when Append sees the same SourceEventKey
// (or legacy IdempotencyKey row) with a different Sequence, Kind, or
// double-count-sensitive payload than the stored fact (requirements 3.1, 3.4, 13.4).
var ErrIdentityCollision = errors.New("metering/journalstore: fact identity collision")

// ErrUniqueRaceMissingRow is returned when a unique-constraint race on Append
// is followed by a lookup that finds no winning row. Callers should treat this
// as a transient inconsistency and retry.
var ErrUniqueRaceMissingRow = errors.New("metering/journalstore: unique race missing winner row")

// ErrSupersessionTarget is returned when a correction/replacement supersedes a
// missing fact or a fact outside the same stream (requirements 6.6, D7).
var ErrSupersessionTarget = errors.New("metering/journalstore: supersession target invalid")

// ErrSupersessionCycle is returned when supersession edges would form a cycle
// (requirements 6.7, D7).
var ErrSupersessionCycle = errors.New("metering/journalstore: supersession cycle")

// ErrQueryTooBroad is returned when List lacks a required selective bound so the
// store cannot safely page without scanning (requirement 14.4/14.8).
var ErrQueryTooBroad = metering.ErrQueryTooBroad
