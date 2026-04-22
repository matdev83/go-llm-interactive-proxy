package continuity

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipsdkc "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuity"
)

// SDKStore wraps a b2bua.Store as a pkg/lipsdk/continuity.Store for stable API boundaries.
func SDKStore(inner b2bua.Store) lipsdkc.Store {
	if inner == nil {
		return nil
	}
	return sdkStore{inner: inner}
}

type sdkStore struct {
	inner b2bua.Store
}

func toALeg(r b2bua.ALegRecord) lipsdkc.ALegRecord {
	return lipsdkc.ALegRecord{
		ALegID:                r.ALegID,
		ContinuityKey:         r.ContinuityKey,
		CreatedAt:             r.CreatedAt,
		LastSeenAt:            r.LastSeenAt,
		WeightedFirstConsumed: r.WeightedFirstConsumed,
	}
}

func toBLeg(r b2bua.BLegRecord) lipsdkc.BLegRecord {
	return lipsdkc.BLegRecord{BLegID: r.BLegID, ALegID: r.ALegID, Seq: r.Seq}
}

func (s sdkStore) ResolveALeg(ctx context.Context, continuityKey string) (lipsdkc.ALegRecord, error) {
	r, err := s.inner.ResolveALeg(ctx, continuityKey)
	if err != nil {
		return lipsdkc.ALegRecord{}, err
	}
	return toALeg(r), nil
}

func (s sdkStore) CreateALeg(ctx context.Context, continuityKey string) (lipsdkc.ALegRecord, error) {
	r, err := s.inner.CreateALeg(ctx, continuityKey)
	if err != nil {
		return lipsdkc.ALegRecord{}, err
	}
	return toALeg(r), nil
}

func (s sdkStore) FetchALeg(ctx context.Context, aLegID string) (lipsdkc.ALegRecord, error) {
	r, err := s.inner.FetchALeg(ctx, aLegID)
	if err != nil {
		return lipsdkc.ALegRecord{}, err
	}
	return toALeg(r), nil
}

func (s sdkStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	return s.inner.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func (s sdkStore) NextBLeg(ctx context.Context, aLegID string) (lipsdkc.BLegRecord, error) {
	r, err := s.inner.NextBLeg(ctx, aLegID)
	if err != nil {
		return lipsdkc.BLegRecord{}, err
	}
	return toBLeg(r), nil
}

func (s sdkStore) RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error {
	return s.inner.RecordAttempt(ctx, rec)
}

func (s sdkStore) LoadAttempts(ctx context.Context, aLegID string) ([]lipapi.AttemptRecord, error) {
	return s.inner.LoadAttempts(ctx, aLegID)
}

var _ lipsdkc.Store = sdkStore{}
