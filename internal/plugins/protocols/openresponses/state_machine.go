package openresponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type StateMachineState string

const (
	StateInit     StateMachineState = "init"
	StateStarted  StateMachineState = "in_progress"
	StateTerminal StateMachineState = "terminal"
)

// StateMachine is the production semantic event normalizer and state machine for OpenResponses.
// It is context-free except explicit owned state and manages sequence numbers, trajectory accumulation,
// lifecycle validation, resource building, and event output.
type StateMachine struct {
	envelope    EnvelopeMetadata
	options     lipapi.GenerationOptions
	state       StateMachineState
	status      string
	sequenceNum int
	eventCount  int // resourceBytes is a conservative incremental upper bound for the serialized
	// response resource. It avoids re-marshaling the full trajectory per delta.
	resourceBytes int
	limits        Limits

	trajectory []lipapi.Item
	usage      UsageStats
	streamErr  *lipapi.StreamError

	// Track active items/parts for stream emission
	activeItemIdx     int
	activeItem        *lipapi.Item
	activeContentIdx  int
	activeContentPart *lipapi.ContentPart
	// activeToolCalls keeps parallel/interleaved tool-call state keyed by the
	// canonical call ID. The trajectory index is stable for the lifetime of a
	// response, so deltas never depend on whichever item is currently active.
	activeToolCalls map[string]int

	// Delta accumulators avoid repeatedly reallocating the complete text or
	// argument string. They are materialized into trajectory items only when a
	// wire resource/item is actually needed.
	textBuilders      map[int]*deltaBuffer
	textPartIndexes   map[int]int
	reasoningBuilders map[int]*deltaBuffer
	toolArgBuilders   map[int]*deltaBuffer

	// tx is set only while ProcessCanonicalEvent is executing. It records lazy
	// item/map mutations so failed events can roll back without cloning the full
	// trajectory on every delta.
	tx *smSnapshot
}

type toolCallChange struct {
	value   int
	present bool
}

type bufferCheckpoint struct {
	buffer *deltaBuffer
	length int
}

type deltaBuffer struct {
	data []byte
}

func (b *deltaBuffer) WriteString(value string) {
	b.data = append(b.data, value...)
}

func (b *deltaBuffer) Write(value []byte) {
	b.data = append(b.data, value...)
}

func (b *deltaBuffer) String() string {
	return string(b.data)
}

func (b *deltaBuffer) Truncate(length int) {
	if length < 0 {
		length = 0
	}
	if length > len(b.data) {
		length = len(b.data)
	}
	b.data = b.data[:length]
}

type smSnapshot struct {
	state            StateMachineState
	status           string
	sequenceNum      int
	eventCount       int
	resourceBytes    int
	trajectoryLen    int
	trajectoryWasNil bool
	itemCopies       map[int]lipapi.Item
	toolChanges      map[string]toolCallChange
	usage            UsageStats
	streamErr        *lipapi.StreamError
	activeItemIdx    int
	activeContentIdx int
	textBuffers      map[int]bufferCheckpoint
	textPartIndexes  map[int]int
	reasoningBuffers map[int]bufferCheckpoint
	toolArgBuffers   map[int]bufferCheckpoint
}

func (sm *StateMachine) takeSnapshot() smSnapshot {
	var errCp *lipapi.StreamError
	if sm.streamErr != nil {
		e := *sm.streamErr
		errCp = &e
	}
	snap := smSnapshot{
		state:            sm.state,
		status:           sm.status,
		sequenceNum:      sm.sequenceNum,
		eventCount:       sm.eventCount,
		resourceBytes:    sm.resourceBytes,
		trajectoryLen:    len(sm.trajectory),
		trajectoryWasNil: sm.trajectory == nil,
		itemCopies:       make(map[int]lipapi.Item),
		toolChanges:      make(map[string]toolCallChange),
		usage:            sm.usage,
		streamErr:        errCp,
		activeItemIdx:    sm.activeItemIdx,
		activeContentIdx: sm.activeContentIdx,
		textBuffers:      snapshotBuffers(sm.textBuilders),
		textPartIndexes:  cloneIntMap(sm.textPartIndexes),
		reasoningBuffers: snapshotBuffers(sm.reasoningBuilders),
		toolArgBuffers:   snapshotBuffers(sm.toolArgBuilders),
	}
	sm.tx = &snap
	return snap
}

func snapshotBuffers(buffers map[int]*deltaBuffer) map[int]bufferCheckpoint {
	out := make(map[int]bufferCheckpoint, len(buffers))
	for index, buffer := range buffers {
		out[index] = bufferCheckpoint{buffer: buffer, length: len(buffer.data)}
	}
	return out
}

