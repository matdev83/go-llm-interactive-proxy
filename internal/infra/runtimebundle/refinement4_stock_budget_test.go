package runtimebundle_test

import (
	"testing"
	"time"
)

const (
	// refinement4StockPhaseWait bounds one synchronous phase wait of the stock
	// observation->economic sentinel. Every phase wait shares the sentinel's
	// single parent context, so that parent must cover the sum of the phase
	// budgets (see refinement4StockSentinelBudget) or one slow phase silently
	// preempts the rest under load.
	refinement4StockPhaseWait = 8 * time.Second

	// refinement4StockPhaseWaitCount is the number of sequential phase waits in
	// TestRefinement4StockObservationToEconomicSettlement: three economic-head
	// waits (provider, customer, customer replay), provider amount, settlement,
	// head revision, and provider current amount.
	refinement4StockPhaseWaitCount = 7

	// refinement4StockSentinelBudget is the sentinel's own deadlock detector,
	// not a performance budget. The composed pipeline is poll-driven at
	// production intervals (100ms observation relay, 1s revision workers), so
	// its wall clock scales several-fold under the package's default parallel
	// test load: isolated it completes in ~4s, and under full-package
	// parallelism it has been measured at ~20.6s (default) and ~24.0s
	// (-parallel=32). Batch C made waitRefinement4StockOutboxDrained inherit
	// this parent (it previously imposed a fixed 4s per-drain child), so the
	// parent is now the only bound on a healthy slow drain. 60s matches the
	// sibling refinement5_2 sentinel that shares the same drain helper and
	// covers the 56s sum of the seven phase waits, while the 4s drain stall
	// window and the per-phase budgets remain the fail-fast detectors.
	refinement4StockSentinelBudget = 60 * time.Second
)

// TestRefinement4StockSentinelBudgetCoversPhaseWaits is the deterministic
// regression for the load-sensitive drain failure. A parent deadline smaller
// than the sum of the phase waits that share it cannot let every phase use its
// own budget: under the package's default parallel test load the pre-fix 25s
// parent was exhausted mid-pipeline and surfaced as the confusing
// "outbox drain parent done ... (last pending "\x00unset")" instead of a
// precise phase timeout, because Batch C made the drain inherit the parent
// rather than imposing a fixed per-drain child. This pins the invariant that
// the sentinel budget dominates every phase budget it is shared with.
func TestRefinement4StockSentinelBudgetCoversPhaseWaits(t *testing.T) {
	t.Parallel()
	minBudget := refinement4StockPhaseWait * refinement4StockPhaseWaitCount
	if refinement4StockSentinelBudget < minBudget {
		t.Fatalf("sentinel budget %s must cover the sum of %d phase waits (%s); a smaller parent silently preempts shared phases under load",
			refinement4StockSentinelBudget, refinement4StockPhaseWaitCount, minBudget)
	}
}
