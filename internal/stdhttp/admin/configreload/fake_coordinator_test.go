package configreload_test

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// fakeCoordinator mirrors the serialized busy/status/host-context behavior needed
// by management adapter goldens without compiling a full runtimehost pipeline.
type fakeCoordinator struct {
	mu          sync.Mutex
	busy        bool
	shutdown    atomic.Bool
	attempts    atomic.Int64
	activeGen   atomic.Int64
	last        configreload.ReloadResult
	fixedSource string
	reloadFn    func(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult
	onComplete  func(configreload.ReloadResult)
}

func newFakeCoordinator(fixed string, fn func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult) *fakeCoordinator {
	c := &fakeCoordinator{fixedSource: fixed, reloadFn: fn}
	c.activeGen.Store(1)
	c.last = configreload.ReloadResult{Category: configreload.ResultPublished, ActiveGeneration: 1}
	return c
}

func (c *fakeCoordinator) FixedSourcePath() string { return c.fixedSource }
func (c *fakeCoordinator) MarkShutdown()           { c.shutdown.Store(true) }
func (c *fakeCoordinator) SetOnComplete(fn func(configreload.ReloadResult)) {
	c.mu.Lock()
	c.onComplete = fn
	c.mu.Unlock()
}

func (c *fakeCoordinator) Status() configreload.ReloadStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return configreload.ReloadStatus{
		ActiveGeneration: c.activeGen.Load(),
		LastResult:       c.last,
		Busy:             c.busy,
	}
}

func (c *fakeCoordinator) Reload(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult {
	if c.shutdown.Load() {
		return configreload.ReloadResult{Category: configreload.ResultCanceled, ReasonCategory: "shutdown"}
	}
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return configreload.ReloadResult{
			Category:         configreload.ResultBusy,
			ActiveGeneration: c.activeGen.Load(),
			ReasonCategory:   "reload-in-progress",
		}
	}
	c.busy = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
	}()

	attempt := c.attempts.Add(1)
	var res configreload.ReloadResult
	if c.reloadFn != nil {
		res = c.reloadFn(ctx, trigger)
	} else {
		res = configreload.ReloadResult{Category: configreload.ResultPublished}
	}
	res.AttemptID = attempt
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGen.Load()
	}
	if res.Category == configreload.ResultPublished {
		prev := c.activeGen.Load()
		res.PreviousGeneration = prev
		c.activeGen.Store(prev + 1)
		res.ActiveGeneration = c.activeGen.Load()
	}
	c.mu.Lock()
	c.last = res
	onComplete := c.onComplete
	c.mu.Unlock()
	if onComplete != nil {
		onComplete(res)
	}
	return res
}
