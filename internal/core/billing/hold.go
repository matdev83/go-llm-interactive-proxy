package billing

import "time"

// DefaultHoldTTL is the authorization hold lifetime when the caller omits ExpiresAt
// and composition does not override HoldTTL.
const DefaultHoldTTL = 15 * time.Minute
