package runtime

// TestExecutor returns an empty executor for tests that assign fields via promoted
// grouped-runtime accessors after construction. Prefer [NewExecutor] with [ExecutorConfig]
// for new composition-root wiring.
func TestExecutor() *Executor {
	return NewExecutor(ExecutorConfig{})
}
