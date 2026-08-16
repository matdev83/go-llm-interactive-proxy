package billing

import "context"

type ExposureRecovery interface {
	RepairExposureNoCharge(ctx context.Context, callID BillingCallID, sourceKey string) (CallSettlement, error)
	RepairIncompleteCallNoCharge(ctx context.Context, callID BillingCallID, sourceKey string) (CallSettlement, error)
}
