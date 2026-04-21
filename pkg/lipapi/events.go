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
	MaxTextBytes          int
	MaxReasoningBytes     int
	MaxToolArgsTotalBytes int
	MaxWarnings           int
}

// DefaultCollectLimits returns conservative defaults for Collect (non-streaming aggregation).
func DefaultCollectLimits() CollectLimits {
	return CollectLimits{
		MaxTextBytes:          64 << 20,
		MaxReasoningBytes:     64 << 20,
		MaxToolArgsTotalBytes: 128 << 20,
		MaxWarnings:           100_000,
	}
}

func toolArgsTotalBytes(m map[string]*strings.Builder) int {
	n := 0
	for _, b := range m {
		if b != nil {
			n += b.Len()
		}
	}
	return n
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
)

// Event is one canonical streaming item.
type Event struct {
	Kind EventKind

	MessageIndex int
	Delta        string
	ToolCallID   string
	ToolName     string

	InputTokens  int
	OutputTokens int

	WarningCode    string
	WarningMessage string

	ErrorCode    string
	ErrorMessage string
}

// ValidateEventEnvelope applies maximum sizes to canonical event string fields (codec output,
// backend mapping, or hook mutations) so one stream chunk cannot force unbounded allocations.
func ValidateEventEnvelope(ev *Event) error {
	if ev == nil {
		return fmt.Errorf("nil event")
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
	return nil
}

// EventStream is the primary execution result from backends and the executor.
// Implementations assume a single goroutine calls Recv until completion or error
// (no concurrent Recv on the same stream). Close may run concurrently with a
// blocked Recv only if the implementation documents that as safe.
type EventStream interface {
	Recv(ctx context.Context) (Event, error) // io.EOF means normal completion after terminal event
	Close() error
}

// FixedEventStream returns a finite stream for tests and in-memory adapters.
// It is not safe for concurrent Recv; use one consumer at a time.
func FixedEventStream(events []Event) EventStream {
	s := append([]Event(nil), events...)
	return &fixedEventStream{events: s}
}

type fixedEventStream struct {
	events []Event
	idx    int
}

func (f *fixedEventStream) Recv(ctx context.Context) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if f.idx >= len(f.events) {
		return Event{}, io.EOF
	}
	e := f.events[f.idx]
	f.idx++
	return e, nil
}

func (f *fixedEventStream) Close() error { return nil }

// Collected aggregates a canonical stream for non-streaming responses.
type Collected struct {
	Text      strings.Builder
	Reasoning strings.Builder
	ToolArgs  map[string]*strings.Builder // keyed by tool_call_id
	// ToolNames maps tool_call_id to the function name from EventToolCallStarted.
	ToolNames map[string]string
	// ToolCallOrder is the order tool_call_ids first appear (started or first args delta).
	ToolCallOrder  []string
	Warnings       []string
	InputTokens    int
	OutputTokens   int
	TerminalError  *Event
	FinishReceived bool
}

func toolCallSeenBefore(order []string, id string) bool {
	for _, x := range order {
		if x == id {
			return true
		}
	}
	return false
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
func Collect(ctx context.Context, s EventStream) (Collected, error) {
	return CollectWithLimits(ctx, s, DefaultCollectLimits())
}

// CollectUnbounded aggregates without CollectLimits checks (legacy / testing only).
func CollectUnbounded(ctx context.Context, s EventStream) (Collected, error) {
	return CollectWithLimits(ctx, s, CollectLimits{})
}

// CollectWithLimits drains a stream until a terminal event or an error.
// Terminal success is EventResponseFinished. Terminal failure is EventError followed by optional EOF.
func CollectWithLimits(ctx context.Context, s EventStream, limits CollectLimits) (Collected, error) {
	if s == nil {
		return Collected{}, ErrNilEventStream
	}
	defer func() { _ = s.Close() }()

	var out Collected
	out.ToolArgs = make(map[string]*strings.Builder)
	out.ToolNames = make(map[string]string)

	var sawResponseStarted bool
	var sawMessage bool

	for {
		ev, err := s.Recv(ctx)
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
		case EventTextDelta, EventReasoningDelta, EventToolCallStarted, EventToolCallArgsDelta, EventToolCallFinished:
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
			if !toolCallSeenBefore(out.ToolCallOrder, id) {
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
			if !toolCallSeenBefore(out.ToolCallOrder, id) {
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
			b := out.ToolArgs[id]
			if b == nil {
				nb := new(strings.Builder)
				out.ToolArgs[id] = nb
				b = nb
			}
			if limits.MaxToolArgsTotalBytes > 0 && toolArgsTotalBytes(out.ToolArgs)+len(ev.Delta) > limits.MaxToolArgsTotalBytes {
				return out, fmt.Errorf("%w: tool arguments aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxToolArgsTotalBytes)
			}
			b.WriteString(ev.Delta)
		case EventToolCallFinished:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallFinished)
			}
			id := ev.ToolCallID
			if !toolCallSeenBefore(out.ToolCallOrder, id) {
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
		}
	}
}

func terminalError(ev Event) error {
	return fmt.Errorf("stream error: %s: %s", ev.ErrorCode, ev.ErrorMessage)
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
		case EventTextDelta, EventReasoningDelta, EventToolCallStarted, EventToolCallArgsDelta, EventToolCallFinished:
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
