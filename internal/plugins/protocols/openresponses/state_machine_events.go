package openresponses

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ProcessCanonicalEvent processes one canonical lipapi.Event, validates sequence/lifecycle rules,
// updates accumulated state, and returns zero or more OpenResponses StreamEvents.
func (sm *StateMachine) ProcessCanonicalEvent(ev lipapi.Event) (events []StreamEvent, err error) {
	snap := sm.takeSnapshot()
	defer func() {
		if err != nil {
			sm.restoreSnapshot(snap)
		}
		sm.tx = nil
	}()

	if sm.state == StateTerminal {
		return nil, &SequenceError{
			Code:     "output_after_terminal",
			Event:    string(ev.Kind),
			Sequence: sm.sequenceNum,
			Message:  "event received after terminal state",
			Err:      ErrOutputAfterTerminal,
		}
	}

	if sm.state == StateInit && ev.Kind != lipapi.EventResponseStarted {
		return nil, &SequenceError{
			Code:     "missing_start",
			Event:    string(ev.Kind),
			Sequence: sm.sequenceNum,
			Message:  "response not started before event",
			Err:      ErrInvalidLifecycleState,
		}
	}

	if err := ValidateEventCount(sm.eventCount+1, sm.limits); err != nil {
		return nil, err
	}
	sm.eventCount++

	switch ev.Kind {
	case lipapi.EventResponseStarted:
		if err := sm.handleResponseStarted(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventMessageStarted:
		if err := sm.handleMessageStarted(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventTextDelta:
		if err := sm.handleTextDelta(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventReasoningDelta, lipapi.EventReasoningSignatureDelta, lipapi.EventReasoningOpaqueDelta, lipapi.EventReasoningPart:
		if err := sm.handleReasoning(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventToolCallStarted:
		if err := sm.handleToolCallStarted(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventToolCallArgsDelta:
		if err := sm.handleToolCallArgsDelta(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventToolCallFinished:
		if err := sm.handleToolCallFinished(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventItem:
		if err := sm.handleCarriedItem(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventUsageDelta:
		if err := sm.handleUsageDelta(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventError:
		if err := sm.handleError(ev, &events); err != nil {
			return nil, err
		}
	case lipapi.EventResponseFinished:
		if err := sm.handleResponseFinished(ev, &events); err != nil {
			return nil, err
		}
	default:
		if err := sm.handleUnknown(ev, &events); err != nil {
			return nil, err
		}
	}

	// Metadata and lifecycle transitions can change serialized size without
	// being represented by a streamed string delta. Validate those bounded
	// checkpoints exactly; hot text/reasoning/tool delta paths remain
	// incremental and do not re-marshal the full trajectory.
	switch ev.Kind {
	case lipapi.EventUsageDelta, lipapi.EventItem, lipapi.EventError, lipapi.EventResponseFinished:
		_, data, snapshotErr := sm.snapshotResource(sm.status)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if limitErr := ValidateResourceBytes(data, sm.limits); limitErr != nil {
			return nil, limitErr
		}
		sm.resourceBytes = len(data)
	}
	return events, nil
}

// synthesizeMessageAfterReasoning inserts a message/text boundary when a
// legacy backend emits text deltas after reasoning without EventMessageStarted.
// It does not invent phase, replay, compaction, structured-output, or extension
// semantics.
func (sm *StateMachine) synthesizeMessageAfterReasoning(events *[]StreamEvent) error {
	if sm.activeItem == nil || sm.activeItem.Kind != lipapi.ItemKindReasoning {
		return &SequenceError{
			Code:     "text_delta_without_message",
			Event:    "text.delta",
			Sequence: sm.sequenceNum,
			Message:  "received text delta without active message item",
			Err:      ErrSequenceViolation,
		}
	}
	if err := sm.closeActiveContentPart(events); err != nil {
		return err
	}
	if err := sm.closeActiveItem(events); err != nil {
		return err
	}
	return sm.startMessageItem(events)
}