func cloneIntMap(values map[int]int) map[int]int {
	out := make(map[int]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func restoreBuffers(current map[int]*deltaBuffer, checkpoints map[int]bufferCheckpoint) {
	for index, buffer := range current {
		checkpoint, ok := checkpoints[index]
		if !ok || checkpoint.buffer != buffer {
			delete(current, index)
			continue
		}
		buffer.Truncate(checkpoint.length)
	}
	for index, checkpoint := range checkpoints {
		current[index] = checkpoint.buffer
		checkpoint.buffer.Truncate(checkpoint.length)
	}
}

func (sm *StateMachine) touchItem(index int) {
	if sm.tx == nil || index < 0 || index >= len(sm.trajectory) {
		return
	}
	if _, exists := sm.tx.itemCopies[index]; !exists {
		sm.tx.itemCopies[index] = cloneItem(sm.trajectory[index])
	}
}

func (sm *StateMachine) touchToolCall(id string) {
	if sm.tx == nil {
		return
	}
	if _, exists := sm.tx.toolChanges[id]; exists {
		return
	}
	value, present := sm.activeToolCalls[id]
	sm.tx.toolChanges[id] = toolCallChange{value: value, present: present}
}

func (sm *StateMachine) restoreSnapshot(snap smSnapshot) {
	sm.state = snap.state
	sm.status = snap.status
	sm.sequenceNum = snap.sequenceNum
	sm.eventCount = snap.eventCount
	sm.resourceBytes = snap.resourceBytes
	if snap.trajectoryWasNil {
		sm.trajectory = nil
	} else {
		sm.trajectory = sm.trajectory[:snap.trajectoryLen]
	}
	for index, item := range snap.itemCopies {
		if index < len(sm.trajectory) {
			sm.trajectory[index] = item
		}
	}
	sm.usage = snap.usage
	sm.streamErr = snap.streamErr
	for id, change := range snap.toolChanges {
		if change.present {
			sm.activeToolCalls[id] = change.value
		} else {
			delete(sm.activeToolCalls, id)
		}
	}
	restoreBuffers(sm.textBuilders, snap.textBuffers)
	restoreBuffers(sm.reasoningBuilders, snap.reasoningBuffers)
	restoreBuffers(sm.toolArgBuilders, snap.toolArgBuffers)
	sm.textPartIndexes = cloneIntMap(snap.textPartIndexes)
	sm.activeItemIdx = snap.activeItemIdx
	sm.activeContentIdx = snap.activeContentIdx
	sm.rebindActivePointers()
}

func (sm *StateMachine) rebindActivePointers() {
	if sm.activeItemIdx >= 0 && sm.activeItemIdx < len(sm.trajectory) {
		sm.activeItem = &sm.trajectory[sm.activeItemIdx]
		if sm.activeContentIdx >= 0 && sm.activeContentIdx < len(sm.activeItem.Content) {
			sm.activeContentPart = &sm.activeItem.Content[sm.activeContentIdx]
		} else {
			sm.activeContentPart = nil
		}
	} else {
		sm.activeItem = nil
		sm.activeContentPart = nil
	}
}

func (sm *StateMachine) materializeItem(index int) {
	if index < 0 || index >= len(sm.trajectory) {
		return
	}
	sm.touchItem(index)
	item := &sm.trajectory[index]
	if builder := sm.textBuilders[index]; builder != nil {
		partIndex := sm.textPartIndexes[index]
		if partIndex >= 0 && partIndex < len(item.Content) {
			item.Content[partIndex].Text = builder.String()
		}
	}
	if builder := sm.reasoningBuilders[index]; builder != nil && item.Reasoning != nil && item.Reasoning.Reasoning != nil {
		item.Reasoning.Reasoning.Text = builder.String()
	}
	if builder := sm.toolArgBuilders[index]; builder != nil && item.ToolCall != nil {
		item.ToolCall.Arguments = []byte(builder.String())
	}
}

func (sm *StateMachine) materializeAll() {
	for index := range sm.trajectory {
		sm.materializeItem(index)
	}
}

// NewStateMachine constructs a new StateMachine for a given response envelope and generation options.
func NewStateMachine(envelope EnvelopeMetadata, options lipapi.GenerationOptions, configured ...Limits) *StateMachine {
	limits := DefaultLimits()
	if len(configured) > 0 {
		limits = configured[0]
	}
	return &StateMachine{
		envelope:          envelope,
		options:           options,
		limits:            limits,
		state:             StateInit,
		status:            "in_progress",
		sequenceNum:       0,
		activeItemIdx:     -1,
		activeToolCalls:   make(map[string]int),
		textBuilders:      make(map[int]*deltaBuffer),
		textPartIndexes:   make(map[int]int),
		reasoningBuilders: make(map[int]*deltaBuffer),
		toolArgBuilders:   make(map[int]*deltaBuffer),
	}
}

// Status returns the current status string ("in_progress", "completed", "failed", "incomplete").
func (sm *StateMachine) Status() string {
	return sm.status
}

// State returns the current StateMachineState.
func (sm *StateMachine) State() StateMachineState {
	return sm.state
}

// SequenceNumber returns the current sequence number.
func (sm *StateMachine) SequenceNumber() int {
	return sm.sequenceNum
}

// Trajectory returns a deep copy of accumulated canonical items.
func (sm *StateMachine) Trajectory() []lipapi.Item {
	sm.materializeAll()
	return cloneItems(sm.trajectory)
}

func (sm *StateMachine) nextSeq() int {
	seq := sm.sequenceNum
	sm.sequenceNum++
	return seq
}

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
		if sm.state != StateInit {
			return nil, &SequenceError{
				Code:     "duplicate_start",
				Event:    string(ev.Kind),
				Sequence: sm.sequenceNum,
				Message:  "response already started",
				Err:      ErrInvalidLifecycleState,
			}
		}
		sm.state = StateStarted
		wireRes, resourceData, err := sm.snapshotResource("in_progress")
		if err != nil {
			return nil, err
		}
		sm.resourceBytes = len(resourceData)
		if err := sm.validateResourceBudget(); err != nil {
			return nil, err
		}
		events = append(events, StreamEvent{
			Type: "response.created",

			SequenceNumber: sm.nextSeq(),
			Response:       wireRes,
		})

	case lipapi.EventMessageStarted:
		if err := sm.closeActiveContentPart(&events); err != nil {
			return nil, err
		}
		if err := sm.closeActiveItem(&events); err != nil {
			return nil, err
		}
		if err := sm.startMessageItem(&events); err != nil {
			return nil, err
		}

	case lipapi.EventTextDelta:
		if sm.activeItem == nil || sm.activeItem.Kind != lipapi.ItemKindMessage || sm.activeContentPart == nil {
			if sm.activeItem != nil && sm.activeItem.Kind == lipapi.ItemKindReasoning {
				// Conservative legacy-stream normalization: legacy backends emit
				// reasoning deltas followed by text deltas in one assistant turn
				// without an explicit message boundary. Synthesize only the
				// message/text boundary; no phase, replay, compaction, structured
				// output, or extension semantics are invented.
				if err := sm.closeActiveContentPart(&events); err != nil {
					return nil, err
				}
				if err := sm.closeActiveItem(&events); err != nil {
					return nil, err
				}
				if err := sm.startMessageItem(&events); err != nil {
					return nil, err
				}
			} else {
				return nil, &SequenceError{
					Code:     "text_delta_without_message",
					Event:    string(ev.Kind),
					Sequence: sm.sequenceNum,
					Message:  "received text delta without active message item",
					Err:      ErrSequenceViolation,
				}
			}
		}

		if err := sm.reserveEncodedDelta(ev.Delta); err != nil {
			return nil, err
		}
		builder := sm.textBuilders[sm.activeItemIdx]

		if builder == nil {
			builder = &deltaBuffer{}
			builder.WriteString(sm.activeContentPart.Text)
			sm.textBuilders[sm.activeItemIdx] = builder
			sm.textPartIndexes[sm.activeItemIdx] = sm.activeContentIdx
		}
		builder.WriteString(ev.Delta)
		events = append(events, StreamEvent{
			Type:           "response.output_text.delta",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    intPtr(sm.activeItemIdx),
			ContentIndex:   intPtr(sm.activeContentIdx),
			Delta:          ev.Delta,
		})

	case lipapi.EventReasoningDelta:
		if sm.activeItem == nil || sm.activeItem.Kind != lipapi.ItemKindReasoning {
			if err := sm.closeActiveContentPart(&events); err != nil {
				return nil, err
			}
			if err := sm.closeActiveItem(&events); err != nil {
				return nil, err
			}

			rsItem := lipapi.Item{
				ID:     fmt.Sprintf("rs_%d", len(sm.trajectory)),
				Kind:   lipapi.ItemKindReasoning,
				Status: lipapi.ItemStatusInProgress,
				Reasoning: &lipapi.ReasoningItem{
					Reasoning: &lipapi.ReasoningPart{
						Text: "",
					},
				},
			}
			sm.trajectory = append(sm.trajectory, rsItem)
			if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
				return nil, err
			}
			sm.activeItemIdx = len(sm.trajectory) - 1
			sm.activeItem = &sm.trajectory[sm.activeItemIdx]
			if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
				return nil, err
			}

			wItem, err := EncodeItem(*sm.activeItem)
			if err != nil {

				return nil, err
			}

			events = append(events, StreamEvent{
				Type:           "response.output_item.added",
				SequenceNumber: sm.nextSeq(),
				OutputIndex:    intPtr(sm.activeItemIdx),
				Item:           &wItem,
			})
		}

		if err := sm.reserveEncodedDelta(ev.Delta); err != nil {
			return nil, err
		}
		if sm.activeItem.Reasoning != nil && sm.activeItem.Reasoning.Reasoning != nil {
			builder := sm.reasoningBuilders[sm.activeItemIdx]

			if builder == nil {
				builder = &deltaBuffer{}
				builder.WriteString(sm.activeItem.Reasoning.Reasoning.Text)
				sm.reasoningBuilders[sm.activeItemIdx] = builder
			}
			builder.WriteString(ev.Delta)
		}
		events = append(events, StreamEvent{
			Type:           "response.reasoning_text.delta",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    intPtr(sm.activeItemIdx),
			ContentIndex:   intPtr(0),
			Delta:          ev.Delta,
		})

	case lipapi.EventToolCallStarted:
		// A new parallel tool call may begin while another tool call remains
		// open. Close ordinary text/reasoning items, but keep existing tool
		// calls indexed so their later deltas can be interleaved safely.
		if sm.activeItem != nil && sm.activeItem.Kind != lipapi.ItemKindToolCall {
			if err := sm.closeActiveContentPart(&events); err != nil {
				return nil, err
			}
			if err := sm.closeActiveItem(&events); err != nil {
				return nil, err
			}
		}

		callID := ev.ToolCallID
		if callID == "" {
			callID = fmt.Sprintf("call_%d", len(sm.trajectory))
		}

		tcItem := lipapi.Item{
			ID:     fmt.Sprintf("fc_%d", len(sm.trajectory)),
			Kind:   lipapi.ItemKindToolCall,
			Status: lipapi.ItemStatusInProgress,
			ToolCall: &lipapi.ToolCallItem{
				CallID:    callID,
				Name:      ev.ToolName,
				Arguments: []byte{},
			},
		}
		sm.trajectory = append(sm.trajectory, tcItem)
		if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
			return nil, err
		}
		sm.activeItemIdx = len(sm.trajectory) - 1
		sm.activeItem = &sm.trajectory[sm.activeItemIdx]
		if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
			return nil, err
		}
		if sm.activeToolCalls == nil {
			sm.activeToolCalls = make(map[string]int)
		}
		if _, exists := sm.activeToolCalls[callID]; exists {
			return nil, &SequenceError{Code: "duplicate_tool_call", Event: string(ev.Kind), ID: callID, Sequence: sm.sequenceNum, Message: "tool call ID already active", Err: ErrMismatchedID}
		}
		sm.touchToolCall(callID)
		sm.activeToolCalls[callID] = sm.activeItemIdx

		wItem, err := EncodeItem(*sm.activeItem)
		if err != nil {
			return nil, err
		}

		events = append(events, StreamEvent{
			Type:           "response.output_item.added",
			SequenceNumber: sm.nextSeq(),
			OutputIndex:    intPtr(sm.activeItemIdx),
			Item:           &wItem,
		})

	case lipapi.EventToolCallArgsDelta:
		callID := ev.ToolCallID
		if callID == "" && sm.activeItem != nil && sm.activeItem.Kind == lipapi.ItemKindToolCall {
			callID = sm.activeItem.ToolCall.CallID
		}
		idx, ok := sm.activeToolCalls[callID]
		if !ok || idx < 0 || idx >= len(sm.trajectory) || sm.trajectory[idx].ToolCall == nil {
			return nil, &SequenceError{Code: "tool_args_without_item", Event: string(ev.Kind), ID: callID, Sequence: sm.sequenceNum, Message: "tool call args delta without active tool call item", Err: ErrMismatchedID}
		}
		if err := sm.reserveEncodedDelta(ev.Delta); err != nil {
			return nil, err
		}
		item := &sm.trajectory[idx]
		builder := sm.toolArgBuilders[idx]
		if builder == nil {
			builder = &deltaBuffer{}
			builder.Write(item.ToolCall.Arguments)
			sm.toolArgBuilders[idx] = builder
		}
		builder.WriteString(ev.Delta)
		events = append(events, StreamEvent{Type: "response.function_call_arguments.delta", SequenceNumber: sm.nextSeq(), ItemID: item.ID, OutputIndex: intPtr(idx), CallID: item.ToolCall.CallID, Delta: ev.Delta})

	case lipapi.EventToolCallFinished:
		callID := ev.ToolCallID
		if callID == "" && sm.activeItem != nil && sm.activeItem.Kind == lipapi.ItemKindToolCall {
			callID = sm.activeItem.ToolCall.CallID
		}
		idx, ok := sm.activeToolCalls[callID]
		if !ok || idx < 0 || idx >= len(sm.trajectory) || sm.trajectory[idx].ToolCall == nil {
			return nil, &SequenceError{Code: "tool_finished_without_item", Event: string(ev.Kind), ID: callID, Sequence: sm.sequenceNum, Message: "tool call finished without active tool call item", Err: ErrMismatchedID}
		}
		if err := sm.closeToolCallAt(idx, &events); err != nil {
			return nil, err
		}

	case lipapi.EventItem:
		// A standalone canonical item carrier (provider compaction item in a
		// compacted ordered window). It is not content-class output and never
		// synthesizes message/reasoning/tool semantics; legacy normalizers never
		// produce it. Close any active item and append the carried item in
		// trajectory order, marking an empty status completed.
		if ev.Item == nil {
			return nil, &SequenceError{
				Code:     "item_event_without_item",
				Event:    string(ev.Kind),
				Sequence: sm.sequenceNum,
				Message:  "item event carried no canonical item",
				Err:      ErrInvalidLifecycleState,
			}
		}
		if err := sm.closeActiveContentPart(&events); err != nil {
			return nil, err
		}
		if err := sm.closeActiveItem(&events); err != nil {
			return nil, err
		}
		carried := cloneItem(*ev.Item)
		if carried.Status == "" {
			carried.Status = lipapi.ItemStatusCompleted
		}
		sm.trajectory = append(sm.trajectory, carried)
		if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
			return nil, err
		}
		sm.activeItemIdx = len(sm.trajectory) - 1
		sm.activeItem = &sm.trajectory[sm.activeItemIdx]
		if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
			return nil, err
		}

		wItem, err := EncodeItem(*sm.activeItem)
		if err != nil {
			return nil, err
		}
		events = append(events, StreamEvent{
			Type:           "response.output_item.added",
			SequenceNumber: sm.nextSeq(),
			OutputIndex:    intPtr(sm.activeItemIdx),
			Item:           &wItem,
		})
		// Standalone items are complete when carried; emit the done marker.
		if err := sm.closeActiveItem(&events); err != nil {
			return nil, err
		}

	case lipapi.EventUsageDelta:
		sm.usage.InputTokens = ev.InputTokens
		sm.usage.OutputTokens = ev.OutputTokens
		sm.usage.TotalTokens = ev.TotalTokens
		sm.usage.CachedTokens = ev.CacheReadTokens
		sm.usage.ReasoningTokens = ev.ReasoningTokens

	case lipapi.EventError:
		if err := sm.reserveEncodedDelta(ev.ErrorMessage); err != nil {
			return nil, err
		}
		if err := sm.closeActiveContentPart(&events); err != nil {
			return nil, err
		}
		if err := sm.closeActiveItem(&events); err != nil {
			return nil, err
		}
		for _, idx := range sm.activeToolCallIndexes() {
			if err := sm.closeToolCallAt(idx, &events); err != nil {
				return nil, err
			}
		}

		sm.status = "failed"
		sm.streamErr = &lipapi.StreamError{
			Code:    ev.ErrorCode,
			Message: ev.ErrorMessage,
		}
		sm.state = StateTerminal

		wireRes, _, err := sm.snapshotResource("failed")
		if err != nil {
			return nil, err
		}

		events = append(events, StreamEvent{
			Type:           "response.failed",
			SequenceNumber: sm.nextSeq(),
			Response:       wireRes,
		})

	case lipapi.EventResponseFinished:
		if err := sm.closeActiveContentPart(&events); err != nil {
			return nil, err
		}
		if err := sm.closeActiveItem(&events); err != nil {
			return nil, err
		}
		for _, idx := range sm.activeToolCallIndexes() {
			if err := sm.closeToolCallAt(idx, &events); err != nil {
				return nil, err
			}
		}

		finalStatus := "completed"
		switch {
		case ev.ResponseStatus == "incomplete":
			// Explicit upstream incomplete semantics win regardless of the
			// finish reason so non-length reasons (content_filter,
			// interruption, unknown, ...) are never rewritten to completed.
			finalStatus = "incomplete"
		case ev.ResponseStatus == "completed":
			// Explicit upstream completed semantics win too; a completed
			// response may legitimately carry finish_reason content_filter.
			finalStatus = "completed"
		case ev.FinishReason == "length" || ev.FinishReason == "max_tokens":
			// Legacy providers do not carry explicit response status; keep the
			// token-limit inference for their terminal event.
			finalStatus = "incomplete"
		}
		sm.status = finalStatus
		sm.state = StateTerminal

		wireRes, _, err := sm.snapshotResource(finalStatus)
		if err != nil {
			return nil, err
		}

		eventType := "response.completed"
		if finalStatus == "incomplete" {
			eventType = "response.incomplete"
		}

		events = append(events, StreamEvent{
			Type:           eventType,
			SequenceNumber: sm.nextSeq(),
			Response:       wireRes})

	default:
		if strings.Contains(string(ev.Kind), ":") {
			events = append(events, StreamEvent{

				Type:           string(ev.Kind),
				SequenceNumber: sm.nextSeq(),
			})
		} else {
			return nil, &SequenceError{
				Code:     "unknown_event_type",
				Event:    string(ev.Kind),
				Sequence: sm.sequenceNum,
				Message:  "unknown unprefixed event type",
				Err:      ErrUnknownDiscriminator,
			}
		}
	}

	// Metadata and lifecycle transitions can change serialized size without	// being represented by a streamed string delta. Validate those bounded
	// checkpoints exactly; hot text/reasoning/tool delta paths remain
	// incremental and do not re-marshal the full trajectory.
	switch ev.Kind {
	case lipapi.EventUsageDelta, lipapi.EventItem, lipapi.EventError, lipapi.EventResponseFinished:
		if _, data, snapshotErr := sm.snapshotResource(sm.status); snapshotErr != nil {
			return nil, snapshotErr
		} else if limitErr := ValidateResourceBytes(data, sm.limits); limitErr != nil {
			return nil, limitErr
		} else {
			sm.resourceBytes = len(data)
		}
	}
	return events, nil

}

