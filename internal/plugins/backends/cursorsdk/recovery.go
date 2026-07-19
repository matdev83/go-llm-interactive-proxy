package cursorsdk

import (
	"sync"
)

type poolInvalidator interface {
	InvalidateAll(cause InvalidationCause)
	InvalidateGeneration(gen int64, cause InvalidationCause)
}

type OnBridgeGenerationDead func(gen int64)

type FailureCoordinatorOpts struct {
	OnInvalidate func(cause InvalidationCause)
}

type FailureCoordinator struct {
	pool  poolInvalidator
	onInv func(cause InvalidationCause)

	mu             sync.Mutex
	invalidatedGen map[int64]struct{}
}

func NewFailureCoordinator(pool poolInvalidator, opts FailureCoordinatorOpts) *FailureCoordinator {
	return &FailureCoordinator{
		pool:           pool,
		onInv:          opts.OnInvalidate,
		invalidatedGen: make(map[int64]struct{}),
	}
}

func (c *FailureCoordinator) InvalidateOnBridgeDeath(gen int64) {
	if c == nil {
		return
	}
	c.invalidateGenerationOnce(gen)
}

func (c *FailureCoordinator) invalidateGenerationOnce(gen int64) {
	c.mu.Lock()
	if _, ok := c.invalidatedGen[gen]; ok {
		c.mu.Unlock()
		return
	}
	c.invalidatedGen[gen] = struct{}{}
	c.mu.Unlock()
	if c.pool != nil {
		c.pool.InvalidateGeneration(gen, InvalidateBridge)
	}
	if c.onInv != nil {
		c.onInv(InvalidateBridge)
	}
}
