//go:build !unix

package main

import (
	"context"
	"errors"
	"os"
	"syscall"
)

// Task 1.5 contract reference: non-Unix API-only reload surface (req 1.8, 11.8).
// Compiles without SIGHUP constants.

var ErrSIGHUPUnavailable = errors.New("lipstd: SIGHUP reload unavailable on this platform")

// RefSIGHUPTrigger is an API-only stub; Notify is a no-op coalesce counter.
type RefSIGHUPTrigger struct {
	coalesced int64
}

func NewRefSIGHUPTrigger() *RefSIGHUPTrigger { return &RefSIGHUPTrigger{} }

func (t *RefSIGHUPTrigger) Coalesced() int64  { return t.coalesced }
func (t *RefSIGHUPTrigger) Delivered() int64  { return 0 }
func (t *RefSIGHUPTrigger) C() <-chan struct{} { return nil }
func (t *RefSIGHUPTrigger) Notify()            { t.coalesced++ }

func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func ReloadSignals() []os.Signal { return nil }

func StartRefSIGHUPAdapter(ctx context.Context, trigger *RefSIGHUPTrigger) (stop func()) {
	return func() {}
}

func SignalsOverlap() bool { return false }

func PlatformReloadMode() string { return "api-only" }
