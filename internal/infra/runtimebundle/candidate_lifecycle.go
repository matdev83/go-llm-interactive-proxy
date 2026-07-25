package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

var (
	_ runtimehost.OwnedCloser   = (*CandidateRuntime)(nil)
	_ runtimehost.QuiesceCloser = (*CandidateRuntime)(nil)
)

func (c *CandidateRuntime) transferLedgerOwnership() *ResourceLedger {
	if c == nil {
		return nil
	}
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.ledgerTransferred || c.lifeClaimed || c.Ledger == nil {
		return nil
	}
	ledger := c.Ledger
	c.Ledger = nil
	c.ledgerTransferred = true
	return ledger
}

func (c *CandidateRuntime) claimLifecycleLedger() *ResourceLedger {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.ledgerTransferred || c.Ledger == nil {
		return nil
	}
	c.lifeClaimed = true
	return c.Ledger
}

func (c *CandidateRuntime) Quiesce(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ledger := c.claimLifecycleLedger(); ledger != nil {
		return ledger.Quiesce(ctx)
	}
	return nil
}

func (c *CandidateRuntime) RollbackUnpublished() error {
	if c == nil {
		return nil
	}
	if ledger := c.claimLifecycleLedger(); ledger != nil {
		return ledger.Rollback(context.Background())
	}
	return nil
}

func (c *CandidateRuntime) Close() error { return c.RollbackUnpublished() }
