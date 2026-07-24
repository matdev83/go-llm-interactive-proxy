package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

// CandidateRuntime satisfies runtimehost.OwnedCloser / QuiesceCloser so a
// generation may own candidate teardown without receiving ProcessServices
// (task 3.1 / req 4.9). CompileCandidate callers retain this lifecycle;
// successful CompileGeneration transfers the ledger away. The ledger is the
// sole generation-owned resource phase owner; CandidateRuntime only performs
// synchronized transfer plus direct delegation (task 7.2).
var (
	_ runtimehost.OwnedCloser   = (*CandidateRuntime)(nil)
	_ runtimehost.QuiesceCloser = (*CandidateRuntime)(nil)
)

// transferLedgerOwnership detaches the resource ledger from this candidate and
// returns it for GenerationRuntime ownership. After transfer, Quiesce/Close on
// the candidate are no-ops and must not close generation resources. Package-
// private for CompileGeneration only (Task 3.3).
//
// Transfer and pre-transfer Quiesce/Close compete for an exclusive ownership
// claim: the first winner permanently owns the ledger path. If lifecycle
// claimed first, transfer returns nil so CompileGeneration cannot publish an
// already-cleaned (or candidate-retained) ledger. lifeMu is never held across
// arbitrary ledger cleanup.
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

// claimLifecycleLedger permanently claims the candidate lifecycle path against
// later transfer and returns the still-candidate-owned ledger. After claim,
// Quiesce and Close may both delegate to that ledger; transfer is denied.
// Returns nil when transfer already won or no ledger remains.
func (c *CandidateRuntime) claimLifecycleLedger() *ResourceLedger {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.ledgerTransferred || c.Ledger == nil {
		return nil
	}
	c.lifeClaimed = true
	return c.Ledger
}

// Quiesce delegates to the ledger before transfer; after transfer it is a no-op.
// The first Quiesce/Close permanently claims against later transfer.
func (c *CandidateRuntime) Quiesce(ctx context.Context) error {
	if c == nil {
		return nil
	}
	ledger := c.claimLifecycleLedger()
	if ledger == nil {
		return nil
	}
	return ledger.Quiesce(ctx)
}

// Close delegates to the ledger's canonical close/rollback decision before
// transfer; after transfer it is a no-op. Never closes ProcessServices.
// The first Quiesce/Close permanently claims against later transfer.
func (c *CandidateRuntime) Close() error {
	if c == nil {
		return nil
	}
	ledger := c.claimLifecycleLedger()
	if ledger == nil {
		return nil
	}
	return ledger.Close(context.Background())
}
