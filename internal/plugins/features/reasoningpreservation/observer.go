package reasoningpreservation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

type StreamObserverFactory struct {
	cfg      Config
	store    TurnStore
	id       string
	order    int
	lastDiag atomic.Value
}

func NewStreamObserverFactory(cfg Config, store TurnStore) *StreamObserverFactory {
	f := &StreamObserverFactory{
		cfg:   cfg,
		store: store,
		id:    ID + "-observer",
		order: 0,
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
	if o.reasoningBytes > o.cfg.State.MaxReasoningBytesPerTurn {
		o.markOversizeLocked()
	}
	return nil
}

func (o *streamObserver) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return nil
	}
	o.finished = true
	if outcome != response.OutcomeSuccessReleased {
		o.clearPendingLocked()
		return nil
	}
	if o.oversized {
		o.factory.recordOutcome(OutcomeOversize, map[string]int{"bytes": o.reasoningBytes})
		o.clearPendingLocked()
		return nil
	}
	o.flushTextLocked()
	o.flushReasoningLocked()
	o.flushToolLocked()
	placed, _, err := DerivePlacementsFromParts(o.parts)
	if err != nil || len(placed) == 0 {
		o.clearPendingLocked()
		return nil
	}
	partition, ok := sessionPartitionOrMiss(o.meta.Session.AuthoritativeSessionID)
	if !ok {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		return nil
	}
	msg := lipapi.Message{Role: lipapi.RoleAssistant, Parts: o.parts}
	anchor, err := ComputeAnchor(msg)
	if err != nil {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		return nil
	}
	reasoningBytes := 0
	for _, p := range placed {
		reasoningBytes += lipapi.ReasoningPayloadBytes(p.Part.Reasoning)
	}
	if reasoningBytes <= 0 || reasoningBytes > o.cfg.State.MaxReasoningBytesPerTurn {
		o.factory.recordOutcome(OutcomeOversize, map[string]int{"bytes": reasoningBytes})
		o.clearPendingLocked()
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
	if _, err := o.store.Append(ctx, partition, art); err != nil {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		return nil
	}
	o.factory.recordOutcome(OutcomeObserved, map[string]int{"count": 1, "bytes": reasoningBytes})
	o.clearPendingLocked()
	return nil
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
	o.parts = append(o.parts, lipapi.Part{
		Kind:      lipapi.PartReasoning,
		Reasoning: o.curReason,
	})
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
