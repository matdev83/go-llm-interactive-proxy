package lipapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CollectLimits bounds memory growth while aggregating streaming events into Collected.
// A zero value disables all limits (CollectUnbounded). Individual fields use zero to mean
// “no limit” for that dimension only when using CollectWithLimits with a partially filled struct.
type CollectLimits struct {
	MaxTextBytes           int
	MaxReasoningBytes      int
	MaxToolArgsTotalBytes  int
	MaxWarnings            int
	MaxAssistantMediaParts int // assistant_image_ref / assistant_file_ref events aggregated into Collected.AssistantMedia; 0 = unlimited
}

// DefaultCollectLimits returns conservative defaults for Collect (non-streaming aggregation).
func DefaultCollectLimits() CollectLimits {
	return CollectLimits{
		MaxTextBytes:           64 << 20,
		MaxReasoningBytes:      64 << 20,
		MaxToolArgsTotalBytes:  128 << 20,
		MaxWarnings:            100_000,
		MaxAssistantMediaParts: 1024,
	}
}

// EventKind identifies canonical stream events.
type EventKind string

const (
	EventResponseStarted   EventKind = "response_started"
	EventMessageStarted    EventKind = "message_started"
	EventTextDelta         EventKind = "text_delta"
	EventReasoningDelta    EventKind = "reasoning_delta"
	EventToolCallStarted   EventKind = "tool_call_started"
	EventToolCallArgsDelta EventKind = "tool_call_args_delta"
	EventToolCallFinished  EventKind = "tool_call_finished"
	EventUsageDelta        EventKind = "usage_delta"
	EventWarning           EventKind = "warning"
	EventError             EventKind = "error"
	EventResponseFinished  EventKind = "response_finished"

	// Assistant-side multimodal references (streaming). Adapters emit these instead of
	// overloading text_delta when the vendor returns image/file output items.
	EventAssistantImageRef EventKind = "assistant_image_ref"
	EventAssistantFileRef  EventKind = "assistant_file_ref"
)

// Event is one canonical streaming item.
type Event struct {
	Kind EventKind

	MessageIndex int
	Delta        string
	ToolCallID   string
	ToolName     string

	// Usage fields apply to EventUsageDelta. InputTokens and OutputTokens are
	// retained as the compatibility totals used by existing frontends.
	InputTokens  int
	OutputTokens int
	// CacheReadTokens are submitted/input tokens served from a provider cache.
	CacheReadTokens int
	// CacheWriteTokens are submitted/input tokens written to a provider cache.
	CacheWriteTokens int
	// ReasoningTokens are response/output tokens used for hidden reasoning or
	// thinking when the provider reports them separately.
	ReasoningTokens int
	// TotalTokens is the provider-reported total when available.
	TotalTokens int
	// CostNanoUnits is a high-precision per-response cost amount in Currency.
	// One whole currency unit is 1e9 nano-units.
	CostNanoUnits int64
	Currency      string
	CostSource    string
	// RawUsageJSON stores bounded provider usage metadata for audit/backfill.
	RawUsageJSON string

	WarningCode    string
	WarningMessage string

	ErrorCode    string
	ErrorMessage string

	// FinishReason is optional metadata on EventResponseFinished (vendor stop/finish taxonomy).
	FinishReason string

	// AssistantRef / AssistantMIME / AssistantName apply to EventAssistantImageRef and
	// EventAssistantFileRef (same meaning as Part.ImageRef / Part.FileRef fields).
	AssistantRef  string
	AssistantMIME string
	AssistantName string
}

