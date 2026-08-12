package billing

import "context"

// UsageRecordAppender persists one TUR. Same key and fingerprint is a no-op;
// a conflicting replay is rejected without mutation.
type UsageRecordAppender interface {
	AppendUsageRecord(context.Context, TurnUsageRecord) error
}

// UsageRecordAppenderFunc adapts a function to UsageRecordAppender.
type UsageRecordAppenderFunc func(context.Context, TurnUsageRecord) error

func (f UsageRecordAppenderFunc) AppendUsageRecord(ctx context.Context, record TurnUsageRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, record)
}
