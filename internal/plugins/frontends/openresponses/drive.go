package openresponses

import (
	"context"
	"errors"
	"io"

	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	errDriveTooManyEvents     = errors.New("canonical response exceeded event limit")
	errDriveInvalidEnvelope   = errors.New("canonical response contained an invalid event")
	errDriveTooMuchText       = errors.New("canonical response exceeded text limit")
	errDriveTooMuchTool       = errors.New("canonical response exceeded tool argument limit")
	errDriveTooManyItems      = errors.New("canonical response exceeded item limit")
	errDriveNoTerminal        = errors.New("canonical response ended without terminal event")
	errDriveMultipleTerminal  = errors.New("canonical response contained multiple terminal events")
	errDriveSequenceInvalid   = errors.New("canonical response sequence was invalid")
	errDriveNilStream         = errors.New("nil canonical event stream")
	errDriveStreamCloseFailed = errors.New("canonical response stream close failed")
	errDriveBuildResource     = errors.New("failed to build response resource")
	errDriveBuildCompact      = errors.New("failed to build compact resource")
	errDriveCommittedWrite    = errors.New("openresponses: committed stream write failed")
	errDriveSessionWrite      = errors.New("openresponses: websocket session write failed")
)

type driveLimits struct {
	maxEvents    int
	maxItems     int
	maxTextBytes int
	maxToolBytes int
}

type driveOptions struct {
	limits              driveLimits
	stopOnTerminal      bool
	errorEventIsFailure bool
	checkContext        bool
	sanitizeError       bool
}

func collectDriveLimits() driveLimits {
	return driveLimits{
		maxEvents:    maxNonStreamingEvents,
		maxItems:     maxNonStreamingItems,
		maxTextBytes: maxNonStreamingTextBytes,
		maxToolBytes: maxNonStreamingToolBytes,
	}
}

func compactDriveLimits() driveLimits {
	return driveLimits{
		maxEvents:    maxCompactEvents,
		maxItems:     maxCompactItems,
		maxTextBytes: maxCompactTextBytes,
		maxToolBytes: maxCompactToolBytes,
	}
}

func streamingDriveLimits() driveLimits {
	return driveLimits{maxEvents: maxStreamingEvents}
}

func isCanonicalTerminal(ev lipapi.Event) bool {
	return ev.Kind == lipapi.EventError || ev.Kind == lipapi.EventResponseFinished
}

func driveStateMachine(
	ctx context.Context,
	stream lipapi.EventStream,
	sm *proto.StateMachine,
	opts driveOptions,
	onEvent func(lipapi.Event, []proto.StreamEvent) error,
) (lipapi.Event, error) {
	if stream == nil {
		return lipapi.Event{}, errDriveNilStream
	}
	var (
		textBytes    int
		toolBytes    int
		terminalSeen bool
		last         lipapi.Event
	)
	for eventCount := 0; eventCount < opts.limits.maxEvents; eventCount++ {
		if opts.checkContext {
			if err := ctx.Err(); err != nil {
				return last, err
			}
		}
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if terminalSeen && !opts.stopOnTerminal {
					return last, nil
				}
				if !terminalSeen && (opts.errorEventIsFailure || !opts.stopOnTerminal) {
					return last, errDriveNoTerminal
				}
			}
			return last, err
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return last, errDriveInvalidEnvelope
		}
		switch ev.Kind {
		case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
			if opts.limits.maxTextBytes > 0 {
				textBytes += len(ev.Delta)
				if textBytes > opts.limits.maxTextBytes {
					return last, errDriveTooMuchText
				}
			}
		case lipapi.EventToolCallArgsDelta:
			if opts.limits.maxToolBytes > 0 {
				toolBytes += len(ev.Delta)
				if toolBytes > opts.limits.maxToolBytes {
					return last, errDriveTooMuchTool
				}
			}
		}
		if ev.Kind == lipapi.EventError {
			if ev.ErrorCode == "" {
				ev.ErrorCode = "backend_error"
			}
			if opts.sanitizeError {
				ev.ErrorMessage = safeCanonicalErrorMessage(ev.ErrorMessage)
			}
		}
		events, processErr := sm.ProcessCanonicalEvent(ev)
		if processErr != nil {
			if opts.errorEventIsFailure {
				return last, errDriveSequenceInvalid
			}
			return last, processErr
		}
		if opts.limits.maxItems > 0 && len(sm.Trajectory()) > opts.limits.maxItems {
			return last, errDriveTooManyItems
		}
		if onEvent != nil {
			if err := onEvent(ev, events); err != nil {
				return ev, err
			}
		}
		last = ev
		if isCanonicalTerminal(ev) {
			if terminalSeen {
				return last, errDriveMultipleTerminal
			}
			terminalSeen = true
			if opts.errorEventIsFailure && ev.Kind == lipapi.EventError {
				return last, lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
			}
			if opts.stopOnTerminal {
				return last, nil
			}
		}
	}
	return last, errDriveTooManyEvents
}
