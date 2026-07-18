package reasoningpreservation

import (
	"context"
	"time"
)

type SessionPartition struct {
	opaque string
}

func NewSessionPartition(opaque string) SessionPartition {
	return SessionPartition{opaque: opaque}
}

func (p SessionPartition) String() string {
	return ""
}

type EvictionSummary struct {
	EvictedTurns int
	EvictedBytes int
	ExpiredTurns int
	ExpiredBytes int
}

type TurnStore interface {
	Append(context.Context, SessionPartition, TurnArtifact) (EvictionSummary, error)
	Snapshot(context.Context, SessionPartition) ([]TurnArtifact, error)
	Delete(context.Context, SessionPartition, ...string) error
}

type StoreOptions struct {
	TTL                      time.Duration
	MaxTurnsPerSession       int
	MaxReasoningBytesPerTurn int
	MaxSessionBytes          int
	Now                      func() time.Time
}

func NewMemoryTurnStore(opts StoreOptions) (TurnStore, error) {
	_ = opts
	return nil, ErrNotImplemented
}
