package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	// maxPreTerminalEconomicCheckpointPending bounds the attempt-owned queue.
	// Cumulative snapshots for one source coalesce into one entry, while delta
	// and replacement observations retain their distinct source revisions.
	maxPreTerminalEconomicCheckpointPending = 64
	// Deferred entries retain observations admitted while the bounded pending
	// queue is full. Keeping a second, equally bounded queue preserves retry
	// ownership without allowing a failed journal to grow receive-path state.
	maxPreTerminalEconomicCheckpointDeferred = maxPreTerminalEconomicCheckpointPending
	// Accepted terminal evidence is already bounded by the billing evidence
	// limit. Keep the durable revision fence within that same attempt bound.
	maxPreTerminalEconomicCheckpointDurableHeads = billing.MaxCallLegEvidenceObservations
	preTerminalEconomicCheckpointBatch           = 8
	preTerminalEconomicCheckpointInterval        = 250 * time.Millisecond
	economicCheckpointFlushTimeout               = 2 * time.Second
)

var errEconomicCheckpointPendingLimit = errors.New("runtime: economic checkpoint pending limit reached")
var errEconomicCheckpointAtomicBatchRequired = errors.New("runtime: economic checkpoint sink requires atomic batch capability")

const (
	economicCheckpointCapacityRejectionLogMessage = "economic_checkpoint_capacity_rejected"
	economicCheckpointCapacityRejectionReason     = "checkpoint_capacity_exhausted"
)

type checkpointDurableHead struct {
	revision uint64
}

type economicCheckpointAdmission uint8

const (
	economicCheckpointRejected economicCheckpointAdmission = iota
	economicCheckpointRetained
	economicCheckpointIgnored
)

type economicCheckpointWrite struct {
	pendingKey  string
	hash        string
	deferred    bool
	observation metering.Observation
}

type economicCheckpointCapacityDiagnostic struct {
	semantics string
	revision  uint64
}

// queueEconomicCheckpoint admits an immutable observation to one of the
// attempt-owned bounded queues. Admission never waits for the journal: a full
// pending queue uses the bounded deferred queue, so receive callbacks retain
// ownership even when a recovery flush is unavailable.
func (a *attemptSession) queueEconomicCheckpoint(observation metering.Observation) economicCheckpointAdmission {
	if a == nil {
		return economicCheckpointRejected
	}
	if a.observationSink == nil {
		// No durable checkpoint consumer is configured; terminal evidence owns
		// the observation and no queue admission is required.
		return economicCheckpointIgnored
	}
	canonical, hash, err := canonicalEconomicCheckpoint(observation)
	if err != nil {
		a.noteEconomicCheckpointError(err)
		return economicCheckpointRejected
	}
	pendingKey := economicCheckpointPendingKey(canonical)

	a.checkpointMu.Lock()
	defer a.checkpointMu.Unlock()
	if canonical.Semantics == metering.SemanticsCumulative {
		if prior, ok := a.checkpointDurableHeads[pendingKey]; ok && canonical.Revision <= prior.revision {
			// A durable cumulative head is authoritative for this attempt. A
			// reordered older revision must not re-enter either queue after a
			// successful flush.
			return economicCheckpointIgnored
		}
	}
	if a.checkpointPending == nil {
		a.checkpointPending = make(map[string]metering.Observation)
	}
	if prior, ok := a.checkpointPending[pendingKey]; ok {
		if canonical.Revision < prior.Revision {
			// A reordered cumulative snapshot must not move the pending head
			// backwards. Delta observations use revision-qualified keys and
			// therefore do not enter this branch for normal delivery.
			return economicCheckpointIgnored
		}
		priorHash, priorErr := prior.ReplayFingerprint()
		if priorErr == nil && priorHash == hash {
			return economicCheckpointIgnored
		}
		a.checkpointPending[pendingKey] = canonical
		return economicCheckpointRetained
	}
	if prior, ok := a.checkpointDeferred[pendingKey]; ok {
		if canonical.Revision < prior.Revision {
			return economicCheckpointIgnored
		}
		priorHash, priorErr := prior.ReplayFingerprint()
		if priorErr == nil && priorHash == hash {
			return economicCheckpointIgnored
		}
		a.checkpointDeferred[pendingKey] = canonical
		return economicCheckpointRetained
	}
	if len(a.checkpointPending) < maxPreTerminalEconomicCheckpointPending {
		a.checkpointPending[pendingKey] = canonical
		a.checkpointOrder = append(a.checkpointOrder, pendingKey)
		return economicCheckpointRetained
	}
	if len(a.checkpointDeferred) < maxPreTerminalEconomicCheckpointDeferred {
		if a.checkpointDeferred == nil {
			a.checkpointDeferred = make(map[string]metering.Observation)
		}
		a.checkpointDeferred[pendingKey] = canonical
		a.checkpointDeferredOrder = append(a.checkpointDeferredOrder, pendingKey)
		return economicCheckpointRetained
	}
	a.checkpointErr = errEconomicCheckpointPendingLimit
	if !a.checkpointCapacityDiagnosticPending && !a.checkpointCapacityDiagnosticEmitted {
		// Retain only bounded enum/numeric context for one coalesced diagnostic.
		// The source identity and observation payload never enter the log path.
		a.checkpointCapacityDiagnosticPending = true
		a.checkpointCapacityDiagnosticSemantics = canonical.Semantics
		a.checkpointCapacityDiagnosticRevision = canonical.Revision
	}
	return economicCheckpointRejected
}

