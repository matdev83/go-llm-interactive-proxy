package decodeqos

import "context"

// Limiter bounds concurrent frontend request body read/preflight/decode work.
type Limiter struct {
	tokens chan struct{}
}

// NewLimiter returns nil for max <= 0 so callers can keep zero-value behavior unlimited.
func NewLimiter(max int) *Limiter {
	if max <= 0 {
		return nil
	}
	return &Limiter{tokens: make(chan struct{}, max)}
}

// TryAcquire attempts to reserve decode capacity without waiting behind saturated work.
func (l *Limiter) TryAcquire(ctx context.Context) (func(), bool, error) {
	if l == nil {
		return func() {}, true, nil
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	default:
	}
	select {
	case l.tokens <- struct{}{}:
		return func() { <-l.tokens }, true, nil
	default:
		return nil, false, nil
	}
}
