package lipapi

// OutputCommitted reports whether ev is the first class of canonical stream item
// that commits the active attempt for failover purposes (no silent retry afterward).
//
// Aligned with streaming-first execution: lifecycle frames alone do not commit;
// user-visible deltas and tool argument streaming do.
func OutputCommitted(ev Event) bool {
	switch ev.Kind {
	case EventTextDelta, EventReasoningDelta, EventToolCallStarted, EventToolCallArgsDelta:
		return true
	default:
		return false
	}
}
