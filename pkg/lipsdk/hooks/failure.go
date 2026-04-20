package hooks

// FailureMode selects how the hook bus treats a non-nil error from Handle.
type FailureMode int

const (
	// FailOpen continues the chain after recording the error (submit/part hooks).
	FailOpen FailureMode = iota
	// FailClosed stops the chain and returns the error.
	FailClosed
)