// reserveEncodedDelta bounds untrusted streamed text, reasoning, tool

// arguments, and error messages without rebuilding the accumulated resource.
// The protocol limit is a byte budget for accumulated output payload; fixed JSON
// envelope/item overhead remains bounded independently by item/event limits.
func (sm *StateMachine) reserveEncodedDelta(delta string) error {
	if delta == "" {
		return nil
	}
	encoded, err := json.Marshal(delta)
	if err != nil {
		return err
	}
	return sm.setResourceBytes(sm.resourceBytes + len(encoded) - 2) // exclude JSON string quotes
}

func (sm *StateMachine) reserveItemBytes(item lipapi.Item) error {
	wire, err := EncodeItem(item)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	// Charge the initial item representation plus conservative array punctuation.
	return sm.setResourceBytes(sm.resourceBytes + len(encoded) + 2)
}

func (sm *StateMachine) setResourceBytes(actual int) error {
	if sm.limits.MaxResourceSizeBytes > 0 && actual > sm.limits.MaxResourceSizeBytes {
		return &LimitExceededError{
			Param:   "resource_size",
			Limit:   sm.limits.MaxResourceSizeBytes,
			Actual:  actual,
			Message: "accumulated output bytes exceed resource limit",
			Err:     ErrLimitExceeded,
		}
	}
	sm.resourceBytes = actual
	return nil
}

