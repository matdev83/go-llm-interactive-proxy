package compactioncontinuity

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/carriers"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/extractor"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/source"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// ParentBranch is the bounded authority captured before any detached child is
// created. Binding is the only value used as a continuity lookup key. The
// remaining fields are correlation metadata for the detached request and are
// never included in its prompt.
type ParentBranch struct {
	Binding string
	TraceID string
	ALegID  string
	BLegID  string
}

// ParentState is an opaque defensive snapshot of one authoritative parent.
// CapsuleJSON and SourceJSON are feature-owned serialized values; a runtime
// adapter remains responsible for persistence, bounds, and compare-and-swap.
type ParentState struct {
	Revision                  uint64
	CapsuleJSON               []byte
	CapsuleDigest             [32]byte
	SourceJSON                []byte
	SourceHighWatermark       string
	PendingJobID              auxiliary.JobID
	PendingJobTargetRevision  uint64
	PendingJobBranchBinding   string
	PendingPreviewIntent      *PreviewIntent
	PendingInjection          *InjectionTarget
	LastReleasedInjection     *InjectionWatermark
	LastCompactionTransaction string
}

// ParentPort is the smallest authority/state seam consumed by this feature.
// Capture must derive the parent from trusted scope in ctx plus PreservationMeta;
// it must not select a branch from child A-leg or untrusted call hints.
// ValidatePendingJob is the per-parent late-result access guard used by later
// response-boundary work; RequestOpened itself never awaits a result.
type ParentPort interface {
	Capture(context.Context, lipapi.Call, compaction.PreservationMeta) (ParentBranch, error)
	CaptureMeta(context.Context, compaction.PreservationMeta) (ParentBranch, error)
	Snapshot(context.Context, ParentBranch) (ParentState, error)
	CommitSource(context.Context, ParentBranch, uint64, []byte, string) (ParentState, error)
	CommitCapsule(context.Context, ParentBranch, uint64, []byte, [32]byte, string) (ParentState, error)
	RecordPendingJob(context.Context, ParentBranch, auxiliary.JobID, uint64) (ParentState, error)
	ValidatePendingJob(context.Context, ParentBranch, auxiliary.JobID) (ParentState, error)
	CommitCapsuleForJob(context.Context, ParentBranch, auxiliary.JobID, string, uint64, []byte, [32]byte, string) (ParentState, error)
	RecordPreviewIntent(context.Context, ParentBranch, PreviewIntent) (ParentState, error)
	BindPreviewIntent(context.Context, ParentBranch, string, string) (ParentState, error)
	SetPendingInjection(context.Context, ParentBranch, InjectionTarget) (ParentState, error)
	ValidateInjection(context.Context, ParentBranch, InjectionTarget) (ParentState, error)
	CommitReleasedInjection(context.Context, ParentBranch, InjectionWatermark) (ParentState, error)
}

// Plugin is the feature-owned successful-Open preserver. It has no global
// state; config is normalized once and the parent port is explicitly bound.
type Plugin struct {
	cfg      Config
	parent   ParentPort
	markerMu sync.Mutex
	markers  map[string]preparedInjectionMarker
}

// New constructs one immutable-generation feature instance.
func New(cfg Config, parent ParentPort) (*Plugin, error) {
	if parent == nil {
		return nil, errors.New("compaction-continuity: parent port is required")
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return &Plugin{cfg: normalized.Snapshot(), parent: parent, markers: make(map[string]preparedInjectionMarker)}, nil
}

// FeatureBundleWithPort contributes the feature callback after runtime
// composition has supplied the process-owned parent authority port.
func FeatureBundleWithPort(cfg Config, parent ParentPort) (lipfeature.FeatureBundle, error) {
	p, err := New(cfg, parent)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:        lipfeature.SchemaVersionV1,
		CompactionPreservers: []compaction.Preserver{p},
	}, nil
}

// FeatureBundle retains the registry's configuration-only compatibility
// surface. Runtime composition must use FeatureBundleWithPort to enable work.
func FeatureBundle(_ Config) lipfeature.FeatureBundle {
	return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
}

func (p *Plugin) ID() string { return ID }

