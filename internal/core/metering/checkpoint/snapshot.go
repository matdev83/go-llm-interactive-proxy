package checkpoint

// Snapshot type and BindScope live in capture.go alongside FrontendIngressInput.
// This file exists to match the Phase 4 file-structure plan.

// IsWire reports whether this snapshot was captured from bounded wire facts
// without retaining a canonical lipapi.Call (Requirements 15.1–15.3, 19).
func (s *Snapshot) IsWire() bool {
	if s == nil {
		return false
	}
	return s.Call.ID == "" && len(s.Call.Messages) == 0 && len(s.Call.Items) == 0 && len(s.Call.Tools) == 0
}