// takeEconomicCheckpointCapacityDiagnostic claims the one coalesced capacity
// rejection signal for this attempt. The caller emits it through the existing
// runtime diagnostic logger, outside checkpointMu, so receive-side admission
// never performs logging while holding checkpoint state.
func (a *attemptSession) takeEconomicCheckpointCapacityDiagnostic() (economicCheckpointCapacityDiagnostic, bool) {
	if a == nil {
		return economicCheckpointCapacityDiagnostic{}, false
	}
	a.checkpointMu.Lock()
	defer a.checkpointMu.Unlock()
	if !a.checkpointCapacityDiagnosticPending || a.checkpointCapacityDiagnosticEmitted {
		return economicCheckpointCapacityDiagnostic{}, false
	}
	a.checkpointCapacityDiagnosticPending = false
	a.checkpointCapacityDiagnosticEmitted = true
	return economicCheckpointCapacityDiagnostic{
		semantics: a.checkpointCapacityDiagnosticSemantics,
		revision:  a.checkpointCapacityDiagnosticRevision,
	}, true
}

func canonicalEconomicCheckpoint(observation metering.Observation) (metering.Observation, string, error) {
	canonical, err := observation.Canonical()
	if err != nil {
		return metering.Observation{}, "", fmt.Errorf("economic checkpoint observation: %w", err)
	}
	hash, err := canonical.ReplayFingerprint()
	if err != nil || strings.TrimSpace(hash) == "" {
		if err == nil {
			err = errors.New("empty replay fingerprint")
		}
		return metering.Observation{}, "", fmt.Errorf("economic checkpoint fingerprint: %w", err)
	}
	return canonical, hash, nil
}

// versionLocalBoundaryObservations turns the boundary accumulator's repeated
// revision-one snapshots into immutable cumulative revisions. Observation
// timestamps describe capture time and therefore do not, by themselves,
// advance a local measurement revision.
func (a *attemptSession) versionLocalBoundaryObservations(observations []metering.Observation) []metering.Observation {
	if a == nil || len(observations) == 0 {
		return nil
	}
	if a.observationSink == nil {
		return observations
	}
	// Boundary snapshots can be requested by receive and terminal paths. Keep
	// revision selection and queue admission in one attempt-owned sequence so a
	// failed admission cannot publish a local head that has no retry owner.
	a.checkpointLocalMu.Lock()
	defer a.checkpointLocalMu.Unlock()

	versioned := make([]metering.Observation, 0, len(observations))
	for _, observation := range observations {
		canonical, _, err := canonicalEconomicCheckpoint(observation)
		if err != nil {
			continue
		}
		pendingKey := economicCheckpointPendingKey(canonical)
		measurementHash, err := localBoundaryMeasurementFingerprint(canonical)
		if err != nil {
			continue
		}
		a.checkpointMu.Lock()
		prior, hasPrior := a.localCheckpointHeads[pendingKey]
		priorHash, hasPriorHash := a.localCheckpointHashes[pendingKey]
		a.checkpointMu.Unlock()
		if hasPriorHash && priorHash == measurementHash {
			if hasPrior {
				versioned = append(versioned, prior.Clone())
			}
			continue
		}
		if hasPrior && canonical.Revision <= prior.Revision {
			canonical.Revision = prior.Revision + 1
		}
		if a.queueEconomicCheckpoint(canonical) != economicCheckpointRetained {
			// Keep the previous local head visible until the new measurement has
			// queue ownership. The bounded deferred queue normally makes this
			// path reachable only when both retry queues are exhausted.
			if hasPrior {
				versioned = append(versioned, prior.Clone())
			} else {
				versioned = append(versioned, canonical.Clone())
			}
			continue
		}
		a.checkpointMu.Lock()
		if a.localCheckpointHeads == nil {
			a.localCheckpointHeads = make(map[string]metering.Observation)
		}
		if a.localCheckpointHashes == nil {
			a.localCheckpointHashes = make(map[string]string)
		}
		a.localCheckpointHeads[pendingKey] = canonical.Clone()
		a.localCheckpointHashes[pendingKey] = measurementHash
		a.checkpointMu.Unlock()
		versioned = append(versioned, canonical.Clone())
	}
	return versioned
}

