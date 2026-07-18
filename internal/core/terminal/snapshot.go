package terminal

// AccumulatorSnapshot is an opaque immutable copy of mutable stream
// accumulators taken by the CAS claim winner.
type AccumulatorSnapshot struct {
	raw             []byte
	outputCommitted bool
}

// NewAccumulatorSnapshot copies data into an opaque snapshot.
func NewAccumulatorSnapshot(data []byte, outputCommitted bool) AccumulatorSnapshot {
	return AccumulatorSnapshot{
		raw:             append([]byte(nil), data...),
		outputCommitted: outputCommitted,
	}
}

// Bytes returns a defensive copy of the snapshot bytes.
func (s AccumulatorSnapshot) Bytes() []byte {
	return append([]byte(nil), s.raw...)
}

// OutputCommitted reports whether visible output was committed when snapped.
func (s AccumulatorSnapshot) OutputCommitted() bool {
	return s.outputCommitted
}

// Clone returns a deep copy.
func (s AccumulatorSnapshot) Clone() AccumulatorSnapshot {
	return NewAccumulatorSnapshot(s.raw, s.outputCommitted)
}

// Equal reports whether two snapshots have identical bytes and flags.
func (s AccumulatorSnapshot) Equal(o AccumulatorSnapshot) bool {
	if s.outputCommitted != o.outputCommitted || len(s.raw) != len(o.raw) {
		return false
	}
	for i := range s.raw {
		if s.raw[i] != o.raw[i] {
			return false
		}
	}
	return true
}
