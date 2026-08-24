package backendplugin

import "time"

// SetFallbackCancelGraceForTest overrides the package-level fallbackCancelGrace for tests
// and returns a function to restore the previous value.
func SetFallbackCancelGraceForTest(d time.Duration) func() {
	prev := fallbackCancelGrace
	fallbackCancelGrace = d
	return func() {
		fallbackCancelGrace = prev
	}
}
