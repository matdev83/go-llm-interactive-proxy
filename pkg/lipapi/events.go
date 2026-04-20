package lipapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

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

// EventStream is the primary execution result from backends and the executor.
type EventStream interface {
	Recv(ctx context.Context) (Event, error) // io.EOF means normal completion after terminal event
	Close() error
}

// FixedEventStream returns a finite stream for tests and in-memory adapters.
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
	Text           strings.Builder
	Reasoning      strings.Builder
	ToolArgs       map[string]*strings.Builder // keyed by tool_call_id
	Warnings       []string
	InputTokens    int
	OutputTokens   int
	TerminalError  *Event
	FinishReceived bool
}

// Collect drains a stream until a terminal event or an error.
// Terminal success is EventResponseFinished. Terminal failure is EventError followed by optional EOF.
func Collect(ctx context.Context, s EventStream) (Collected, error) {
	defer func() { _ = s.Close() }()

	var out Collected
	out.ToolArgs = make(map[string]*strings.Builder)

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
			out.Text.WriteString(ev.Delta)
		case EventReasoningDelta:
			out.Reasoning.WriteString(ev.Delta)
		case EventToolCallArgsDelta:
			b := out.ToolArgs[ev.ToolCallID]
			if b == nil {
				nb := new(strings.Builder)
				out.ToolArgs[ev.ToolCallID] = nb
				b = nb
			}
			b.WriteString(ev.Delta)
		case EventWarning:
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
