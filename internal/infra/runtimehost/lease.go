package runtimehost

import "sync/atomic"

// Lease is a hot-path request lease (req 5.3-5.4, 15.1).
type Lease struct {
	gen         *Generation
	released    atomic.Bool
	transferred atomic.Bool
}

// Generation returns the bound generation.
func (l *Lease) Generation() *Generation {
	if l == nil {
		return nil
	}
	return l.gen
}

// Release drops the lease retain exactly once; double-release is a no-op (req 10.4).
func (l *Lease) Release() {
	if l == nil || !l.released.CompareAndSwap(false, true) || l.transferred.Load() {
		return
	}
	l.gen.releaseRef()
}

// TransferPin converts the lease retain into an async/SSE/provider pin (req 5.7, 10.3).
func (l *Lease) TransferPin(kind PinKind) (*Pin, bool) {
	if l == nil || !l.released.CompareAndSwap(false, true) {
		return nil, false
	}
	l.transferred.Store(true)
	return &Pin{gen: l.gen, kind: kind}, true
}

// Pin retains a generation across SSE/async/provider work.
type Pin struct {
	gen      *Generation
	kind     PinKind
	released atomic.Bool
}

// Kind returns the pin classification.
func (p *Pin) Kind() PinKind {
	if p == nil {
		return 0
	}
	return p.kind
}

// Generation returns the pinned generation.
func (p *Pin) Generation() *Generation {
	if p == nil {
		return nil
	}
	return p.gen
}

// Release drops the pin retain exactly once.
func (p *Pin) Release() {
	if p == nil || !p.released.CompareAndSwap(false, true) {
		return
	}
	p.gen.releaseRef()
}