func (sm *StateMachine) validateResourceBudget() error {
	return sm.setResourceBytes(sm.resourceBytes)
}

// startMessageItem opens a new assistant message item with one empty text
// content part and emits the output_item.added/content_part.added events. It
// must be called only after any active item has been closed.
func (sm *StateMachine) startMessageItem(events *[]StreamEvent) error {
	msgItem := lipapi.Item{
		ID:     fmt.Sprintf("msg_%d", len(sm.trajectory)),
		Kind:   lipapi.ItemKindMessage,
		Role:   lipapi.RoleAssistant,
		Status: lipapi.ItemStatusInProgress,
	}
	sm.trajectory = append(sm.trajectory, msgItem)
	if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
		return err
	}
	sm.activeItemIdx = len(sm.trajectory) - 1
	sm.activeItem = &sm.trajectory[sm.activeItemIdx]

	wItem, err := EncodeItem(*sm.activeItem)
	if err != nil {
		return err
	}

	*events = append(*events, StreamEvent{
		Type:           "response.output_item.added",
		SequenceNumber: sm.nextSeq(),
		OutputIndex:    intPtr(sm.activeItemIdx),
		Item:           &wItem,
	})

	// Start content part
	cPart := lipapi.ContentPart{Kind: lipapi.ContentPartText}
	sm.activeItem.Content = append(sm.activeItem.Content, cPart)
	sm.activeContentIdx = len(sm.activeItem.Content) - 1
	sm.activeContentPart = &sm.activeItem.Content[sm.activeContentIdx]
	if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
		return err
	}

	wPart := encodeContentPart(*sm.activeContentPart, lipapi.RoleAssistant)
	*events = append(*events, StreamEvent{
		Type:           "response.content_part.added",
		SequenceNumber: sm.nextSeq(),
		ItemID:         sm.activeItem.ID,
		OutputIndex:    intPtr(sm.activeItemIdx),
		ContentIndex:   intPtr(sm.activeContentIdx),
		Part:           &wPart,
	})
	return nil
}

