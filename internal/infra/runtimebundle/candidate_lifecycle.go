package runtimebundle

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

var (
	_ runtimehost.OwnedCloser   = (*candidateAssembly)(nil)
	_ runtimehost.QuiesceCloser = (*candidateAssembly)(nil)
)

func (c *candidateAssembly) transferLedgerOwnership() *ResourceLedger {
	if c == nil {
		return nil
	}
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.ledgerTransferred || c.lifeClaimed || c.ledger == nil {
		return nil
	}
	ledger := c.ledger
	c.ledger = nil
	c.ledgerTransferred = true
	return ledger
}

func (c *candidateAssembly) claimLifecycleLedger() *ResourceLedger {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.ledgerTransferred || c.ledger == nil {
		return nil
	}
	c.lifeClaimed = true
	return c.ledger
}

func (c *candidateAssembly) Quiesce(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ledger := c.claimLifecycleLedger(); ledger != nil {
		return ledger.Quiesce(ctx)
	}
	return nil
}

func (c *candidateAssembly) RollbackUnpublished() error {
	if c == nil {
		return nil
	}
	if ledger := c.claimLifecycleLedger(); ledger != nil {
		return ledger.Rollback(context.Background())
	}
	return nil
}

func (c *candidateAssembly) Close() error { return c.RollbackUnpublished() }

// discardAcquiredKeepwarm releases a featurehost-acquired keep-warm cleanup
// when candidate compilation fails before any candidate ledger exists to own
// it, so rejected candidates never leak managers into the process registry.
func discardAcquiredKeepwarm(ctx context.Context, compileErr error, quiesce func(context.Context) error) error {
	if quiesce == nil {
		return compileErr
	}
	if qErr := quiesce(ctxOrBackground(ctx)); qErr != nil {
		return errors.Join(compileErr, qErr)
	}
	return compileErr
}
