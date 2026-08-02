package continuation

import (
	"context"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// StreamRecorder observes canonical events and attempts one terminal write.
// Storage failures are deliberately retained as diagnostics only; they never
// become stream errors and therefore cannot trigger retry after commitment.
type StreamRecorder struct {
	mu         sync.Mutex
	recorder   lipcont.Recorder
	record     lipcont.ContinuationRecord
	events     []lipapi.Event
	eventBytes int64
	closed     bool
	stored     bool
	overflow   bool
	storeErr   error
	cleanup    func()
}

// NewStreamRecorder creates an observer for one reserved response record.
func NewStreamRecorder(recorder lipcont.Recorder, record lipcont.ContinuationRecord, cleanup func()) *StreamRecorder {
	if cleanup == nil {
		panic("continuation: stream recorder cleanup callback is required")
	}
	return &StreamRecorder{
		recorder:   recorder,
		record:     lipcont.CloneRecord(record),
		cleanup:    cleanup,
		eventBytes: lipcont.EstimateItemsBytes(record.InputItems),
	}
}

// Observe records a defensive copy of each event and stores only after the
// canonical terminal event. Non-terminal failures and cancellation are never
// eligible for persistence.
func (r *StreamRecorder) Observe(ctx context.Context, event lipapi.Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed || r.stored || r.overflow {
		r.mu.Unlock()
		return
	}
	if max := r.record.Policy.Limits.MaxRecordBytes; max > 0 && r.eventBytes+recorderEventSize(event) > max {
		r.overflow = true
		r.stored = true
		record := lipcont.CloneRecord(r.record)
		record.Terminal = true
		record.Status = lipcont.RecordStatusFailed
		recorder := r.recorder
		release := r.cleanup
		r.mu.Unlock()
		if release != nil {
			// The cleanup callback owns reservation release for overflow. Do
			// not also call RecordTerminal: the standard TerminalRecorder
			// treats failed records as ineligible and would delete the same
			// reservation a second time.
			release()
			return
		}
		// Keep the recorder fallback for callers that intentionally provide no
		// cleanup callback and use a recorder that owns failed-record cleanup.
		if recorder != nil {
			if err := recorder.RecordTerminal(ctx, record); err != nil {
				r.mu.Lock()
				r.storeErr = err
				r.mu.Unlock()
			}
		}
		return
	}
	r.events = append(r.events, cloneEvent(event))
	r.eventBytes += recorderEventSize(event)
	if event.Kind != lipapi.EventResponseFinished && event.Kind != lipapi.EventError {
		r.mu.Unlock()
		return
	}
	record := lipcont.CloneRecord(r.record)
	record.Terminal = true
	if event.Kind == lipapi.EventError {
		record.Status = lipcont.RecordStatusFailed
	} else if event.FinishReason != "" && event.FinishReason != "stop" && event.FinishReason != "end_turn" {
		record.Status = lipcont.RecordStatusIncomplete
	} else {
		record.Status = lipcont.RecordStatusCompleted
	}
	record.OutputItems = outputItems(r.events)
	record.MaterializedBytes = lipcont.EstimateItemsBytes(record.InputItems) + lipcont.EstimateItemsBytes(record.OutputItems)
	recorder := r.recorder
	r.stored = true
	r.mu.Unlock()
	if recorder != nil {
		if err := recorder.RecordTerminal(ctx, record); err != nil {
			r.mu.Lock()
			r.storeErr = err
			r.mu.Unlock()
		}
	}
}

func recorderEventSize(event lipapi.Event) int64 {
	size := int64(len(event.Delta) + len(event.Signature) + len(event.Opaque))
	if event.Reasoning != nil {
		size += int64(len(event.Reasoning.Text) + len(event.Reasoning.Signature) + len(event.Reasoning.Opaque))
	}
	return size
}

// Close is idempotent and does not turn a partial/cancelled stream into a
// stored continuation record.
func (r *StreamRecorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	stored := r.stored
	release := r.cleanup
	if !stored {
		// Consume the callback while holding the lock so a concurrent terminal
		// observation or repeated Close cannot release the same reservation.
		r.cleanup = nil
	}
	r.mu.Unlock()
	if !stored && release != nil {
		release()
	}
	return nil
}

// StorageError exposes a best-effort persistence failure for diagnostics/tests.
func (r *StreamRecorder) StorageError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storeErr
}

