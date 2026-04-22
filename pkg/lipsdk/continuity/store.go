// Package continuity defines the stable persistence contract for A-leg / B-leg continuity
// and attempt lineage used by documentation and optional external tooling.
//
// Store, ALegRecord, and BLegRecord must stay aligned with the core implementation in
// internal/core/b2bua (same field names, types, and Store method set). Drift is caught by
// TestContinuityContract_* in internal/core/b2bua/store_contract_test.go.
package continuity

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ALegRecord is the logical client session row for routing and lineage.
type ALegRecord struct {
	ALegID                string
	ContinuityKey         string
	CreatedAt             time.Time
	LastSeenAt            time.Time
	WeightedFirstConsumed bool
}

// BLegRecord identifies one backend attempt slot within an A-leg.
type BLegRecord struct {
	BLegID string
	ALegID string
	Seq    int
}

// Store persists continuity and attempt lineage (mirrors internal/core/b2bua.Store).
type Store interface {
	ResolveALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	CreateALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	GetALeg(ctx context.Context, aLegID string) (ALegRecord, error)
	SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error
	NextBLeg(ctx context.Context, aLegID string) (BLegRecord, error)
	RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error
	LoadAttempts(ctx context.Context, aLegID string) ([]lipapi.AttemptRecord, error)
}