func (sm *StateMachine) closeActiveContentPart(events *[]StreamEvent) error {
	if sm.activeContentPart == nil || sm.activeItem == nil {
		return nil
	}

	sm.materializeItem(sm.activeItemIdx)
	sm.activeItem = &sm.trajectory[sm.activeItemIdx]
	sm.activeContentPart = &sm.activeItem.Content[sm.activeContentIdx]
	wPart := encodeContentPart(*sm.activeContentPart, sm.activeItem.Role)
	if sm.activeContentPart.Kind == lipapi.ContentPartText {
		*events = append(*events, StreamEvent{
			Type:           "response.output_text.done",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    intPtr(sm.activeItemIdx),
			ContentIndex:   intPtr(sm.activeContentIdx),
			Text:           sm.activeContentPart.Text,
		})
	}

	*events = append(*events, StreamEvent{
		Type:           "response.content_part.done",
		SequenceNumber: sm.nextSeq(),
		ItemID:         sm.activeItem.ID,
		OutputIndex:    intPtr(sm.activeItemIdx),
		ContentIndex:   intPtr(sm.activeContentIdx),
		Part:           &wPart,
	})

	sm.activeContentPart = nil
	sm.activeContentIdx = -1
	return nil
}

func (sm *StateMachine) closeActiveItem(events *[]StreamEvent) error {
	if sm.activeItem == nil {
		return nil
	}
	if sm.activeItem.Kind == lipapi.ItemKindToolCall {
		return sm.closeToolCallAt(sm.activeItemIdx, events)
	}

	sm.touchItem(sm.activeItemIdx)
	sm.materializeItem(sm.activeItemIdx)
	sm.activeItem = &sm.trajectory[sm.activeItemIdx]
	sm.activeItem.Status = lipapi.ItemStatusCompleted
	if sm.activeItem.Kind == lipapi.ItemKindReasoning && sm.activeItem.Reasoning != nil && sm.activeItem.Reasoning.Reasoning != nil {
		*events = append(*events, StreamEvent{
			Type:           "response.reasoning_text.done",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    intPtr(sm.activeItemIdx),
			ContentIndex:   intPtr(0),
			Text:           sm.activeItem.Reasoning.Reasoning.Text,
		})
	}

	wItem, err := EncodeItem(*sm.activeItem)
	if err != nil {
		return err
	}
	*events = append(*events, StreamEvent{
		Type:           "response.output_item.done",
		SequenceNumber: sm.nextSeq(),
		OutputIndex:    intPtr(sm.activeItemIdx),
		Item:           &wItem,
	})

	sm.activeItem = nil
	sm.activeItemIdx = -1
	return nil
}

