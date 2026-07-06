package lipapi

// OutputCommitted reports whether ev is the first class of canonical stream item
// that commits the active attempt for failover purposes (no silent retry afterward).
//
// Aligned with streaming-first execution: lifecycle frames alone do not commit;
// user-visible deltas and tool argument streaming do.
//
// EventReasoningSignatureDelta is intentionally excluded: it is Anthropic
// integrity metadata that arrives after reasoning text (which already commits),
// not user-visible output content.
func OutputCommitted(ev Event) bool {
	switch ev.Kind {
	case EventTextDelta, EventReasoningDelta, EventToolCallStarted, EventToolCallArgsDelta,
		EventAssistantImageRef, EventAssistantFileRef:
		return true
	default:
		return false
	}
}
