package billingstore

import (
	"context"
	"fmt"
)

// ReclaimExpiredHolds previously released open holds when expires_at elapsed.
// That violated Req 15.6: stale hold cleanup requires A-leg inactivity plus
// maximum execution lifetime and safety grace. expires_at alone cannot prove
// non-execution — in-flight streams share the same durable shape as abandons
// (open hold, no usage_record_processing row).
//
// Automatic TTL reclaim is therefore a no-op. Unused exposure is released only by:
//   - admission ReleaseUnused / handoff exhaustion with no B-leg evidence
//     (execution_not_started),
//   - atomic settlement hold close,
//   - explicit ReleaseAuthorization with ReleaseStaleSafe (inactive+life+grace)
//     or operator_release.
//
// The method remains on the store so the post-turn worker can call it without
// special-casing; returning 0 keeps the worker loop safe.
func (s *DurableStore) ReclaimExpiredHolds(ctx context.Context, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("billingstore: nil store")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	_ = limit
	return 0, nil
}