// ValidateEventEnvelope applies maximum sizes to canonical event string fields (codec output,
// backend mapping, or hook mutations) so one stream chunk cannot force unbounded allocations.
func ValidateEventEnvelope(ev *Event) error {
	if ev == nil {
		return &ValidationError{Field: "Event", Message: "nil event"}
	}
	if len(ev.Delta) > MaxEventDeltaBytes {
		return &ValidationError{Field: "Delta", Message: fmt.Sprintf("exceeds %d bytes", MaxEventDeltaBytes)}
	}
	if len(ev.ToolCallID) > MaxRefStringBytes {
		return &ValidationError{Field: "ToolCallID", Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	if len(ev.ToolName) > MaxRefStringBytes {
		return &ValidationError{Field: "ToolName", Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	if len(ev.WarningCode) > MaxEventCodeFieldBytes {
		return &ValidationError{Field: "WarningCode", Message: fmt.Sprintf("exceeds %d bytes", MaxEventCodeFieldBytes)}
	}
	if len(ev.WarningMessage) > MaxEventDiagMessageBytes {
		return &ValidationError{Field: "WarningMessage", Message: fmt.Sprintf("exceeds %d bytes", MaxEventDiagMessageBytes)}
	}
	if len(ev.ErrorCode) > MaxEventCodeFieldBytes {
		return &ValidationError{Field: "ErrorCode", Message: fmt.Sprintf("exceeds %d bytes", MaxEventCodeFieldBytes)}
	}
	if len(ev.ErrorMessage) > MaxEventDiagMessageBytes {
		return &ValidationError{Field: "ErrorMessage", Message: fmt.Sprintf("exceeds %d bytes", MaxEventDiagMessageBytes)}
	}
	if err := validateStringField("FinishReason", ev.FinishReason, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("AssistantRef", ev.AssistantRef, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("AssistantMIME", ev.AssistantMIME, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("AssistantName", ev.AssistantName, MaxRefStringBytes); err != nil {
		return err
	}
	switch ev.Kind {
	case EventAssistantImageRef, EventAssistantFileRef:
		if strings.TrimSpace(ev.AssistantRef) == "" {
			return &ValidationError{Field: "AssistantRef", Message: "required for assistant media ref events"}
		}
	case EventResponseFinished:
		// FinishReason optional
	default:
		// Other kinds ignore assistant/finish fields at envelope validation time.
	}
	return nil
}

// EventStream is the primary execution result from backends and the executor.
// Implementations assume a single goroutine calls Recv until completion or error
// (no concurrent Recv on the same stream). Close may run concurrently with a
// blocked Recv only when the implementation documents that guarantee locally.
// See each concrete stream type for its Close/Recv concurrency contract.
//
// Cancellation: Recv should respect ctx when the underlying source supports it
// (for example blocking on a channel that also selects on ctx.Done). Some vendor
// SDKs block without consulting ctx; in those cases Recv may remain blocked until
// Close unblocks the SDK. Callers that cancel ctx must still call Close (typically
// via defer right after obtaining the stream) so blocked reads and background work
// tear down promptly.
//
// Recv requires a non-nil ctx (Go context contract). Passing nil returns [ErrNilContext]
// from core and reference implementations in this module; other implementations should
// follow the same rule or document divergent behavior.
type EventStream interface {
	Recv(ctx context.Context) (Event, error) // io.EOF means normal completion after terminal event
	Close() error
}

// FixedEventStream is a finite stream for tests and in-memory adapters.
// It is not safe for concurrent Recv; use one consumer at a time.
// Recv on a nil *FixedEventStream returns [ErrNilFixedEventStream]. Close on nil returns nil.
type FixedEventStream struct {
	events []Event
	pos    int
}

// NewFixedEventStream returns a copied finite stream for tests and in-memory adapters.
func NewFixedEventStream(events []Event) *FixedEventStream {
	s := append([]Event(nil), events...)
	return &FixedEventStream{events: s}
}

func (f *FixedEventStream) Recv(ctx context.Context) (Event, error) {
	if f == nil {
		return Event{}, ErrNilFixedEventStream
	}
	if ctx == nil {
		return Event{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if f.pos >= len(f.events) {
		return Event{}, io.EOF
	}
	e := f.events[f.pos]
	f.pos++
	return e, nil
}

func (f *FixedEventStream) Close() error { return nil }

// Collected aggregates a canonical stream for non-streaming responses.
type Collected struct {
	Text      strings.Builder
	Reasoning strings.Builder
	ToolArgs  map[string]*strings.Builder // keyed by tool_call_id
	// ToolNames maps tool_call_id to the function name from EventToolCallStarted.
	ToolNames map[string]string
	// ToolCallOrder is the order tool_call_ids first appear (started or first args delta).
	ToolCallOrder    []string
	Warnings         []string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	TerminalError    *Event
	FinishReceived   bool

	// FinishReason is copied from the terminal EventResponseFinished when set.
	FinishReason string
	// AssistantMedia collects EventAssistantImageRef / EventAssistantFileRef in order.
	AssistantMedia []Part
}

// ToolCallSummary is one completed tool invocation aggregated from the stream.
type ToolCallSummary struct {
	ID        string
	Name      string
	Arguments string
}

// OrderedToolCalls returns tool calls in first-seen order for stable encoding.
func (c Collected) OrderedToolCalls() []ToolCallSummary {
	if len(c.ToolCallOrder) == 0 {
		return nil
	}
	out := make([]ToolCallSummary, 0, len(c.ToolCallOrder))
	for _, id := range c.ToolCallOrder {
		name := ""
		if c.ToolNames != nil {
			name = c.ToolNames[id]
		}
		args := ""
		if b := c.ToolArgs[id]; b != nil {
			args = b.String()
		}
		out = append(out, ToolCallSummary{ID: id, Name: name, Arguments: args})
	}
	return out
}

// Collect drains a stream until a terminal event or an error using DefaultCollectLimits.
// Terminal success is EventResponseFinished. Terminal failure is EventError followed by optional EOF.
// ctx must be non-nil; nil returns [ErrNilContext].
func Collect(ctx context.Context, s EventStream) (Collected, error) {
	return CollectWithLimits(ctx, s, DefaultCollectLimits())
}

// CollectUnbounded aggregates without CollectLimits checks (legacy / testing only).
// ctx must be non-nil; nil returns [ErrNilContext].
func CollectUnbounded(ctx context.Context, s EventStream) (Collected, error) {
	return CollectWithLimits(ctx, s, CollectLimits{})
}

// CollectWithLimits drains a stream until a terminal event or an error.
// Terminal success is EventResponseFinished. Terminal failure is EventError followed by optional EOF.
// ctx must be non-nil; nil returns [ErrNilContext].
func CollectWithLimits(ctx context.Context, s EventStream, limits CollectLimits) (out Collected, err error) {
	if ctx == nil {
		return Collected{}, ErrNilContext
	}
	if s == nil {
		return Collected{}, ErrNilEventStream
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			closeErr := fmt.Errorf("lipapi: close event stream: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()

	out = Collected{}
	out.ToolArgs = make(map[string]*strings.Builder)
	out.ToolNames = make(map[string]string)
	seenTool := make(map[string]struct{})
	var toolArgBytes int

	var sawResponseStarted bool
	var sawMessage bool

	var ev Event
	for {
		ev, err = s.Recv(ctx)
		if errors.Is(err, io.EOF) {
			if out.FinishReceived {
				return out, nil
			}
			if out.TerminalError != nil {
				return out, terminalError(*out.TerminalError)
			}
			return out, fmt.Errorf("stream ended without terminal event")
		}
		if err != nil {
			return out, err
		}

		switch ev.Kind {
		case EventResponseStarted:
			if sawResponseStarted {
				return out, fmt.Errorf("duplicate %s", EventResponseStarted)
			}
			sawResponseStarted = true
		case EventMessageStarted:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", EventMessageStarted, EventResponseStarted)
			}
			sawMessage = true
		case EventTextDelta, EventReasoningDelta, EventToolCallStarted, EventToolCallArgsDelta, EventToolCallFinished,
			EventAssistantImageRef, EventAssistantFileRef:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			if !sawMessage {
				return out, fmt.Errorf("%s before %s", ev.Kind, EventMessageStarted)
			}
		case EventUsageDelta, EventWarning:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			// Usage and warnings may appear without a message frame on some adapters.
		case EventError:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", EventError, EventResponseStarted)
			}
			cp := ev
			out.TerminalError = &cp
			return out, terminalError(ev)
		case EventResponseFinished:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", EventResponseFinished, EventResponseStarted)
			}
			out.FinishReceived = true
			out.FinishReason = ev.FinishReason
			return out, nil
		default:
			return out, fmt.Errorf("unknown event kind %q", ev.Kind)
		}

		switch ev.Kind {
		case EventTextDelta:
			if limits.MaxTextBytes > 0 && out.Text.Len()+len(ev.Delta) > limits.MaxTextBytes {
				return out, fmt.Errorf("%w: text aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxTextBytes)
			}
			out.Text.WriteString(ev.Delta)
		case EventReasoningDelta:
			if limits.MaxReasoningBytes > 0 && out.Reasoning.Len()+len(ev.Delta) > limits.MaxReasoningBytes {
				return out, fmt.Errorf("%w: reasoning aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxReasoningBytes)
			}
			out.Reasoning.WriteString(ev.Delta)
		case EventToolCallStarted:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallStarted)
			}
			id := ev.ToolCallID
			if _, ok := seenTool[id]; !ok {
				seenTool[id] = struct{}{}
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
			if strings.TrimSpace(ev.ToolName) != "" {
				out.ToolNames[id] = ev.ToolName
			}
		case EventToolCallArgsDelta:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallArgsDelta)
			}
			id := ev.ToolCallID
			if _, ok := seenTool[id]; !ok {
				seenTool[id] = struct{}{}
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
			b := out.ToolArgs[id]
			if b == nil {
				nb := new(strings.Builder)
				out.ToolArgs[id] = nb
				b = nb
			}
			if limits.MaxToolArgsTotalBytes > 0 && toolArgBytes+len(ev.Delta) > limits.MaxToolArgsTotalBytes {
				return out, fmt.Errorf("%w: tool arguments aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxToolArgsTotalBytes)
			}
			toolArgBytes += len(ev.Delta)
			b.WriteString(ev.Delta)
		case EventToolCallFinished:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallFinished)
			}
			id := ev.ToolCallID
			if _, ok := seenTool[id]; !ok {
				seenTool[id] = struct{}{}
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
		case EventWarning:
			if limits.MaxWarnings > 0 && len(out.Warnings) >= limits.MaxWarnings {
				return out, fmt.Errorf("%w: warning count would exceed %d", ErrCollectLimitExceeded, limits.MaxWarnings)
			}
			out.Warnings = append(out.Warnings, ev.WarningMessage)
		case EventUsageDelta:
			out.InputTokens += ev.InputTokens
			out.OutputTokens += ev.OutputTokens
			out.CacheReadTokens += ev.CacheReadTokens
			out.CacheWriteTokens += ev.CacheWriteTokens
			out.ReasoningTokens += ev.ReasoningTokens
			out.TotalTokens = mergeTotalTokens(out.TotalTokens, ev.TotalTokens)
		case EventAssistantImageRef:
			if limits.MaxAssistantMediaParts > 0 && len(out.AssistantMedia) >= limits.MaxAssistantMediaParts {
				return out, fmt.Errorf("%w: assistant media parts would exceed %d", ErrCollectLimitExceeded, limits.MaxAssistantMediaParts)
			}
			p := Part{Kind: PartImageRef, ImageRef: ev.AssistantRef, ImageMIME: ev.AssistantMIME}
			if err := p.validate(); err != nil {
				return out, fmt.Errorf("assistant_image_ref: %w", err)
			}
			out.AssistantMedia = append(out.AssistantMedia, p)
		case EventAssistantFileRef:
			if limits.MaxAssistantMediaParts > 0 && len(out.AssistantMedia) >= limits.MaxAssistantMediaParts {
				return out, fmt.Errorf("%w: assistant media parts would exceed %d", ErrCollectLimitExceeded, limits.MaxAssistantMediaParts)
			}
			p := Part{Kind: PartFileRef, FileRef: ev.AssistantRef, FileMIME: ev.AssistantMIME, FileName: ev.AssistantName}
			if err := p.validate(); err != nil {
				return out, fmt.Errorf("assistant_file_ref: %w", err)
			}
			out.AssistantMedia = append(out.AssistantMedia, p)
		}
	}
}

func mergeTotalTokens(cur, next int) int {
	if next == 0 {
		return cur
	}
	return next
}

func terminalError(ev Event) error {
	return NewStreamError(ev.ErrorCode, ev.ErrorMessage)
}

// ValidateEventSequence checks ordering rules for a replayed event slice.
// A well-formed sequence ends with EventResponseFinished or EventError after EventResponseStarted.
func ValidateEventSequence(events []Event) error {
	var sawResponseStarted bool
	var sawMessage bool

	for _, ev := range events {
		switch ev.Kind {
		case EventResponseStarted:
			if sawResponseStarted {
				return fmt.Errorf("duplicate %s", EventResponseStarted)
			}
			sawResponseStarted = true
		case EventMessageStarted:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", EventMessageStarted, EventResponseStarted)
			}
			sawMessage = true
		case EventTextDelta, EventReasoningDelta, EventToolCallStarted, EventToolCallArgsDelta, EventToolCallFinished,
			EventAssistantImageRef, EventAssistantFileRef:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			if !sawMessage {
				return fmt.Errorf("%s before %s", ev.Kind, EventMessageStarted)
			}
		case EventUsageDelta, EventWarning:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
		case EventError:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", EventError, EventResponseStarted)
			}
			return nil
		case EventResponseFinished:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", EventResponseFinished, EventResponseStarted)
			}
			return nil
		default:
			return fmt.Errorf("unknown event kind %q", ev.Kind)
		}
	}
	return fmt.Errorf("missing terminal event")
}
