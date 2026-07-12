package authoritystore

import (
	"context"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

var (
	_ app.StateStore = (*MemoryStore)(nil)
	_ app.StateStore = (*DurableStore)(nil)
)

// MemoryStore is a race-safe in-memory authority store.
type MemoryStore struct {
	mu sync.Mutex
	c  *storeCore
}

// NewMemory returns an empty in-memory store seeded with the provided config.
func NewMemory(cfg Config) *MemoryStore {
	return &MemoryStore{c: newStoreCore(cfg)}
}

// Close implements a no-op close so the memory store matches the durable store shape.
func (s *MemoryStore) Close() error { return nil }

// CheckReadiness reports the configured readiness posture.
func (s *MemoryStore) CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error) {
	if err := ctx.Err(); err != nil {
		return domain.AuthorityStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.readiness(), nil
}

// Reserve atomically records a reservation against a matching live limit row.
func (s *MemoryStore) Reserve(ctx context.Context, cmd app.ReserveCommand) (app.ReserveResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReserveResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.reserve(cmd, discardMutationLog{})
}

// Settle reconciles final or partial usage against a matching reservation.
func (s *MemoryStore) Settle(ctx context.Context, cmd app.SettleCommand) (app.SettleResult, error) {
	if err := ctx.Err(); err != nil {
		return app.SettleResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.settle(cmd, discardMutationLog{})
}

// Release releases unused reservation capacity for swallowed or losing attempts.
func (s *MemoryStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.release(cmd, discardMutationLog{})
}

// ApplyUsage applies final usage/cost to matched advisory windows without a
// reservation (requirement 7.7).
func (s *MemoryStore) ApplyUsage(ctx context.Context, cmd app.ApplyUsageCommand) (app.ApplyUsageResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ApplyUsageResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.applyUsage(cmd, discardMutationLog{})
}

// ActiveLimit returns one configured current limit row without scanning
// historical windows or mutating store state.
func (s *MemoryStore) ActiveLimit(ctx context.Context, q app.ActiveLimitQuery) (controlplane.AccountingLimitStatusRow, bool, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.AccountingLimitStatusRow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, key, ok := s.c.configuredLimitRow(q.RuleID, q.Dimensions, q.At)
	if !ok {
		return controlplane.AccountingLimitStatusRow{}, false, nil
	}
	if row := s.c.limits[key]; row != nil {
		return *row, true, nil
	}
	return candidate, true, nil
}

// LimitStatus returns bounded live limit rows.
func (s *MemoryStore) LimitStatus(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.limitStatus(q)
}

// DecisionHistory returns bounded decision rows.
func (s *MemoryStore) DecisionHistory(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.decisionHistory(q)
}
