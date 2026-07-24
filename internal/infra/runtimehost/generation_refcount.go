package runtimehost

func (g *Generation) tryRetain() bool {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenActive || refs == ^uint32(0) {
			return false
		}
		if g.word.CompareAndSwap(cur, packLease(st, refs+1)) {
			return true
		}
	}
}

// tryRetainWhileBound increments ownership for a child pin while a request lease
// (or transferred pin path) already proves the generation is still live.
// Active and post-retirement drain states with outstanding refs are allowed so
// a publication race cannot close the generation between child-pin acquisition
// and use. New acquires after drain/close fail closed.
func (g *Generation) tryRetainWhileBound() bool {
	if g == nil {
		return false
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if !childRetainable(st) || refs == 0 || refs == ^uint32(0) {
			return false
		}
		if g.word.CompareAndSwap(cur, packLease(st, refs+1)) {
			return true
		}
	}
}

func childRetainable(st GenLifecycle) bool {
	switch st {
	case GenActive, GenRetiring, GenQuiescing, GenQuiesced:
		return true
	default:
		return false
	}
}

func (g *Generation) releaseRef() {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if refs == 0 {
			return
		}
		nextRefs := refs - 1
		if !g.word.CompareAndSwap(cur, packLease(st, nextRefs)) {
			continue
		}
		if nextRefs == 0 && drainable(st) {
			g.signalDrained()
		}
		return
	}
}

// drainable reports whether last-ref release (or markRetiring with refs=0) may
// transition to GenDrained and close Drained(). GenQuiescing is intentionally
// excluded: quiesce work must finish via MarkQuiesced before drain/close.
func drainable(st GenLifecycle) bool {
	return st == GenRetiring || st == GenQuiesced
}

func (g *Generation) markRetiring() {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenActive {
			return
		}
		if g.word.CompareAndSwap(cur, packLease(GenRetiring, refs)) {
			if refs == 0 {
				g.signalDrained()
			}
			return
		}
	}
}

func (g *Generation) signalDrained() {
	g.drainMu.Lock()
	defer g.drainMu.Unlock()
	if g.drainClosed {
		return
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if refs != 0 {
			return
		}
		if st == GenDrained {
			break
		}
		if !drainable(st) {
			return
		}
		if g.word.CompareAndSwap(cur, packLease(GenDrained, 0)) {
			break
		}
	}
	g.drainClosed = true
	close(g.drainCh)
}
