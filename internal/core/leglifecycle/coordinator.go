// Package leglifecycle owns A-leg scoped cancellation and B-leg teardown policy.
package leglifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var ErrALegCanceled = errors.New("leglifecycle: a-leg canceled")

type CancelKind = lipapi.CancelKind

const (
	CancelExplicit    = lipapi.CancelExplicit
	CancelClientGone  = lipapi.CancelClientGone
	CancelContextDone = lipapi.CancelContextDone
	CancelRaceLoser   = lipapi.CancelRaceLoser
)

type CancelCause = lipapi.CancelCause

type CancelMode = lipapi.CancelMode

const (
	CancelModeNone      = lipapi.CancelModeNone
	CancelModeProvider  = lipapi.CancelModeProvider
	CancelModeTransport = lipapi.CancelModeTransport
	CancelModeCloseOnly = lipapi.CancelModeCloseOnly
)

type CancelResult = lipapi.CancelResult

type BLegAttempt = lipapi.ManagedEventStream

type BLegHandle struct {
	ID      string
	Attempt BLegAttempt
}

type CloseOnlyAttempt struct {
	Closer interface{ Close() error }
}

func (a CloseOnlyAttempt) Cancel(context.Context, CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeCloseOnly}
}

func (a CloseOnlyAttempt) Close() error {
	if a.Closer == nil {
		return nil
	}
	return a.Closer.Close()
}

type CoordinatorConfig struct {
	CancelTimeout time.Duration
}

type Coordinator struct {
	mu    sync.Mutex
	cfg   CoordinatorConfig
	alegs map[string]*ALeg
}

func NewCoordinator(cfg CoordinatorConfig) *Coordinator {
	return &Coordinator{cfg: cfg, alegs: map[string]*ALeg{}}
}

func (c *Coordinator) ensureALegsLocked() {
	if c != nil && c.alegs == nil {
		c.alegs = map[string]*ALeg{}
	}
}

func (c *Coordinator) StartALeg(id string) *ALeg {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureALegsLocked()
	if a := c.alegs[id]; a != nil {
		return a
	}
	a := &ALeg{id: id, coordinator: c, blegs: map[string]BLegAttempt{}}
	c.alegs[id] = a
	return a
}

func (c *Coordinator) CancelALeg(ctx context.Context, id string, cause CancelCause) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.ensureALegsLocked()
	a := c.alegs[id]
	if a == nil {
		a = &ALeg{id: id, coordinator: c, blegs: map[string]BLegAttempt{}}
		c.alegs[id] = a
	}
	c.mu.Unlock()
	return a.Cancel(ctx, cause)
}

func (c *Coordinator) EndALeg(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.alegs, id)
	c.mu.Unlock()
}

type ALeg struct {
	id          string
	coordinator *Coordinator

	mu       sync.Mutex
	canceled bool
	cause    CancelCause
	blegs    map[string]BLegAttempt
}

func (a *ALeg) RegisterBLeg(ctx context.Context, h BLegHandle) error {
	if a == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.Attempt == nil {
		return nil
	}
	a.mu.Lock()
	if a.canceled {
		cause := a.cause
		a.mu.Unlock()
		cleanupErr := cancelAndClose(ctx, a.cancelTimeout(), h.Attempt, cause)
		if cleanupErr != nil {
			return errors.Join(ErrALegCanceled, cleanupErr)
		}
		return ErrALegCanceled
	}
	a.blegs[h.ID] = h.Attempt
	a.mu.Unlock()
	return nil
}

func (a *ALeg) Cancel(ctx context.Context, cause CancelCause) error {
	if a == nil {
		return nil
	}
	if cause.Kind == "" {
		cause.Kind = CancelContextDone
	}
	a.mu.Lock()
	if a.canceled {
		a.mu.Unlock()
		return nil
	}
	a.canceled = true
	a.cause = cause
	blegs := make([]BLegAttempt, 0, len(a.blegs))
	for _, b := range a.blegs {
		blegs = append(blegs, b)
	}
	a.mu.Unlock()
	var cleanupErr error
	for _, b := range blegs {
		cleanupErr = errors.Join(cleanupErr, cancelAndClose(ctx, a.cancelTimeout(), b, cause))
	}
	if cleanupErr != nil {
		return fmt.Errorf("leglifecycle: cancel and close b-legs: %w", cleanupErr)
	}
	return nil
}

func (a *ALeg) Err() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.canceled {
		return ErrALegCanceled
	}
	return nil
}

func (a *ALeg) End() {
	if a == nil || a.coordinator == nil {
		return
	}
	a.coordinator.EndALeg(a.id)
}

func (a *ALeg) ReleaseBLeg(id string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.blegs, id)
	a.mu.Unlock()
}

func (a *ALeg) cancelTimeout() time.Duration {
	if a == nil || a.coordinator == nil {
		return 0
	}
	return a.coordinator.cfg.CancelTimeout
}

func cancelAndClose(parent context.Context, timeout time.Duration, b BLegAttempt, cause CancelCause) error {
	if b == nil {
		return nil
	}
	ctx := parent
	cancel := func() {}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), timeout)
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	defer cancel()
	var cleanupErr error
	if res := b.Cancel(ctx, cause); res.Err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cancel b-leg: %w", res.Err))
	}
	if err := b.Close(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("close b-leg: %w", err))
	}
	return cleanupErr
}
