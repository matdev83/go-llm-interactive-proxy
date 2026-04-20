package lipapi

// ToolEventKind classifies tool-call lifecycle items passed to tool reactors.
type ToolEventKind string

const (
	ToolEventStarted   ToolEventKind = "tool_call_started"
	ToolEventArgsDelta ToolEventKind = "tool_call_args_delta"
	ToolEventFinished  ToolEventKind = "tool_call_finished"
)

// ToolEvent is the canonical tool-call subset exposed to tool reactors.
type ToolEvent struct {
	Kind ToolEventKind

	ToolCallID string
	ToolName   string

	// ArgsDelta carries incremental JSON/tool arguments fragments for ToolEventArgsDelta.
	ArgsDelta string
}

// ToolEventFromEvent maps a single stream Event to a ToolEvent when applicable.
// The second return value is false for non-tool event kinds.
func ToolEventFromEvent(ev Event) (ToolEvent, bool) {
	switch ev.Kind {
	case EventToolCallStarted:
		if ev.ToolCallID == "" {
			return ToolEvent{}, false
		}
		return ToolEvent{
			Kind:       ToolEventStarted,
			ToolCallID: ev.ToolCallID,
			ToolName:   ev.ToolName,
		}, true
	case EventToolCallArgsDelta:
		if ev.ToolCallID == "" {
			return ToolEvent{}, false
		}
		return ToolEvent{
			Kind:       ToolEventArgsDelta,
			ToolCallID: ev.ToolCallID,
			ToolName:   ev.ToolName,
			ArgsDelta:  ev.Delta,
		}, true
	case EventToolCallFinished:
		if ev.ToolCallID == "" {
			return ToolEvent{}, false
		}
		return ToolEvent{
			Kind:       ToolEventFinished,
			ToolCallID: ev.ToolCallID,
			ToolName:   ev.ToolName,
		}, true
	default:
		return ToolEvent{}, false
	}
}
