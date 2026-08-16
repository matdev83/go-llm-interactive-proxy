package runtimebundle

import (
	"context"
	"sync"
)

// startOwnedLoop starts a generation-owned loop and registers its cancel+join
// release with the ledger before application work begins (req 4.2). A nil
// ledger means there is no ownership context, so no loop is started.
func startOwnedLoop(ledger *ResourceLedger, name string, phase ClosePhase, parent context.Context, loop func(ctx context.Context)) {
	if ledger == nil {
		return
	}
	parent = ctxOrBackground(parent)
	ctx, cancel := context.WithCancel(parent)
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-ctx.Done():
			return
		case <-started:
		}
		if ctx.Err() != nil {
			return
		}
		loop(ctx)
	})
	ledger.AddClose(name, phase, func() error {
		cancel()
		wg.Wait()
		return nil
	})
	close(started)
}
