package reasoningpreservation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

type StreamObserverFactory struct {
	cfg            Config
	store          TurnStore
	tel            *Telemetry
	id             string
	order          int
	lastDiag       atomic.Value
	postAppendHook PostAppendHook
}

func NewStreamObserverFactory(cfg Config, store TurnStore, tel ...*Telemetry) *StreamObserverFactory {
	return NewStreamObserverFactoryWithPostAppendHook(cfg, store, nil, tel...)
}

// NewStreamObserverFactoryWithPostAppendHook creates a factory with an immutable post-append hook.
// The hook is assigned once at construction and never mutated thereafter, avoiding data races.
// When compression is disabled the hook must be nil.
func NewStreamObserverFactoryWithPostAppendHook(cfg Config, store TurnStore, hook PostAppendHook, tel ...*Telemetry) *StreamObserverFactory {
	var t *Telemetry
	if len(tel) > 0 {
		t = tel[0]
	}
	if t == nil {
		t = NewTelemetry()
	}
	f := &StreamObserverFactory{
		cfg:            cfg,
		store:          store,
		tel:            t,
		id:             ID + "-observer",
		order:          0,
		postAppendHook: hook,
	}
	f.lastDiag.Store("")
	return f
}

func (f *StreamObserverFactory) ID() string { return f.id }
func (f *StreamObserverFactory) Order() int { return f.order }
func (f *StreamObserverFactory) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (f *StreamObserverFactory) LastSafeDiagnostic() string {
	if v, ok := f.lastDiag.Load().(string); ok {
		return v
	}
	return ""
}

func (f *StreamObserverFactory) recordOutcome(outcome SafeOutcome, counts map[string]int) {
	if f.tel != nil {
		f.tel.Record(outcome, counts)
	}
	diag, err := FormatSafeDiagnostic(outcome, "", counts)
	if err != nil {
		diag, _ = ProjectSafeError(err)
	}
	f.lastDiag.Store(diag)
}

func (f *StreamObserverFactory) Open(_ context.Context, meta response.StreamMeta, _ response.Services) (response.StreamObserver, error) {
	if f.store == nil {
		return nil, fmt.Errorf("%s: store is required", ID)
	}
	meta.BackendPrefixes = slices.Clone(meta.BackendPrefixes)
	match, err := ResolveMatch(f.cfg, CandidateIdentity{
		BackendID:       meta.BackendID,
		BackendPrefixes: meta.BackendPrefixes,
		Model:           meta.Model,
	})
	if err != nil || !MatchEligible(match.Kind) {
		return inertStreamObserver{}, nil
	}
	return &streamObserver{
		factory: f,
		cfg:     f.cfg,
		store:   f.store,
		meta:    meta,
		now:     time.Now,
	}, nil
}

type streamObserver struct {
	factory *StreamObserverFactory
	cfg     Config
	store   TurnStore
	meta    response.StreamMeta
	now     func() time.Time

	mu             sync.Mutex
	finished       bool
	oversized      bool
	parts          []lipapi.Part
	curText        string
	curReason      *lipapi.ReasoningPart
	reasoningBytes int
	toolID         string
	toolName       string
	toolArgs       strings.Builder
	toolOpen       bool
}

