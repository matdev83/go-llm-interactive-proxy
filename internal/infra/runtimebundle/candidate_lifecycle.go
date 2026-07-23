package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

// CandidateRuntime satisfies runtimehost.OwnedCloser / QuiesceCloser so a
// generation may own candidate teardown without receiving ProcessServices
// (task 3.1 / req 4.9). CompileCandidate callers retain this lifecycle;
// successful CompileGeneration transfers the ledger away. The ledger is the
// sole generation-owned resource; there is no aggregate closer view (task 4.2).
var (
	_ runtimehost.OwnedCloser   = (*CandidateRuntime)(nil)
	_ runtimehost.QuiesceCloser = (*CandidateRuntime)(nil)
)

// transferLedgerOwnership detaches the resource ledger from this candidate and
// returns it for GenerationRuntime ownership. After transfer, Quiesce/Close on
// the candidate are no-ops and must not close generation resources. Package-
// private for CompileGeneration only (Task 3.3).
func (c *CandidateRuntime) transferLedgerOwnership() *ResourceLedger {
	if c == nil {
		return nil
	}
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.ledgerTransferred {
		return nil
	}
	ledger := c.Ledger
	c.Ledger = nil
	c.ledgerTransferred = true
	return ledger
}

// Quiesce stops admission-independent generation workers once (req 10.5).
func (c *CandidateRuntime) Quiesce(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.quiesceOnce.Do(func() {
		c.lifeMu.Lock()
		transferred := c.ledgerTransferred
		ledger := c.Ledger
		c.lifeMu.Unlock()
		if transferred {
			return
		}
		c.didQuiesce.Store(true)
		if ledger != nil {
			c.quiesceErr = ledger.Quiesce(ctx)
		}
	})
	return c.quiesceErr
}

// Close disposes generation-owned resources in reverse order via the ledger.
// Unpublished discard / compile failure uses full ledger rollback; after
// Quiesce, only remaining close-phase resources are released. Never closes
// ProcessServices. After transferLedgerOwnership, Close is a no-op. A nil or
// zero-value ledger makes Close a safe no-op (req 2.8, 3.8, 8.3-8.4).
func (c *CandidateRuntime) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.lifeMu.Lock()
		transferred := c.ledgerTransferred
		ledger := c.Ledger
		didQ := c.didQuiesce.Load()
		c.lifeMu.Unlock()
		if transferred || ledger == nil {
			return
		}
		if didQ {
			c.closeErr = ledger.Close(context.Background())
		} else {
			c.closeErr = ledger.Rollback(context.Background())
		}
	})
	return c.closeErr
}
