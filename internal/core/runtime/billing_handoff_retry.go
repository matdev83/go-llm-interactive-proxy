package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func (c *billingTurnCollector) scheduleRetry(job billing.HandoffRetryJob) {
	if c == nil || c.exec == nil || c.exec.BillingTerminalHandoff == nil || c.outbox == nil {
		return
	}
	if job.ALegID == "" || job.AccountID == "" || job.AuthorizationID == "" {
		return
	}
	_ = c.outbox.Enqueue(context.Background(), job)
	c.ensureRetryLoop()
}

func (c *billingTurnCollector) ensureRetryLoop() {
	if c == nil || c.retry == nil {
		return
	}
	_ = c.retry.Start(context.Background())
}

func (c *billingTurnCollector) waitRetries() {
	if c == nil || c.retry == nil {
		return
	}
	_ = c.retry.ProcessUntilIdle(context.Background())
	_ = c.retry.Stop(context.Background())
}

func (c *billingTurnCollector) waitRetriesForClose() {
	if c == nil || c.retry == nil {
		return
	}
	timeout := billing.DefaultHandoffCloseWait
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = c.retry.Stop(ctx)
}

func (c *billingTurnCollector) stopRetries() {
	if c == nil || c.retry == nil {
		return
	}
	_ = c.retry.Stop(context.Background())
}

func (e *Executor) WaitBillingHandoffRetries() {
	if e == nil {
		return
	}
	e.billingTurns().waitRetries()
}

func (e *Executor) WaitBillingHandoffRetriesForClose() {
	if e == nil {
		return
	}
	e.billingTurns().waitRetriesForClose()
}

func (e *Executor) stopBillingHandoffRetries() {
	if e == nil {
		return
	}
	e.billingTurns().stopRetries()
}