// closeToolCallAt closes one tool call by trajectory index. It always emits
// arguments.done before output_item.done, including when closure is implicit.
func (sm *StateMachine) closeToolCallAt(idx int, events *[]StreamEvent) error {
	if idx < 0 || idx >= len(sm.trajectory) || sm.trajectory[idx].ToolCall == nil {
		return &SequenceError{Code: "tool_finished_without_item", Sequence: sm.sequenceNum, Message: "tool call trajectory entry is unavailable", Err: ErrMismatchedID}
	}
	sm.touchItem(idx)
	sm.materializeItem(idx)
	item := &sm.trajectory[idx]
	item.Status = lipapi.ItemStatusCompleted

	callID := item.ToolCall.CallID
	wItem, err := EncodeItem(*item)
	if err != nil {
		return err
	}
	*events = append(*events,
		StreamEvent{Type: "response.function_call_arguments.done", SequenceNumber: sm.nextSeq(), ItemID: item.ID, OutputIndex: intPtr(idx), CallID: callID, Arguments: string(item.ToolCall.Arguments)},
		StreamEvent{Type: "response.output_item.done", SequenceNumber: sm.nextSeq(), OutputIndex: intPtr(idx), Item: &wItem})
	sm.touchToolCall(callID)
	delete(sm.activeToolCalls, callID)

	if sm.activeItemIdx == idx {
		sm.activeItem = nil
		sm.activeItemIdx = -1
	}
	return nil
}