func (o *streamObserver) Observe(_ context.Context, ev lipapi.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished || o.oversized {
		return nil
	}
	switch ev.Kind {
	case lipapi.EventTextDelta:
		o.flushToolLocked()
		o.flushReasoningLocked()
		o.curText += ev.Delta
	case lipapi.EventReasoningDelta:
		o.flushToolLocked()
		o.flushTextLocked()
		if o.curReason == nil {
			o.curReason = &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
			}
		}
		o.curReason.Text += ev.Delta
		o.reasoningBytes += len(ev.Delta)
	case lipapi.EventReasoningSignatureDelta:
		o.flushToolLocked()
		o.flushTextLocked()
		if o.curReason == nil {
			o.curReason = &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectAnthropicThinkingV1,
			}
		}
		o.curReason.Signature += ev.Signature
		if o.curReason.Dialect == lipapi.ReasoningDialectOpenAIChatTextV1 {
			o.curReason.Dialect = lipapi.ReasoningDialectAnthropicThinkingV1
		}
		o.reasoningBytes += len(ev.Signature)
	case lipapi.EventReasoningOpaqueDelta:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		if len(ev.Opaque) == 0 {
			break
		}
		opaque := append(json.RawMessage(nil), ev.Opaque...)
		o.parts = append(o.parts, lipapi.Part{
			Kind: lipapi.PartReasoning,
			Reasoning: &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectAnthropicRedactedThinkingV1,
				Opaque:  opaque,
			},
		})
		o.reasoningBytes += len(opaque)
	case lipapi.EventReasoningPart:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		if err := o.captureExactReasoningPartLocked(ev.Reasoning); err != nil {
			return err
		}
	case lipapi.EventToolCallStarted:
		o.flushTextLocked()
		o.flushReasoningLocked()
		o.flushToolLocked()
		o.toolOpen = true
		o.toolID = ev.ToolCallID
		o.toolName = ev.ToolName
		o.toolArgs.Reset()
	case lipapi.EventToolCallArgsDelta:
		if !o.toolOpen {
			o.toolOpen = true
			o.toolID = ev.ToolCallID
			o.toolName = ev.ToolName
		}
		o.toolArgs.WriteString(ev.Delta)
	case lipapi.EventToolCallFinished:
		o.flushTextLocked()
		o.flushReasoningLocked()
		if ev.ToolCallID != "" {
			o.toolID = ev.ToolCallID
		}
		if ev.ToolName != "" {
			o.toolName = ev.ToolName
		}
		if ev.Delta != "" && o.toolArgs.Len() == 0 {
			o.toolArgs.WriteString(ev.Delta)
		}
		o.flushToolLocked()
	case lipapi.EventAssistantImageRef:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		o.parts = append(o.parts, lipapi.Part{
			Kind:      lipapi.PartImageRef,
			ImageRef:  ev.AssistantRef,
			ImageMIME: ev.AssistantMIME,
		})
	case lipapi.EventAssistantFileRef:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		o.parts = append(o.parts, lipapi.Part{
			Kind:     lipapi.PartFileRef,
			FileRef:  ev.AssistantRef,
			FileMIME: ev.AssistantMIME,
			FileName: ev.AssistantName,
		})
	}
	if o.cfg.State.MaxReasoningBytesPerTurn > 0 && o.reasoningBytes > o.cfg.State.MaxReasoningBytesPerTurn {
		o.markOversizeLocked()
	}
	return nil
}

