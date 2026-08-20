package runtime

import (
	"fmt"

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
