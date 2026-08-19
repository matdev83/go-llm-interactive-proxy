package compactioncontinuity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/carriers"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/injection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/observability"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/source"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// BeforeRequest consumes only pure detector metadata. It may prepare a
// non-billable intent and inject an already available capsule, but it never
// submits fresh auxiliary work before the primary request opens.
func (p *Plugin) BeforeRequest(ctx context.Context, call *lipapi.Call, preview compaction.RequestPreview, meta compaction.PreservationMeta, services compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			p.observeFailure(observability.StageCallback, observability.OutcomePanic, meta.TransactionID, "")
			err = nil
			return
		}
		if err != nil {
			p.observeError(observability.StageCallback, err, meta.TransactionID)
		}
	}()
	previewOutcome := observability.OutcomeObserved
	if preview.Kind == compaction.PreviewCompletionCandidate {
		previewOutcome = observability.OutcomeCandidate
	}
	p.observe(observability.Observation{Stage: observability.StagePreview, Outcome: previewOutcome, Evidence: observability.BoundedID(string(preview.Evidence)), RuleID: observability.BoundedID(preview.RuleID), CorrelationHash: observability.HashID(previewBoundary(preview))})
	if p == nil || p.parent == nil || ctx == nil || call == nil || preview.Kind != compaction.PreviewCompletionCandidate {
		p.observeFailure(observability.StagePreview, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	cfg, enabled := p.effectiveConfig(ctx)
	if !enabled {
		p.observeFailure(observability.StageEligibility, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	parent, err := p.parent.Capture(ctx, *call, meta)
	if err != nil || !validParentBranch(parent) {
		p.observeError(observability.StageCallback, err, preview.TransactionID)
		return nil
	}
	boundary := previewBoundary(preview)
	if boundary == "" {
		p.observeFailure(observability.StagePreview, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		p.observeError(observability.StagePreviewIntent, err, boundary)
		return nil
	}
	intent := PreviewIntent{Key: previewIntentKey(parent.Binding, boundary, state.Revision), TargetSourceRevision: state.Revision}
	if _, err = p.parent.RecordPreviewIntent(ctx, parent, intent); err != nil {
		p.observeError(observability.StagePreviewIntent, err, boundary)
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StagePreviewIntent, Outcome: observability.OutcomeCreated, CorrelationHash: observability.HashID(boundary), Revision: state.Revision})
	previous, window, err := p.previousState(parent, state)
	if err != nil {
		p.observeError(observability.StageCapsule, err, boundary)
		return nil
	}
	prepared, err := source.Prepare(ctx, source.Input{
		Call: *call, Existing: window, Previous: window.HighWatermark,
		Recognizer: carrierRecognizer{}, Config: source.Config{MaxBytes: stateBound(cfg.Source.MaxBytes, source.DefaultConfig().MaxBytes)},
	})
	if err != nil {
		return nil
	}
	watermark := encodeWatermark(prepared.HighWatermark)
	state, err = p.parent.CommitSource(ctx, parent, state.Revision, []byte(prepared.Envelope.Canonical()), watermark)
	if err != nil {
		p.observeError(observability.StageCapsule, err, boundary)
		return nil
	}
	previous, state, err = p.applyPreviewDeterministic(ctx, parent, state, previous, prepared, watermark, cfg)
	if err != nil {
		p.observeError(observability.StageCapsule, err, boundary)
		return nil
	}
	if strings.TrimSpace(string(state.PendingJobID)) != "" && services.BackgroundAux != nil {
		state, _ = p.consumePending(ctx, parent, state, services, cfg)
		if len(state.CapsuleJSON) != 0 {
			previous, _, err = p.previousState(parent, state)
			if err != nil {
				p.observeError(observability.StageCapsule, err, boundary)
				return nil
			}
		}
	}
	if previous.BranchBinding == "" {
		p.observeFailure(observability.StageEligibility, observability.OutcomeIneligible, boundary, "")
		return nil
	}
	target := InjectionTarget{BoundaryKey: boundary, CapsuleRevision: previous.Revision}
	if state.PendingInjection != nil {
		if _, err = p.parent.ValidateInjection(ctx, parent, *state.PendingInjection); err != nil {
			p.observeError(observability.StageReinjection, err, boundary)
			return nil
		}
	}
	injected, err := injection.Inject(injection.Input{
		Call: *call, Capsule: previous, ExpectedBranchBinding: parent.Binding,
		BoundaryKey: boundary, Limits: injection.ProjectionLimits{MaxBytes: cfg.Capsule.MaxBytes, MaxTokens: cfg.Capsule.MaxTokens},
		Marker: p.preparedMarker(meta, parent.Binding, InjectionTarget{BoundaryKey: boundary, CapsuleRevision: previous.Revision}),
	})
	if err != nil {
		p.observeFailure(observability.StageAugmentation, observability.OutcomeRollback, boundary, "")
		return nil
	}
	if _, err = p.parent.SetPendingInjection(ctx, parent, target); err != nil {
		p.observeFailure(observability.StageReinjection, observability.OutcomeRollback, boundary, "")
		return err
	}
	p.observe(observability.Observation{Stage: observability.StageAugmentation, Outcome: observability.OutcomeCompleted, CorrelationHash: observability.HashID(boundary), Revision: previous.Revision})
	p.observe(observability.Observation{Stage: observability.StageReinjection, Outcome: observability.OutcomeCreated, CorrelationHash: observability.HashID(boundary), Revision: previous.Revision})
	p.recordPreparedMarker(meta, target)
	*call = injected.Call
	return nil
}

func previewBoundary(preview compaction.RequestPreview) string {
	if value := strings.TrimSpace(preview.BoundaryFingerprint); value != "" {
		return value
	}
	return strings.TrimSpace(preview.TransactionID)
}

func previewIntentKey(binding, boundary string, revision uint64) string {
	h := sha256.New()
	_, _ = h.Write([]byte("lip.compaction-continuity.preview.v1\x00"))
	for _, value := range []string{strings.TrimSpace(binding), strings.TrimSpace(boundary), fmt.Sprint(revision)} {
		_, _ = fmt.Fprintf(h, "%d:", len(value))
		_, _ = h.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (p *Plugin) applyPreviewDeterministic(ctx context.Context, parent ParentBranch, state ParentState, previous capsule.Envelope, prepared source.Prepared, watermark string, cfg Config) (capsule.Envelope, ParentState, error) {
	dirty := len(state.CapsuleJSON) == 0
	if previous.BranchBinding == "" && prepared.Candidate {
		var err error
		previous, err = capsule.New(parent.Binding)
		if err != nil {
			return capsule.Envelope{}, ParentState{}, err
		}
		dirty = true
	}
	for _, entry := range prepared.NewEntries {
		if entry.Carrier == nil || !cfg.Preserve.Plan {
			continue
		}
		update, matched, err := extractCarrierUpdate(*entry.Carrier)
		if err != nil || !matched {
			outcome := observability.OutcomeUnmatched
			if err != nil {
				outcome = observability.OutcomeInvalid
			}
			p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: outcome, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(watermark)})
			continue
		}
		p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: observability.OutcomeMatched, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(watermark)})
		previous, err = carriers.Apply(previous, update)
		if err != nil {
			return capsule.Envelope{}, ParentState{}, err
		}
		dirty = true
	}
	if !dirty {
		return previous, state, nil
	}
	previous, err := capsule.PruneWithLimits(previous, capsule.Limits{MaxBytes: cfg.Capsule.MaxBytes, MaxTokens: cfg.Capsule.MaxTokens})
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	serialized, err := capsule.Encode(previous)
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	digest, err := digestArray(previous.ContentDigest)
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	state, err = p.parent.CommitCapsule(ctx, parent, state.Revision, serialized, digest, watermark)
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	p.observeCapsule(observability.OutcomeCommitted, previous, len(serialized), watermark)
	return previous, state, nil
}

func (p *Plugin) consumePending(ctx context.Context, parent ParentBranch, state ParentState, services compaction.Services, cfg Config) (ParentState, error) {
	previous, window, err := p.previousState(parent, state)
	if err != nil {
		return state, err
	}
	decoder := resultmerge.NewExtractorDecoder(resultmerge.ExtractorDecoderConfig{AllowedSourceRefs: sourceRefs(previous, window)})
	service, err := resultmerge.New(services.BackgroundAux, parentCoordinator{ctx: ctx, port: p.parent, parent: parent}, decoder, resultmerge.Config{MaxCapsuleBytes: cfg.Capsule.MaxBytes, MaxCapsuleTokens: cfg.Capsule.MaxTokens})
	if err != nil {
		p.observeError(observability.StageBarrier, err, string(state.PendingJobID))
		return state, err
	}
	barrierCtx, cancel := context.WithTimeout(ctx, cfg.Barrier.Timeout)
	defer cancel()
	started := time.Now()
	outcome, consumeErr := service.Consume(barrierCtx, resultmerge.Job{ID: state.PendingJobID, ParentBranchBinding: parent.Binding})
	barrierOutcome := observability.OutcomeCompleted
	switch outcome.Status {
	case resultmerge.StatusPending:
		barrierOutcome = observability.OutcomePending
	case resultmerge.StatusRejected:
		barrierOutcome = observability.OutcomeRejected
	}
	if consumeErr != nil {
		p.observeErrorDuration(observability.StageBarrier, consumeErr, string(state.PendingJobID), time.Since(started))
	} else {
		p.observe(observability.Observation{Stage: observability.StageBarrier, Outcome: barrierOutcome, CorrelationHash: observability.HashID(string(state.PendingJobID)), Duration: time.Since(started)})
	}
	if outcome.State.Revision != 0 {
		state.Revision = outcome.State.Revision
		state.CapsuleJSON = append([]byte(nil), outcome.State.CapsuleJSON...)
		state.CapsuleDigest = outcome.State.CapsuleDigest
		state.SourceHighWatermark = outcome.State.SourceHighWatermark
		state.PendingJobID = outcome.State.PendingJobID
		state.PendingJobTargetRevision = outcome.State.PendingJobTargetRevision
		state.PendingJobBranchBinding = outcome.State.PendingJobBranchBinding
		if len(state.CapsuleJSON) != 0 {
			if decoded, decodeErr := capsule.Decode(state.CapsuleJSON); decodeErr == nil {
				p.observeCapsule(barrierOutcome, decoded, len(state.CapsuleJSON), string(state.PendingJobID))
			}
		}
	}
	return state, consumeErr
}
