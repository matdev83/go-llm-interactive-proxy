package billing

import "context"

// AuthorizationLookup reads the durable authorization hold for one account and
// TUR key so stock rating can populate RatingInput.Authorization. It does not
// create, extend, or release a hold, and it is not part of AuthoritativeBilling;
// admission-only fakes must not be required to implement it. A missing hold is
// not found; lookup must not invent a hold from TUR refs alone.
type AuthorizationLookup interface {
	GetAuthorization(ctx context.Context, accountID, turKey string) (Authorization, error)
}