func (sm *StateMachine) activeToolCallIndexes() []int {
	indexes := make([]int, 0, len(sm.activeToolCalls))
	for _, idx := range sm.activeToolCalls {
		indexes = append(indexes, idx)
	}
	// Trajectory order makes implicit closure deterministic.
	for i := 1; i < len(indexes); i++ {
		for j := i; j > 0 && indexes[j] < indexes[j-1]; j-- {
			indexes[j], indexes[j-1] = indexes[j-1], indexes[j]
		}
	}
	return indexes
}

func cloneToolCallIndexes(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (sm *StateMachine) snapshotResource(status string) (*WireResponseResource, []byte, error) {
	sm.materializeAll()
	env := sm.envelope
	env.Status = status
	return BuildResponseResource(env, sm.trajectory, sm.usage, sm.options, sm.streamErr)
}

// AccumulateResource returns the accumulated WireResponseResource and JSON bytes.
func (sm *StateMachine) AccumulateResource() (*WireResponseResource, []byte, error) {
	return sm.snapshotResource(sm.status)
}

// ConservativeLegacyNormalize maps a slice of lipapi.Event into a slice of StreamEvents
// using conservative normalization rules (no invented phase, replay, compaction, or extensions).
func ConservativeLegacyNormalize(envelope EnvelopeMetadata, options lipapi.GenerationOptions, events []lipapi.Event) ([]StreamEvent, *WireResponseResource, error) {
	sm := NewStateMachine(envelope, options)
	var streamEvents []StreamEvent

	for _, ev := range events {
		se, err := sm.ProcessCanonicalEvent(ev)
		if err != nil {
			return nil, nil, err
		}
		streamEvents = append(streamEvents, se...)
	}

	res, _, err := sm.AccumulateResource()
	if err != nil {
		return nil, nil, err
	}

	return streamEvents, res, nil
}

func intPtr(i int) *int {
	return &i
}

func cloneItems(items []lipapi.Item) []lipapi.Item {
	if items == nil {
		return nil
	}
	res := make([]lipapi.Item, len(items))
	for i, item := range items {
		res[i] = cloneItem(item)
	}
	return res
}

func cloneItem(item lipapi.Item) lipapi.Item {
	cp := item
	if item.Content != nil {
		cp.Content = make([]lipapi.ContentPart, len(item.Content))
		copy(cp.Content, item.Content)
	}
	if item.ToolCall != nil {
		tc := *item.ToolCall
		if item.ToolCall.Arguments != nil {
			tc.Arguments = make([]byte, len(item.ToolCall.Arguments))
			copy(tc.Arguments, item.ToolCall.Arguments)
		}
		cp.ToolCall = &tc
	}
	if item.ToolResult != nil {
		tr := *item.ToolResult
		if item.ToolResult.Parts != nil {
			tr.Parts = make([]lipapi.ContentPart, len(item.ToolResult.Parts))
			copy(tr.Parts, item.ToolResult.Parts)
		}
		cp.ToolResult = &tr
	}
	if item.Reasoning != nil {
		r := *item.Reasoning
		if item.Reasoning.Reasoning != nil {
			rp := *item.Reasoning.Reasoning
			r.Reasoning = &rp
		}
		cp.Reasoning = &r
	}
	if item.Compaction != nil {
		c := *item.Compaction
		if item.Compaction.Opaque != nil {
			c.Opaque = make([]byte, len(item.Compaction.Opaque))
			copy(c.Opaque, item.Compaction.Opaque)
		}
		cp.Compaction = &c
	}
	if item.Extension != nil {
		ext := *item.Extension
		if item.Extension.Data != nil {
			ext.Data = make([]byte, len(item.Extension.Data))
			copy(ext.Data, item.Extension.Data)
		}
		cp.Extension = &ext
	}
	if item.Reference != nil {
		ref := *item.Reference
		cp.Reference = &ref
	}
	return cp
}
