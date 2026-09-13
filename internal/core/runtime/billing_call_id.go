package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func stampBillingCallID(prep *preparedRequest) error {
	if prep == nil {
		return fmt.Errorf("%w: prepared request is required", billing.ErrBillingCallIDInvalid)
	}
	if prep.billingCallID != "" {
		return prep.billingCallID.Validate()
	}
	id, err := billing.NewBillingCallID()
	if err == nil {
		prep.billingCallID = id
		prep.billingCallState = newBillingCallState(id)
	}
	return err
}

// stampBillingStoreID freezes the trusted durable-store lineage alongside the
// request BillingCallID. An empty resolver result is intentionally retained as
// an absent fact; terminal V1 compatibility evidence may still be recorded, but
// V2 observations must not be assigned a fabricated store identity.
func (e *Executor) stampBillingStoreID(ctx context.Context, request *preparedRequest) {
	if e == nil || request == nil || request.billingStoreIDStamped {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if e.BillingIdentity.StoreID != nil {
		request.billingStoreID = strings.TrimSpace(e.BillingIdentity.StoreID(ctx))
	}
	request.billingStoreIDStamped = true
}