func (o *streamObserver) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		return nil
	}
	o.finished = true
	if outcome != response.OutcomeSuccessReleased {
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	if !o.captureEligible() {
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	if o.oversized {
		o.factory.recordOutcome(OutcomeOversize, map[string]int{"bytes": o.reasoningBytes})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	o.flushTextLocked()
	o.flushReasoningLocked()
	o.flushToolLocked()
	placed, _, err := DerivePlacementsFromParts(o.parts)
	if err != nil || len(placed) == 0 {
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	partition, ok := sessionPartitionOrMiss(o.meta.Session.AuthoritativeSessionID)
	if !ok {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	msg := lipapi.Message{Role: lipapi.RoleAssistant, Parts: o.parts}
	anchor, err := ComputeAnchor(msg)
	if err != nil {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	reasoningBytes := 0
	for _, p := range placed {
		reasoningBytes = int(lipapi.SaturatingAddInt64(int64(reasoningBytes), int64(lipapi.ReasoningPayloadBytes(p.Part.Reasoning))))
	}
	if reasoningBytes <= 0 || reasoningBytes > o.cfg.State.MaxReasoningBytesPerTurn {
		o.factory.recordOutcome(OutcomeOversize, map[string]int{"bytes": reasoningBytes})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	art := TurnArtifact{
		ID:             nextArtifactID(),
		Anchor:         anchor,
		SourceBackend:  o.meta.BackendID,
		SourceModel:    o.meta.Model,
		Reasoning:      placed,
		CreatedAt:      o.now().UTC(),
		ReasoningBytes: reasoningBytes,
	}
	sum, err := o.store.Append(ctx, partition, art)
	if err != nil {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	if sum.EvictedTurns > 0 || sum.ExpiredTurns > 0 || sum.EvictedBytes > 0 || sum.ExpiredBytes > 0 {
		o.factory.recordOutcome(OutcomeEvicted, map[string]int{
			"count": sum.EvictedTurns + sum.ExpiredTurns,
			"bytes": sum.EvictedBytes + sum.ExpiredBytes,
		})
	}
	o.factory.recordOutcome(OutcomeObserved, map[string]int{"count": 1, "bytes": reasoningBytes})
	var hook PostAppendHook
	var corr PostAppendCorrelation
	var hasCorr bool
	if o.factory.cfg.Compression.Enabled && o.factory.postAppendHook != nil {
		if segs := ExtractSemanticSegments(art.Reasoning); len(segs) > 0 {
			semDigest := computeSemanticDigest(art.Reasoning)
			egressHash := computeEgressPolicyRefHash(o.factory.cfg.Compression.EgressPolicyRef)
			corr = PostAppendCorrelation{
				Partition:           partition,
				ArtifactID:          art.ID,
				Anchor:              anchor,
				OriginalDigest:      anchor,
				SemanticDigest:      semDigest,
				EgressPolicyRefHash: egressHash,
				TraceID:             o.meta.TraceID,
				ALegID:              o.meta.ALegID,
				BLegID:              o.meta.BLegID,
				BranchBinding:       o.meta.CandidateKey,
				Scope:               o.meta.Scope.Clone(),
				PolicyRevision:      o.factory.cfg.Compression.EgressPolicyRef,
			}
			hook = o.factory.postAppendHook
			hasCorr = true
		}
	}
	o.clearPendingLocked()
	o.mu.Unlock()
	if hasCorr && hook != nil {
		_ = hook(ctx, corr)
	}
	return nil
}

func (o *streamObserver) captureEligible() bool {
	match, err := ResolveMatch(o.cfg, CandidateIdentity{
		BackendID:       o.meta.BackendID,
		BackendPrefixes: o.meta.BackendPrefixes,
		Model:           o.meta.Model,
	})
	if err != nil {
		return false
	}
	return MatchEligible(match.Kind)
}

func (o *streamObserver) markOversizeLocked() {
	o.oversized = true
	o.clearPendingLocked()
}

func (o *streamObserver) clearPendingLocked() {
	o.parts = nil
	o.curText = ""
	o.curReason = nil
	o.toolOpen = false
	o.toolID = ""
	o.toolName = ""
	o.toolArgs.Reset()
}

func (o *streamObserver) captureExactReasoningPartLocked(src *lipapi.ReasoningPart) error {
	if src == nil {
		return nil
	}
	cloned := &lipapi.ReasoningPart{
		Dialect:   src.Dialect,
		Text:      src.Text,
		Signature: src.Signature,
	}
	if src.Opaque != nil {
		cloned.Opaque = append(json.RawMessage(nil), src.Opaque...)
	}
	probe := lipapi.Event{Kind: lipapi.EventReasoningPart, Reasoning: cloned}
	if err := lipapi.ValidateEventEnvelope(&probe); err != nil {
		return nil
	}
	add := lipapi.ReasoningPayloadBytes(cloned)
	if add <= 0 {
		return nil
	}
	next := int(lipapi.SaturatingAddInt64(int64(o.reasoningBytes), int64(add)))
	o.reasoningBytes = next
	if o.cfg.State.MaxReasoningBytesPerTurn > 0 && next > o.cfg.State.MaxReasoningBytesPerTurn {
		o.markOversizeLocked()
		return nil
	}
	o.parts = append(o.parts, lipapi.Part{
		Kind:      lipapi.PartReasoning,
		Reasoning: cloned,
	})
	return nil
}

func (o *streamObserver) flushTextLocked() {
	if o.curText == "" {
		return
	}
	o.parts = append(o.parts, lipapi.TextPart(o.curText))
	o.curText = ""
}

func (o *streamObserver) flushReasoningLocked() {
	if o.curReason == nil {
		return
	}
	if o.curReason.Text == "" && o.curReason.Signature == "" && len(o.curReason.Opaque) == 0 {
		o.curReason = nil
		return
	}
	o.parts = append(o.parts, clonePart(lipapi.Part{
		Kind:      lipapi.PartReasoning,
		Reasoning: o.curReason,
	}))
	o.curReason = nil
}

func (o *streamObserver) flushToolLocked() {
	if !o.toolOpen && o.toolArgs.Len() == 0 && o.toolID == "" {
		return
	}
	content := []byte(o.toolArgs.String())
	if len(content) == 0 {
		content = []byte("{}")
	}
	o.parts = append(o.parts, lipapi.Part{
		Kind:       lipapi.PartJSON,
		ToolCallID: o.toolID,
		ToolName:   o.toolName,
		Content:    content,
	})
	o.toolOpen = false
	o.toolID = ""
	o.toolName = ""
	o.toolArgs.Reset()
}

var artifactSeq atomic.Uint64

func nextArtifactID() string {
	return fmt.Sprintf("rp-%d", artifactSeq.Add(1))
}