// RequestOpened schedules at most one detached extraction for one committed
// detector transaction. All failures are deliberately feature-local and
// fail-open because the primary request is already open.
func (p *Plugin) RequestOpened(ctx context.Context, call lipapi.Call, events []compaction.Event, meta compaction.PreservationMeta, services compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	if p == nil || p.parent == nil || ctx == nil || len(events) == 0 {
		return nil
	}
	if services.BackgroundAux == nil {
		return nil
	}
	event, ok := committedEvent(events)
	if !ok {
		return nil
	}

	parent, err := p.parent.Capture(ctx, call, meta)
	if err != nil || strings.TrimSpace(parent.Binding) == "" {
		return nil
	}
	if strings.TrimSpace(parent.TraceID) == "" {
		parent.TraceID = strings.TrimSpace(meta.TraceID)
	}
	if strings.TrimSpace(parent.ALegID) == "" {
		parent.ALegID = strings.TrimSpace(meta.ALegID)
	}
	if strings.TrimSpace(parent.BLegID) == "" {
		parent.BLegID = strings.TrimSpace(event.BLegID)
	}
	if !validParentBranch(parent) {
		return nil
	}

	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		return nil
	}
	previewBound := false
	if state.PendingPreviewIntent != nil {
		bound, bindErr := p.parent.BindPreviewIntent(ctx, parent, state.PendingPreviewIntent.Key, event.TransactionID)
		if bindErr == nil {
			state = bound
			previewBound = true
		}
	}
	previous, sourceWindow, err := p.previousState(parent, state)
	if err != nil {
		return nil
	}

	prepared, err := source.Prepare(ctx, source.Input{
		Call:       call,
		Existing:   sourceWindow,
		Previous:   sourceWindow.HighWatermark,
		Recognizer: carrierRecognizer{},
		Config: source.Config{
			MaxBytes: stateBound(p.cfg.Source.MaxBytes, source.DefaultConfig().MaxBytes),
		},
	})
	if err != nil {
		return nil
	}

	// Source/high-watermark commit is intentionally before deterministic plan
	// and semantic eligibility. The source is canonical JSON, bounded by the
	// source package, and contains no parent identifiers or raw tool dumps.
	watermark := encodeWatermark(prepared.HighWatermark)
	state, err = p.parent.CommitSource(ctx, parent, state.Revision, []byte(prepared.Envelope.Canonical()), watermark)
	if err != nil {
		return nil
	}

	deterministicCount := 0
	capsuleDirty := len(state.CapsuleJSON) == 0
	if len(previous.BranchBinding) == 0 {
		previous, err = capsule.New(parent.Binding)
		if err != nil {
			return nil
		}
		capsuleDirty = true
	}
	for _, entry := range prepared.NewEntries {
		if entry.Carrier == nil || !p.cfg.Preserve.Plan {
			continue
		}
		update, matched, extractErr := extractCarrierUpdate(*entry.Carrier)
		if extractErr != nil || !matched {
			continue
		}
		previous, err = carriers.Apply(previous, update)
		if err != nil {
			return nil
		}
		deterministicCount++
		capsuleDirty = true
	}

	if capsuleDirty {
		previous, err = capsule.PruneWithLimits(previous, capsule.Limits{
			MaxBytes:  p.cfg.Capsule.MaxBytes,
			MaxTokens: p.cfg.Capsule.MaxTokens,
		})
		if err != nil {
			return nil
		}
		serialized, encodeErr := capsule.Encode(previous)
		if encodeErr != nil {
			return nil
		}
		digest, digestErr := digestArray(previous.ContentDigest)
		if digestErr != nil {
			return nil
		}
		state, err = p.parent.CommitCapsule(ctx, parent, state.Revision, serialized, digest, watermark)
		if err != nil {
			return nil
		}
	}
	if previewBound && state.PendingInjection != nil && state.PendingInjection.BoundaryKey != event.TransactionID && injectionContainsProjection(call, previous, parent.Binding, p.cfg) {
		_, _ = p.parent.SetPendingInjection(ctx, parent, InjectionTarget{BoundaryKey: event.TransactionID, CapsuleRevision: state.PendingInjection.CapsuleRevision})
		p.rebindPreparedMarker(meta, event.TransactionID, state.PendingInjection.CapsuleRevision)
	}

	// A plan carrier is sufficient only when the operator requested plan-only
	// preservation. User decisions, constraints, rationale, or rejected
	// alternatives remain semantic categories and keep the model eligible.
	if !semanticExtractionEligible(p.cfg, prepared.Candidate || previewBound, deterministicCount > 0) || state.PendingJobID != "" {
		return nil
	}
	coalesceKey := coalesceKey(parent.Binding, event.TransactionID, watermark)
	input := extractor.Input{
		Route:               p.cfg.Extractor.Route,
		Inherit:             p.cfg.Extractor.Inherit,
		InheritedRoute:      call.Route.Selector,
		ParentBranchBinding: parent.Binding,
		ParentTraceID:       parent.TraceID,
		ParentALegID:        parent.ALegID,
		ParentBLegID:        parent.BLegID,
		Previous:            previous,
		SanitizedDelta:      prepared.NewEntries,
		SourceHighWatermark: watermark,
		MaxInputTokens:      p.cfg.Extractor.MaxInputTokens,
		MaxOutputTokens:     p.cfg.Extractor.MaxOutputTokens,
		Timeout:             p.cfg.Extractor.Timeout,
	}
	jobID, err := extractor.Submit(ctx, services.BackgroundAux, input, coalesceKey)
	if err != nil {
		if strings.TrimSpace(string(jobID)) != "" {
			services.BackgroundAux.Forget(jobID)
		}
		return nil
	}
	if strings.TrimSpace(string(jobID)) == "" {
		return nil
	}
	if _, err = p.parent.RecordPendingJob(ctx, parent, jobID, state.Revision); err != nil {
		// Submit has already crossed the accounting boundary. Forgetting only
		// releases unusable retained raw output; it does not cancel child usage.
		services.BackgroundAux.Forget(jobID)
	}
	return nil
}
