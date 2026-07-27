package runtimehost

// Close closes generation-owned resources from Closing (req 10.6, 10.12).
// On success it transitions to GenClosed exactly once. On failure it remains
// GenClosing so an explicitly owned cleanup retry policy may call Close again
// (design Closing→Closing). Successful close of owned resources happens once:
// a failed attempt retains the owned closer for retry. CloseCount tracks the
// number of close *attempts* (successful or not), not just successes.
//
// Terminal compatibility: after a successful Discard (GenFailed with no
// owned/request-plane payload), Close returns ErrAlreadyClosed. After a failed
// Discard that restored the payload pair, Close returns ErrIllegalTransition
// so Discard remains the retry path.
func (g *Generation) Close() error {
	if g == nil {
		return nil
	}
	g.closeMu.Lock()
	defer g.closeMu.Unlock()

	st, _ := unpackLease(g.word.Load())
	if st == GenClosed {
		return ErrAlreadyClosed
	}
	if st == GenFailed {
		g.payloadMu.Lock()
		empty := g.owned == nil && g.requestPlane == nil
		g.payloadMu.Unlock()
		if empty {
			return ErrAlreadyClosed
		}
		return ErrIllegalTransition
	}
	if st != GenClosing {
		return ErrIllegalTransition
	}

	g.payloadMu.Lock()
	owned := g.owned
	g.payloadMu.Unlock()

	g.closeCount.Add(1)
	if owned != nil {
		if err := owned.Close(); err != nil {
			return err
		}
	}

	g.payloadMu.Lock()
	g.owned = nil
	g.requestPlane = nil
	g.payloadMu.Unlock()
	for {
		cur := g.word.Load()
		_, refs := unpackLease(cur)
		if g.word.CompareAndSwap(cur, packLease(GenClosed, refs)) {
			break
		}
	}
	return nil
}

// Discard rolls back an unpublished candidate (preparing/prepared/failed).
// It closes generation-owned resources exactly once and ends in GenFailed
// (req 10.9). It never uses the published drain→BeginClose→Close path and
// never touches process services.
//
// Terminal transition and payload claim share payloadMu with Attach* so a
// binding cannot commit after discard has claimed an empty pair. requestPlane
// and owned are one ownership pair: claimed/cleared together, restored
// together on close failure, and left nil after successful discard.
func (g *Generation) Discard() error {
	if g == nil {
		return nil
	}
	g.closeMu.Lock()
	defer g.closeMu.Unlock()

	g.payloadMu.Lock()
	firstTransition := false
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		switch st {
		case GenPreparing, GenPrepared:
			if !g.word.CompareAndSwap(cur, packLease(GenFailed, refs)) {
				continue
			}
			firstTransition = true
		case GenFailed:
			// already terminal; only an owned claim below can still succeed.
		default:
			g.payloadMu.Unlock()
			return ErrIllegalTransition
		}
		break
	}

	owned := g.owned
	plane := g.requestPlane
	g.owned = nil
	g.requestPlane = nil
	g.payloadMu.Unlock()

	if !firstTransition && owned == nil && plane == nil {
		return ErrAlreadyClosed
	}

	g.closeCount.Add(1)
	if owned == nil {
		return nil
	}
	if err := owned.Close(); err != nil {
		g.payloadMu.Lock()
		if g.owned == nil && g.requestPlane == nil {
			g.owned = owned
			g.requestPlane = plane
		}
		g.payloadMu.Unlock()
		return err
	}
	return nil
}
