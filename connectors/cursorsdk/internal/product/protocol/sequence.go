package protocol

// RunSequencer tracks monotonic sequence and terminal uniqueness per run ID.
type RunSequencer struct {
	lastSeq  map[string]int64
	terminal map[string]bool
}

// NewRunSequencer returns an empty sequencer.
func NewRunSequencer() *RunSequencer {
	return &RunSequencer{
		lastSeq:  make(map[string]int64),
		terminal: make(map[string]bool),
	}
}

// Accept validates and records one run event frame.
func (s *RunSequencer) Accept(f *Frame) error {
	if s == nil {
		return protoErr(ErrorInvalidEvent, "nil sequencer")
	}
	if err := ValidateFrame(f); err != nil {
		return err
	}
	if f.Type != TypeEvent {
		return protoErr(ErrorInvalidEvent, "expected event")
	}
	runID := f.RunID
	if s.terminal[runID] {
		return protoErr(ErrorDuplicateTerminal, "run already terminated")
	}
	seq := *f.Seq
	prev := s.lastSeq[runID]
	if seq <= prev {
		return protoErr(ErrorSequenceRegression, "seq must increase")
	}
	s.lastSeq[runID] = seq
	if IsTerminalKind(f.Kind) {
		s.terminal[runID] = true
	}
	return nil
}

// Terminated reports whether the run already received a terminal event.
func (s *RunSequencer) Terminated(runID string) bool {
	if s == nil {
		return false
	}
	return s.terminal[runID]
}