func localBoundaryMeasurementFingerprint(observation metering.Observation) (string, error) {
	canonical, err := observation.Canonical()
	if err != nil {
		return "", err
	}
	canonical.Revision = 1
	canonical.ObservedAt = time.Unix(1, 0).UTC()
	canonical.ReceivedAt = canonical.ObservedAt
	return canonical.ReplayFingerprint()
}

func economicCheckpointPendingKey(observation metering.Observation) string {
	identity := observation.SourceEventIdentity()
	if observation.Semantics != metering.SemanticsCumulative {
		return identity
	}
	// IdentityKey places revision in the final component. Cumulative snapshots
	// may replace a pending revision from the same source. Replacement and
	// correction revisions retain their full identity because their supersession
	// graph must remain durable even when both revisions arrive in one flush.
	if idx := strings.LastIndexByte(identity, 0); idx >= 0 {
		return identity[:idx]
	}
	return identity
}

func (a *attemptSession) shouldFlushEconomicCheckpoints(now time.Time, force bool) bool {
	if a == nil || a.observationSink == nil {
		return false
	}
	a.checkpointMu.Lock()
	defer a.checkpointMu.Unlock()
	pendingCount := len(a.checkpointPending) + len(a.checkpointDeferred)
	if pendingCount == 0 {
		return false
	}
	if force || a.checkpointLastFlush.IsZero() || pendingCount >= preTerminalEconomicCheckpointBatch {
		return true
	}
	return !now.Before(a.checkpointLastFlush.Add(preTerminalEconomicCheckpointInterval))
}

