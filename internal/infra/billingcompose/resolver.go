package billingcompose

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

var errNilAuthorizationLookup = errors.New("billingcompose: nil authorization lookup")

// JoinRatingResolver is the stock billing.RatingResolver: it loads the durable
// hold, then catalog bodies for the sealed TUR's version refs. Missing hold or
// missing snapshot fails closed; it never invents rates or a synthetic hold amount.
type JoinRatingResolver struct {
	catalog *SnapshotCatalog
	holds   billing.AuthorizationLookup
}

var _ billing.RatingResolver = (*JoinRatingResolver)(nil)

// NewRatingResolver constructs the stock rating join. Catalog and hold lookup
// are both required.
func NewRatingResolver(catalog *SnapshotCatalog, holds billing.AuthorizationLookup) (billing.RatingResolver, error) {
	if catalog == nil {
		return nil, errNilSnapshotCatalog
	}
	if holds == nil {
		return nil, errNilAuthorizationLookup
	}
	return &JoinRatingResolver{catalog: catalog, holds: holds}, nil
}

// ResolveRating loads the durable hold for the TUR, then exact catalog snapshot
// bodies. ModelPricing is passed through as SnapshotsFor returns it (override
// cards share the TUR CustomerPricingRef, not the override document identity).
func (r *JoinRatingResolver) ResolveRating(ctx context.Context, record billing.TurnUsageRecord) (billing.RatingInput, error) {
	auth, err := r.holds.GetAuthorization(ctx, record.AccountID, record.Key)
	if err != nil {
		return billing.RatingInput{}, fmt.Errorf("billingcompose: authorization lookup: %w", err)
	}
	pricing, policy, rates, modelPricing, err := r.catalog.SnapshotsFor(record)
	if err != nil {
		return billing.RatingInput{}, fmt.Errorf("billingcompose: snapshot catalog: %w", err)
	}
	return billing.RatingInput{
		Record:          record,
		Authorization:   auth,
		CustomerPricing: pricing,
		CustomerPolicy:  policy,
		OperatorRates:   rates,
		ModelPricing:    modelPricing,
	}, nil
}
