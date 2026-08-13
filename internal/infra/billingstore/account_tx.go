package billingstore

import (
	"context"
	"fmt"
	"time"
)

// accountTxRetry is the shared SQLite-busy / unique-race budget for one
// account-scoped store transaction. Callers keep their existing attempt
// counts and backoff; ReconcileAccount's stale-snapshot loop is not this path.
type accountTxRetry struct {
	Attempts  int
	Delay     time.Duration
	Classify  func(error) error
	Exhausted error
}

func withAccountTx[T any](ctx context.Context, cfg accountTxRetry, fn func() (T, error)) (T, error) {
	var zero T
	if fn == nil {
		return zero, fmt.Errorf("billingstore: nil account transaction")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := cfg.Attempts
	if attempts <= 0 {
		attempts = 40
	}
	delay := cfg.Delay
	if delay <= 0 {
		delay = 3 * time.Millisecond
	}
	var lastErr error
	for attempt := range attempts {
		out, err := fn()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !isSQLiteBusy(err) && !isUniqueViolation(err) {
			if cfg.Classify != nil {
				return zero, cfg.Classify(err)
			}
			return zero, err
		}
		if attempt == attempts-1 {
			break
		}
		if waitErr := waitContention(ctx, time.Duration(attempt+1)*delay); waitErr != nil {
			return zero, waitErr
		}
	}
	if cfg.Exhausted != nil {
		return zero, cfg.Exhausted
	}
	return zero, lastErr
}

func withAccountTxErr(ctx context.Context, cfg accountTxRetry, fn func() error) error {
	_, err := withAccountTx(ctx, cfg, func() (struct{}, error) {
		if fn == nil {
			return struct{}{}, fmt.Errorf("billingstore: nil account transaction")
		}
		return struct{}{}, fn()
	})
	return err
}