func outputItems(events []lipapi.Event) []lipapi.Item {
	items := make([]lipapi.Item, 0, 2)
	var text strings.Builder
	var reasoningActive bool
	var reasoningText strings.Builder
	var reasoningSig strings.Builder
	var reasoningOpaque []byte
	toolCalls := make(map[string]*lipapi.ToolCallItem)
	toolOrder := make([]string, 0)
	var activeToolID string

	flushText := func() {
		if text.Len() == 0 {
			return
		}
		items = append(items, lipapi.Item{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Status: lipapi.ItemStatusCompleted, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text.String()}}})
		text.Reset()
	}
	flushReasoning := func() {
		if !reasoningActive {
			return
		}
		if reasoningText.Len() == 0 && reasoningSig.Len() == 0 && len(reasoningOpaque) == 0 {
			reasoningActive = false
			return
		}
		items = append(items, lipapi.Item{Kind: lipapi.ItemKindReasoning, Status: lipapi.ItemStatusCompleted, Reasoning: &lipapi.ReasoningItem{Reasoning: &lipapi.ReasoningPart{
			Text:      reasoningText.String(),
			Signature: reasoningSig.String(),
			Opaque:    append([]byte(nil), reasoningOpaque...),
		}}})
		reasoningText.Reset()
		reasoningSig.Reset()
		reasoningOpaque = nil
		reasoningActive = false
	}

	for _, event := range events {
		switch event.Kind {
		case lipapi.EventTextDelta:
			flushReasoning()
			text.WriteString(event.Delta)
		case lipapi.EventReasoningDelta:
			flushText()
			reasoningActive = true
			reasoningText.WriteString(event.Delta)
		case lipapi.EventReasoningSignatureDelta, lipapi.EventReasoningOpaqueDelta:
			flushText()
			reasoningActive = true
			if event.Kind == lipapi.EventReasoningSignatureDelta {
				reasoningSig.WriteString(event.Signature)
			} else {
				reasoningOpaque = append(reasoningOpaque, event.Opaque...)
			}
		case lipapi.EventReasoningPart:
			flushText()
			flushReasoning()
			if event.Reasoning != nil {
				reasoningActive = true
				reasoningText.WriteString(event.Reasoning.Text)
				reasoningSig.WriteString(event.Reasoning.Signature)
				reasoningOpaque = append([]byte(nil), event.Reasoning.Opaque...)
			}
		case lipapi.EventToolCallStarted:
			flushText()
			flushReasoning()
			id := event.ToolCallID
			if id != "" {
				activeToolID = id
				if _, exists := toolCalls[id]; !exists {
					toolCalls[id] = &lipapi.ToolCallItem{CallID: id, Name: event.ToolName}
					toolOrder = append(toolOrder, id)
				} else if event.ToolName != "" {
					toolCalls[id].Name = event.ToolName
				}
			}
		case lipapi.EventToolCallArgsDelta:
			id := event.ToolCallID
			if id == "" {
				id = activeToolID
			}
			if id != "" {
				tc, exists := toolCalls[id]
				if !exists {
					tc = &lipapi.ToolCallItem{CallID: id}
					toolCalls[id] = tc
					toolOrder = append(toolOrder, id)
				}
				tc.Arguments = append(tc.Arguments, []byte(event.Delta)...)
			}
		case lipapi.EventToolCallFinished:
			flushText()
			flushReasoning()
			id := event.ToolCallID
			if id == "" {
				id = activeToolID
			}
			if id != "" {
				if tc, ok := toolCalls[id]; ok {
					items = append(items, lipapi.Item{
						Kind:     lipapi.ItemKindToolCall,
						Status:   lipapi.ItemStatusCompleted,
						ToolCall: tc,
					})
					delete(toolCalls, id)
				}
			}
		}
	}
	flushText()
	flushReasoning()
	for _, id := range toolOrder {
		if tc, ok := toolCalls[id]; ok {
			items = append(items, lipapi.Item{
				Kind:     lipapi.ItemKindToolCall,
				Status:   lipapi.ItemStatusCompleted,
				ToolCall: tc,
			})
		}
	}
	return items
}

func cloneEvent(in lipapi.Event) lipapi.Event {
	out := in
	out.Opaque = append([]byte(nil), in.Opaque...)
	if in.Reasoning != nil {
		copy := *in.Reasoning
		copy.Opaque = append([]byte(nil), in.Reasoning.Opaque...)
		out.Reasoning = &copy
	}
	out.UsageScopes = append([]lipapi.ScopedUsageDelta(nil), in.UsageScopes...)
	return out
}
