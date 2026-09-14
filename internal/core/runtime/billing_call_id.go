package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/submission"
)

func stampBillingCallID(prep *preparedRequest) error {
	if prep == nil {
		return fmt.Errorf("%w: prepared request is required", billing.ErrBillingCallIDInvalid)
	}
	if prep.billingCallID != "" {
		if prep.submission.Trusted() &&
			(prep.submission.Authority.Kind == submission.KindToolContinuation || prep.submission.Authority.Kind == submission.KindHistoricalReplay || prep.submission.Authority.Kind == submission.KindTransportRetry) &&
			strings.TrimSpace(prep.submission.Authority.BillingCallID) != "" &&
			strings.TrimSpace(prep.submission.Authority.BillingCallID) != prep.billingCallID.String() {
			return fmt.Errorf("%w: continuation cannot change billing call identity", submission.ErrScopeMismatch)
		}
		if prep.submission.Trusted() &&
			(prep.submission.Authority.Kind == submission.KindNewSubmission || prep.submission.Authority.Kind == submission.KindFollowUp) &&
			strings.TrimSpace(prep.submission.Authority.BillingCallID) != prep.billingCallID.String() {
			return fmt.Errorf("%w: new submission cannot reuse billing call identity", submission.ErrLifecycleConflict)
		}
		if err := prep.billingCallID.Validate(); err != nil {
			return err
		}
		if prep.billingCallState == nil {
			prep.billingCallState = newBillingCallState(prep.billingCallID)
		}
		if prep.submission.Billable() {
			if err := prep.billingCallState.ensureSubmissionID(prep.submission.Authority.SubmissionID); err != nil {
				return err
			}
		}
		return nil
	}
	if prep.submission.Trusted() && strings.TrimSpace(prep.submission.Authority.BillingCallID) != "" {
		id := billing.BillingCallID(strings.TrimSpace(prep.submission.Authority.BillingCallID))
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%w: trusted continuation billing call id: %v", billing.ErrBillingCallIDInvalid, err)
		}
		prep.billingCallID = id
		prep.billingCallState = newBillingCallState(id)
		if prep.submission.Billable() {
			if err := prep.billingCallState.ensureSubmissionID(prep.submission.Authority.SubmissionID); err != nil {
				return err
			}
		}
		return nil
	}
	id, err := billing.NewBillingCallID()
	if err == nil {
		prep.billingCallID = id
		prep.billingCallState = newBillingCallState(id)
		if prep.submission.Billable() {
			if err := prep.billingCallState.ensureSubmissionID(prep.submission.Authority.SubmissionID); err != nil {
				return err
			}
		}
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
