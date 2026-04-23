package completion

// Meta carries attempt-scoped identifiers for completion gates (no transport types).
type Meta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int
}
