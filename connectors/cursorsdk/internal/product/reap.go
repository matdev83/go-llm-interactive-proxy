package product

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (b *bridgeProcess) reapLateStart(started <-chan startResult) {
	out := <-started
	if out.proc == nil {
		return
	}
	_ = out.proc.Kill()
	_ = out.proc.Wait()
}

func (b *bridgeProcess) reapCurrentLocked() error {
	b.mu.Lock()
	old := b.proc
	oldDone := b.waitDone
	if old != nil {
		b.proc = nil
		if b.state != bridgeClosing && b.state != bridgeClosed {
			b.state = bridgeIdle
		}
	}
	b.mu.Unlock()
	if old == nil {
		return nil
	}
	return b.killAndReapHandle(old, oldDone, b.reapTimeout())
}

func (b *bridgeProcess) reapTimeout() time.Duration {
	if b.cfg.ShutdownTimeout > 0 {
		return b.cfg.ShutdownTimeout
	}
	return defaultReapTimeout
}

// killAndReapHandle tree-kills an owned Process and awaits its wait owner with a bound.
func (b *bridgeProcess) killAndReapHandle(proc Process, done chan struct{}, timeout time.Duration) error {
	return b.killAndReapOwned(proc, processIdentity{}, done, timeout, false)
}

// killAndReapOwned kills an owned process. Generation+pointer ownership is authoritative.
// When checkIdentity is set and identity mismatches, tree kill is skipped and only the
// process handle is killed, then wait is still bounded (never blocks forever on done).
func (b *bridgeProcess) killAndReapOwned(proc Process, identity processIdentity, done chan struct{}, timeout time.Duration, checkIdentity bool) error {
	if proc == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultReapTimeout
	}
	var killErr error
	if checkIdentity && identity.PID > 0 && !b.inspector.stillSame(proc, identity) {
		killErr = killProcessHandle(proc)
		if killErr != nil {
			killErr = fmt.Errorf("cursorsdk: identity mismatch; handle kill: %w", killErr)
		}
	} else {
		killErr = proc.Kill()
	}
	if done != nil {
		select {
		case <-done:
			return killErr
		case <-time.After(timeout):
			return errors.Join(killErr, errors.New("cursorsdk: reap timed out"))
		}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- proc.Wait() }()
	select {
	case err := <-waitCh:
		return errors.Join(killErr, err)
	case <-time.After(timeout):
		return errors.Join(killErr, errors.New("cursorsdk: wait timed out"))
	}
}

func (b *bridgeProcess) killAndReapGeneration(gen int64, identity processIdentity) error {
	b.mu.Lock()
	if b.gen != gen {
		b.mu.Unlock()
		return nil
	}
	proc := b.proc
	done := b.waitDone
	if proc != nil {
		b.proc = nil
	}
	diag := sanitizeBridgeDiag(b.stderrBuf, b.cfg.APIKey)
	if diag != "" {
		diag = "stderr=" + diag
	}
	b.closeRunsForGenerationLocked(gen, BridgeExited(nil, diag))
	b.mu.Unlock()
	return b.killAndReapOwned(proc, identity, done, b.reapTimeout(), true)
}

// KillGeneration kills and reaps the subprocess for gen when this bridge still
// owns that generation (identity-protected). Stale gens are a no-op.
func (b *bridgeProcess) KillGeneration(ctx context.Context, gen int64) error {
	if b == nil || gen <= 0 {
		return nil
	}
	_ = ctx // reap is bounded by ShutdownTimeout; never block on caller deadline
	b.mu.Lock()
	if b.gen != gen {
		b.mu.Unlock()
		return nil
	}
	identity := b.identity
	b.mu.Unlock()
	return b.killAndReapGeneration(gen, identity)
}

// killGenerationIfCurrent is for delayed kills: skip tree kill on identity mismatch
// to avoid PID reuse; never blocks forever.
func (b *bridgeProcess) killGenerationIfCurrent(gen int64, identity processIdentity) error {
	b.mu.Lock()
	if b.gen != gen {
		b.mu.Unlock()
		return nil
	}
	proc := b.proc
	done := b.waitDone
	b.mu.Unlock()
	if proc == nil {
		return nil
	}
	if !b.inspector.stillSame(proc, identity) {
		return errors.New("cursorsdk: delayed kill skipped: process identity mismatch")
	}
	b.mu.Lock()
	if b.gen != gen || b.proc != proc {
		b.mu.Unlock()
		return nil
	}
	b.proc = nil
	b.mu.Unlock()
	return b.killAndReapHandle(proc, done, b.reapTimeout())
}