func (a *attemptSession) economicCheckpointNow() time.Time {
	if a != nil && a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

// flushEconomicCheckpoints appends immutable observations only. It never calls
// billing/authority code and can therefore be used around receive callbacks.
// A non-forced flush is cadence/size gated; terminal callers pass force=true.
// The sink must implement metering.AtomicObservationSink: retaining the queue
// after an error is safe only when the complete batch is atomic and idempotent.
func (a *attemptSession) flushEconomicCheckpoints(ctx context.Context, force bool) error {
	if a == nil || a.observationSink == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		a.noteEconomicCheckpointError(err)
		return err
	}
	// Snapshot and append are one serialized operation. Without this gate a
	// receive-side flush and terminal flush could both observe the same pending
	// head and append it twice before either removed it from the queue.
	a.checkpointFlushMu.Lock()
	defer a.checkpointFlushMu.Unlock()
	if err := ctx.Err(); err != nil {
		a.noteEconomicCheckpointError(err)
		return err
	}
	now := a.economicCheckpointNow()
	if !a.shouldFlushEconomicCheckpoints(now, force) {
		return nil
	}

	a.checkpointMu.Lock()
	writes := make([]economicCheckpointWrite, 0, len(a.checkpointPending)+len(a.checkpointDeferred))
	for _, pendingKey := range a.checkpointOrder {
		observation, ok := a.checkpointPending[pendingKey]
		if !ok {
			continue
		}
		canonical, hash, err := canonicalEconomicCheckpoint(observation)
		if err != nil {
			a.checkpointMu.Unlock()
			a.noteEconomicCheckpointError(err)
			return err
		}
		writes = append(writes, economicCheckpointWrite{pendingKey: pendingKey, hash: hash, observation: canonical})
	}
	for _, pendingKey := range a.checkpointDeferredOrder {
		observation, ok := a.checkpointDeferred[pendingKey]
		if !ok {
			continue
		}
		canonical, hash, err := canonicalEconomicCheckpoint(observation)
		if err != nil {
			a.checkpointMu.Unlock()
			a.noteEconomicCheckpointError(err)
			return err
		}
		writes = append(writes, economicCheckpointWrite{pendingKey: pendingKey, hash: hash, deferred: true, observation: canonical})
	}
	a.checkpointMu.Unlock()

	if len(writes) == 0 {
		a.checkpointMu.Lock()
		a.removeStaleEconomicCheckpointsLocked()
		a.checkpointLastFlush = now
		a.checkpointErr = nil
		a.checkpointMu.Unlock()
		return nil
	}

	observations := make([]metering.Observation, 0, len(writes))
	for _, write := range writes {
		observations = append(observations, write.observation)
	}
	batchSink, ok := a.observationSink.(metering.AtomicObservationSink)
	if !ok {
		err := errEconomicCheckpointAtomicBatchRequired
		a.noteEconomicCheckpointError(err)
		return err
	}
	if err := batchSink.AppendObservations(ctx, observations); err != nil {
		err = fmt.Errorf("runtime: economic checkpoint append batch: %w", err)
		a.noteEconomicCheckpointError(err)
		return err
	}

	a.checkpointMu.Lock()
	for _, write := range writes {
		current, ok := a.checkpointPending[write.pendingKey]
		if write.deferred {
			current, ok = a.checkpointDeferred[write.pendingKey]
		}
		if !ok {
			continue
		}
		currentHash, hashErr := current.ReplayFingerprint()
		if hashErr == nil && currentHash == write.hash {
			if write.deferred {
				delete(a.checkpointDeferred, write.pendingKey)
			} else {
				delete(a.checkpointPending, write.pendingKey)
			}
		}
	}
	a.recordDurableEconomicHeadsLocked(writes)
	a.removeStaleEconomicCheckpointsLocked()
	a.checkpointLastFlush = now
	a.checkpointErr = nil
	a.checkpointMu.Unlock()
	return nil
}

func (a *attemptSession) removeStaleEconomicCheckpointsLocked() {
	if a == nil {
		return
	}
	if len(a.checkpointPending) == 0 {
		a.checkpointOrder = nil
	} else {
		order := a.checkpointOrder[:0]
		for _, pendingKey := range a.checkpointOrder {
			if _, ok := a.checkpointPending[pendingKey]; ok {
				order = append(order, pendingKey)
			}
		}
		a.checkpointOrder = order
	}
	if len(a.checkpointDeferred) == 0 {
		a.checkpointDeferredOrder = nil
	} else {
		order := a.checkpointDeferredOrder[:0]
		for _, pendingKey := range a.checkpointDeferredOrder {
			if _, ok := a.checkpointDeferred[pendingKey]; ok {
				order = append(order, pendingKey)
			}
		}
		a.checkpointDeferredOrder = order
	}
}

func (a *attemptSession) recordDurableEconomicHeadsLocked(writes []economicCheckpointWrite) {
	if a == nil || len(writes) == 0 {
		return
	}
	if a.checkpointDurableHeads == nil {
		a.checkpointDurableHeads = make(map[string]checkpointDurableHead)
	}
	for _, write := range writes {
		if write.observation.Semantics != metering.SemanticsCumulative {
			continue
		}
		prior, exists := a.checkpointDurableHeads[write.pendingKey]
		if exists && prior.revision > write.observation.Revision {
			continue
		}
		if !exists && len(a.checkpointDurableHeads) >= maxPreTerminalEconomicCheckpointDurableHeads {
			// The terminal evidence bound is the lifetime cap for accepted
			// observations. Do not grow the revision fence beyond that cap.
			continue
		}
		a.checkpointDurableHeads[write.pendingKey] = checkpointDurableHead{revision: write.observation.Revision}
	}
}

func (a *attemptSession) noteEconomicCheckpointError(err error) {
	if a == nil || err == nil {
		return
	}
	a.checkpointMu.Lock()
	a.checkpointErr = err
	a.checkpointMu.Unlock()
}

func (a *attemptSession) flushEconomicCheckpointsAtTerminal(ctx context.Context) error {
	if a == nil || a.observationSink == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), economicCheckpointFlushTimeout)
	defer cancel()
	return a.flushEconomicCheckpoints(persistCtx, true)
}
