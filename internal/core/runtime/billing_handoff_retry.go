package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// billingHandoffRetryJob carries identity needed to seal a TUR after the request
// terminal owner has returned. It never carries provider SDK types.
type billingHandoffRetryJob struct {
	stream          *retryRecvStream
	command         sdkterminal.Command
	accountID       string
	authorizationID string
	aLegID          string
	sessionID       string
	customerPricing billing.VersionRef
	chargePolicy    billing.VersionRef
	// upstreamOpened is true when a backend Open returned a stream for this
	// A-leg (or a terminal stream handoff ran). Exhausted empty persist must
	// retain the hold; execution_not_started release is only for never-opened
	// turns with no B-leg evidence.
	upstreamOpened bool
}

// billingHandoffRetryMaxAttempts bounds detached TUR seal retries when no
// billable evidence exists. Tests may lower it to keep no-evidence exhaustion
// repros fast.
var billingHandoffRetryMaxAttempts = 10

// billingHandoffEvidenceRetryMaxAttempts bounds retries while shared B-leg
// evidence is present. Zero means unlimited (production default): never drop
// provider-accepted usage. Tests that inject permanent persist failures must
// set a positive bound before WaitBillingHandoffRetries.
var billingHandoffEvidenceRetryMaxAttempts = 0

// billingHandoffCloseWait bounds Host.Close / PhaseQuiesce joining of detached
// TUR handoff retries. Live retries stay unlimited while the process is up.
var billingHandoffCloseWait = 10 * time.Second

func (c *billingTurnCollector) scheduleRetry(job billingHandoffRetryJob) {
	if c == nil || c.exec == nil || c.exec.BillingTerminalHandoff == nil || job.aLegID == "" || job.accountID == "" || job.authorizationID == "" {
		return
	}
	if c.retriesStopped() {
		return
	}
	c.retryMu.Lock()
	if c.retryByALeg == nil {
		c.retryByALeg = make(map[string]struct{})
	}
	if _, exists := c.retryByALeg[job.aLegID]; exists {
		c.retryMu.Unlock()
		return
	}
	c.retryByALeg[job.aLegID] = struct{}{}
	c.retryMu.Unlock()

	c.retryWG.Go(func() {
		c.runRetry(job)
	})
}

func (c *billingTurnCollector) waitRetries() {
	if c == nil {
		return
	}
	c.retryWG.Wait()
}

func (c *billingTurnCollector) waitRetriesForClose() {
	if c == nil {
		return
	}
	c.stopRetries()
	timeout := billingHandoffCloseWait
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	done := make(chan struct{})
	go func() {
		c.retryWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		if c.exec != nil && c.exec.Log != nil {
			c.exec.Log.Debug("billing TUR handoff retries still running after close wait")
		}
	}
}

func (c *billingTurnCollector) stopChLocked() chan struct{} {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	if c.stopCh == nil {
		c.stopCh = make(chan struct{})
	}
	return c.stopCh
}

func (c *billingTurnCollector) stopRetries() {
	if c == nil {
		return
	}
	c.retryMu.Lock()
	if c.stopCh == nil {
		c.stopCh = make(chan struct{})
	}
	ch := c.stopCh
	c.retryMu.Unlock()
	c.stopOnce.Do(func() { close(ch) })
}

func (c *billingTurnCollector) retriesStopped() bool {
	if c == nil {
		return true
	}
	c.retryMu.Lock()
	ch := c.stopCh
	c.retryMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (c *billingTurnCollector) runRetry(job billingHandoffRetryJob) {
	defer func() {
		c.retryMu.Lock()
		delete(c.retryByALeg, job.aLegID)
		c.retryMu.Unlock()
		c.forgetSealed(job.aLegID)
	}()
	const barrierWait = 250 * time.Millisecond
	const writeTimeout = 2 * time.Second
	backoff := 100 * time.Millisecond
	const maxBackoff = 5 * time.Second
	noEvidenceAttempts := 0
	evidenceAttempts := 0
	stop := c.stopChLocked()
	for {
		select {
		case <-stop:
			return
		default:
		}
		if c.sealed(job.aLegID) {
			c.syncStreamSuccess(job)
			return
		}
		if job.stream != nil {
			job.stream.billingHandoffMu.Lock()
			done := job.stream.billingHandoffSuccess
			job.stream.billingHandoffMu.Unlock()
			if done {
				c.markSealed(job.aLegID)
				return
			}
		}
		barrierCtx, barrierCancel := context.WithTimeout(context.Background(), barrierWait)
		completed := c.waitBarrier(barrierCtx, job.aLegID)
		barrierCancel()
		var err error
		if completed {
			writeCtx, writeCancel := context.WithTimeout(context.Background(), writeTimeout)
			if job.stream != nil {
				job.stream.billingHandoffMu.Lock()
				if !job.stream.billingHandoffSuccess && !c.sealed(job.aLegID) {
					err = c.persist(writeCtx, job)
					if err == nil {
						c.markSealed(job.aLegID)
						job.stream.billingHandoffSuccess = true
					}
				}
				job.stream.billingHandoffMu.Unlock()
			} else {
				err = c.persist(writeCtx, job)
				if err == nil {
					c.markSealed(job.aLegID)
				}
			}
			writeCancel()
		} else {
			err = errBillingHandoffBarrierIncomplete
		}
		if err == nil {
			return
		}
		hasEvidence := len(c.peek(job.aLegID)) > 0
		if !hasEvidence {
			noEvidenceAttempts++
			if noEvidenceAttempts >= billingHandoffRetryMaxAttempts {
				if job.upstreamOpened || len(c.peek(job.aLegID)) > 0 {
					if c.exec != nil && c.exec.Log != nil {
						c.exec.Log.Debug("billing TUR handoff exhausted after upstream Open; hold retained", "a_leg_id", job.aLegID)
					}
					return
				}
				c.releaseHoldAfterExhausted(job)
				if c.exec != nil && c.exec.Log != nil {
					c.exec.Log.Debug("billing TUR handoff retry exhausted without evidence", "a_leg_id", job.aLegID)
				}
				return
			}
		} else {
			evidenceAttempts++
			if billingHandoffEvidenceRetryMaxAttempts > 0 && evidenceAttempts >= billingHandoffEvidenceRetryMaxAttempts {
				if c.exec != nil && c.exec.Log != nil {
					c.exec.Log.Debug("billing TUR handoff evidence retry budget exhausted; hold retained", "a_leg_id", job.aLegID)
				}
				return
			}
			if c.exec != nil && c.exec.Log != nil {
				c.exec.Log.Debug("billing TUR handoff retry continuing with evidence", "a_leg_id", job.aLegID, "error", err)
			}
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (c *billingTurnCollector) syncStreamSuccess(job billingHandoffRetryJob) {
	if job.stream == nil {
		return
	}
	job.stream.billingHandoffMu.Lock()
	job.stream.billingHandoffSuccess = true
	job.stream.billingHandoffMu.Unlock()
}

var errBillingHandoffBarrierIncomplete = errors.New("runtime: billing evidence barrier incomplete")

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
